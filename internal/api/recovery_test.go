package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
	"github.com/zhiwei/panel/internal/twofa"
)

// v0.8.0 恢复码全链路测试：
// 启用即发码 → 恢复码登录 → 一次性 → 重发作废旧码 → 关闭清码 → Token 403。

// enable2FAAndGetCodes 走 setup→enable 完整流程，返回恢复码明文列表。
func enable2FAAndGetCodes(t *testing.T, s *Server, db *store.DB) []string {
	t.Helper()
	rec := withSession(t, s, db, http.MethodPost, "/api/2fa/setup", "")
	if rec.Code != 200 {
		t.Fatalf("setup 应 200，实际 %d", rec.Code)
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	code, err := twofa.CodeAt(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec = withSession(t, s, db, http.MethodPost, "/api/2fa/enable", `{"code":"`+code+`"}`)
	if rec.Code != 200 {
		t.Fatalf("enable 应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var en struct {
		Enabled       bool     `json:"enabled"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &en); err != nil {
		t.Fatal(err)
	}
	if !en.Enabled || len(en.RecoveryCodes) != 10 {
		t.Fatalf("启用响应应带 10 枚恢复码: %+v", en)
	}
	return en.RecoveryCodes
}

func TestRecoveryCodeLoginFlow(t *testing.T) {
	s, db := new2FAServer(t)
	codes := enable2FAAndGetCodes(t, s, db)

	// status 带恢复码余量
	rec := withSession(t, s, db, http.MethodGet, "/api/2fa/status", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"recovery_remaining":10`) {
		t.Fatalf("status 应显示 10 枚未用恢复码: %d %s", rec.Code, rec.Body.String())
	}

	// 用第一枚恢复码登录
	lr := loginAs(t, s, "admin", "testpassword123")
	var pending struct {
		TOTPRequired bool   `json:"totp_required"`
		LoginToken   string `json:"login_token"`
	}
	if err := json.Unmarshal(lr.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if !pending.TOTPRequired || pending.LoginToken == "" {
		t.Fatalf("登录应要求两步验证: %s", lr.Body.String())
	}
	rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+pending.LoginToken+`","code":"`+codes[0]+`"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("恢复码应能登录: %d %s", rec.Code, rec.Body.String())
	}
	if n := sessionCookies(rec); n != 1 {
		t.Errorf("恢复码登录应下发 session cookie，实际 %d", n)
	}

	// 同一枚再登录 → 一次性，应 401
	lr = loginAs(t, s, "admin", "testpassword123")
	_ = json.Unmarshal(lr.Body.Bytes(), &pending)
	rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+pending.LoginToken+`","code":"`+codes[0]+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("已消费的恢复码再次登录应 401，实际 %d", rec.Code)
	}

	// status 余量降到 9
	rec = withSession(t, s, db, http.MethodGet, "/api/2fa/status", "")
	if !strings.Contains(rec.Body.String(), `"recovery_remaining":9`) {
		t.Errorf("消费一枚后余量应为 9: %s", rec.Body.String())
	}

	// 重新生成 → 旧码作废、新码可用
	rec = withSession(t, s, db, http.MethodPost, "/api/2fa/recovery-codes", "")
	if rec.Code != 200 {
		t.Fatalf("重新生成应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var regen struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &regen); err != nil {
		t.Fatal(err)
	}
	if len(regen.RecoveryCodes) != 10 {
		t.Fatalf("重新生成应返回 10 枚，实际 %d", len(regen.RecoveryCodes))
	}
	// 旧码（没用过的那枚）现在必须失效
	lr = loginAs(t, s, "admin", "testpassword123")
	_ = json.Unmarshal(lr.Body.Bytes(), &pending)
	rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+pending.LoginToken+`","code":"`+codes[1]+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("重新生成后旧恢复码应作废（401），实际 %d", rec.Code)
	}
	// 新码可用
	lr = loginAs(t, s, "admin", "testpassword123")
	_ = json.Unmarshal(lr.Body.Bytes(), &pending)
	rec = postJSON(t, s, "/api/auth/verify-totp", "", `{"login_token":"`+pending.LoginToken+`","code":"`+regen.RecoveryCodes[0]+`"}`)
	if rec.Code != 200 {
		t.Errorf("新恢复码应能登录，实际 %d", rec.Code)
	}
}

func TestRecoveryCodesClearedOnDisable(t *testing.T) {
	s, db := new2FAServer(t)
	codes := enable2FAAndGetCodes(t, s, db)
	secret, _, _ := db.GetTOTP("admin")

	// 关闭 2FA（需当前动态口令）
	code, err := twofa.CodeAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec := withSession(t, s, db, http.MethodPost, "/api/2fa/disable", `{"code":"`+code+`"}`)
	if rec.Code != 200 {
		t.Fatalf("disable 应 200，实际 %d", rec.Code)
	}
	// 恢复码应清空
	rec = withSession(t, s, db, http.MethodGet, "/api/2fa/status", "")
	if !strings.Contains(rec.Body.String(), `"enabled":false`) ||
		!strings.Contains(rec.Body.String(), `"recovery_remaining":0`) {
		t.Errorf("关闭后恢复码应清零: %s", rec.Body.String())
	}
	// 未启用时不允许生成恢复码
	rec = withSession(t, s, db, http.MethodPost, "/api/2fa/recovery-codes", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("未启用 2FA 时生成恢复码应 400，实际 %d", rec.Code)
	}
	// 未启用时登录根本不进两步阶段
	lr := loginAs(t, s, "admin", "testpassword123")
	if strings.Contains(lr.Body.String(), "totp_required") {
		t.Errorf("2FA 关闭后登录不应要求两步验证: %s", lr.Body.String())
	}
	_ = codes
}

func TestRecoveryCodesTokenForbidden(t *testing.T) {
	s, db := new2FAServer(t)
	if err := db.SaveAPIToken(&model.APIToken{
		ID: "tok-rc", Name: "t", Prefix: "zw_test", Token: "zw_testtoken1234567890",
		Scopes: []string{"read", "write"}, CreatedBy: "admin", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/2fa/recovery-codes", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer zw_testtoken1234567890")
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("API Token 访问 recovery-codes 应 403，实际 %d", w.Code)
	}
}
