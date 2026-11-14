package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestDeriveAuditAction 锁死审计动作名的推导规则。
//
// 这些名字会出现在等保审计报表里，规则不能随便变——
// 一旦改名，历史记录的归类口径就跟着漂了。
func TestDeriveAuditAction(t *testing.T) {
	cases := []struct {
		method string
		path   string
		action string
		target string
	}{
		// 站点
		{http.MethodPost, "/api/sites", "site.create", ""},
		{http.MethodGet, "/api/sites", "site.get", ""},
		{http.MethodPut, "/api/sites/abc123", "site.update", "abc123"},
		{http.MethodDelete, "/api/sites/abc123", "site.delete", "abc123"},
		{http.MethodPost, "/api/sites/abc123/toggle", "site.toggle", "abc123"},
		{http.MethodPost, "/api/sites/batch", "site.batch", ""},

		// 证书
		{http.MethodPost, "/api/certs/issue", "cert.issue", ""},
		{http.MethodPost, "/api/certs/import", "cert.import", ""},
		{http.MethodDelete, "/api/certs/abc123", "cert.delete", "abc123"},

		// Token
		{http.MethodPost, "/api/tokens", "token.create", ""},
		{http.MethodDelete, "/api/tokens/xyz", "token.delete", "xyz"},

		// 告警
		{http.MethodPost, "/api/alerts/test", "alert.test", ""},

		// system 命名空间：第二段才是真正的资源
		{http.MethodPost, "/api/system/config-apply", "config.apply", ""},
		{http.MethodPost, "/api/system/config-history/20260101T000000-1234/restore", "config-history.restore", "20260101T000000-1234"},
		{http.MethodGet, "/api/system/config-history", "config-history.get", ""},

		// 兜底
		{"", "", "unknown", ""},
	}
	for _, c := range cases {
		action, target := deriveAuditAction(c.method, c.path)
		if action != c.action || target != c.target {
			t.Errorf("%s %s → (%q, %q), want (%q, %q)",
				c.method, c.path, action, target, c.action, c.target)
		}
	}
}

// TestDeriveAuditActionSystemNamespaceTarget 回归测试：
// system 命名空间曾经把动作推成 "system.action"、target 取成 "config-history"，
// 导致"谁回滚了哪一版配置"完全查不出来。
func TestDeriveAuditActionSystemNamespaceTarget(t *testing.T) {
	action, target := deriveAuditAction(http.MethodPost, "/api/system/config-history/snap-42/restore")
	if action == "system.action" {
		t.Errorf("动作名退化成 system.action: %q", action)
	}
	if target != "snap-42" {
		t.Errorf("target = %q, want snap-42", target)
	}
}

// TestPrincipalHasScope 权限判定。
func TestPrincipalHasScope(t *testing.T) {
	cases := []struct {
		name      string
		p         principal
		wantRead  bool
		wantWrite bool
	}{
		{"会话=管理员全权限", principal{Username: "admin"}, true, true},
		{"Token 无 scope=全权限", principal{ViaToken: true}, true, true},
		{"只读 token", principal{ViaToken: true, Scopes: []string{"read"}}, true, false},
		{"读写 token", principal{ViaToken: true, Scopes: []string{"read", "write"}}, true, true},
		{"只给 write", principal{ViaToken: true, Scopes: []string{"write"}}, false, true},
		{"星号通配", principal{ViaToken: true, Scopes: []string{"*"}}, true, true},
		{"空 scope 列表", principal{ViaToken: true, Scopes: []string{}}, true, true},
	}
	for _, c := range cases {
		if got := c.p.HasScope("read"); got != c.wantRead {
			t.Errorf("%s: HasScope(read) = %v, want %v", c.name, got, c.wantRead)
		}
		if got := c.p.HasScope("write"); got != c.wantWrite {
			t.Errorf("%s: HasScope(write) = %v, want %v", c.name, got, c.wantWrite)
		}
	}
}

// TestBearerToken 各种取 token 的写法。
func TestBearerToken(t *testing.T) {
	mk := func(auth, xtoken string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "/api/sites", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if xtoken != "" {
			r.Header.Set("X-API-Token", xtoken)
		}
		return r
	}
	cases := []struct {
		name string
		req  *http.Request
		want string
	}{
		{"Bearer 标准", mk("Bearer sp_abc", ""), "sp_abc"},
		{"bearer 小写", mk("bearer sp_abc", ""), "sp_abc"},
		{"BEARER 大写", mk("BEARER sp_abc", ""), "sp_abc"},
		{"前后空格", mk("  Bearer   sp_abc  ", ""), "sp_abc"},
		{"裸 token 无空格", mk("sp_abc", ""), "sp_abc"},
		{"X-API-Token 头", mk("", "sp_xyz"), "sp_xyz"},
		{"两个都有时 Authorization 优先", mk("Bearer sp_auth", "sp_hdr"), "sp_auth"},
		{"Basic 不能当 token", mk("Basic dXNlcjpwYXNz", ""), ""},
		{"空", mk("", ""), ""},
	}
	for _, c := range cases {
		if got := bearerToken(c.req); got != c.want {
			t.Errorf("%s: bearerToken = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestIsAuditedMethod 只有写操作需要留痕。
func TestIsAuditedMethod(t *testing.T) {
	yes := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	no := []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	for _, m := range yes {
		if !isAuditedMethod(m) {
			t.Errorf("%s 应当留痕", m)
		}
	}
	for _, m := range no {
		if isAuditedMethod(m) {
			t.Errorf("%s 不该留痕", m)
		}
	}
}

// TestIsReadMethod 只读方法不做 scope 检查。
func TestIsReadMethod(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !isReadMethod(m) {
			t.Errorf("%s 应当算只读", m)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if isReadMethod(m) {
			t.Errorf("%s 不该算只读", m)
		}
	}
}

// TestRandomToken token 必须够长、随机、可安全放进 URL 与 HTTP 头。
func TestRandomToken(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok := randomToken()
		if len(tok) != 43 {
			// 32 字节 base64url（无 padding）= 43 字符
			t.Fatalf("token 长度 = %d, want 43", len(tok))
		}
		// base64url 字符集：A-Za-z0-9-_ ，不能有 + / = 等 URL 敏感字符
		if strings.ContainsAny(tok, "+/= \t") {
			t.Fatalf("token 含非法字符: %q", tok)
		}
		if seen[tok] {
			t.Fatalf("token 重复: %q", tok)
		}
		seen[tok] = true
	}
}

// TestRandomTokenUniqueness 额外确认前缀拼接后的完整 token 也不重复。
func TestRandomTokenUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		full := tokenPrefix + randomToken()
		if !strings.HasPrefix(full, tokenPrefix) {
			t.Fatalf("token 缺少前缀 %q: %q", tokenPrefix, full)
		}
		if seen[full] {
			t.Fatalf("token 重复: %q", full)
		}
		seen[full] = true
	}
}
