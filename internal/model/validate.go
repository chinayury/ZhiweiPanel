package model

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// nginx 配置文本安全性校验
//
// 站点配置里有一批字段会被**原样**写进 nginx 配置文件。如果这些值里带有
// nginx 的语法分隔符（; { } " ' \ 换行），生成的就不再是"用户填的那份配置"，
// 而是"另一份配置"：
//
//	站点名填 "a\n    location / { return 403; }"
//	  → 注释行被换行截断，后面那行变成真正的指令
//
//	IP 白名单填 "127.0.0.1; deny all"
//	  → 多出一条 deny 指令，站点把自己锁死
//
//	UA 屏蔽词填 'x" { return 403; } if ($x="'
//	  → 破掉引号，if 表达式结构被改写
//
// 两种后果都很糟：
//  1. 生成的配置不是用户以为的那份 —— 静默的行为改变，最难排查；
//  2. nginx -t 失败 → ApplyAllConfig 整体回滚 → **所有站点**的本次变更
//     都不生效，而用户完全看不出是哪一格填错了。
//
// 所以这里把"能安全写进 nginx 文本"的规则显式化，并分成两处使用：
//
//	写入路径（建站/改站）：ValidateSite 返回精确到字段的错误，让用户改对；
//	渲染路径（Render）：internal/nginx 用同一批谓词做净化降级，
//	                    保证历史脏数据不会把整个面板拖垮。
//
// 校验与净化共用同一套谓词，避免出现"校验放行但渲染拒绝"（或反之）的漂移。
// ---------------------------------------------------------------------------

// ErrInvalidValue 字段值非法。
//
// 单独定义而不是复用 ErrBadRequest，是为了让 API 层能把它映射成 400
// 并保留"哪个字段错了"的细节，同时不与"整包请求格式错误"混淆。
var ErrInvalidValue = errors.New("字段值非法")

// fieldError 带字段名的校验错误。
//
// 错误文本直接是「字段名 + 原因」，不加前缀：这些文本会原样返回给用户，
// "字段值非法: 站点名 不能为空"这种前缀只是噪音。
type fieldError struct {
	field  string
	reason string
}

func (e *fieldError) Error() string { return e.field + " " + e.reason }

// Unwrap 让 errors.Is(err, ErrInvalidValue) 成立，
// API 层据此把校验失败映射成 400 而不是 500。
func (e *fieldError) Unwrap() error { return ErrInvalidValue }

// invalidField 构造带字段名的校验错误。
func invalidField(field, reason string) error {
	return &fieldError{field: field, reason: reason}
}

// ---------------------------------------------------------------------------
// 通用谓词
// ---------------------------------------------------------------------------

// nginxUnsafeRune 判断 r 是否会破坏 nginx 配置语法。
//
// 覆盖三类：
//   - 空白与控制字符（含换行）——换行会提前结束当前指令；
//   - 指令/块分隔符 ; { } ——能终止指令或开闭配置块；
//   - 引号与转义 \ ' " ——能逃出字符串上下文。
//
// 刻意**不**排除 $ 与 ( ) [ ] | . * + ? ：这些在正则与 location 匹配里
// 是合法且必要的（且不改变指令结构）。需要它们的字段另走各自的谓词。
func nginxUnsafeRune(r rune) bool {
	switch r {
	case ';', '{', '}', '"', '\'', '\\':
		return true
	}
	// 空格、制表符、换行、回车、NUL 及其它控制字符
	if r <= 0x20 || r == 0x7f {
		return true
	}
	return false
}

// NginxSafeToken 报告 v 能否安全写入 nginx 配置的单个指令参数。
//
// 用于：默认文档名、扩展名、日志格式名、TLS 协议名这类"标识符"型取值。
func NginxSafeToken(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if nginxUnsafeRune(r) {
			return false
		}
	}
	return true
}

// NginxSafeIdentifier 报告 v 是否是"标识符"型取值（字母数字与 _ - . 组成）。
//
// 比 NginxSafeToken 更严：不允许 $ ( ) [ ] | * + ? 等正则元字符。
// 用于扩展名、禁用函数名、HTTP 方法、数据库用户名这类取值。
func NginxSafeIdentifier(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// NginxSafePathPrefix 报告 v 能否作为 location 路径前缀。
//
// 允许路径里常见的字符（含 $ 与正则元字符，用户常用通配），但不允许
// 空白与 ; { } " ' \ —— 那三者能改变配置结构。
func NginxSafePathPrefix(v string) bool {
	if v == "" || len(v) > 512 {
		return false
	}
	if !strings.HasPrefix(v, "/") {
		return false
	}
	for _, r := range v {
		if nginxUnsafeRune(r) {
			return false
		}
	}
	return true
}

// NginxSafeIP 报告 v 是否是合法 IP、CIDR 或 "all"。
//
// allow / deny 指令只接受这几种。塞进别的值会让 nginx -t 失败。
func NginxSafeIP(v string) bool {
	if v == "all" {
		return true
	}
	if _, _, err := net.ParseCIDR(v); err == nil {
		return true
	}
	return net.ParseIP(v) != nil
}

// NginxSafeURL 报告 v 是否是合法上游地址（http/https + 主机 [+ 端口] [+ 路径]）。
//
// 用于反代的 proxy_pass。必须限制协议，否则 file:// 之类能把本机文件
// 通过反代暴露出去。
func NginxSafeURL(v string) bool {
	if v == "" || len(v) > 512 {
		return false
	}
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	// 主机部分不允许出现 nginx 语法字符
	for _, r := range u.Host {
		if nginxUnsafeRune(r) {
			return false
		}
	}
	return true
}

// NginxSafeSize 报告 v 是否是 nginx 合法的大小值（10m / 512k / 1g / 100）。
func NginxSafeSize(v string) bool {
	if v == "" || len(v) > 16 {
		return false
	}
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	unit := strings.ToLower(v[i:])
	switch unit {
	case "", "k", "m", "g":
		return true
	}
	return false
}

// NginxSafeBandwidth 报告 v 是否是合法限速值（512k / 2m）。
func NginxSafeBandwidth(v string) bool {
	if v == "" || len(v) > 16 {
		return false
	}
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	switch strings.ToLower(v[i:]) {
	case "k", "m":
		return true
	}
	return false
}

// NginxSafeLogFormatName 报告 v 是否是合法 log_format 名。
//
// nginx 的 log_format 名要写在 access_log 的第三个参数位置，
// 只能是标识符形态。
func NginxSafeLogFormatName(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// NginxSafeTLSProtocol 报告 v 是否是受支持的 TLS 协议名。
func NginxSafeTLSProtocol(v string) bool {
	switch v {
	case "TLSv1.2", "TLSv1.3":
		return true
	}
	return false
}

// NginxSafeHTTPMethod 报告 v 是否是合法 HTTP 方法。
func NginxSafeHTTPMethod(v string) bool {
	switch v {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS", "CONNECT", "TRACE":
		return true
	}
	return false
}

// NginxSafeRegexBody 报告 v 能否安全放进 if 条件里的正则体。
//
// 与 NginxSafeToken 的区别：这里额外排除正则的分组与量词字符
// （( ) [ ] { } | * + ?），因为屏蔽词被拼进 "^(kw1|kw2)" 这种分组里，
// 一个落单的括号就会让 nginx -t 报语法错。
func NginxSafeRegexBody(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if nginxUnsafeRune(r) {
			return false
		}
		switch r {
		case '(', ')', '[', ']', '{', '}', '|', '*', '+', '?', '^', '$':
			return false
		}
	}
	return true
}

// NginxSafeRedirectTarget 报告 v 能否作为重定向目标。
//
// 目标会写进 return <code> <target>;，target 里带 ; 就能追加指令。
func NginxSafeRedirectTarget(v string) bool {
	if v == "" || len(v) > 1024 {
		return false
	}
	for _, r := range v {
		if nginxUnsafeRune(r) {
			return false
		}
	}
	return true
}

// NginxSafeCertPath 报告 v 能否作为证书文件路径。
//
// 证书路径写进 ssl_certificate，允许绝对路径与普通文件名字符，
// 但不允许穿越（..）与 nginx 语法字符。
func NginxSafeCertPath(v string) bool {
	if v == "" || len(v) > 512 {
		return false
	}
	if !strings.HasPrefix(v, "/") {
		return false
	}
	for _, r := range v {
		if nginxUnsafeRune(r) {
			return false
		}
	}
	clean := strings.TrimSuffix(v, "/")
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 站点级校验
// ---------------------------------------------------------------------------

// ValidateSite 校验站点全部会进入 nginx 配置文本的字段。
//
// 供写入路径（建站 / 改站）调用：返回第一个出错的字段，让用户知道
// 哪一格填错了，而不是等 nginx -t 失败后看到"所有站点都没生效"。
//
// 渲染路径不做硬校验（历史数据可能已经违规，硬失败会让整个面板不可用），
// 改由 internal/nginx 的净化逻辑降级处理。
func ValidateSite(s *Site) error {
	if s == nil {
		return invalidField("站点", "不能为空")
	}
	if err := s.Domains.Validate(); err != nil {
		return err
	}
	if err := validateSiteName(s.Name); err != nil {
		return err
	}
	if err := validateIndexFiles(s.Index.Files); err != nil {
		return err
	}
	if err := validateAccess(s.Access); err != nil {
		return err
	}
	if err := validateTraffic(s.Traffic); err != nil {
		return err
	}
	if err := validateSecurity(s.Security); err != nil {
		return err
	}
	if err := validateHotlink(s.Hotlink); err != nil {
		return err
	}
	if err := validateSSL(s.SSL); err != nil {
		return err
	}
	if err := validateProxy(s.Proxy); err != nil {
		return err
	}
	if err := validateRedirects(s.Redirect); err != nil {
		return err
	}
	if err := validateSubDir(s.SubDir); err != nil {
		return err
	}
	if err := validatePHP(s.PHP); err != nil {
		return err
	}
	if err := validateExtra(s.Extra); err != nil {
		return err
	}
	if err := validateLogs(s.Logs); err != nil {
		return err
	}
	if err := validateRoot(s.Root); err != nil {
		return err
	}
	return validateAlerts(s.Alert)
}

func validateSiteName(name string) error {
	if name == "" {
		return invalidField("站点名", "不能为空")
	}
	if len(name) > 128 {
		return invalidField("站点名", "长度不能超过 128")
	}
	for _, r := range name {
		// 站点名会写进 nginx 配置的注释行，换行会截断注释并让后续内容变成指令
		if r == '\n' || r == '\r' || r == 0 {
			return invalidField("站点名", "不能包含换行或空字符")
		}
		if r < 0x20 || r == 0x7f {
			return invalidField("站点名", "不能包含控制字符")
		}
	}
	return nil
}

func validateIndexFiles(files []string) error {
	if len(files) > 16 {
		return invalidField("默认文档", "最多 16 个")
	}
	for _, f := range files {
		if !NginxSafeIdentifier(f) {
			return invalidField("默认文档", fmt.Sprintf("%q 非法（只允许字母数字与 _ - .）", f))
		}
	}
	return nil
}

func validateAccess(a AccessConf) error {
	if len(a.Allow) > 256 {
		return invalidField("IP 白名单", "条目过多")
	}
	for _, v := range a.Allow {
		if !NginxSafeIP(strings.TrimSpace(v)) {
			return invalidField("IP 白名单", fmt.Sprintf("%q 不是合法的 IP 或网段", v))
		}
	}
	if len(a.Deny) > 256 {
		return invalidField("IP 黑名单", "条目过多")
	}
	for _, v := range a.Deny {
		if !NginxSafeIP(strings.TrimSpace(v)) {
			return invalidField("IP 黑名单", fmt.Sprintf("%q 不是合法的 IP 或网段", v))
		}
	}
	for _, m := range a.AllowMethods {
		if !NginxSafeHTTPMethod(strings.ToUpper(strings.TrimSpace(m))) {
			return invalidField("允许的请求方法", fmt.Sprintf("%q 不是合法 HTTP 方法", m))
		}
	}
	if len(a.AuthUser) > 64 {
		return invalidField("访问认证用户名", "长度不能超过 64")
	}
	for _, r := range a.AuthUser {
		if nginxUnsafeRune(r) {
			return invalidField("访问认证用户名", "不能包含空格或引号等特殊字符")
		}
	}
	// 冒号是 htpasswd 文件的"用户名:哈希"分隔符，换行会截断文件——
	// 用户名里出现这两个字符会直接写坏口令文件，让 nginx 认不出用户。
	if strings.ContainsAny(a.AuthUser, ":\r\n") {
		return invalidField("访问认证用户名", "不能包含冒号或换行")
	}
	if a.AuthEnabled && strings.TrimSpace(a.AuthUser) == "" {
		return invalidField("访问认证用户名", "开启访问认证时不能为空")
	}
	if len(a.AuthPass) > 256 {
		return invalidField("访问认证密码", "长度不能超过 256")
	}
	for _, r := range a.AuthPass {
		if r == '\n' || r == '\r' || r == '"' || r == '\'' || r == '\\' || r == ';' {
			return invalidField("访问认证密码", "不能包含换行、引号或分号")
		}
	}
	return nil
}

func validateTraffic(t TrafficConf) error {
	if t.BandwidthLimit != "" && !NginxSafeBandwidth(t.BandwidthLimit) {
		return invalidField("带宽限制", fmt.Sprintf("%q 非法（形如 512k / 2m）", t.BandwidthLimit))
	}
	if t.RateLimitPerIP < 0 || t.RateLimitPerIP > 100000 {
		return invalidField("单 IP 频率限制", "取值需在 0-100000 之间")
	}
	if t.MonthlyQuotaMB < 0 {
		return invalidField("月流量配额", "不能为负数")
	}
	return nil
}

func validateSecurity(s SecurityConf) error {
	if len(s.BlockUAKeywords) > 64 {
		return invalidField("UA 屏蔽词", "最多 64 条")
	}
	for _, k := range s.BlockUAKeywords {
		if !NginxSafeRegexBody(strings.TrimSpace(k)) {
			return invalidField("UA 屏蔽词", fmt.Sprintf("%q 含非法字符（不能包含括号、引号、分号等）", k))
		}
	}
	if len(s.DenyPaths) > 64 {
		return invalidField("禁止访问的路径", "最多 64 条")
	}
	for _, p := range s.DenyPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		body := strings.TrimPrefix(p, "~")
		if body == "" || len(body) > 512 {
			return invalidField("禁止访问的路径", fmt.Sprintf("%q 非法", p))
		}
		for _, r := range body {
			if nginxUnsafeRune(r) {
				return invalidField("禁止访问的路径", fmt.Sprintf("%q 含非法字符", p))
			}
		}
	}
	if len(s.DenyExtensions) > 64 {
		return invalidField("禁止访问的扩展名", "最多 64 条")
	}
	for _, e := range s.DenyExtensions {
		if !NginxSafeIdentifier(strings.TrimPrefix(strings.TrimSpace(e), ".")) {
			return invalidField("禁止访问的扩展名", fmt.Sprintf("%q 非法", e))
		}
	}
	return nil
}

func validateHotlink(h HotlinkConf) error {
	if len(h.FileExtensions) > 64 {
		return invalidField("防盗链扩展名", "最多 64 条")
	}
	for _, e := range h.FileExtensions {
		if !NginxSafeIdentifier(strings.TrimPrefix(strings.TrimSpace(e), ".")) {
			return invalidField("防盗链扩展名", fmt.Sprintf("%q 非法", e))
		}
	}
	if len(h.AllowedDomains) > 64 {
		return invalidField("防盗链允许域名", "最多 64 条")
	}
	for _, d := range h.AllowedDomains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if err := validateRefererPattern(d); err != nil {
			return invalidField("防盗链允许域名", err.Error())
		}
	}
	if h.ReturnCode != 0 && (h.ReturnCode < 100 || h.ReturnCode > 599) {
		return invalidField("防盗链返回码", "需在 100-599 之间")
	}
	return nil
}

// refererPatternSafe 报告 p 能否作为 valid_referers 的模式。
//
// valid_referers 支持三种形态：域名、*.域名、通配符 *.example.*、
// 以及 ~ 开头的正则。这里只做"不含 nginx 语法分隔符"的检查，
// 不限制正则内容 —— 那是用户的正当需求。
//
// 校验与净化共用这个谓词（见 validateRefererPattern 与 SanitizeSite）。
func refererPatternSafe(p string) bool {
	if p == "" || len(p) > 253 {
		return false
	}
	for _, r := range p {
		if nginxUnsafeRune(r) {
			return false
		}
	}
	return true
}

// validateRefererPattern 校验 valid_referers 的模式（写入路径用，带错误信息）。
func validateRefererPattern(p string) error {
	if len(p) > 253 {
		return fmt.Errorf("%q 过长", p)
	}
	for _, r := range p {
		if nginxUnsafeRune(r) {
			return fmt.Errorf("%q 含非法字符（不能包含空格、引号、分号）", p)
		}
	}
	return nil
}

func validateSSL(s SSLConf) error {
	if s.CertFile != "" && !NginxSafeCertPath(s.CertFile) {
		return invalidField("证书路径", fmt.Sprintf("%q 非法（必须是绝对路径且不含 .. 与特殊字符）", s.CertFile))
	}
	if s.KeyFile != "" && !NginxSafeCertPath(s.KeyFile) {
		return invalidField("私钥路径", fmt.Sprintf("%q 非法（必须是绝对路径且不含 .. 与特殊字符）", s.KeyFile))
	}
	if len(s.Protocols) > 8 {
		return invalidField("TLS 协议", "最多 8 项")
	}
	for _, p := range s.Protocols {
		if !NginxSafeTLSProtocol(p) {
			return invalidField("TLS 协议", fmt.Sprintf("%q 不支持（仅 TLSv1.2 / TLSv1.3）", p))
		}
	}
	if s.HSTSMaxAge < 0 || s.HSTSMaxAge > 63072000 {
		return invalidField("HSTS 有效期", "需在 0-63072000 秒之间")
	}
	return nil
}

func validateProxy(p ProxyConf) error {
	if len(p.Rules) > 64 {
		return invalidField("反向代理规则", "最多 64 条")
	}
	for _, r := range p.Rules {
		if r.Path != "" && !NginxSafePathPrefix(r.Path) {
			return invalidField("反代路径", fmt.Sprintf("%q 非法（必须以 / 开头且不含空格、分号、引号）", r.Path))
		}
		if r.Target != "" && !NginxSafeURL(r.Target) {
			return invalidField("反代目标", fmt.Sprintf("%q 非法（需形如 http://host:port/path）", r.Target))
		}
		if r.Timeout < 0 || r.Timeout > 3600 {
			return invalidField("反代超时", "需在 0-3600 秒之间")
		}
		if r.CacheTime < 0 || r.CacheTime > 525600 {
			return invalidField("反代缓存时长", "需在 0-525600 分钟之间")
		}
	}
	return nil
}

func validateRedirects(rc RedirectConf) error {
	if len(rc.Rules) > 64 {
		return invalidField("重定向规则", "最多 64 条")
	}
	for _, r := range rc.Rules {
		if r.From == "" {
			return invalidField("重定向来源", "不能为空")
		}
		body := strings.TrimPrefix(strings.TrimSpace(r.From), "~")
		if body == "" {
			return invalidField("重定向来源", fmt.Sprintf("%q 非法", r.From))
		}
		if !strings.HasPrefix(body, "/") && !strings.HasPrefix(strings.TrimSpace(r.From), "~") {
			return invalidField("重定向来源", fmt.Sprintf("%q 必须以 / 开头（正则请用 ~ 前缀）", r.From))
		}
		for _, ch := range body {
			if nginxUnsafeRune(ch) {
				return invalidField("重定向来源", fmt.Sprintf("%q 含非法字符", r.From))
			}
		}
		if r.To == "" {
			return invalidField("重定向目标", "不能为空")
		}
		if !NginxSafeRedirectTarget(r.To) {
			return invalidField("重定向目标", fmt.Sprintf("%q 含非法字符", r.To))
		}
		switch r.Status {
		case 0, 301, 302, 307, 308:
		default:
			return invalidField("重定向状态码", fmt.Sprintf("%d 不支持（仅 301/302/307/308）", r.Status))
		}
	}
	return nil
}

func validateSubDir(s SubDirConf) error {
	if len(s.Rules) > 32 {
		return invalidField("子目录绑定", "最多 32 条")
	}
	for _, r := range s.Rules {
		if r.Path != "" && !NginxSafePathPrefix(r.Path) {
			return invalidField("子目录路径", fmt.Sprintf("%q 非法", r.Path))
		}
	}
	return nil
}

func validatePHP(p PHPConf) error {
	if p.Version != "" && !isVersionLike(p.Version) {
		return invalidField("PHP 版本", fmt.Sprintf("%q 非法（形如 8.2）", p.Version))
	}
	if p.Socket != "" {
		if !strings.HasPrefix(p.Socket, "/") {
			return invalidField("PHP socket", "必须是绝对路径")
		}
		for _, r := range p.Socket {
			if nginxUnsafeRune(r) {
				return invalidField("PHP socket", fmt.Sprintf("%q 含非法字符", p.Socket))
			}
		}
	}
	if p.MaxExecutionTime < 0 || p.MaxExecutionTime > 3600 {
		return invalidField("PHP 执行时间", "需在 0-3600 秒之间")
	}
	if p.MemoryLimit != "" {
		if !NginxSafeSize(strings.ToUpper(p.MemoryLimit)) {
			return invalidField("PHP 内存限制", fmt.Sprintf("%q 非法（形如 256M）", p.MemoryLimit))
		}
	}
	if len(p.DisableFunctions) > 64 {
		return invalidField("禁用函数", "最多 64 项")
	}
	for _, f := range p.DisableFunctions {
		if !NginxSafeIdentifier(f) {
			return invalidField("禁用函数", fmt.Sprintf("%q 非法", f))
		}
	}
	return nil
}

// isVersionLike 判断形如 8.2 / 7.4 / 8.3.1 的版本号。
func isVersionLike(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func validateExtra(e ExtraConf) error {
	if e.ClientMaxBodySize != "" && !NginxSafeSize(e.ClientMaxBodySize) {
		return invalidField("上传限制", fmt.Sprintf("%q 非法（形如 50m）", e.ClientMaxBodySize))
	}
	if e.GzipType != "" {
		for _, t := range strings.Fields(e.GzipType) {
			if !NginxSafeMimeType(t) {
				return invalidField("gzip 类型", fmt.Sprintf("%q 非法", t))
			}
		}
	}
	if len(e.StaticCacheExt) > 64 {
		return invalidField("静态缓存扩展名", "最多 64 条")
	}
	for _, x := range e.StaticCacheExt {
		if !NginxSafeIdentifier(strings.TrimPrefix(strings.TrimSpace(x), ".")) {
			return invalidField("静态缓存扩展名", fmt.Sprintf("%q 非法", x))
		}
	}
	if e.Custom404 != "" {
		p := strings.TrimSpace(e.Custom404)
		if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			return invalidField("自定义 404 页面", "必须是相对站点根目录的路径")
		}
		for _, r := range p {
			if nginxUnsafeRune(r) {
				return invalidField("自定义 404 页面", fmt.Sprintf("%q 含非法字符", p))
			}
		}
	}
	for _, c := range e.TrustedProxies {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(c)); err != nil {
			return invalidField("可信代理网段", fmt.Sprintf("%q 不是合法 CIDR", c))
		}
	}
	if e.BrowserCache < 0 || e.BrowserCache > 31536000 {
		return invalidField("浏览器缓存", "需在 0-31536000 秒之间")
	}
	if e.GzipMinLength < 0 {
		return invalidField("gzip 最小长度", "不能为负数")
	}
	for _, t := range []int{e.KeepaliveTimeout, e.SendTimeout, e.ReadTimeout} {
		if t < 0 || t > 3600 {
			return invalidField("超时设置", "需在 0-3600 秒之间")
		}
	}
	return nil
}

// NginxSafeMimeType 报告 v 是否是合法 MIME 类型（可含 / 与 + ）。
func NginxSafeMimeType(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/' || r == '-' || r == '.' || r == '+' || r == '_':
		default:
			return false
		}
	}
	return true
}

func validateLogs(l LogsConf) error {
	if l.CustomFormat != "" && !NginxSafeLogFormatName(l.CustomFormat) {
		return invalidField("日志格式名", fmt.Sprintf("%q 非法（只允许字母数字与下划线）", l.CustomFormat))
	}
	if l.RetentionDays < 0 || l.RetentionDays > 3650 {
		return invalidField("日志保留天数", "需在 0-3650 之间")
	}
	return nil
}

func validateRoot(r RootConf) error {
	if r.RunPath != "" {
		p := strings.TrimSpace(r.RunPath)
		if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			return invalidField("运行目录", "必须是相对站点根目录的路径")
		}
		for _, ch := range p {
			if nginxUnsafeRune(ch) {
				return invalidField("运行目录", fmt.Sprintf("%q 含非法字符", p))
			}
		}
	}
	return nil
}

func validateAlerts(a AlertConf) error {
	allowedLevels := map[string]bool{"info": true, "warning": true, "critical": true}
	allowedChannels := map[string]bool{"email": true, "webhook": true, "dingtalk": true, "wecom": true}

	for _, r := range a.Rules {
		if r.Type != "" && !IsValidAlertRuleType(r.Type) {
			return invalidField("告警类型", fmt.Sprintf("%q 不支持", r.Type))
		}
		if r.Level != "" && !allowedLevels[r.Level] {
			return invalidField("告警级别", fmt.Sprintf("%q 不支持", r.Level))
		}
		if r.WindowMinutes < 0 || r.WindowMinutes > 10080 {
			return invalidField("告警统计窗口", "需在 0-10080 分钟之间")
		}
	}
	for _, c := range a.Channels {
		if c.Type != "" && !allowedChannels[c.Type] {
			return invalidField("通知渠道类型", fmt.Sprintf("%q 不支持", c.Type))
		}
		target := strings.TrimSpace(c.Target)
		if target == "" {
			continue
		}
		for _, r := range target {
			if r < 0x20 || r == 0x7f {
				return invalidField("通知渠道地址", "不能包含控制字符")
			}
		}
		if c.Type == "webhook" || c.Type == "dingtalk" || c.Type == "wecom" {
			if !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
				return invalidField("通知渠道地址", "必须是 http(s) URL")
			}
		}
	}
	return nil
}

// MysqlAllowedCharsets 面板支持的 MySQL 字符集白名单。
//
// 字符集会原样拼进 `CREATE DATABASE ... CHARACTER SET <charset> COLLATE <collation>`，
// 而 DDL 不支持占位符参数化 —— 所以它必须是**固定枚举**，不能是任意字符串。
var MysqlAllowedCharsets = []string{"utf8mb4", "utf8", "latin1"}

// ValidMysqlCharset 报告 charset 是否在白名单内。
func ValidMysqlCharset(charset string) bool {
	for _, c := range MysqlAllowedCharsets {
		if charset == c {
			return true
		}
	}
	return false
}

// ValidateMysql 校验站点绑定的数据库参数。
//
// 主机固定 localhost 是产品约定，不接受客户端改（见 model.Database 注释）。
//
// 注意：这些值最终会进 MySQL 的 DDL（CREATE USER / CREATE DATABASE / GRANT），
// 而 MySQL 协议里 DDL **不支持** prepared statement 的占位符参数化，
// 只能靠字符串拼接 + 标识符转义来保证安全。所以这里必须严格白名单。
func ValidateMysql(d *Database) error {
	if d == nil {
		return nil
	}
	if d.Name != "" && !NginxSafeIdentifier(d.Name) {
		return invalidField("数据库名", fmt.Sprintf("%q 非法", d.Name))
	}
	if d.User != "" && !NginxSafeIdentifier(d.User) {
		return invalidField("数据库用户名", fmt.Sprintf("%q 非法", d.User))
	}
	if d.Port != 0 && (d.Port < 1 || d.Port > 65535) {
		return invalidField("数据库端口", "需在 1-65535 之间")
	}
	if d.Charset != "" && !ValidMysqlCharset(d.Charset) {
		return invalidField("数据库字符集", fmt.Sprintf("%q 不支持（仅 %s）",
			d.Charset, strings.Join(MysqlAllowedCharsets, " / ")))
	}
	if d.Host != "" && d.Host != "localhost" && d.Host != "127.0.0.1" {
		return invalidField("数据库地址", "仅支持 localhost")
	}
	return nil
}

// ValidateSitePaths 校验站点涉及的全部路径（写入路径用）。
//
// 面板以 root 运行，且这些路径会直接进入 nginx 配置（root / alias / log），
// 所以必须在落库前拦住：
//   - Root.Path：站点根目录，也是最敏感的（nginx root 指向哪就能对外伺服哪）
//   - SubDir.Rules[].Root：子目录绑定目标，用 alias 注入
//
// 两类检查都要做：
//   - ValidatePath 管文件系统语义（绝对路径、不许穿越、不许指向系统目录）；
//   - NginxSafeFSPath 管 nginx 文本语义（不许带空格/引号/分号，
//     否则 nginx -t 失败 → ApplyAllConfig 整体回滚 → 所有站点一起卡住）。
//
// 注意：这批路径**没有**"安全降级"语义——把非法路径替换成别的目录，
// 会变成"面板上看起来配好了、实际访问 404"的静默故障，比直接报错更难查。
// 所以路径非法一律当作硬错误，由调用方隔离到单个站点。
func ValidateSitePaths(s *Site) error {
	if s == nil {
		return invalidField("站点", "不能为空")
	}
	if err := ValidatePath(s.Root.Path); err != nil {
		return fmt.Errorf("%w: 站点根目录 %q", err, s.Root.Path)
	}
	if !NginxSafeFSPath(s.Root.Path) {
		return invalidField("站点根目录", fmt.Sprintf("%q 不能包含空格、引号、分号等特殊字符", s.Root.Path))
	}
	for _, rule := range s.SubDir.Rules {
		if err := ValidatePath(rule.Root); err != nil {
			return fmt.Errorf("%w: 子目录绑定目标 %q", err, rule.Root)
		}
		if !NginxSafeFSPath(rule.Root) {
			return invalidField("子目录绑定目标", fmt.Sprintf("%q 不能包含空格、引号、分号等特殊字符", rule.Root))
		}
	}
	return nil
}

// NginxSafeConfPath 报告路径能否安全写进 nginx 配置文本。
//
// 与 NginxSafeFSPath 的区别：
//   - 不要求绝对路径：面板数据目录在开发机上是 Windows 路径
//     （C:\...\data），那里本来就没有 nginx，不该在这里报错；
//   - 但仍然拒绝空白、引号、分号、花括号、反斜杠与控制字符——
//     它们会改变配置结构或截断指令。
//
// 路径会先做 ToSlash：nginx 只认正斜杠，
// 而 filepath.Join 在 Windows 上会产出反斜杠。
//
// 用途：面板数据目录 → htpasswd 口令文件路径 → auth_basic_user_file。
func NginxSafeConfPath(p string) bool {
	if p == "" || len(p) > 512 {
		return false
	}
	for _, r := range filepath.ToSlash(p) {
		if nginxUnsafeRune(r) {
			return false
		}
	}
	return true
}

// NginxSafeFSPath 报告 p 能否作为站点根目录 / 子目录绑定目标进入 nginx 配置。
//
// 与 ValidatePath 的区别：ValidatePath 管的是"文件系统语义"（绝对路径、
// 不许穿越、不许指向系统目录）；这里管的是"能不能原样写进 nginx 文本"——
// 带空格的路径会让 `root /www/wwwroot/my site;` 触发 nginx -t 失败，
// 而一次失败会让 ApplyAllConfig 整体回滚、**所有站点**的变更都不生效。
//
// 站点根目录里出现空格、引号、分号、花括号在真实世界里基本不存在，
// 所以这里直接拒绝而不是做转义：转义后的路径与磁盘上的真实路径不一致，
// 会变成"看起来配好了、实际 404"的静默故障。
func NginxSafeFSPath(p string) bool {
	if p == "" || len(p) > 255 {
		return false
	}
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for _, r := range p {
		if nginxUnsafeRune(r) {
			return false
		}
	}
	return true
}
