package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 面板入口守卫测试：安全入口路径 + IP 白名单。
//
// 这两项都能把管理员锁在门外，必须覆盖"路径改写、保留路径、
// 白名单命中/拒绝、回环自救、受信代理 XFF、自锁校验"。

func TestNormalizePanelPath(t *testing.T) {
	valid := map[string]string{
		"panel":       "panel",
		"/panel/":     "panel",
		"  zw-9x_ab ": "zw-9x_ab",
		"":            "",
	}
	for in, want := range valid {
		got, err := normalizePanelPath(in)
		if err != nil {
			t.Fatalf("normalizePanelPath(%q) 报错: %v", in, err)
		}
		if got != want {
			t.Fatalf("normalizePanelPath(%q)=%q，期望 %q", in, got, want)
		}
	}

	invalid := []string{"a", "panel/path", "panel.path", "api", "API", "ws", "js", "index.html", "..", strings.Repeat("x", 33)}
	for _, in := range invalid {
		if _, err := normalizePanelPath(in); err == nil {
			t.Fatalf("normalizePanelPath(%q) 应被拒绝", in)
		}
	}
}

func TestParseIPAllowlist(t *testing.T) {
	entries, nets, invalid := parseIPAllowlist("10.0.0.0/8\n192.168.1.10, ::1; bad-value")
	if len(invalid) != 1 || invalid[0] != "bad-value" {
		t.Fatalf("invalid=%v，期望 [bad-value]", invalid)
	}
	if len(entries) != 3 || len(nets) != 3 {
		t.Fatalf("entries=%v nets=%d，期望 3 条", entries, len(nets))
	}
	if !ipInNets(nets, "10.9.8.7") || ipInNets(nets, "11.0.0.1") {
		t.Fatalf("10.0.0.0/8 匹配结果不正确")
	}
	if !ipInNets(nets, "192.168.1.10") || ipInNets(nets, "192.168.1.11") {
		t.Fatalf("裸 IP 应等价于 /32")
	}
	if !ipInNets(nets, "::1") {
		t.Fatalf("::1 应命中")
	}
}

func TestPanelGuardPathAndIP(t *testing.T) {
	srv, db := new2FAServer(t)
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(r.URL.Path))
	})
	handler := srv.Guard(echo)

	// 未启用：一切照旧。
	rec := doGuardRequest(handler, "/", "192.0.2.1:1234")
	if rec.Code != http.StatusOK || rec.Body.String() != "/" {
		t.Fatalf("未启用入口时 / 应 200 且原样透传，得到 %d %q", rec.Code, rec.Body.String())
	}

	// 启用安全入口。
	if err := db.SetSettings(map[string]string{"panel_path": "zw-secret"}); err != nil {
		t.Fatal(err)
	}
	srv.panelGuard.Reload()

	cases := []struct {
		path     string
		code     int
		body     string
		location string
	}{
		{"/", http.StatusNotFound, "", ""},
		{"/zw-secret", http.StatusMovedPermanently, "", "/zw-secret/"},
		{"/zw-secret/", http.StatusOK, "/", ""},
		{"/zw-secret/js/app.js", http.StatusOK, "/js/app.js", ""},
		{"/zw-secret?x=1", http.StatusMovedPermanently, "", "/zw-secret/?x=1"},
		{"/api/auth/me", http.StatusOK, "/api/auth/me", ""},
		{"/ws/logs", http.StatusOK, "/ws/logs", ""},
		{"/other", http.StatusNotFound, "", ""},
	}
	for _, c := range cases {
		rec := doGuardRequest(handler, c.path, "192.0.2.1:1234")
		if rec.Code != c.code {
			t.Fatalf("%s: code=%d，期望 %d", c.path, rec.Code, c.code)
		}
		if c.body != "" && rec.Body.String() != c.body {
			t.Fatalf("%s: body=%q，期望 %q", c.path, rec.Body.String(), c.body)
		}
		if c.location != "" && rec.Header().Get("Location") != c.location {
			t.Fatalf("%s: Location=%q，期望 %q", c.path, rec.Header().Get("Location"), c.location)
		}
	}

	// IP 白名单：命中放行、未命中拒绝、回环永远放行。
	if err := db.SetSettings(map[string]string{"panel_ip_allowlist": "10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	srv.panelGuard.Reload()
	for _, tc := range []struct {
		remote string
		code   int
	}{
		{"10.1.2.3:9999", http.StatusOK},
		{"8.8.8.8:9999", http.StatusForbidden},
		{"127.0.0.1:9999", http.StatusOK},
		{"[::1]:9999", http.StatusOK},
	} {
		rec := doGuardRequest(handler, "/zw-secret/", tc.remote)
		if rec.Code != tc.code {
			t.Fatalf("remote=%s code=%d，期望 %d", tc.remote, rec.Code, tc.code)
		}
	}

	// 受信代理：直连方在代理网段内时才采信 XFF。
	srv.trustedProxies = newTrustedProxySet([]string{"10.0.0.0/8"})
	srv.panelGuard.trusted = srv.trustedProxies
	if err := db.SetSettings(map[string]string{"panel_ip_allowlist": "192.168.1.0/24"}); err != nil {
		t.Fatal(err)
	}
	srv.panelGuard.Reload()
	req := httptest.NewRequest(http.MethodGet, "/zw-secret/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "192.168.1.9")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("受信代理转发的白名单 IP 应放行，得到 %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/zw-secret/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("受信代理转发的非白名单 IP 应 403，得到 %d", rec.Code)
	}
}

func doGuardRequest(h http.Handler, path, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSettingsPanelSecurityValidation(t *testing.T) {
	srv, db := new2FAServer(t)

	// 合法：路径归一化 + 白名单包含当前 IP。
	rec := withSession(t, srv, db, http.MethodPut, "/api/settings",
		`{"settings":{"panel_path":"/zw-panel/","panel_ip_allowlist":"192.0.2.0/24","panel_port":"8899","panel_bind":"0.0.0.0"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法设置应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := db.GetSetting("panel_path"); got != "zw-panel" {
		t.Fatalf("panel_path 落库=%q，期望 zw-panel", got)
	}
	if got, _ := db.GetSetting("panel_port"); got != "8899" {
		t.Fatalf("panel_port 落库=%q，期望 8899", got)
	}

	// 非法入口路径。
	rec = withSession(t, srv, db, http.MethodPut, "/api/settings",
		`{"settings":{"panel_path":"api"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("保留路径应 400，得到 %d", rec.Code)
	}

	// 自锁：白名单不含当前客户端 IP（httptest 默认 192.0.2.1）。
	rec = withSession(t, srv, db, http.MethodPut, "/api/settings",
		`{"settings":{"panel_ip_allowlist":"10.0.0.0/8"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("自锁配置应 400，得到 %d: %s", rec.Code, rec.Body.String())
	}
}
