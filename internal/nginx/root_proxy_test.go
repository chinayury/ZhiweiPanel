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
	// 3) force_https 的 301 跳转必须保留——P2 拆块后由 80 块的 location / 承担
	//    （v0.1.3 曾把它补写进接管的 location ^~ / 内，现改为独立 80 块）。
	if !strings.Contains(conf, "return 301 https://$host$request_uri") {
		t.Errorf("整站反代 + force_https 时必须保留 301 跳转:\n%s", conf)
	}
	// 拆块后绝不再用 if($scheme=http) 跳转。
	if strings.Contains(conf, "if ($scheme = http)") {
		t.Errorf("不应再用 if($scheme=http) 跳转:\n%s", conf)
	}
	// 4) 关键：不能出现 duplicate location。拆块后 80 块有 location / {（301），
	// 443 块有 location ^~ / {（proxy_pass），各出现一次、互不冲突。
	if main := strings.Count(conf, "    location / {"); main != 1 {
		t.Errorf("80 块应有且只有一个 location / {（301），实际 %d 次:\n%s", main, conf)
	}
	if proxyRoot := strings.Count(conf, "    location ^~ / {"); proxyRoot != 1 {
		t.Errorf("443 块应有且只有一个 location ^~ / {（proxy_pass），实际 %d 次:\n%s", proxyRoot, conf)
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
// P2 的解法：SSL 就绪 + 强制 HTTPS 时拆 80 / 443 两个 server 块，
// 80 块承担 301 跳转（location / { return 301 }），443 块专注反代。
// 本测试锁死三个方向：
//  1. 整站反代 + force_https 开 → 跳转在 80 块、反代在 443 块；
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
		if !strings.Contains(conf, "return 301 https://$host$request_uri;") {
			t.Errorf("缺少 301 跳转指令:\n%s", conf)
		}
		// P2 拆块：跳转在 80 块、反代在 443 块，不再用 if($scheme=http)。
		if strings.Contains(conf, "if ($scheme = http)") {
			t.Errorf("不应再用 if($scheme=http) 跳转:\n%s", conf)
		}
		if !strings.Contains(conf, "    location ^~ / {") {
			t.Errorf("缺少整站反代 location ^~ /:\n%s", conf)
		}
		if !strings.Contains(conf, "proxy_pass http://127.0.0.1:8787") {
			t.Errorf("缺少 proxy_pass:\n%s", conf)
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
