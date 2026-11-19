package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/service"
	"github.com/zhiwei/panel/internal/store"
	"github.com/zhiwei/panel/internal/twofa"
)

// 2FA（TOTP）流程端到端测试：真实 SQLite + 真实登录/会话链路。
//
// 覆盖：登录返回 totp_required、错码 401、对码发 session、pending 一次性、
// 5 次错误作废、setup/enable/disable 语义、API Token 403。

func new2FAServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// EnsureAdminUser 定义在本包（auth.go），测试同包可直接调用
	if err := EnsureAdminUser(db, "admin", "testpassword123"); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	svc := service.New(db, nil, nil, nil, nil, nil, dir, dir, 0, false)
	return NewServer(svc, true, nil), db
}

// loginAs 用密码走完整登录，返回响应（2FA 启用时含 totp_required/login_token）。
func loginAs(t *testing.T, s *Server, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

// withSession 构造带会话 Cookie 的请求。
func withSession(t *testing.T, s *Server, db *store.DB, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	tok := store.NewID() + store.NewID()
	if err := db.SaveSession(tok, "admin", sessionTTL); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	req := httptest.NewRequest(method, path, nil)
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

func postJSON(t *testing.T, s *Server, path, cookieToken, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookieToken != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookieToken})
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

func sessionCookies(rec *httptest.ResponseRecorder) int {
	n := 0
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			n++
		}
	}
	return n
}

func Test2FASetupEnableDisableFlow(t *testing.T) {
	s, db := new2FAServer(t)
	if err := db.SaveSession("sess-1", "admin", sessionTTL); err != nil {
		t.Fatal(err)
	}

	// 未启用时 status.enabled=false
	rec := withSession(t, s, db, http.MethodGet, "/api/2fa/status", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("初始 status 应为 false: %d %s", rec.Code, rec.Body.String())
	}

	// setup → 拿到 secret 与 otpauth
	rec = withSession(t, s, db, http.MethodPost, "/api/2fa/setup", "")
	if rec.Code != 200 {
		t.Fatalf("setup 应 200，实际 %d", rec.Code)
	}
	var setup struct {
		Secret  string `json:"secret"`
		OTPAuth string `json:"otpauth"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	if setup.Secret == "" || !strings.Contains(setup.OTPAuth, "otpauth://totp/") {
		t.Fatalf("setup 响应缺少 secret/otpauth: %+v", setup)
	}

	// 未验证首码前，登录仍不要求 TOTP（pending 未启用）
	rec = loginAs(t, s, "admin", "testpassword123")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "totp_required") {
		t.Fatalf("pending 未启用时登录应直接成功: %d %s", rec.Code, rec.Body.String())
	}

	// enable：错码 → 400
	secret, _, err := db.GetTOTP("admin")
	if err != nil || secret == "" {
		t.Fatalf("GetTOTP: %v", err)
	}
	rec = postJSON(t, s, "/api/2fa/enable", "sess-1", `{"code":"000000"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("错码 enable 应 400，实际 %d", rec.Code)
	}
	// enable：用 CodeAt 计算的正确码 → 200
	now := time.Now()
	code, err := twofa.CodeAt(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	rec = postJSON(t, s, "/api/2fa/enable", "sess-1", `{"code":"`+code+`"}`)
	if rec.Code != 200 {
		t.Fatalf("对码 enable 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}

	// status → enabled=true
	rec = withSession(t, s, db, http.MethodGet, "/api/2fa/status", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("启用后 status 应为 true: %d %s", rec.Code, rec.Body.String())
	}

	// 关闭：错码 → 401
	rec = postJSON(t, s, "/api/2fa/disable", "sess-1", `{"code":"000000"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错码 disable 应 401，实际 %d", rec.Code)
	}
	// 关闭：对码 → 200
	code, err = twofa.CodeAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec = postJSON(t, s, "/api/2fa/disable", "sess-1", `{"code":"`+code+`"}`)
	if rec.Code != 200 {
		t.Fatalf("对码 disable 应 200，实际 %d", rec.Code)
	}
	if _, enabled, _ := db.GetTOTP("admin"); enabled {
		t.Error("关闭后 enabled 应为 false")
	}
}

func Test2FARequiresSession(t *testing.T) {
	s, _ := new2FAServer(t)
	for _, p := range []string{"/api/2fa/setup", "/api/2fa/enable", "/api/2fa/disable"} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, req)
		// 未认证 → 401（authMiddleware）；Token 认证 → 403（会话专用）
		if w.Code != http.StatusUnauthorized {
			t.Errorf("未认证访问 %s 应 401，实际 %d", p, w.Code)
		}
	}
}

func TestLoginWithTOTP(t *testing.T) {
	s, db := new2FAServer(t)

	// 预置：管理员 + 已启用 TOTP
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	if err := db.SetTOTPSecret("admin", secret); err != nil {
		t.Fatal(err)
	}
	if err := db.EnableTOTP("admin"); err != nil {
		t.Fatal(err)
	}

	// 第一步：密码登录 → totp_required + login_token，且不发 session cookie
	rec := loginAs(t, s, "admin", "testpassword123")
	if rec.Code != 200 {
		t.Fatalf("登录应 200，实际 %d", rec.Code)
	}
	var lr struct {
		TOTPRequired bool   `json:"totp_required"`
		LoginToken   string `json:"login_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatal(err)
	}
	if !lr.TOTPRequired || lr.LoginToken == "" {
		t.Fatalf("启用 2FA 后登录应返回 totp_required+login_token: %+v", lr)
	}
	if sessionCookies(rec) != 0 {
		t.Error("totp_required 阶段不应下发 session cookie")
	}

	// 第二步：错码 → 401
	rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+lr.LoginToken+`","code":"000000"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错码 verify-totp 应 401，实际 %d", rec.Code)
	}

	// 第二步：对码（当前步）→ 200 + Set-Cookie
	now := time.Now()
	code, err := twofa.CodeAt(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+lr.LoginToken+`","code":"`+code+`"}`)
	if rec.Code != 200 {
		t.Fatalf("对码 verify-totp 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if sessionCookies(rec) != 1 {
		t.Error("TOTP 通过后应下发 session cookie")
	}

	// 第三步：pending 已消费 → 重放同一 token 应 401
	rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+lr.LoginToken+`","code":"`+code+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("pending 一次性，重放应 401，实际 %d", rec.Code)
	}
}

func TestLoginTOTPAttemptsExhausted(t *testing.T) {
	s, db := new2FAServer(t)
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if err := db.SetTOTPSecret("admin", secret); err != nil {
		t.Fatal(err)
	}
	if err := db.EnableTOTP("admin"); err != nil {
		t.Fatal(err)
	}

	rec := loginAs(t, s, "admin", "testpassword123")
	var lr struct {
		TOTPRequired bool   `json:"totp_required"`
		LoginToken   string `json:"login_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &lr); err != nil {
		t.Fatal(err)
	}

	// 5 次错码耗尽尝试次数
	for i := 0; i < 5; i++ {
		rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+lr.LoginToken+`","code":"000000"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错码应 401，实际 %d", i+1, rec.Code)
		}
	}
	// 第 6 次：即使码对了，pending 已作废 → 401
	now := time.Now()
	code, err := twofa.CodeAt(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+lr.LoginToken+`","code":"`+code+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("尝试耗尽后对码也应 401，实际 %d", rec.Code)
	}
}

func Test2FATokenForbidden(t *testing.T) {
	s, db := new2FAServer(t)
	// 建 write scope 的 API Token → 访问 2fa 管理端点应 403（会话专用）
	if err := db.SaveAPIToken(&model.APIToken{
		ID: "tok-1", Name: "t", Prefix: "zw_test", Token: "zw_testtoken1234567890",
		Scopes: []string{"read", "write"}, CreatedBy: "admin", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/api/2fa/setup", "/api/2fa/enable", "/api/2fa/disable"} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer zw_testtoken1234567890")
		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("API Token 访问 %s 应 403，实际 %d", p, w.Code)
		}
	}
	// GET /api/2fa/status 同样会话专用
	greq := httptest.NewRequest(http.MethodGet, "/api/2fa/status", nil)
	greq.Header.Set("Authorization", "Bearer zw_testtoken1234567890")
	gw := httptest.NewRecorder()
	s.Routes().ServeHTTP(gw, greq)
	if gw.Code != http.StatusForbidden {
		t.Errorf("API Token 访问 /api/2fa/status 应 403，实际 %d", gw.Code)
	}
}
