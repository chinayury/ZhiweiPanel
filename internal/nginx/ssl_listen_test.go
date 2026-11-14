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
// 早期把跳转写在 server 级，server 级 if(...) return 先于 location 匹配执行，
// 于是 /.well-known/acme-challenge/ 也被 301 到 https，LE 验证失败、
// 89 天后证书到期站点下线。跳转必须落在 location / 内，且 ACME 目录要用 ^~ 放行。
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

	// 2. 跳转必须位于 location / 内部，不能裸露在 server 级
	locStart := strings.Index(conf, "location / {")
	if locStart < 0 {
		t.Fatalf("缺少 location /")
	}
	locEnd := strings.Index(conf[locStart:], "\n    }")
	if locEnd < 0 {
		t.Fatalf("location / 未闭合")
	}
	locBody := conf[locStart : locStart+locEnd]
	if !strings.Contains(locBody, "if ($scheme = http)") {
		t.Errorf("跳转不在 location / 内，会连 ACME 挑战一起 301:\n%s", locBody)
	}

	// 3. server 级（location 之外）不允许出现跳转
	serverLevel := conf[:locStart]
	if strings.Contains(serverLevel, "return 301 https://") {
		t.Errorf("server 级存在跳转，会在 location 匹配前拦截 ACME:\n%s", serverLevel)
	}

	// 4. ACME location 自身不能含跳转
	acmeStart := strings.Index(conf, "location ^~ /.well-known/acme-challenge/ {")
	if acmeStart >= 0 {
		acmeEnd := strings.Index(conf[acmeStart:], "\n    }")
		if acmeEnd > 0 {
			acmeBody := conf[acmeStart : acmeStart+acmeEnd]
			if strings.Contains(acmeBody, "return 301") {
				t.Errorf("ACME location 内含跳转:\n%s", acmeBody)
			}
		}
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

	// 2. server 级（第一个 location 之前）必须有 root
	firstLoc := strings.Index(conf, "location ")
	if firstLoc < 0 {
		t.Fatalf("没有任何 location")
	}
	serverLevel := conf[:firstLoc]
	if !strings.Contains(serverLevel, "root /www/wwwroot/a.test/;") {
		t.Errorf("server 级缺少 root，PHP location 的 $document_root 会取错:\n%s", serverLevel)
	}
}

var _ = time.Now
