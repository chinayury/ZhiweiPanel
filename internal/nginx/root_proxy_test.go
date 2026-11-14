package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestRenderRootProxyReplacesMainLocation 整站反代场景：site.proxy.rules[0]
// 路径为 "/" 且 enabled 时，渲染器必须把 location / 让给 proxy_pass，
// 不再输出 try_files / expires / gzip / force_https 跳转等本地静态分支。
//
// 早期实现的问题：buildLocation 与 buildProxy 各自 claimLocation("/")，
// buildLocation 先跑占掉，buildProxy 后跑被 claim 拒绝，渲染出
// "跳过重复的反代路径" 注释但不输出实际 proxy_pass。导致整站反代
// 静默失效——配置看起来有反代，curl 时仍命中主 location 的 try_files。
func TestRenderRootProxyReplacesMainLocation(t *testing.T) {
	site := &model.Site{
		ID:      "site-rootproxy",
		Name:    "rootproxy",
		Domains: model.DomainList{"rp.test"},
		Root:    model.RootConf{Path: "/www/wwwroot/rp.test"},
		Index:   model.IndexConf{Files: []string{"index.html", "index.php"}},
		SSL:     model.SSLConf{Enabled: true, CertFile: "/c.pem", KeyFile: "/k.pem", ForceHTTPS: true},
		PHP:     model.PHPConf{Enabled: true, Version: "8.1"},
		Proxy: model.ProxyConf{
			Enabled: true,
			Rules: []model.ProxyRule{{
				Path:      "/",
				Target:    "http://127.0.0.1:19901",
				Enabled:   true,
				WebSocket: true,
				Timeout:   300,
			}},
		},
	}

	res, err := Render(site, RenderOptions{
		SiteID:           site.ID,
		DefaultPHPSocket: "/run/php/php-8.1-fpm.sock",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	conf := res.SiteConf

	// 1) 必须有 proxy_pass
	if !strings.Contains(conf, "proxy_pass http://127.0.0.1:19901") {
		t.Errorf("期望 proxy_pass 输出，实际:\n%s", conf)
	}
	// 2) 不能再有 try_files（主 location 已让给反代，静态 fallback 失效）
	if strings.Contains(conf, "try_files $uri $uri/ /index.php?$query_string") {
		t.Errorf("主 location 不应再输出 try_files（应让位给 proxy_pass），但实际有:\n%s", conf)
	}
	// 3) force_https 的 301 跳转必须保留——从接管的 location ^~ / 内部
	//    发出（v0.1.3 修复）。旧实现在这里断言"整站反代不应再叠加 301"，
	//    把缺陷当成了预期行为：跳转丢失后面板上 force_https 开关仍显示
	//    "已开启"，http:// 访问却直接服务不跳转。
	if !strings.Contains(conf, "return 301 https://$host$request_uri") {
		t.Errorf("整站反代 + force_https 时必须保留 301 跳转:\n%s", conf)
	}
	// 跳转必须在 location ^~ / 内（server 级会把 ACME 挑战一起跳掉）。
	proxyIdx := strings.Index(conf, "    location ^~ / {")
	redirectIdx := strings.Index(conf, "if ($scheme = http)")
	if proxyIdx < 0 || redirectIdx < proxyIdx {
		t.Errorf("301 跳转应写在 location ^~ / 内部:\n%s", conf)
	}
	// 4) 关键：不能出现 duplicate location（"location / {" 与 "location ^~ / {" 同 server 块）
	// 简单检查："location / {" 出现 0 次，且 "location ^~ / {" 出现 1 次
	main := strings.Count(conf, "    location / {")
	proxyRoot := strings.Count(conf, "    location ^~ / {")
	if main > 0 {
		t.Errorf("整站反代下不应再输出主 location /，但出现 %d 次", main)
	}
	if proxyRoot < 1 {
		t.Errorf("整站反代下应输出 location ^~ / { ... proxy_pass ... }，但出现 0 次")
	}
	// 5) WebSocket 透传也要有
	if !strings.Contains(conf, "proxy_set_header Upgrade $http_upgrade") {
		t.Errorf("WS 启用时必须透传 Upgrade 头，但未找到:\n%s", conf)
	}
}

// TestRenderRootProxyForceHTTPSRedirect 回归测试（2026-11-01 用户报缺陷）：
// 整站反代（path="/"）接管主 location / 后 buildLocation 被整体跳过，
// force_https 的 HTTP→HTTPS 跳转曾随之静默丢失——面板上开关显示"已开启"，
// http:// 访问却直接服务不跳转。
//
// 修复（v0.1.3）：跳转指令补写进接管的 location ^~ / 内。不能放 server 级：
// server 级的 if(...) return 先于 location 匹配执行，会把 ACME 挑战目录
// 一起 301 掉，证书续签随之失败。本测试锁死三个方向：
//  1. 整站反代 + force_https 开 → 跳转在 location ^~ / 内、proxy_pass 之前；
//  2. 整站反代 + force_https 关 → 跳转不在；
//  3. 子路径反代（/api）→ 主 location / 及其跳转照旧，不受影响。
func TestRenderRootProxyForceHTTPSRedirect(t *testing.T) {
	newSite := func(forceHTTPS bool, proxyPath string) *model.Site {
		return &model.Site{
			ID:      "site-fh",
			Name:    "fh",
			Domains: model.DomainList{"fh.test"},
			Root:    model.RootConf{Path: "/www/wwwroot/fh.test"},
			SSL:     model.SSLConf{Enabled: true, CertFile: "/c.pem", KeyFile: "/k.pem", ForceHTTPS: forceHTTPS},
			Proxy: model.ProxyConf{
				Enabled: true,
				Rules: []model.ProxyRule{{
					Path:    proxyPath,
					Target:  "http://127.0.0.1:8787",
					Enabled: true,
				}},
			},
		}
	}
	render := func(s *model.Site) string {
		res, err := Render(s, RenderOptions{SiteID: s.ID})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return res.SiteConf
	}

	t.Run("整站反代+force_https开", func(t *testing.T) {
		conf := render(newSite(true, "/"))
		locIdx := strings.Index(conf, "    location ^~ / {")
		redIdx := strings.Index(conf, "if ($scheme = http)")
		passIdx := strings.Index(conf, "proxy_pass http://127.0.0.1:8787")
		if locIdx < 0 {
			t.Fatalf("缺少 location ^~ /:\n%s", conf)
		}
		if redIdx < locIdx {
			t.Errorf("301 跳转应写在 location ^~ / 内部:\n%s", conf)
		}
		if redIdx > passIdx {
			t.Errorf("301 跳转应在 proxy_pass 之前（先跳转再反代）:\n%s", conf)
		}
		if !strings.Contains(conf, "return 301 https://$host$request_uri") {
			t.Errorf("缺少 301 跳转指令:\n%s", conf)
		}
	})

	t.Run("整站反代+force_https关", func(t *testing.T) {
		conf := render(newSite(false, "/"))
		if strings.Contains(conf, "return 301 https://$host$request_uri") {
			t.Errorf("force_https 关闭时不应有 301 跳转:\n%s", conf)
		}
	})

	t.Run("子路径反代不影响主location跳转", func(t *testing.T) {
		conf := render(newSite(true, "/api"))
		if !strings.Contains(conf, "    location / {") {
			t.Errorf("子路径反代应保留主 location /:\n%s", conf)
		}
		if !strings.Contains(conf, "return 301 https://$host$request_uri") {
			t.Errorf("子路径反代时主 location 的 301 跳转应保留:\n%s", conf)
		}
		if !strings.Contains(conf, "    location ^~ /api {") {
			t.Errorf("缺少 /api 反代 location:\n%s", conf)
		}
	})
}

// TestRenderSubpathProxyKeepsMainLocation 非根路径的反代：/api 这种
// 仍应保留主 location / 的本地静态服务。
func TestRenderSubpathProxyKeepsMainLocation(t *testing.T) {
	site := &model.Site{
		ID:      "site-subproxy",
		Name:    "subproxy",
		Domains: model.DomainList{"sp.test"},
		Root:    model.RootConf{Path: "/www/wwwroot/sp.test"},
		Proxy: model.ProxyConf{
			Enabled: true,
			Rules: []model.ProxyRule{{
				Path:    "/api",
				Target:  "http://127.0.0.1:3000",
				Enabled: true,
				Timeout: 60,
			}},
		},
	}

	res, err := Render(site, RenderOptions{SiteID: site.ID})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	conf := res.SiteConf
	if !strings.Contains(conf, "    location ^~ /api {") {
		t.Errorf("期望 /api 反代 location:\n%s", conf)
	}
	if !strings.Contains(conf, "proxy_pass http://127.0.0.1:3000") {
		t.Errorf("期望 proxy_pass 指向 3000:\n%s", conf)
	}
	if !strings.Contains(conf, "    location / {") {
		t.Errorf("非根反代下主 location / 应保留:\n%s", conf)
	}
}
