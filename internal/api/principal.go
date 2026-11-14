package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// principal 是当前请求的调用者身份。
//
// 两种来源：
//   - 浏览器会话：zhiweipanel_session cookie → users 表
//   - API Token：Authorization: Bearer <token> 或 X-API-Token 头 → api_tokens 表
//
// 为什么要区分 ViaToken：审计日志里必须能看出"这次改动是人点的还是脚本干的"。
// 凭据泄漏事故里，最关键的问题就是"脚本是不是被用了"。
type principal struct {
	Username string
	// ViaToken 为 true 表示用 API Token 认证。
	ViaToken bool
	// TokenID 是 API Token 的 ID（会话认证时为空）。
	TokenID string
	// Scopes 是 API Token 的权限列表；会话认证时为 nil（= 全部权限）。
	Scopes []string
}

// HasScope 判断调用者是否具备某个权限。
func (p principal) HasScope(scope string) bool {
	if !p.ViaToken {
		// 会话认证 = 管理员本人，全权限。
		return true
	}
	if len(p.Scopes) == 0 {
		// 没写 scope 的 token 视为全权限（创建时的默认行为）。
		return true
	}
	for _, s := range p.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
}

// contextKey 是本包私有的 context key 类型。
type contextKey string

// principalKey 是 principal 在 context 里的键。
const principalKey contextKey = "zhiweipanel.principal"

// withPrincipal 把 principal 塞进 context。
func withPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// principalFrom 从 context 取 principal。
func principalFrom(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(principalKey).(principal)
	return p, ok
}

// bearerToken 从请求头里提取 API Token 明文。
//
// 支持两种写法：
//   - Authorization: Bearer <token>（标准）
//   - X-API-Token: <token>（给不方便设置 Authorization 的场景，
//     比如某些 webhook 工具或浏览器控制台）
func bearerToken(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("Authorization")); h != "" {
		// 大小写不敏感地匹配 "Bearer " 前缀
		if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:])
		}
		// 有些客户端直接放裸 token（不带 Bearer）。只在没有空格时接受，
		// 避免把 "Basic xxx" 之类的凭据误当 token 去查库。
		if !strings.ContainsAny(h, " \t") {
			return h
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Token"))
}

// currentPrincipal 解析请求的调用者身份。
//
// 先看 session cookie，再看 API Token。
// cookie 优先是因为浏览器场景下它总是有效的，token 只在显式提供时才查库。
func (s *Server) currentPrincipal(r *http.Request) (principal, bool) {
	if user, ok := s.currentUser(r); ok {
		return principal{Username: user}, true
	}
	tok := bearerToken(r)
	if tok == "" {
		return principal{}, false
	}
	t, err := s.svc.DB().GetAPITokenByHash(tok)
	if err != nil || t == nil {
		return principal{}, false
	}
	if t.Disabled {
		return principal{}, false
	}
	if t.Expired(time.Now()) {
		return principal{}, false
	}
	// 记录最后使用时间。失败不影响认证结果。
	go s.svc.DB().TouchAPIToken(t.ID)
	return principal{
		Username: t.CreatedBy,
		ViaToken: true,
		TokenID:  t.ID,
		Scopes:   t.Scopes,
	}, true
}

// isReadMethod 判断请求是否只读。
//
// 只读方法不做 scope 检查——只读 token 可以读所有 GET 接口。
func isReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// errTokenScopeDenied 是 scope 不足时的错误。
var errTokenScopeDenied = model.ErrForbidden
