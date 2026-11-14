package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestProxyStillRendersAfterReorder 锁死一次真实回归：
// 调整 build 顺序让主 location 先占位后，反代 location 整个消失了
// （线上实测 /api/ 从 401 变 404）。根因是 claimLocation 的注册表
// 被先跑的 build 方法污染，或 buildProxy 被提前 return。
func TestProxyStillRendersAfterReorder(t *testing.T) {
	s := &model.Site{
		ID:      "px2",
		Name:    "px2",
		Status:  model.SiteStatusRunning,
		Domains: model.DomainList{"px.test"},
		Root:    model.RootConf{Path: "/www/px"},
		SSL: model.SSLConf{
			Enabled: true, CertFile: "/c.pem", KeyFile: "/k.pem",
			ForceHTTPS: true, Protocols: []string{"TLSv1.2", "TLSv1.3"},
		},
		PHP: model.PHPConf{Enabled: true, Socket: "/run/php/php8.1-fpm.sock"},
		Proxy: model.ProxyConf{
			Enabled: true,
			Rules: []model.ProxyRule{
				{Path: "/api", Target: "http://127.0.0.1:8888", Enabled: true, Timeout: 30},
			},
		},
		SubDir:   model.SubDirConf{Enabled: true, Rules: []model.SubDirBindingRule{{Path: "/sub", Root: "/data/sub"}}},
		Redirect: model.RedirectConf{Rules: []model.RedirectRule{{From: "/old.html", To: "/", Status: 301, Enable: true}}},
		Security: model.SecurityConf{DenyPaths: []string{"/secret.html"}, BlockSQLi: true},
		Hotlink:  model.HotlinkConf{Enabled: true, AllowedDomains: []string{"px.test"}, FileExtensions: []string{"png"}},
	}

	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf

	// 反代必须还在
	if !strings.Contains(conf, "location ^~ /api {") {
		t.Errorf("反代 location 丢失:\n%s", conf)
	}
	if !strings.Contains(conf, "proxy_pass http://127.0.0.1:8888;") {
		t.Errorf("proxy_pass 丢失:\n%s", conf)
	}
	// 主 location 必须在
	if !strings.Contains(conf, "location / {") {
		t.Errorf("主 location 丢失:\n%s", conf)
	}
	// HTTPS 跳转必须在 location / 内
	if !strings.Contains(conf, "if ($scheme = http)") {
		t.Errorf("HTTPS 跳转丢失:\n%s", conf)
	}
	// 其它 location 也要在
	for _, must := range []string{
		"location ^~ /logs/",
		"location ~* ^/secret\\.html",
		"location ^~ /.well-known/acme-challenge/",
		"location /sub/",
		"location /old.html",
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("缺少 %q:\n%s", must, conf)
		}
	}
	// 不能有重复
	if dups := duplicateLocationPrefixes(conf); len(dups) > 0 {
		t.Errorf("重复 location: %v", dups)
	}
}
