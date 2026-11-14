package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// fullFeatureSite 构造一个开启全部功能的站点，用于全量渲染断言。
// 用例名用 ASCII 前缀编号，避免中文在不同编辑器/终端间转码时被破坏。
func fullFeatureSite() *model.Site {
	return &model.Site{
		ID:      "full1",
		Name:    "full-site",
		Domains: model.DomainList{"a.com", "www.a.com", "b.com"},
		SubDir: model.SubDirConf{
			Enabled: true,
			Rules:   []model.SubDirBindingRule{{Path: "/blog", Root: "/data/blog"}},
		},
		Root: model.RootConf{Path: "/www/wwwroot/a.com"},
		Access: model.AccessConf{
			Enabled:      true,
			Allow:        []string{"192.168.1.0/24"},
			AllowMethods: []string{"GET", "POST"},
		},
		Traffic: model.TrafficConf{
			RateLimitPerIP: 20,
			BandwidthLimit: "1m",
			MonthlyQuotaMB: 10240,
			EnforceQuota:   true,
		},
		Rewrite: model.RewriteConf{
			Enabled: true,
			Rules:   "if (!-e $request_filename){ rewrite ^(.*)$ /index.php?s=/$1 last; }",
		},
		Index:   model.IndexConf{Files: []string{"index.php", "index.html"}},
		RawConf: "add_header X-Demo \"1\";",
		SSL: model.SSLConf{
			Enabled: true, CertFile: "/c.pem", KeyFile: "/k.pem",
			ForceHTTPS: true, HSTS: true, HSTSMaxAge: 31536000,
			Protocols: []string{"TLSv1.2", "TLSv1.3"}, OCSP: true,
		},
		PHP: model.PHPConf{
			Enabled: true, Version: "8.2",
			MaxExecutionTime: 60, MemoryLimit: "512M",
			DisableFunctions: []string{"exec", "shell_exec"},
		},
		Redirect: model.RedirectConf{},
		Proxy: model.ProxyConf{
			Enabled: true,
			Rules: []model.ProxyRule{
				{Path: "/api/", Target: "http://127.0.0.1:8000", Enabled: true, WebSocket: true},
				{Path: "/ws", Target: "http://127.0.0.1:9000", Enabled: true, WebSocket: true, Timeout: 300},
			},
		},
		Hotlink: model.HotlinkConf{
			Enabled: true, AllowedDomains: []string{"a.com"},
			FileExtensions: []string{"jpg", "png", "zip"}, ReturnCode: 403,
		},
		Lock: model.LockConf{
			Enabled: true, Files: []string{"index.php", "config/"},
			AlertOnTamper: true, AutoRestore: true,
		},
		Security: model.SecurityConf{
			BlockSQLi: true, BlockXSS: true, BlockTraversal: true,
			BlockUA: true, BlockUAKeywords: []string{"sqlmap", "nikto"},
			DenyPaths:       []string{"/admin", "/.git"},
			DenyExtensions:  []string{"sql", "bak", "env"},
			ServerTokensOff: true,
			PreventIframe:   true,
		},
		Logs: model.LogsConf{
			AccessLog: true, ErrorLog: true, RetentionDays: 30,
		},
		Alert: model.AlertConf{
			Enabled: true,
			Rules: []model.AlertRule{
				{Type: "status_5xx", Level: "warning", Value: 10, WindowMinutes: 5, Enabled: true},
			},
			Channels: []model.AlertChannel{{Type: "webhook", Target: "http://x", Enabled: true}},
		},
		Extra: model.ExtraConf{
			Gzip: true, GzipMinLength: 1024, ClientMaxBodySize: "100m",
			BrowserCache: 86400, StaticCacheExt: []string{"css", "js", "png"},
			DNSResolver: "223.5.5.5",
			// 显式给出可信代理网段；留空时 real_ip 整段不输出
			TrustedProxies: []string{"10.0.0.0/8", "192.168.1.0/24"},
		},
	}
}

// TestFullFeatureConfigIsValid 全功能组合必须生成合法配置，且 16 项功能的
// 关键指令都要出现在配置里（防止功能静默丢失）。
func TestFullFeatureConfigIsValid(t *testing.T) {
	s := fullFeatureSite()
	s.Redirect.Rules = []model.RedirectRule{
		{From: "/old", To: "https://a.com/new", Status: 301, Enable: true},
		{From: "/tmp-page", To: "/new-page", Status: 302, Enable: true},
	}

	res, err := Render(s, RenderOptions{SiteID: s.ID, DefaultPHPSocket: "/run/php/php8.2-fpm.sock"})
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	v := Validate(res.SiteConf, testHTTPConf())
	if !v.OK() {
		t.Fatalf("full-feature config invalid:\n%s\n\n---- site conf ----\n%s\n\n---- http conf ----\n%s",
			v.Summary(), res.SiteConf, res.HTTPConf)
	}

	conf := res.SiteConf
	checks := []struct {
		name string
		want string
	}{
		{"01-domain", "server_name a.com www.a.com b.com;"},
		{"02-subdir-alias", "alias /data/blog/;"},
		{"03-root", "root /www/wwwroot/a.com/;"},
		{"04-access-allow", "allow 192.168.1.0/24;"},
		{"05-rate-limit", "limit_rate 1m;"},
		{"05-req-limit", "limit_req zone=full1_rate burst=40 nodelay;"},
		{"06-index", "index index.php index.html;"},
		{"07-raw-conf", "add_header X-Demo \"1\";"},
		{"08-ssl-cert", "ssl_certificate /c.pem;"},
		{"08-ssl-force-https", "return 301 https://$host$request_uri;"},
		{"08-ssl-hsts", "Strict-Transport-Security"},
		{"08-ssl-both-listeners", "listen 443 ssl;"},
		{"09-php", "fastcgi_pass unix:/run/php/php8.2-fpm.sock;"},
		{"10-redirect", "return 301 https://a.com/new;"},
		{"11-proxy", "proxy_pass http://127.0.0.1:8000/;"},
		{"11-websocket", "proxy_set_header Upgrade $http_upgrade;"},
		{"12-hotlink", "valid_referers none blocked a.com;"},
		{"13-waf-sqli", "union(?:\\s|%20|\\+)+select"},
		{"13-waf-ua", "sqlmap"},
		{"13-deny-ext", "\\.(sql|bak|env)$"},
		{"14-access-log", "access_log /www/wwwroot/a.com/logs/access.log"},
		{"15-real-ip", "set_real_ip_from 10.0.0.0/8;"},
		{"16-gzip", "gzip on;"},
		{"16-body-size", "client_max_body_size 100m;"},
		{"16-static-cache", "expires 86400;"},

		// --- v0.1.0 新增的安全/健壮性断言 ---
		// 日志目录默认拒绝外网，否则 /logs/access.log 可被公开下载
		{"17-logs-dir-denied", "location ^~ /logs/ { deny all; }"},
		// 反代必须用 ^~，否则正则 location（\.php$、deny 正则）抢走它的请求
		{"18-proxy-prefix-priority", "location ^~ /api/ {"},
		// 静态缓存 location 有自己的 add_header，必须重复输出安全头
		{"19-security-header-in-cache-loc", "X-Frame-Options"},
		// 绝不允许信任全网代理
		{"20-no-trust-all-proxies", "set_real_ip_from 0.0.0.0/0;"},
	}
	for _, c := range checks {
		if c.name == "20-no-trust-all-proxies" {
			if strings.Contains(conf, c.want) {
				t.Errorf("%s: config must NOT contain %q (trusting all proxies lets X-Forwarded-For bypass IP allowlist)", c.name, c.want)
			}
			continue
		}
		if !strings.Contains(conf, c.want) {
			t.Errorf("%s: config missing %q", c.name, c.want)
		}
	}

	// 静态缓存 location 内部必须重复输出安全头（nginx 的 add_header 不跨层继承）
	cacheIdx := strings.Index(conf, "location ~* \\.(css|js|png)$")
	if cacheIdx >= 0 {
		end := strings.Index(conf[cacheIdx:], "\n    }")
		if end > 0 {
			body := conf[cacheIdx : cacheIdx+end]
			if !strings.Contains(body, "X-Frame-Options") {
				t.Errorf("static-cache location lost inherited security headers:\n%s", body)
			}
		}
	}
}

// TestNoTrustAllProxies 锁死 real_ip 全网信任缺陷：
// set_real_ip_from 0.0.0.0/0 会让攻击者直连源站伪造 X-Forwarded-For，
// 同时绕过 IP 白名单与按 IP 限速。只有显式配置可信网段才输出 real_ip。
func TestNoTrustAllProxies(t *testing.T) {
	// 配了可信网段 -> 输出，且不含 0.0.0.0/0
	s := fullFeatureSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.SiteConf, "set_real_ip_from 10.0.0.0/8;") {
		t.Error("configured trusted proxy missing")
	}
	if strings.Contains(res.SiteConf, "0.0.0.0/0") {
		t.Error("must never trust 0.0.0.0/0")
	}

	// 没配可信网段 -> 整段 real_ip 不输出
	s2 := fullFeatureSite()
	s2.Extra.TrustedProxies = nil
	res2, err := Render(s2, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res2.SiteConf, "real_ip_header") {
		t.Error("without trusted proxies, real_ip must not be emitted at all")
	}
}

// TestNoHTTPOnlyDirectiveInServer 关键回归：http 级指令绝不能进 server 块。
func TestNoHTTPOnlyDirectiveInServer(t *testing.T) {
	s := fullFeatureSite()
	s.Traffic.RateLimitPerIP = 15
	s.SubDir.Enabled = true
	res, _ := Render(s, RenderOptions{SiteID: s.ID})

	v := Validate(res.SiteConf, testHTTPConf())
	if !v.OK() {
		t.Errorf("http-level directive leaked into server block:\n%s", v.Summary())
	}

	httpConf := testHTTPConf()
	if !strings.Contains(httpConf, "log_format") {
		t.Error("test http conf must contain log_format")
	}
	if !strings.Contains(res.HTTPConf, "limit_req_zone") {
		t.Error("http conf must contain limit_req_zone")
	}
}

// TestStoppedSiteStillValid 停用站点只在前面加注释，配置依然合法。
func TestStoppedSiteStillValid(t *testing.T) {
	s := fullFeatureSite()
	res, _ := Render(s, RenderOptions{SiteID: s.ID})
	stopped := "# site stopped\n" + res.SiteConf
	v := Validate(stopped, testHTTPConf())
	if !v.OK() {
		t.Errorf("stopped site config invalid:\n%s", v.Summary())
	}
}

// TestSSLWithoutCertFallsBack 开启 SSL 但无证书时必须退化为 80，
// 否则 nginx 会因缺少证书文件拒绝启动。
func TestSSLWithoutCertFallsBack(t *testing.T) {
	s := fullFeatureSite()
	s.SSL.Enabled = true
	s.SSL.CertFile = ""
	s.SSL.KeyFile = ""

	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.SiteConf, "listen 443") {
		t.Error("without cert must not listen on 443")
	}
	if strings.Contains(res.SiteConf, "ssl_certificate") {
		t.Error("without cert must not emit ssl_certificate")
	}
	if strings.Contains(res.SiteConf, "return 301 https://") {
		t.Error("without cert must not force https (would loop)")
	}
	v := Validate(res.SiteConf, testHTTPConf())
	if !v.OK() {
		t.Errorf("no-cert config invalid:\n%s", v.Summary())
	}
}

// TestVariousPHPVersions PHP 版本切换只影响 socket。
func TestVariousPHPVersions(t *testing.T) {
	for _, ver := range []string{"7.4", "8.0", "8.1", "8.2", "8.3"} {
		s := fullFeatureSite()
		s.PHP.Version = ver
		res, _ := Render(s, RenderOptions{SiteID: s.ID})
		want := "fastcgi_pass unix:/run/php/php" + ver + "-fpm.sock;"
		if !strings.Contains(res.SiteConf, want) {
			t.Errorf("PHP %s: missing %s", ver, want)
		}
	}
}

// TestValidatorCatchesRealErrors 校验器必须能抓到真实错误。
func TestValidatorCatchesRealErrors(t *testing.T) {
	cases := []struct {
		name string
		conf string
		want string
	}{
		{"unbalanced-braces", "server {\n    location / {\n", "大括号不配平"},
		{"extra-braces", "server {\n}\n}\n", "多余"},
		{"http-directive-in-server", "server {\n    limit_req_zone $binary_remote_addr zone=a:1m rate=1r/s;\n}\n", "http"},
	}
	for _, c := range cases {
		v := Validate(c.conf, "")
		if v.OK() {
			t.Errorf("%s: validator missed the error", c.name)
			continue
		}
		if !strings.Contains(v.Summary(), c.want) {
			t.Errorf("%s: message lacks %q, got %s", c.name, c.want, v.Summary())
		}
	}
}

// TestQuotaEnforceActuallyBlocks 锁死配额空操作缺陷：
// $zw_quota_lock 早期只 set 不读，界面显示"已锁定"实际流量照常放行。
func TestQuotaEnforceActuallyBlocks(t *testing.T) {
	s := fullFeatureSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID, QuotaExceeded: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.SiteConf, "set $zw_quota_lock 1;") {
		t.Fatal("quota lock variable not set")
	}
	if !strings.Contains(res.SiteConf, "if ($zw_quota_lock)") {
		t.Error("quota lock is set but never read -> enforce_quota is a no-op")
	}

	// 未超额时不应出现拦截
	s2 := fullFeatureSite()
	res2, _ := Render(s2, RenderOptions{SiteID: s.ID, QuotaExceeded: false})
	if strings.Contains(res2.SiteConf, "zw_quota_lock") {
		t.Error("quota lock must not appear when not exceeded")
	}
}
