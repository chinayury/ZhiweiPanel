package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestMinimalSiteConfig 基线配置快照：确认最小站点生成的 server 块结构正确。
func TestMinimalSiteConfig(t *testing.T) {
	s := &model.Site{
		ID:      "min1",
		Name:    "minimal",
		Domains: model.DomainList{"example.com"},
		Root:    model.RootConf{Path: "/www/wwwroot/example.com"},
	}
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	conf := res.SiteConf

	want := []string{
		"server {",
		"listen 80;",
		"server_name example.com;",
		"root /www/wwwroot/example.com/;",
		"try_files $uri $uri/ /index.php?$query_string;",
	}
	for _, w := range want {
		if !strings.Contains(conf, w) {
			t.Errorf("缺少 %q", w)
		}
	}
	// 纯静态站点不应出现 PHP 配置
	if strings.Contains(conf, "fastcgi_pass") {
		t.Errorf("未启用 PHP 时不应输出 fastcgi_pass")
	}
	// 未启用 SSL 时不应输出证书指令
	if strings.Contains(conf, "ssl_certificate") {
		t.Errorf("未启用 SSL 时不应输出 ssl_certificate")
	}
}
