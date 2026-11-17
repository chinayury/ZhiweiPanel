// Package model 定义面板的核心领域模型。
// 一个 Site 聚合了用户要求的全部 16 项功能配置，渲染器负责把它翻译成 Nginx 配置。
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// SiteStatus 站点状态。
type SiteStatus string

const (
	SiteStatusRunning  SiteStatus = "running"
	SiteStatusStopped  SiteStatus = "stopped"
	SiteStatusError    SiteStatus = "error"
	SiteStatusDisabled SiteStatus = "disabled"
)

// Site 是一个站点的完整定义。
type Site struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    SiteStatus `json:"status"`
	Remark    string     `json:"remark"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Edate 站点到期时间（v0.2.0 借鉴宝塔 edate）。
	// nil/零值表示无到期；到期前 7 天列表标黄，过期后标红。
	Edate *time.Time `json:"edate,omitempty"`

	// ListenPorts 站点对外监听端口（v0.2.1 借鉴宝塔"添加站点"可选非标端口）。
	// 默认 [80]，启用 SSL 时自动 append 443；用户可改：
	//   - 单端口（如 8080：80 被其它面板占时）
	//   - 多端口（如 [80, 8080]：兼容老用户用 8080 访问）
	// 留空 = 默认 [80]。
	// 面板启动 flag -public-port 启用全局对外端口（覆盖所有站点）。
	// 出于实际运维考虑：
	//   - 非标端口必须云厂商安全组 + 防火墙同时放行
	//   - 同一端口只能给一个站点（nginx 监听冲突）
	//   - 推荐做法：80/443 上挂主站点，非标端口的"次要服务"用反代指向 127.0.0.1:<port>
	ListenPorts []int `json:"listen_ports"`

	Domains DomainList  `json:"domains"`
	SubDir  SubDirConf  `json:"sub_dir"`
	Root    RootConf    `json:"root"`
	Access  AccessConf  `json:"access"`
	Traffic TrafficConf `json:"traffic"`
	Rewrite RewriteConf `json:"rewrite"`
	Index   IndexConf   `json:"index"`
	RawConf string      `json:"raw_conf"`
	// ExtraConf 站点级「自定义 Nginx 配置」片段：用户手写的 nginx 指令，原样注入。
	// ExtraConfPos 控制注入位置：inside=server 块内 / outside=server 块后（文件末尾，
	// 供 upstream / limit_req_zone 等 http 上下文指令用）。留空按 inside 处理。
	// 与 RawConf 的关系：RawConf 是早期「仅 server 块内、固定末尾」的自定义配置，
	// 保留作读回退（旧库兼容）；渲染时 ExtraConf 为空则回退到 RawConf（inside）。
	ExtraConf    string       `json:"extra_conf"`
	ExtraConfPos string       `json:"extra_conf_pos"`
	SSL          SSLConf      `json:"ssl"`
	PHP          PHPConf      `json:"php"`
	Redirect     RedirectConf `json:"redirect"`
	Proxy        ProxyConf    `json:"proxy"`
	Hotlink      HotlinkConf  `json:"hotlink"`
	Lock         LockConf     `json:"lock"`
	Security     SecurityConf `json:"security"`
	Logs         LogsConf     `json:"logs"`
	Alert        AlertConf    `json:"alert"`
	Extra        ExtraConf    `json:"extra"`

	// Mysql 站点级 MySQL/MariaDB 数据库绑定。
	// nil 表示该站点不绑定数据库；非 nil 时面板在 CreateSite/DeleteSite
	// 联动调用 MySQL 驱动建/删 database+user+grant。
	// Password 在创建时一次性返回给用户（仅这一次明文），落库后用
	// model.MaskDatabase 过滤再下发，避免前端误持久化。
	Mysql *Database `json:"mysql,omitempty"`

	Cert *CertRef `json:"cert,omitempty"`
}

// DomainList 域名列表，支持多域名。
type DomainList []string

// ValidatePath 校验站点相关路径（站点根目录、子目录绑定目标、受保护文件）。
//
// 面板以 root 运行，路径校验不是可选项：
//   - 必须绝对路径，否则相对路径会随面板工作目录漂移；
//   - 禁止 ".."，否则可穿越到 /etc、/root 等任意目录并对外服务；
//   - 禁止指向 "/" 与常见系统目录，避免把整个文件系统暴露成静态站；
//   - 禁止 NUL 与控制字符，避免注入 nginx 配置或让 os 调用行为异常。
//
// 返回 nil 表示合法；否则为 ErrInvalidPath。
func ValidatePath(p string) error {
	if p == "" {
		return ErrInvalidPath
	}
	// 控制字符（含 NUL）一律拒绝
	for _, r := range p {
		if r == 0 || r < 0x20 || r == 0x7f {
			return ErrInvalidPath
		}
	}
	// 统一分隔符后再判断，兼容 Windows 风格输入
	clean := filepath.ToSlash(filepath.Clean(p))
	if !strings.HasPrefix(clean, "/") {
		return ErrInvalidPath
	}
	// Clean 后仍含 ".." 说明原始输入就是穿越路径
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return ErrInvalidPath
		}
	}
	if clean == "/" {
		return ErrInvalidPath
	}
	// 禁止把系统目录当站点根
	for _, bad := range []string{
		"/etc", "/root", "/boot", "/dev", "/proc", "/sys", "/run",
		"/bin", "/sbin", "/lib", "/lib64", "/usr", "/var/log",
	} {
		if clean == bad || strings.HasPrefix(clean, bad+"/") {
			return ErrInvalidPath
		}
	}
	// 超长路径在文件系统与 nginx 配置里都可能出问题
	if len(clean) > 255 {
		return ErrInvalidPath
	}
	return nil
}

// ValidateListenPorts 校验站点对外监听端口。
//
// 规则（v0.2.1）：
//   - 留空 = 默认 [80]
//   - 每端口 1-65535
//   - 同一站点端口数 ≤ 8（防滥用）
//   - 不接受 0、负数、>65535
//   - 端口重复自动去重
//
// 全局对外端口（-public-port）由 main.go 在 render 前覆盖本字段，
// 该函数只校验 site 自身声明的端口列表。
func ValidateListenPorts(ports []int) ([]int, error) {
	if len(ports) == 0 {
		return []int{80}, nil
	}
	if len(ports) > 8 {
		return nil, errors.New("单站点最多 8 个端口")
	}
	seen := make(map[int]struct{}, len(ports))
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("端口 %d 非法（1-65535）", p)
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("端口列表为空")
	}
	return out, nil
}

// ValidateID 校验站点 ID。
//
// ID 会直接参与构成 nginx 配置文件名（10-site-<id>.conf）与备份目录路径，
// 因此必须是服务端生成的十六进制串。允许客户端传值是危险的：
// filepath.Join 会规范化 ".."，反而帮助 ".." 穿越生效，
// 可把渲染结果写到 conf.d 之外并被主配置 include，形成持久的 nginx 注入。
func ValidateID(id string) error {
	if id == "" {
		return ErrBadRequest
	}
	if len(id) < 8 || len(id) > 64 {
		return ErrBadRequest
	}
	for _, r := range id {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			return ErrBadRequest
		}
	}
	return nil
}

// SanitizeForRead 返回脱敏副本：Mysql.Password 与访问认证口令置空，其它字段保留。
// 用于 GET /api/sites、GET /api/sites/{id} 等读路径，避免明文密码
// 通过列表/详情接口泄漏。返回的 *Site 是浅拷贝，调用方安全修改。
func (s *Site) SanitizeForRead() *Site {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Mysql != nil {
		cp.Mysql = s.Mysql.SafeCopy()
	}
	cp.Access.AuthPass = ""
	cp.Access.AuthHash = ""
	return &cp
}

// SanitizeAccessSecrets 返回清掉了访问认证口令的副本。
//
// 与 SanitizeForRead 的区别：这里**保留** Mysql.Password。
// 建库成功时密码要一次性回显给用户（用户拿不到第二次），
// 但访问认证口令无论如何都不能回显。
func (s *Site) SanitizeAccessSecrets() *Site {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Access.AuthPass = ""
	cp.Access.AuthHash = ""
	return &cp
}

// Database 站点绑定的 MySQL/MariaDB 数据库。
//
// 命名规则（创建站点时由面板推导，不让用户手填）：
//   - Name: site_<siteid 前 12 位 hex>     ——保证短+唯一+不可猜
//   - User: site_<siteid 前 12 位 hex>     ——同名同名，简化心智
//   - Host: 固定 localhost                 ——拒绝远程连接，最小暴露面
//   - Port: 默认 3306                      ——MariaDB 默认
//   - Charset: utf8mb4                     ——正确支持 emoji 与中文
//   - Password: 24 位十六进制随机串        ——由面板在 CreateSite 时生成
//
// 创建后 Password 仅在 HTTP 响应里明文返回一次（让用户记下），落库后通过
// MaskDatabase 返回脱敏版本供常规 API 调用。再次查看密码需要走一次性
// "重置密码" 流程。
type Database struct {
	Name string `json:"name"`
	User string `json:"user"`
	// Password 在 Service.CreateSite 后由面板生成并落库。
	// API 层在 GET/List 路径上调用 SafeCopy 把 Password 置空再序列化；
	// POST 创建响应里允许明文回显（标 show_password=true 提示用户保存）。
	// 任意后续读路径都看不到明文。重置密码走独立端点。
	Password string `json:"password,omitempty"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Charset  string `json:"charset"`
}

// SafeCopy 返回脱敏副本：Name/User/Host/Port/Charset 保留，Password 置空。
// 用于 ListSites / GetSite 等常规 API 响应，避免密码因日志或前端缓存泄漏。
// 创建响应的"一次性"明文密码不走这个脱敏——它显式标注 "show_password: true"。
func (d *Database) SafeCopy() *Database {
	if d == nil {
		return nil
	}
	cp := *d
	cp.Password = ""
	return &cp
}

// MaskDatabase 与 SafeCopy 区别：保留 Password 的长度与前后 2 字符，
// 方便用户识别"是不是同一个密码"而不会泄漏中间部分。
// 形如 "ab**********cd"，用于详情页"已设密码"指示。
func (d *Database) Masked() *Database {
	if d == nil {
		return nil
	}
	cp := *d
	if len(d.Password) <= 4 {
		cp.Password = "****"
	} else {
		cp.Password = d.Password[:2] + strings.Repeat("*", len(d.Password)-4) + d.Password[len(d.Password)-2:]
	}
	return &cp
}

// Primary 返回主域名（列表第一个）。
func (d DomainList) Primary() string {
	if len(d) == 0 {
		return ""
	}
	return d[0]
}

// All 返回全部域名的 server_name 拼接串。
func (d DomainList) All() string {
	if len(d) == 0 {
		return "_"
	}
	return strings.Join(d, " ")
}

// Validate 校验域名合法性。
func (d DomainList) Validate() error {
	for _, raw := range d {
		v := strings.TrimSpace(raw)
		if v == "" {
			return ErrInvalidDomain
		}
		if len(v) > 253 {
			return ErrInvalidDomain
		}
		// 允许通配符域名 *.example.com
		body := strings.TrimPrefix(v, "*.")
		for _, label := range strings.Split(body, ".") {
			if label == "" {
				return ErrInvalidDomain
			}
			for _, r := range label {
				isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
				if !isAlnum && r != '-' && r != '_' {
					return ErrInvalidDomain
				}
			}
		}
	}
	return nil
}

// SubDirConf 子目录绑定：把某个子路径指向独立的站点根目录。
type SubDirConf struct {
	Enabled bool                `json:"enabled"`
	Rules   []SubDirBindingRule `json:"rules"`
}

// SubDirBindingRule 单条子目录绑定规则。
type SubDirBindingRule struct {
	Path string `json:"path"` // 站点内路径，如 /blog
	Root string `json:"root"` // 实际目录
}

// RootConf 网站目录相关配置。
type RootConf struct {
	Path    string `json:"path"`
	User    string `json:"user"`
	Group   string `json:"group"`
	Symlink bool   `json:"symlink"`
	// RunPath 运行目录（v0.2.0 借鉴宝塔）。
	// 相对 root.path，用于 fastcgi_param SCRIPT_FILENAME 实际指向的目录。
	// 典型场景：Laravel/ThinkPHP 把 public/ 作为对外入口，
	// root.path = /www/wwwroot/foo，run_path = "public"。
	// 留空表示与 root.path 相同。
	RunPath string `json:"run_path"`
}

// AccessConf 访问限制。
type AccessConf struct {
	Enabled bool     `json:"enabled"`
	Allow   []string `json:"allow"` // IP 白名单，空表示不限制
	Deny    []string `json:"deny"`  // IP 黑名单
	// AuthEnabled 开启后需要 HTTP Basic Auth 才能访问。
	AuthEnabled bool `json:"auth_enabled"`
	// AuthUser 认证用户名。
	AuthUser string `json:"auth_user"`
	// AuthPass 是**只写**字段：创建/更新时提交明文口令，
	// 服务端立刻换成 apr1 哈希写进 AuthHash，
	// 明文既不入库、也不回显、也不落任何文件。
	//
	// 为什么不直接存明文：这个字段会随站点 JSON 一起存进 SQLite，
	// 而 GET /api/sites/{id} 会把整份站点配置返回给前端——
	// 只要 GET 回显了明文，任何一个能读站点接口的账号（含只读 API Token）
	// 就能拿到全站访问密码。早期的实现正是这么做的。
	AuthPass string `json:"auth_pass"`
	// AuthHash 存 apr1 哈希（$apr1$...），**只由服务端维护**：
	// 客户端提交的值一律忽略（否则可以塞明文或弱哈希做降级）。
	// 哈希本身不回显前端（见 Site.SanitizeForRead）。
	AuthHash string `json:"auth_hash"`
	// AllowMethods 允许的 HTTP 方法，空表示全部允许。
	AllowMethods []string `json:"allow_methods"`
}

// TrafficConf 流量限制。
type TrafficConf struct {
	// BandwidthLimit 单连接限速，如 512k / 2m，为空表示不限。
	BandwidthLimit string `json:"bandwidth_limit"`
	// MonthlyQuotaMB 月流量配额（MB），0 表示不限。超出后触发告警。
	MonthlyQuotaMB int64 `json:"monthly_quota_mb"`
	// EnforceQuota 为 true 时超额直接拒绝访问，否则仅告警。
	EnforceQuota bool `json:"enforce_quota"`
	// RateLimitPerIP 单 IP 请求频率限制，如 10r/s，0 表示不限。
	RateLimitPerIP int `json:"rate_limit_per_ip"`
}

// RewriteConf 伪静态配置。
type RewriteConf struct {
	Enabled bool   `json:"enabled"`
	Rules   string `json:"rules"` // 原始 rewrite 指令块
	// File 伪静态配置文件名（对应 conf/rewrite/<site>.conf）。
	File string `json:"file"`
}

// IndexConf 默认文档。
type IndexConf struct {
	Files []string `json:"files"`
}

// SSLConf SSL 配置。
type SSLConf struct {
	Enabled    bool   `json:"enabled"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	ForceHTTPS bool   `json:"force_https"`
	HSTS       bool   `json:"hsts"`
	HSTSMaxAge int    `json:"hsts_max_age"`
	// Protocol TLS 版本，如 TLSv1.2 TLSv1.3。
	Protocols []string `json:"protocols"`
	// OCSP stapling。
	OCSP bool `json:"ocsp"`
	// UseHTTP2 是否开启 HTTP/2。
	UseHTTP2 bool `json:"use_http2"`
}

// PHPConf PHP 配置。
type PHPConf struct {
	Enabled bool   `json:"enabled"`
	Version string `json:"version"` // 8.2 / 7.4 等
	// Socket PHP-FPM unix socket 路径，留空则按版本推导。
	Socket string `json:"socket"`
	// MaxExecutionTime PHP 执行时间上限（秒）。
	MaxExecutionTime int `json:"max_execution_time"`
	// MemoryLimit 内存限制，如 256M。
	MemoryLimit string `json:"memory_limit"`
	// DisableFunctions 禁用的 PHP 函数。
	DisableFunctions []string `json:"disable_functions"`
	// CustomPHPConfig 追加到 php.ini 的自定义内容。
	CustomPHPConfig string `json:"custom_php_config"`
}

// RedirectConf 重定向配置。
type RedirectConf struct {
	Rules []RedirectRule `json:"rules"`
}

// RedirectRule 单条重定向规则。
type RedirectRule struct {
	From   string `json:"from"`   // 来源路径或域名，支持正则前缀 ~
	To     string `json:"to"`     // 目标
	Status int    `json:"status"` // 301 / 302
	Enable bool   `json:"enable"`
}

// ProxyConf 反向代理配置。
type ProxyConf struct {
	Enabled bool        `json:"enabled"`
	Rules   []ProxyRule `json:"rules"`
}

// ProxyRule 单条反向代理规则。
type ProxyRule struct {
	Path        string `json:"path"`   // 匹配路径前缀，如 /api
	Target      string `json:"target"` // 上游地址，如 http://127.0.0.1:3000
	WebSocket   bool   `json:"websocket"`
	Enabled     bool   `json:"enabled"`
	SNI         bool   `json:"sni"`
	Timeout     int    `json:"timeout"` // 超时秒数，0 用默认 60
	CacheEnable bool   `json:"cache_enable"`
	CacheTime   int    `json:"cache_time"`
}

// HotlinkConf 防盗链配置。
type HotlinkConf struct {
	Enabled bool `json:"enabled"`
	// AllowedDomains 允许的来源域名。
	AllowedDomains []string `json:"allowed_domains"`
	// FileExtensions 受防盗链保护的文件扩展名。
	FileExtensions []string `json:"file_extensions"`
	// ReturnCode 触发防盗链时的响应码，默认 403。
	ReturnCode int `json:"return_code"`
}

// LockConf 防复改配置：检测并阻止核心文件被篡改。
type LockConf struct {
	Enabled bool `json:"enabled"`
	// Files 受保护的文件列表。
	Files []string `json:"files"`
	// AutoRestore 检测到篡改时自动从备份恢复。
	AutoRestore bool `json:"auto_restore"`
	// AlertOnTamper 篡改时触发告警。
	AlertOnTamper bool `json:"alert_on_tamper"`
}

// SecurityConf 网站安全配置。
type SecurityConf struct {
	// EnableHTTPSForce 已在 SSLConf 中，此处为站点级安全开关集合。
	BlockSQLi       bool     `json:"block_sqli"`
	BlockXSS        bool     `json:"block_xss"`
	BlockTraversal  bool     `json:"block_traversal"`
	BlockUA         bool     `json:"block_ua"`
	BlockUAKeywords []string `json:"block_ua_keywords"`
	// DenyPaths 禁止访问的路径片段。
	DenyPaths []string `json:"deny_paths"`
	// DenyExtensions 禁止访问的文件扩展名。
	DenyExtensions []string `json:"deny_extensions"`
	// ServerTokensOff 隐藏 nginx 版本号。
	ServerTokensOff bool `json:"server_tokens_off"`
	// PreventIframe 禁止被 iframe 嵌套。
	PreventIframe bool `json:"prevent_iframe"`
}

// LogsConf 网站日志配置。
type LogsConf struct {
	AccessLog bool `json:"access_log"`
	ErrorLog  bool `json:"error_log"`
	// CustomFormat 自定义 log_format 名，为空使用默认。
	CustomFormat string `json:"custom_format"`
	// RetentionDays 日志保留天数，0 表示不自动清理。
	RetentionDays int `json:"retention_days"`
	// DisableLog 关闭日志记录。
	DisableLog bool `json:"disable_log"`
}

// AlertConf 网站告警配置。
type AlertConf struct {
	Enabled bool `json:"enabled"`
	// Rules 告警规则。
	Rules []AlertRule `json:"rules"`
	// Channels 通知渠道。
	Channels []AlertChannel `json:"channels"`
}

// AlertRule 单条告警规则。
type AlertRule struct {
	Type  string  `json:"type"`  // 见 AlertRuleTypes
	Level string  `json:"level"` // info / warning / critical
	Value float64 `json:"value"` // 阈值
	// WindowMinutes 统计窗口（分钟）。
	WindowMinutes int  `json:"window_minutes"`
	Enabled       bool `json:"enabled"`
}

// AlertRuleType 描述一种告警规则类型。
type AlertRuleType struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Unit        string `json:"unit,omitempty"`
}

// AlertRuleTypes 站点告警规则的**权威类型清单**。
//
// 这里必须与 internal/alert/checker.go 实际执行的检查一一对应：
// 清单多一项，用户就能选到一个永远不会触发的规则；
// 清单少一项，用户就配不出真正有用的规则。
//
// 三方枚举（前端下拉 / 后端检查器 / 本清单）曾经各不一致：
// 前端写的是 status_5xx / status_4xx / traffic_quota / cert_expire /
// file_tamper / site_down / response_time，其中
//   - status_4xx 与 response_time 后端根本不认识 → 规则永远不触发；
//   - cert_expire 与 file_tamper 只是与后端的 cert_expiry / tamper
//     差一个字 → 静默失效，最难发现的一类；
//   - 后端支持的 nginx_config 前端压根选不到。
//
// 现在以本清单为唯一事实来源：后端校验用它，
// GET /api/alert-types 把它暴露给前端，前端动态渲染下拉框——
// 不再各写一份，也就不会再漂移。
var AlertRuleTypes = []AlertRuleType{
	{
		Value:       "status_5xx",
		Label:       "5xx 错误数",
		Description: "统计窗口内 5xx 响应数量超过阈值时告警",
		Unit:        "次",
	},
	{
		Value:       "traffic_quota",
		Label:       "流量超额",
		Description: "站点月流量配额用尽时告警",
		Unit:        "MB",
	},
	{
		Value:       "cert_expiry",
		Label:       "证书临期",
		Description: "站点证书距离到期不足阈值天数时告警",
		Unit:        "天",
	},
	{
		Value:       "site_down",
		Label:       "站点不可访问",
		Description: "站点探测失败（连不通或返回 5xx）时告警",
	},
	{
		Value:       "tamper",
		Label:       "文件被篡改",
		Description: "受保护文件的哈希与基线不一致时告警",
		Unit:        "个",
	},
	{
		Value:       "nginx_config",
		Label:       "nginx 配置异常",
		Description: "nginx 配置检测失败时告警",
	},
	{
		Value:       "config_drift",
		Label:       "配置漂移",
		Description: "nginx 配置被面板以外的途径修改（与最近一次成功下发的快照不一致）时告警",
	},
}

// IsValidAlertRuleType 报告 t 是否是受支持的告警规则类型。
func IsValidAlertRuleType(t string) bool {
	for _, it := range AlertRuleTypes {
		if it.Value == t {
			return true
		}
	}
	return false
}

// AlertChannel 通知渠道。
type AlertChannel struct {
	Type    string `json:"type"` // email / webhook / dingtalk / wecom
	Target  string `json:"target"`
	Enabled bool   `json:"enabled"`
}

// ExtraConf 其他设置。
type ExtraConf struct {
	// Gzip 压缩。
	Gzip          bool   `json:"gzip"`
	GzipType      string `json:"gzip_type"`
	GzipMinLength int    `json:"gzip_min_length"`
	// ClientMaxBodySize 上传限制，如 50m。
	ClientMaxBodySize string `json:"client_max_body_size"`
	// KeepaliveTimeout / SendTimeout / ReadTimeout 单位秒，0 用默认。
	KeepaliveTimeout int `json:"keepalive_timeout"`
	SendTimeout      int `json:"send_timeout"`
	ReadTimeout      int `json:"read_timeout"`
	// DNSResolver 反代场景下的 DNS 解析器。
	DNSResolver string `json:"dns_resolver"`
	// TrustedProxies 可信反代/CDN 的 CIDR 列表。
	// 只有列出网段才会开启 real_ip 处理；留空表示不信任任何代理，
	// 避免 set_real_ip_from 0.0.0.0/0 让 X-Forwarded-For 可伪造。
	TrustedProxies []string `json:"trusted_proxies"`
	// BrowserCache 浏览器缓存秒数。
	BrowserCache int `json:"browser_cache"`
	// StaticCacheExt 静态文件缓存扩展名。
	StaticCacheExt []string `json:"static_cache_ext"`
	// Custom404 自定义 404 页面（v0.2.0 借鉴宝塔）。
	// 相对 root.path 解析，如 "404.html" 或 "errors/404.php"。
	// 留空 = nginx 默认 404。
	Custom404 string `json:"custom_404"`
}

// CertRef 站点绑定的证书。
type CertRef struct {
	CertID    string    `json:"cert_id"`
	Domains   []string  `json:"domains"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Issuer    string    `json:"issuer"`
	Source    string    `json:"source"` // letsencrypt / manual / imported
}

// UnmarshalJSON 容忍空字符串时间字段。
//
// 背景（2026-11-01 线上故障）：前端站点详情页曾给"未绑定证书"的站点
// 回填 {not_before:"", not_after:""} 的默认 cert 分组。空字符串无法
// 解码进 time.Time，导致这类站点的每次 PUT 都在 bindSite 阶段 400——
// 详情页永远无法保存，且与用户所改的字段无关（该站点连续 9 次 PUT
// 全部 400，根因即此）。前端已修（不再回填该分组），这里做后端兜底：
// 任意客户端（API Token、脚本、未来的前端回归）提交空时间字符串时，
// 归一为零值时间——与"没有该字段"表达同一件事。
//
// 非空但非法的值仍然报错：不放松对真实脏数据的拦截。
func (c *CertRef) UnmarshalJSON(data []byte) error {
	// 影子结构：时间字段用 *string 接住，避免 stdlib 对 "" 直接报错。
	var shadow struct {
		CertID    string   `json:"cert_id"`
		Domains   []string `json:"domains"`
		NotBefore *string  `json:"not_before"`
		NotAfter  *string  `json:"not_after"`
		Issuer    string   `json:"issuer"`
		Source    string   `json:"source"`
	}
	if err := json.Unmarshal(data, &shadow); err != nil {
		return err
	}
	c.CertID = shadow.CertID
	c.Domains = shadow.Domains
	c.Issuer = shadow.Issuer
	c.Source = shadow.Source
	// 空串 / 缺失 / null 都归一为零值时间；非空值必须能按 RFC3339 解析
	// （Go time.Time 的默认序列化形态，含纳秒）。
	if p := shadow.NotBefore; p != nil && *p != "" {
		t, err := time.Parse(time.RFC3339, *p)
		if err != nil {
			return fmt.Errorf("not_before: %w", err)
		}
		c.NotBefore = t
	}
	if p := shadow.NotAfter; p != nil && *p != "" {
		t, err := time.Parse(time.RFC3339, *p)
		if err != nil {
			return fmt.Errorf("not_after: %w", err)
		}
		c.NotAfter = t
	}
	return nil
}

// IsExpiring 判断证书是否在 days 天内到期。
func (c *CertRef) IsExpiring(days int) bool {
	if c == nil || c.NotAfter.IsZero() {
		return false
	}
	return time.Now().AddDate(0, 0, days).After(c.NotAfter)
}

// DaysLeft 剩余有效天数。
func (c *CertRef) DaysLeft() int {
	if c == nil || c.NotAfter.IsZero() {
		return 0
	}
	return int(time.Until(c.NotAfter).Hours() / 24)
}

// Cert 证书实体。
type Cert struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Domains   []string  `json:"domains"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Issuer    string    `json:"issuer"`
	Source    string    `json:"source"`
	CertFile  string    `json:"cert_file"`
	KeyFile   string    `json:"key_file"`
	// AutoRenew 自动续签。
	AutoRenew bool `json:"auto_renew"`
	// ACMEAccount 绑定的 ACME 账户。
	ACMEAccount string `json:"acme_account"`
	// DNSProvider DNS-01 提供商 key（alidns/dnspod/tencentcloud/cloudflare）。
	// 仅 Method=dns 时非空。
	DNSProvider string `json:"dns_provider"`
	// DNSConfig DNS-01 凭据。续签时必须用它重建 provider，
	// 否则通配符证书到期后无法自动续期。
	// 注意：这是敏感数据，落库前调用方应确保数据目录权限为 0700。
	DNSConfig map[string]string `json:"dns_config"`
	// Method 验证方式：http / dns。
	Method    string    `json:"method"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DaysLeft 剩余有效天数。
func (c *Cert) DaysLeft() int {
	if c.NotAfter.IsZero() {
		return 0
	}
	return int(time.Until(c.NotAfter).Hours() / 24)
}

// ACMEAccount ACME 账户。
type ACMEAccount struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CA        string    `json:"ca"` // letsencrypt / letsencrypt_staging
	KeyType   string    `json:"key_type"`
	CreatedAt time.Time `json:"created_at"`
	// KeyPath 账户私钥存储路径。
	KeyPath string `json:"key_path"`
}

// Alert 告警记录。
type Alert struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id"`
	SiteName  string    `json:"site_name"`
	Type      string    `json:"type"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
	// Resolved 是否已处理。
	Resolved bool `json:"resolved"`
}

// LogRecord 一条访问日志记录。
type LogRecord struct {
	RemoteAddr string    `json:"remote_addr"`
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Protocol   string    `json:"protocol"`
	Status     int       `json:"status"`
	Size       int64     `json:"size"`
	Referer    string    `json:"referer"`
	UA         string    `json:"ua"`
	ReqTime    float64   `json:"req_time"`
	// Host 虚拟主机名。
	Host string `json:"host"`
	// UpstreamTime 上游响应时间。
	UpstreamTime string `json:"upstream_time"`
}

// TrafficStat 流量统计。
type TrafficStat struct {
	Date      string `json:"date"` // YYYY-MM-DD
	SiteID    string `json:"site_id"`
	Requests  int64  `json:"requests"`
	Bytes     int64  `json:"bytes"`
	Status2xx int64  `json:"status_2xx"`
	Status3xx int64  `json:"status_3xx"`
	Status4xx int64  `json:"status_4xx"`
	Status5xx int64  `json:"status_5xx"`
	UniqueIPs int64  `json:"unique_ips"`
}

// SiteSummary 站点列表页的汇总信息。
type SiteSummary struct {
	Site        *Site `json:"site"`
	TodayReqs   int64 `json:"today_reqs"`
	TodayBytes  int64 `json:"today_bytes"`
	MonthBytes  int64 `json:"month_bytes"`
	CertDays    int   `json:"cert_days"`
	TamperCount int   `json:"tamper_count"`
	OpenAlerts  int   `json:"open_alerts"`
}

// ---------- v0.2.0: 计划任务 ----------

// CronJobType 计划任务类型。
type CronJobType string

const (
	CronShell      CronJobType = "shell"       // 自由 shell
	CronBackupSite CronJobType = "backup_site" // 备份站点
	CronBackupDB   CronJobType = "backup_db"   // 备份数据库
	CronLogSplit   CronJobType = "log_split"   // 切割访问日志
	CronReleaseMem CronJobType = "release_mem" // 释放内存
	CronAccessURL  CronJobType = "access_url"  // 访问 URL
)

// CronJobStatus 任务执行状态。
type CronJobStatus string

const (
	CronStatusIdle    CronJobStatus = ""        // 从未执行
	CronStatusRunning CronJobStatus = "running" // 正在跑
	CronStatusOK      CronJobStatus = "ok"      // 上次成功
	CronStatusFailed  CronJobStatus = "failed"  // 上次失败
)

// CronJob 一条计划任务。
//
// 字段命名沿用 store 表结构，避免反复 map。
// Payload 是 JSON 字符串，结构因 Type 而异（见 cron.PayloadSpec）。
type CronJob struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Type       CronJobType   `json:"type"`
	Enabled    bool          `json:"enabled"`
	Schedule   string        `json:"schedule"` // 5 段 crontab 表达式
	Target     string        `json:"target"`   // 关联对象：site_id / db name / url / 空
	Payload    string        `json:"payload"`  // JSON
	LastRunAt  *time.Time    `json:"last_run_at,omitempty"`
	LastStatus CronJobStatus `json:"last_status"`
	LastMsg    string        `json:"last_msg"`
	NextRunAt  *time.Time    `json:"next_run_at,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

// CronRun 单次执行历史。
type CronRun struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Status    string    `json:"status"` // ok / failed
	Message   string    `json:"message"`
}

// ---------- v0.2.0: 备份 ----------

// BackupKind 备份种类。
type BackupKind string

const (
	BackupSite     BackupKind = "site"
	BackupDatabase BackupKind = "database"
)

// Backup 一条备份记录。
type Backup struct {
	ID         string     `json:"id"`
	Kind       BackupKind `json:"kind"`
	Target     string     `json:"target"`
	TargetName string     `json:"target_name"`
	Size       int64      `json:"size"`
	Path       string     `json:"path"`
	Encrypted  bool       `json:"encrypted"`
	Note       string     `json:"note"`
	CreatedAt  time.Time  `json:"created_at"`
}
