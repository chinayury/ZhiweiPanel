package model

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
)

// ---------------------------------------------------------------------------
// 渲染前的净化
//
// 写入路径（建站 / 改站）会调用 ValidateSite 拒绝非法值，但线上库里可能
// 已经存在历史脏数据（早期版本根本没有这些校验）。渲染路径若因此直接报错，
// 后果比"这一个站点配置不对"严重得多：
//
//	nginx.Render 失败 → ApplyAllConfig 整体中止 → **所有站点**的配置
//	都停在旧版本。用户改 A 站点的字段，会连带把 B、C、D 站点一起卡住，
//	而且面板不会告诉他"是你 A 站点填错了一个字符"。
//
// 所以渲染前先把站点过一遍净化：非法值丢弃或回落到安全默认值，
// 保证"一个站点的脏字段拖不垮整台面板"。
//
// 净化与校验共用 validate.go 里的同一批谓词，两边规则不会漂移——
// 不会出现"校验放行但渲染拒绝"（或反之）这种最难排查的错配。
//
// 重要：SanitizeSite **不修改原对象**。渲染是只读操作，
// 不能因为脏数据就把数据库里合法的配置改掉。
// ---------------------------------------------------------------------------

// SanitizeSite 返回 s 的安全副本。
//
// 会修改的语义：
//   - 列表类字段：非法项被丢弃，条数截断到上限；
//   - 枚举/数值类字段：非法值回落到默认值（0 / 空串 / 403）；
//   - 路径类字段（Root.Path / SubDir.Rules[].Root）：**不做替换**——
//     替换会让 nginx 服务另一个目录，变成"看起来配好了、实际 404"的静默故障。
//     它们由 ValidateSitePaths 单独判定为硬错误，由调用方隔离到单个站点。
func SanitizeSite(s *Site) *Site {
	if s == nil {
		return nil
	}
	out := *s // 浅拷贝：标量字段天然隔离
	out.Domains = append(DomainList(nil), s.Domains...)

	// ---- 站点名：写进 nginx 配置的注释行，换行会截断注释 ----
	out.Name = sanitizeLine(s.Name, 128)

	// ---- 站点 ID：参与构成 zone 名、配置文件名与备份路径 ----
	out.ID = sanitizeID(s.ID)

	// ---- 监听端口 ----
	out.ListenPorts = sanitizePorts(s.ListenPorts)

	// ---- 默认文档 ----
	out.Index.Files = sanitizeList(s.Index.Files, 16, func(v string) (string, bool) {
		return v, NginxSafeIdentifier(v)
	})

	// ---- 访问控制 ----
	out.Access.Allow = sanitizeList(s.Access.Allow, 256, func(v string) (string, bool) {
		v = strings.TrimSpace(v)
		return v, NginxSafeIP(v)
	})
	out.Access.Deny = sanitizeList(s.Access.Deny, 256, func(v string) (string, bool) {
		v = strings.TrimSpace(v)
		return v, NginxSafeIP(v)
	})
	out.Access.AllowMethods = sanitizeList(s.Access.AllowMethods, 16, func(v string) (string, bool) {
		v = strings.ToUpper(strings.TrimSpace(v))
		return v, NginxSafeHTTPMethod(v)
	})
	out.Access.AuthUser = sanitizeHTPasswdField(s.Access.AuthUser, 64)
	out.Access.AuthPass = sanitizeHTPasswdField(s.Access.AuthPass, 256)

	// ---- 流量 ----
	if !NginxSafeBandwidth(s.Traffic.BandwidthLimit) {
		out.Traffic.BandwidthLimit = ""
	}
	if s.Traffic.RateLimitPerIP < 0 || s.Traffic.RateLimitPerIP > 100000 {
		out.Traffic.RateLimitPerIP = 0
	}
	if s.Traffic.MonthlyQuotaMB < 0 {
		out.Traffic.MonthlyQuotaMB = 0
	}

	// ---- 安全 ----
	out.Security.BlockUAKeywords = sanitizeList(s.Security.BlockUAKeywords, 64, func(v string) (string, bool) {
		v = strings.TrimSpace(v)
		return v, NginxSafeRegexBody(v)
	})
	out.Security.DenyPaths = sanitizeList(s.Security.DenyPaths, 64, func(v string) (string, bool) {
		v = strings.TrimSpace(v)
		body := strings.TrimPrefix(v, "~")
		if body == "" || len(body) > 512 {
			return "", false
		}
		for _, r := range body {
			if nginxUnsafeRune(r) {
				return "", false
			}
		}
		return v, true
	})
	out.Security.DenyExtensions = sanitizeList(s.Security.DenyExtensions, 64, func(v string) (string, bool) {
		return v, NginxSafeIdentifier(strings.TrimPrefix(strings.TrimSpace(v), "."))
	})

	// ---- 防盗链 ----
	out.Hotlink.FileExtensions = sanitizeList(s.Hotlink.FileExtensions, 64, func(v string) (string, bool) {
		return v, NginxSafeIdentifier(strings.TrimPrefix(strings.TrimSpace(v), "."))
	})
	out.Hotlink.AllowedDomains = sanitizeList(s.Hotlink.AllowedDomains, 64, func(v string) (string, bool) {
		v = strings.TrimSpace(v)
		return v, refererPatternSafe(v)
	})
	if s.Hotlink.ReturnCode != 0 && (s.Hotlink.ReturnCode < 100 || s.Hotlink.ReturnCode > 599) {
		out.Hotlink.ReturnCode = 403
	}

	// ---- SSL ----
	if !NginxSafeCertPath(s.SSL.CertFile) {
		out.SSL.CertFile = ""
	}
	if !NginxSafeCertPath(s.SSL.KeyFile) {
		out.SSL.KeyFile = ""
	}
	out.SSL.Protocols = sanitizeList(s.SSL.Protocols, 8, func(v string) (string, bool) {
		return v, NginxSafeTLSProtocol(v)
	})
	if s.SSL.HSTSMaxAge < 0 || s.SSL.HSTSMaxAge > 63072000 {
		out.SSL.HSTSMaxAge = 0
	}

	// ---- 反向代理 ----
	proxyRules := make([]ProxyRule, 0, len(s.Proxy.Rules))
	for _, r := range s.Proxy.Rules {
		if r.Path != "" && !NginxSafePathPrefix(r.Path) {
			continue
		}
		if r.Target != "" && !NginxSafeURL(r.Target) {
			continue
		}
		if r.Timeout < 0 || r.Timeout > 3600 {
			r.Timeout = 0
		}
		if r.CacheTime < 0 || r.CacheTime > 525600 {
			r.CacheTime = 0
		}
		proxyRules = append(proxyRules, r)
	}
	out.Proxy.Rules = proxyRules

	// ---- 重定向 ----
	redirectRules := make([]RedirectRule, 0, len(s.Redirect.Rules))
	for _, r := range s.Redirect.Rules {
		if r.From == "" || r.To == "" {
			continue
		}
		body := strings.TrimPrefix(strings.TrimSpace(r.From), "~")
		if body == "" {
			continue
		}
		ok := true
		for _, ch := range body {
			if nginxUnsafeRune(ch) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if !NginxSafeRedirectTarget(r.To) {
			continue
		}
		switch r.Status {
		case 0, 301, 302, 307, 308:
		default:
			r.Status = 301
		}
		redirectRules = append(redirectRules, r)
	}
	out.Redirect.Rules = redirectRules

	// ---- 子目录绑定 ----
	subRules := make([]SubDirBindingRule, 0, len(s.SubDir.Rules))
	for _, r := range s.SubDir.Rules {
		if r.Path != "" && !NginxSafePathPrefix(r.Path) {
			continue
		}
		subRules = append(subRules, r)
	}
	out.SubDir.Rules = subRules

	// ---- PHP ----
	if s.PHP.Version != "" && !isVersionLike(s.PHP.Version) {
		out.PHP.Version = ""
	}
	if s.PHP.Socket != "" {
		ok := strings.HasPrefix(s.PHP.Socket, "/")
		if ok {
			for _, r := range s.PHP.Socket {
				if nginxUnsafeRune(r) {
					ok = false
					break
				}
			}
		}
		if !ok {
			out.PHP.Socket = ""
		}
	}
	if s.PHP.MaxExecutionTime < 0 || s.PHP.MaxExecutionTime > 3600 {
		out.PHP.MaxExecutionTime = 0
	}
	if s.PHP.MemoryLimit != "" && !NginxSafeSize(strings.ToUpper(s.PHP.MemoryLimit)) {
		out.PHP.MemoryLimit = ""
	}
	out.PHP.DisableFunctions = sanitizeList(s.PHP.DisableFunctions, 64, func(v string) (string, bool) {
		return v, NginxSafeIdentifier(v)
	})

	// ---- 其它设置 ----
	if s.Extra.ClientMaxBodySize != "" && !NginxSafeSize(s.Extra.ClientMaxBodySize) {
		out.Extra.ClientMaxBodySize = ""
	}
	if s.Extra.GzipType != "" {
		fields := strings.Fields(s.Extra.GzipType)
		kept := make([]string, 0, len(fields))
		for _, t := range fields {
			if NginxSafeMimeType(t) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			out.Extra.GzipType = ""
		} else {
			out.Extra.GzipType = strings.Join(kept, " ")
		}
	}
	if s.Extra.GzipMinLength < 0 {
		out.Extra.GzipMinLength = 0
	}
	out.Extra.StaticCacheExt = sanitizeList(s.Extra.StaticCacheExt, 64, func(v string) (string, bool) {
		return v, NginxSafeIdentifier(strings.TrimPrefix(strings.TrimSpace(v), "."))
	})
	out.Extra.Custom404 = sanitizeRelativePath(s.Extra.Custom404)
	out.Extra.TrustedProxies = sanitizeList(s.Extra.TrustedProxies, 64, func(v string) (string, bool) {
		v = strings.TrimSpace(v)
		_, _, err := net.ParseCIDR(v)
		return v, err == nil
	})
	if s.Extra.BrowserCache < 0 || s.Extra.BrowserCache > 31536000 {
		out.Extra.BrowserCache = 0
	}
	for _, t := range []*int{&out.Extra.KeepaliveTimeout, &out.Extra.SendTimeout, &out.Extra.ReadTimeout} {
		if *t < 0 || *t > 3600 {
			*t = 0
		}
	}

	// ---- 日志 ----
	if s.Logs.CustomFormat != "" && !NginxSafeLogFormatName(s.Logs.CustomFormat) {
		out.Logs.CustomFormat = ""
	}
	if s.Logs.RetentionDays < 0 || s.Logs.RetentionDays > 3650 {
		out.Logs.RetentionDays = 0
	}

	// ---- 运行目录 ----
	out.Root.RunPath = sanitizeRelativePath(s.Root.RunPath)

	// ---- 告警 ----
	out.Alert = sanitizeAlert(s.Alert)

	// MySQL 绑定：只保留可安全用于 SQL 标识符的部分。
	if s.Mysql != nil {
		m := *s.Mysql
		if !NginxSafeIdentifier(m.Name) {
			m.Name = ""
		}
		if !NginxSafeIdentifier(m.User) {
			m.User = ""
		}
		if !NginxSafeIdentifier(m.Charset) {
			m.Charset = ""
		}
		if m.Port < 0 || m.Port > 65535 {
			m.Port = 0
		}
		out.Mysql = &m
	}

	return &out
}

// ---------------------------------------------------------------------------
// 净化辅助
// ---------------------------------------------------------------------------

// sanitizeList 逐项净化列表：先按 fn 变换，再按 ok 判定过滤，最后截断到 max 条。
//
// fn 返回 false 的项被丢弃（而不是保留原值），因为留着非法值等于没净化。
func sanitizeList(in []string, max int, fn func(string) (string, bool)) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, it := range in {
		v, ok := fn(it)
		if !ok || v == "" {
			continue
		}
		out = append(out, v)
		if len(out) >= max {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sanitizeLine 把任意文本压成安全的单行：丢弃控制字符（含换行），
// 截断到 maxLen。
//
// 用于站点名这类"写进注释行"的字段：换行会截断注释，
// 后面那行内容会被 nginx 当成真正的指令执行。
func sanitizeLine(v string, maxLen int) string {
	var sb strings.Builder
	for _, r := range v {
		if r == '\n' || r == '\r' || r == 0x7f || (r < 0x20 && r != '\t') {
			continue
		}
		sb.WriteRune(r)
		if sb.Len() >= maxLen {
			break
		}
	}
	return strings.TrimSpace(sb.String())
}

// sanitizeID 净化站点 ID。
//
// ID 会参与构成 zone 名（limit_req_zone $binary_remote_addr zone=<id>_rate:10m），
// 而 zone 名在同一个 http 上下文里必须唯一，否则 nginx 报 duplicate zone、
// 整份配置回滚——所以这里既要保证字符安全，也要保证**不同 ID 净化后不撞车**。
//
// 做法：保留标识符字符（字母数字、下划线、短横线）；若发生改写，
// 再追加原 ID 的短摘要，保证"改写后的值"与"原值"一一对应。
func sanitizeID(v string) string {
	var sb strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == '_' || r == '-':
			sb.WriteRune(r)
		}
		if sb.Len() >= 32 {
			break
		}
	}
	out := sb.String()
	if out == v {
		return out
	}
	// 发生过改写：追加原值的摘要，避免两个不同 ID 净化后撞成同一个 zone 名。
	if out == "" {
		out = "x"
	}
	sum := sha256.Sum256([]byte(v))
	return out + "-" + hex.EncodeToString(sum[:])[:8]
}

// sanitizePorts 过滤非法端口并去重。
func sanitizePorts(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(in))
	out := make([]int, 0, len(in))
	for _, p := range in {
		if p < 1 || p > 65535 {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sanitizeHTPasswdField 净化 HTTP Basic Auth 的用户名 / 密码。
//
// 这两个值不进 nginx 配置文本，而是写进 htpasswd 文件。
// 但仍要拒绝换行（会破坏 htpasswd 的行结构）与冒号（htpasswd 的分隔符），
// 否则用户可以在密码里塞一行新条目。
func sanitizeHTPasswdField(v string, maxLen int) string {
	var sb strings.Builder
	for _, r := range v {
		if r == '\n' || r == '\r' || r == ':' || r == 0 || r == 0x7f {
			continue
		}
		sb.WriteRune(r)
		if sb.Len() >= maxLen {
			break
		}
	}
	return strings.TrimSpace(sb.String())
}

// sanitizeRelativePath 净化"相对站点根目录"的路径（自定义 404、运行目录）。
//
// 必须相对（不能以 / 开头）、不能含 ..（不能跳出站点根）、
// 且不含 nginx 语法字符（会被原样写进 error_page / fastcgi_param）。
func sanitizeRelativePath(v string) string {
	p := strings.TrimSpace(v)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return ""
	}
	for _, r := range p {
		if nginxUnsafeRune(r) {
			return ""
		}
	}
	return p
}

// sanitizeAlert 净化告警配置（类型 / 级别 / 渠道地址）。
func sanitizeAlert(a AlertConf) AlertConf {
	allowedLevels := map[string]bool{"info": true, "warning": true, "critical": true}
	allowedChannels := map[string]bool{"email": true, "webhook": true, "dingtalk": true, "wecom": true}

	rules := make([]AlertRule, 0, len(a.Rules))
	for _, r := range a.Rules {
		if r.Type != "" && !IsValidAlertRuleType(r.Type) {
			continue
		}
		if r.Level != "" && !allowedLevels[r.Level] {
			continue
		}
		if r.WindowMinutes < 0 || r.WindowMinutes > 10080 {
			r.WindowMinutes = 0
		}
		rules = append(rules, r)
	}

	channels := make([]AlertChannel, 0, len(a.Channels))
	for _, c := range a.Channels {
		if c.Type != "" && !allowedChannels[c.Type] {
			continue
		}
		target := strings.TrimSpace(c.Target)
		if target != "" {
			bad := false
			for _, r := range target {
				if r < 0x20 || r == 0x7f {
					bad = true
					break
				}
			}
			if bad {
				continue
			}
			if c.Type == "webhook" || c.Type == "dingtalk" || c.Type == "wecom" {
				if !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
					continue
				}
			}
		}
		c.Target = target
		channels = append(channels, c)
	}

	a.Rules = rules
	a.Channels = channels
	return a
}
