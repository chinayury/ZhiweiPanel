package nginx

import (
	"strings"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// sslSite 构造一个开了 SSL + 强制 HTTPS 的站点。
func sslSite() *model.Site {
	return &model.Site{
		ID:      "site-ssl",
		Name:    "ssl-test",
		Status:  model.SiteStatusRunning,
		Domains: model.DomainList{"a.test", "www.a.test"},
		Root:    model.RootConf{Path: "/www/wwwroot/a.test"},
		Index:   model.IndexConf{Files: []string{"index.html"}},
		SSL: model.SSLConf{
			Enabled:    true,
			CertFile:   "/c.pem",
			KeyFile:    "/k.pem",
			ForceHTTPS: true,
			HSTS:       true,
			Protocols:  []string{"TLSv1.2", "TLSv1.3"},
		},
	}
}

// TestSSLRendersBothListeners SSL 就绪时必须同时监听 80 与 443。
// 只输出 443 会让 http 请求落到默认 server，并让 ACME HTTP-01 无法验证。
func TestSSLRendersBothListeners(t *testing.T) {
	res, err := Render(sslSite(), RenderOptions{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"listen 80;", "listen 443 ssl;"} {
		if !strings.Contains(res.SiteConf, want) {
			t.Errorf("缺少 %q，完整配置:\n%s", want, res.SiteConf)
		}
	}
}

// TestForceHTTPSDoesNotBlockACME 锁死一个会静默掐断证书续期的缺陷：
// 强制 HTTPS 的跳转绝不能把 /.well-known/acme-challenge/ 一起 301 掉。
// P2 的解法是 80 与 443 拆两个 server 块：80 块里 ACME location 精确前缀
// 先于 location / 的 301 命中，续期不受影响；443 块专注站点本身。
func TestForceHTTPSDoesNotBlockACME(t *testing.T) {
	res, err := Render(sslSite(), RenderOptions{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf

	// 1. 必须有独立的 ACME location，且用 ^~ 前缀匹配
	if !strings.Contains(conf, "location ^~ /.well-known/acme-challenge/ {") {
		t.Fatalf("缺少 ACME 挑战 location，续签会失败:\n%s", conf)
	}

	// 2. 跳转由 80 块的 location / { return 301 ... } 承担，不再用 if($scheme=http)
	if strings.Contains(conf, "if ($scheme = http)") {
		t.Errorf("不应再用 if($scheme=http) 跳转（应拆 80/443 两个 server 块）:\n%s", conf)
	}
	if !strings.Contains(conf, "return 301 https://$host$request_uri;") {
		t.Errorf("缺少 301 跳转指令:\n%s", conf)
	}

	// 3. 80 块里 ACME location 必须先于 location / 的 301 命中
	acmeIdx := strings.Index(conf, "location ^~ /.well-known/acme-challenge/ {")
	redirLocIdx := strings.Index(conf, "location / {")
	if acmeIdx < 0 || redirLocIdx < 0 || acmeIdx > redirLocIdx {
		t.Errorf("ACME location 应位于 80 块的 location / 之前（否则续期被 301 拦截）:\n%s", conf)
	}

	// 4. ACME location 自身不能含跳转
	acmeBody := conf[acmeIdx:]
	if i := strings.Index(acmeBody, "\n    }"); i > 0 {
		acmeBody = acmeBody[:i]
	}
	if strings.Contains(acmeBody, "return 301") {
		t.Errorf("ACME location 内含跳转:\n%s", acmeBody)
	}
}

// TestNoSSLStaysHTTPOnly 没有证书时不能输出 ssl 监听，否则 nginx 起不来。
func TestNoSSLStaysHTTPOnly(t *testing.T) {
	s := sslSite()
	s.SSL.CertFile = ""
	res, err := Render(s, RenderOptions{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(res.SiteConf, "listen 443") {
		t.Errorf("缺少证书时不应监听 443:\n%s", res.SiteConf)
	}
	if !strings.Contains(res.SiteConf, "listen 80;") {
		t.Errorf("应监听 80:\n%s", res.SiteConf)
	}
	// 没有证书就不该跳转，否则会跳到一个打不开的 https
	if strings.Contains(res.SiteConf, "return 301 https://") {
		t.Errorf("无证书时不应输出 HTTPS 跳转:\n%s", res.SiteConf)
	}
}

// TestServerLevelRootPresent 锁死 PHP 404 的根因：
// location ~ \.php$ 用 $document_root 拼 SCRIPT_FILENAME，而 $document_root
// 取当前层级生效的 root。root 只在 location / 里写时，PHP location 会回落到
// nginx 默认根目录，.php 全部 404 "File not found."——而直连 FPM 是正常的。
func TestServerLevelRootPresent(t *testing.T) {
	s := sslSite()
	s.PHP.Enabled = true
	s.PHP.Socket = "/run/php/php8.1-fpm.sock"
	res, err := Render(s, RenderOptions{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf

	// 1. 必须存在 PHP location
	phpIdx := strings.Index(conf, "location ~ \\.php$ {")
	if phpIdx < 0 {
		t.Fatalf("缺少 PHP location:\n%s", conf)
	}

	// 2. root 必须在 443 块的 server 级（4 空格缩进），不能只在 location / 里。
	// P2 拆块后 80 块是纯跳转块、没有 root；真正的 root 在 443 块 server 级，
	// 因此这里直接断言 server 级的 root 指令存在（location / 内的是 8 空格缩进）。
	if !strings.Contains(conf, "    root /www/wwwroot/a.test/;") {
		t.Errorf("443 块 server 级缺少 root，PHP location 的 $document_root 会取错:\n%s", conf)
	}
}

var _ = time.Now
