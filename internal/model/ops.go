// Package model —— 运维域模型（API Token / 操作审计）。
//
// 与 model.go 里的"站点配置域"分开放：站点配置是用户可见的业务对象，
// 而 Token 与审计记录是面板自身的安全设施，生命周期与访问面都不同。
package model

import "time"

// APIToken 是机器访问凭据。
//
// 用途：把面板暴露给 CI / 监控 / 备份脚本这类自动化。
// 现状是所有接口只能靠 session cookie 认证，而 cookie 由浏览器持有，
// 自动化根本没有安全的接入方式——只能让脚本登录再存 cookie，
// 那等于把管理员密码写进脚本。
//
// 安全设计：
//   - 明文只在创建时返回一次，库里只存 SHA-256 摘要；
//   - 库里另存 prefix（前 8 位）用于列表展示与人工识别，
//     前缀本身不能用于认证；
//   - 可设置过期时间与最后使用时间，便于识别僵尸凭据；
//   - 吊销即删行，不做软删除——凭据泄漏后必须能立刻失效。
type APIToken struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Prefix 是明文 token 的前 8 位，仅用于展示。
	Prefix string `json:"prefix"`
	// Token 是明文，**只在创建响应里出现一次**，其它任何读路径都不返回。
	Token string `json:"token,omitempty"`
	// Scopes 限定该 token 能做什么。留空 = 全部权限（等同管理员）。
	// 目前只区分 "read" 与 "write"；写 token 才能调 POST/PUT/DELETE。
	Scopes []string `json:"scopes"`
	// CreatedBy 创建者用户名。
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Disabled   bool       `json:"disabled"`
}

// HasScope 判断 token 是否具备某个权限。
//
// 没有 scope 列表 = 旧版本/全权限 token，等价于拥有全部权限。
// 只写了 "read" 的 token 只能读；写了 "read"+"write" 的能写。
func (t *APIToken) HasScope(scope string) bool {
	if len(t.Scopes) == 0 {
		return true
	}
	for _, s := range t.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
}

// Expired 判断 token 是否已过期。
func (t *APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && !t.ExpiresAt.IsZero() && now.After(*t.ExpiresAt)
}

// AuditLog 是一次写操作的审计记录。
//
// 面板以 root 运行、可以改任意配置、可以删文件。没有审计记录的话，
// 出事之后无法回答"谁在什么时候动了什么"——这是等保场景下的硬要求。
//
// 只记录写操作与登录事件：读操作量大且价值低，写一条就多一份噪音。
type AuditLog struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	// Action 是归一化后的动作名，如 site.create / token.delete / auth.login。
	Action string `json:"action"`
	// Target 是被操作对象的标识（站点 ID、token 名等）。
	Target string `json:"target"`
	// Detail 是补充说明（脱敏后的，不含密码明文）。
	Detail string `json:"detail"`
	// Method/Path 是原始请求信息。
	Method string `json:"method"`
	Path   string `json:"path"`
	// Status 是 HTTP 响应码。>=400 的记录值得重点看。
	Status int    `json:"status"`
	IP     string `json:"ip"`
	UA     string `json:"ua"`
	// ViaToken 为 true 表示这次操作是用 API Token 而非浏览器会话做的。
	ViaToken  bool      `json:"via_token"`
	CreatedAt time.Time `json:"created_at"`
}
