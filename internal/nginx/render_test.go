package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// 构造一个覆盖多项功能的测试站点。
func testSite() *model.Site {
	return &model.Site{
		ID:      "site123",
		Name:    "test",
		Domains: model.DomainList{"example.com", "www.example.com"},
		Root:    model.RootConf{Path: "/www/wwwroot/example.com"},
		PHP:     model.PHPConf{Enabled: true, Version: "8.2"},
		SSL:     model.SSLConf{Enabled: true, CertFile: "/c.pem", KeyFile: "/k.pem", ForceHTTPS: true},
		Traffic: model.TrafficConf{RateLimitPerIP: 10, BandwidthLimit: "512k"},
	}
}

func TestRenderBasic(t *testing.T) {
	s := testSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID, DefaultPHPSocket: "/run/php/php8.2-fpm.sock"})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	conf := res.SiteConf

	checks := []struct {
		name    string
		want    string
		notWant string
	}{
		{"server_name 多域名", "server_name example.com www.example.com;", ""},
		// SSL 就绪时必须同时监听 80 和 443。早期版本只输出 443，导致：
		// http 请求落到默认 server 返回别的站点内容、force_https 变成死代码、
		// 以及 ACME HTTP-01 无法在 80 端口验证而使证书续期失败。
		{"SSL 监听 443", "listen 443 ssl;", ""},
		{"SSL 就绪时仍监听 80", "listen 80;", ""},
		{"强制 HTTPS 跳转", "return 301 https://$host$request_uri;", ""},
		{"ACME 挑战目录不被跳转拦截", "location ^~ /.well-known/acme-challenge/ {", ""},
		{"PHP fastcgi", "fastcgi_pass unix:/run/php/php8.2-fpm.sock;", ""},
		{"单连接限速", "limit_rate 512k;", ""},
		{"站点根目录", "root /www/wwwroot/example.com/;", ""},
		{"默认文档", "index index.php index.html index.htm;", ""},
		{"SSL 证书路径", "ssl_certificate /c.pem;", ""},
		{"SSL 私钥路径", "ssl_certificate_key /k.pem;", ""},
	}
	for _, c := range checks {
		if c.want != "" && !strings.Contains(conf, c.want) {
			t.Errorf("%s: 站点配置缺少 %q", c.name, c.want)
		}
		if c.notWant != "" && strings.Contains(conf, c.notWant) {
			t.Errorf("%s: 站点配置不应包含 %q", c.name, c.notWant)
		}
	}
	// limit_req_zone 只能出现在 http 上下文
	if !strings.Contains(res.HTTPConf, "limit_req_zone $binary_remote_addr zone=site123_rate:10m rate=10r/s;") {
		t.Errorf("http 级配置缺少 limit_req_zone:\n%s", res.HTTPConf)
	}
	// limit_req_zone 不能出现在 server 块内
	if strings.Contains(conf, "limit_req_zone") {
		t.Errorf("limit_req_zone 出现在 server 块内，nginx 会报语法错误")
	}
}

func TestRenderAccessLimit(t *testing.T) {
	s := testSite()
	s.SSL.Enabled = false
	s.Access.Enabled = true
	s.Access.Allow = []string{"192.168.1.0/24", "10.0.0.1"}
	res, _ := Render(s, RenderOptions{SiteID: s.ID})

	if !strings.Contains(res.SiteConf, "allow 192.168.1.0/24; 10.0.0.1;") {
		t.Errorf("缺少 IP 白名单配置:\n%s", res.SiteConf)
	}
	if !strings.Contains(res.SiteConf, "deny all;") {
		t.Errorf("白名单模式缺少 deny all 兜底")
	}
}

func TestRenderSubDirBinding(t *testing.T) {
	s := testSite()
	s.SubDir.Enabled = true
	s.SubDir.Rules = []model.SubDirBindingRule{
		{Path: "/blog", Root: "/data/blog"},
	}
	res, _ := Render(s, RenderOptions{SiteID: s.ID})

	if !strings.Contains(res.SiteConf, "alias /data/blog/;") {
		t.Errorf("子目录绑定缺少 alias:\n%s", res.SiteConf)
	}
	if !strings.Contains(res.SiteConf, "location /blog/ {") {
		t.Errorf("子目录绑定缺少 location")
	}
}

func TestRenderRedirectAndProxy(t *testing.T) {
	s := testSite()
	s.Redirect.Rules = []model.RedirectRule{
		{From: "/old", To: "https://example.com/new", Status: 301, Enable: true},
	}
	s.Proxy.Enabled = true
	s.Proxy.Rules = []model.ProxyRule{
		{Path: "/api", Target: "http://127.0.0.1:3000", Enabled: true, WebSocket: true},
	}
	res, _ := Render(s, RenderOptions{SiteID: s.ID})
	conf := res.SiteConf

	if !strings.Contains(conf, "return 301 https://example.com/new;") {
		t.Errorf("缺少重定向规则")
	}
	if !strings.Contains(conf, "proxy_pass http://127.0.0.1:3000;") {
		t.Errorf("缺少反向代理")
	}
	if !strings.Contains(conf, "proxy_set_header Upgrade $http_upgrade;") {
		t.Errorf("WebSocket 支持缺失")
	}
}

func TestRenderHotlink(t *testing.T) {
	s := testSite()
	s.Hotlink.Enabled = true
	s.Hotlink.AllowedDomains = []string{"example.com"}
	s.Hotlink.FileExtensions = []string{"jpg", "png"}
	res, _ := Render(s, RenderOptions{SiteID: s.ID})

	if !strings.Contains(res.SiteConf, "valid_referers none blocked example.com;") {
		t.Errorf("防盗链 valid_referers 缺失")
	}
	if !strings.Contains(res.SiteConf, "if ($invalid_referer)") {
		t.Errorf("防盗链缺少 invalid_referer 判断")
	}
}

func TestRenderSecurity(t *testing.T) {
	s := testSite()
	s.Security.BlockSQLi = true
	s.Security.ServerTokensOff = true
	s.Security.DenyPaths = []string{"/admin"}
	res, _ := Render(s, RenderOptions{SiteID: s.ID})

	if !strings.Contains(res.SiteConf, "server_tokens off;") {
		t.Errorf("缺少 server_tokens off")
	}
	// nginx 正则里 \s 需要写成 \\s；同时模式已扩展出 URL 编码变体
	if !strings.Contains(res.SiteConf, `union(?:\s|%20|\+)+select`) {
		t.Errorf("缺少 SQL 注入拦截规则")
	}
	if !strings.Contains(res.SiteConf, "location ~* ^/admin") {
		t.Errorf("缺少禁止路径规则")
	}
}

func TestRenderRawConf(t *testing.T) {
	s := testSite()
	s.RawConf = "add_header X-Custom \"test\";"
	res, _ := Render(s, RenderOptions{SiteID: s.ID})
	if !strings.Contains(res.SiteConf, "add_header X-Custom \"test\";") {
		t.Errorf("自定义配置未写入")
	}
}

// 大括号必须配平，否则 nginx 直接语法错误。
func TestRenderBraceBalance(t *testing.T) {
	s := testSite()
	s.SubDir.Enabled = true
	s.SubDir.Rules = []model.SubDirBindingRule{{Path: "/blog", Root: "/data/blog"}}
	s.Redirect.Rules = []model.RedirectRule{{From: "/a", To: "/b", Status: 302, Enable: true}}
	s.Proxy.Enabled = true
	s.Proxy.Rules = []model.ProxyRule{{Path: "/api", Target: "http://127.0.0.1:1", Enabled: true}}
	s.Hotlink.Enabled = true
	s.Hotlink.FileExtensions = []string{"jpg"}
	s.Security.BlockXSS = true
	s.Access.Enabled = true

	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	for name, content := range map[string]string{"site": res.SiteConf, "http": res.HTTPConf} {
		open := strings.Count(content, "{")
		close := strings.Count(content, "}")
		if open != close {
			t.Errorf("%s 配置大括号不配平: { =%d, } =%d", name, open, close)
		}
	}
}

func TestValidateDomain(t *testing.T) {
	valid := model.DomainList{"example.com", "www.example.com", "*.example.com", "a-b.example.com"}
	if err := valid.Validate(); err != nil {
		t.Errorf("合法域名被拒: %v", err)
	}
	invalid := []model.DomainList{
		{""},
		{"example..com"},
		{"http://example.com"},
		{"例え.テスト"},
	}
	for _, d := range invalid {
		if err := d.Validate(); err == nil {
			t.Errorf("非法域名未被拒: %v", d)
		}
	}
}
