package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSideEffectEndpointsRejectNonPost 锁死一条不变量：
//
//	凡是会写库 / 写文件 / 对外发信的端点，都不能用 GET 触发。
//
// 原因在 principal.go 的 isReadMethod + authMiddleware 里写死了：
// 只读 Token（scopes:["read"]）对 GET/HEAD/OPTIONS 完全免检，
// 而任何非 GET 方法都要求 write scope。
// 于是「GET 可达 + 有副作用」= 只读 Token 的权限提升通道。
//
// 这条测试存在的意义是防止**以后新增端点时重犯**。
// 早期版本已经有 8 个端点栽在这上面（toggle / config-apply /
// alerts resolve / certs revoke / certs bind / certs renew / certs delete），
// 第二轮审计又找出 baseline / tamper 两个——
// 后者尤其隐蔽：CheckTamper 名字叫"检测"，实际会写 tamper_events 表
// 并触发告警通知。
//
// 每个用例都直连 handler（绕开鉴权中间件），且只测**被拒的方法**：
// 方法闸门必须在**触碰数据库之前**就生效——否则一个未认证的 GET
// 就能先把站点读出来再判断，等于白改。
//
// Server 故意留空 svc：如果闸门写晚了（排在 GetSite 之后），
// 这里会直接 nil panic，测试立刻红。
//
// 放行方向（POST 不被误拒）由 TestRequirePost / TestRequirePutOrPost 覆盖。
func TestSideEffectEndpointsRejectNonPost(t *testing.T) {
	cases := []struct {
		name    string
		handler func(*Server, http.ResponseWriter, *http.Request)
		path    string
		// 允许的方法；其余一律应被 405 拒绝
		allowed []string
	}{
		{
			name:    "建立防篡改基线会改写 integrity 表",
			handler: (*Server).handleSiteByID,
			path:    "/api/sites/abc123/baseline",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "篡改检测会写事件表并触发告警通知",
			handler: (*Server).handleSiteByID,
			path:    "/api/sites/abc123/tamper",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "启停站点会改状态并下发配置",
			handler: (*Server).handleSiteByID,
			path:    "/api/sites/abc123/toggle",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "重载站点配置会下发 nginx 配置",
			handler: (*Server).handleSiteByID,
			path:    "/api/sites/abc123/reload",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "吊销证书会删证书记录",
			handler: (*Server).handleCertByID,
			path:    "/api/certs/abc123/revoke",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "删除证书会删证书记录",
			handler: (*Server).handleCertByID,
			path:    "/api/certs/abc123/delete",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "续签证书会改写证书与站点绑定",
			handler: (*Server).handleCertByID,
			path:    "/api/certs/abc123/renew",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "绑定证书会改写站点 SSL 配置",
			handler: (*Server).handleCertByID,
			path:    "/api/certs/abc123/bind",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "关闭告警会改告警状态",
			handler: (*Server).handleAlertByID,
			path:    "/api/alerts/abc123/resolve",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "全量下发配置会重写 nginx 配置目录",
			handler: (*Server).handleConfigApply,
			path:    "/api/system/config-apply",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "登出会删会话记录",
			handler: (*Server).handleLogout,
			path:    "/api/auth/logout",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "改密会写用户表并作废其它会话",
			handler: (*Server).handleChangePassword,
			path:    "/api/auth/password",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "改邮箱会写账户资料",
			handler: (*Server).handleSetEmail,
			path:    "/api/admin/account/email",
			allowed: []string{http.MethodPut, http.MethodPost},
		},
		{
			name:    "触发邮箱验证会写 challenge 并发信",
			handler: (*Server).handleSendEmailVerify,
			path:    "/api/admin/account/email/verify",
			allowed: []string{http.MethodPost},
		},
		{
			name:    "SMTP 测试会对外发信",
			handler: (*Server).handleTestSMTP,
			path:    "/api/admin/smtp/test",
			allowed: []string{http.MethodPost},
		},
	}

	// 全部要尝试的**不应被放行**的方法
	all := []string{
		http.MethodGet, http.MethodHead,
		http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions,
	}

	for _, c := range cases {
		allowed := map[string]bool{}
		for _, m := range c.allowed {
			allowed[m] = true
		}
		for _, m := range all {
			if allowed[m] {
				continue
			}
			s := &Server{}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(m, c.path, nil)
			c.handler(s, w, r)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: %s %s 应被拒（405），实际 %d —— 只读 Token 可借此提权",
					c.name, m, c.path, w.Code)
			}
		}
	}
}

// TestRequirePutOrPost 只允许 PUT / POST。
func TestRequirePutOrPost(t *testing.T) {
	for _, m := range []string{http.MethodPut, http.MethodPost} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(m, "/x", nil)
		if !requirePutOrPost(w, r) {
			t.Errorf("%s 应被放行，实际拒绝（code=%d）", m, w.Code)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodDelete,
		http.MethodPatch, http.MethodOptions} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(m, "/x", nil)
		if requirePutOrPost(w, r) {
			t.Errorf("%s 应被拒绝，实际放行", m)
		}
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: code = %d, want 405", m, w.Code)
		}
	}
}

// TestRequirePost 只允许 POST。
func TestRequirePost(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	if !requirePost(w, r) {
		t.Errorf("POST 应被放行")
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut,
		http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(m, "/x", nil)
		if requirePost(w, r) {
			t.Errorf("%s 应被拒绝，实际放行", m)
		}
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: code = %d, want 405", m, w.Code)
		}
	}
}
