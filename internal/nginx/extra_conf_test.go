package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestForceHTTPSSplitsServerBlocks 锁死 P2：SSL 就绪 + 强制 HTTPS 时，
// 80 与 443 拆成两个 server 块，且不再用 if($scheme=http) 做跳转。
func TestForceHTTPSSplitsServerBlocks(t *testing.T) {
	s := testSite() // SSL.Enabled=true + ForceHTTPS=true
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf

	if got := strings.Count(conf, "server {"); got != 2 {
		t.Errorf("强制 HTTPS 应拆成 2 个 server 块，实际 %d 个:\n%s", got, conf)
	}
	for _, must := range []string{
		"listen 443 ssl;",
		"return 301 https://$host$request_uri;",
		"location ^~ /.well-known/acme-challenge/ {",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("缺少 %q:\n%s", must, conf)
		}
	}
	// 拆块后绝不能再出现 if($scheme=http) 跳转。
	if strings.Contains(conf, "if ($scheme = http)") {
		t.Errorf("仍残留 if($scheme=http) 跳转:\n%s", conf)
	}
	// 80 块里 ACME 精确前缀必须先于 location / 的 301 命中。
	if strings.Index(conf, "location ^~ /.well-known/acme-challenge/") > strings.Index(conf, "location / {") {
		t.Errorf("ACME 挑战 location 应位于 location / 之前（否则续期被 301 拦截）:\n%s", conf)
	}
}

// TestNoForceHTTPSKeepsSingleBlock 锁死：未开强制 HTTPS 时保持单 server 块。
func TestNoForceHTTPSKeepsSingleBlock(t *testing.T) {
	s := testSite()
	s.SSL.ForceHTTPS = false
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := strings.Count(res.SiteConf, "server {"); got != 1 {
		t.Errorf("未开强制 HTTPS 应保持单 server 块，实际 %d 个", got)
	}
	if strings.Contains(res.SiteConf, "return 301 https://$host$request_uri;") {
		t.Errorf("未开强制 HTTPS 不应出现 301 跳转")
	}
}

// TestDefaultSecurityHeaders 锁死 P3：三个默认安全头无条件输出。
func TestDefaultSecurityHeaders(t *testing.T) {
	s := testSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, h := range []string{
		`add_header X-Frame-Options "SAMEORIGIN" always;`,
		`add_header X-Content-Type-Options "nosniff" always;`,
		`add_header Referrer-Policy "strict-origin-when-cross-origin" always;`,
	} {
		if !strings.Contains(res.SiteConf, h) {
			t.Errorf("缺少默认安全头 %q:\n%s", h, res.SiteConf)
		}
	}
}

// TestProxyTimeoutDefault30 锁死 P1：反代超时默认 30s（不再是 60s）。
func TestProxyTimeoutDefault30(t *testing.T) {
	s := testSite()
	s.SSL.Enabled = false
	s.Proxy.Enabled = true
	s.Proxy.Rules = []model.ProxyRule{{Path: "/api", Target: "http://127.0.0.1:3000", Enabled: true}}
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(res.SiteConf, "proxy_read_timeout 30s;") {
		t.Errorf("反代默认超时应为 30s:\n%s", res.SiteConf)
	}
	if strings.Contains(res.SiteConf, "proxy_read_timeout 60s;") {
		t.Errorf("反代默认超时不应再是 60s:\n%s", res.SiteConf)
	}
}

// TestExtraConfInside 锁死 P0：extra_conf 以 inside 位置注入 server 块内，
// 带注释分隔，且排在 .well-known 之前。
func TestExtraConfInside(t *testing.T) {
	s := testSite()
	s.SSL.Enabled = false
	s.ExtraConf = "location ^~ /x { return 403; }"
	s.ExtraConfPos = "inside"
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf
	for _, must := range []string{
		"# ==== 自定义配置开始 ====",
		"location ^~ /x { return 403; }",
		"# ==== 自定义配置结束 ====",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("缺少 %q:\n%s", must, conf)
		}
	}
	if strings.Index(conf, "location ^~ /x") > strings.Index(conf, "location ^~ /.well-known") {
		t.Errorf("inside 自定义配置应排在 .well-known 之前:\n%s", conf)
	}
}

// TestExtraConfOutside 锁死 P0：extra_conf 以 outside 位置注入 server 块之后。
func TestExtraConfOutside(t *testing.T) {
	s := testSite()
	s.SSL.Enabled = false
	s.ExtraConf = "upstream backend { server 127.0.0.1:3000; }"
	s.ExtraConfPos = "outside"
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf
	for _, must := range []string{
		"# ==== 自定义配置开始 ====",
		"upstream backend { server 127.0.0.1:3000; }",
		"# ==== 自定义配置结束 ====",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("缺少 %q:\n%s", must, conf)
		}
	}
	// outside 内容必须在 server 块闭合之后。
	if strings.Index(conf, "upstream backend") < strings.Index(conf, "location /") {
		t.Errorf("outside 自定义配置应位于 server 块之后:\n%s", conf)
	}
}

// TestExtraConfRawFallback 锁死：ExtraConf 为空时回退到早期字段 RawConf。
func TestExtraConfRawFallback(t *testing.T) {
	s := testSite()
	s.SSL.Enabled = false
	s.RawConf = `add_header X-Custom "test";`
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(res.SiteConf, `add_header X-Custom "test";`) {
		t.Errorf("RawConf 回退渲染失败:\n%s", res.SiteConf)
	}
}
