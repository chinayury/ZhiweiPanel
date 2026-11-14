package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestWAFPatternCoversURLEncoding 锁死一个静默漏报：
// $request_uri 是未经解码的原始 URI，攻击方把 < 空格 编码成 %3C %20 后，
// 只匹配字面量的正则整类放过（实测 ?q=%3Cscript%3E 返回 200）。
func TestWAFPatternCoversURLEncoding(t *testing.T) {
	cases := []struct {
		name     string
		pattern  string
		mustHave []string
		mustNot  []string
	}{
		{
			name:     "空格类要同时匹配字面量与 %20",
			pattern:  `union\s+select`,
			mustHave: []string{`union(?:\s|%20|\+)+select`},
		},
		{
			name:     "尖括号要匹配编码形式",
			pattern:  `<script`,
			mustHave: []string{`(?:<|%3C)script`},
		},
		{
			name:     "点分转义序列不能被拆坏",
			pattern:  `\.\./`,
			mustHave: []string{`\.\.(?:/|%2F)`},
		},
		{
			name:     "插入的括号不能被二次展开",
			pattern:  `a\s+b`,
			mustNot:  []string{`(?:\(|%28)`, `(?:\)|%29)`},
			mustHave: []string{`a(?:\s|%20|\+)+b`},
		},
	}
	for _, c := range cases {
		got := wafPattern(c.pattern)
		for _, m := range c.mustHave {
			if !strings.Contains(got, m) {
				t.Errorf("%s: 输出 %q 缺少 %q", c.name, got, m)
			}
		}
		for _, m := range c.mustNot {
			if strings.Contains(got, m) {
				t.Errorf("%s: 输出 %q 不应包含 %q（括号被二次展开）", c.name, got, m)
			}
		}
	}
}

// TestDenyPathAndRedirectExactMatch 锁死"补尾斜杠"缺陷：
// normalizePathPrefix 会强制补 /，于是 /secret.html 渲染成
// ^/secret\.html/，请求 /secret.html 反而匹配不上，黑名单形同虚设。
func TestDenyPathAndRedirectExactMatch(t *testing.T) {
	s := &model.Site{
		ID:      "s1",
		Name:    "t",
		Status:  model.SiteStatusRunning,
		Domains: model.DomainList{"a.test"},
		Root:    model.RootConf{Path: "/www/a"},
		Security: model.SecurityConf{
			DenyPaths: []string{"/secret.html", "/config.php"},
		},
		Redirect: model.RedirectConf{
			Rules: []model.RedirectRule{
				{From: "/old.html", To: "/", Status: 301, Enable: true},
			},
		},
	}
	res, err := Render(s, RenderOptions{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf

	for _, must := range []string{
		`location ~* ^/secret\.html`,
		`location ~* ^/config\.php`,
		`location /old.html {`,
	} {
		if !strings.Contains(conf, must) {
			t.Errorf("缺少 %q，完整配置:\n%s", must, conf)
		}
	}
	// 不能出现被补了尾斜杠的形态
	for _, bad := range []string{
		`^/secret\.html/`,
		`^/config\.php/`,
		`location /old.html/ {`,
	} {
		if strings.Contains(conf, bad) {
			t.Errorf("不应包含 %q（尾斜杠导致精确路径失效）:\n%s", bad, conf)
		}
	}
}

// TestRewritesAreRendered 锁死伪静态静默丢失：
// RenderOptions.PathRewrites 早期定义了却从未被消费，
// 面板显示"已启用"但 nginx 里一行 rewrite 都没有。
func TestRewritesAreRendered(t *testing.T) {
	s := &model.Site{
		ID:      "s2",
		Name:    "t",
		Status:  model.SiteStatusRunning,
		Domains: model.DomainList{"a.test"},
		Root:    model.RootConf{Path: "/www/a"},
	}
	rules := "rewrite ^/old/(.*)$ /new/$1 permanent;\nrewrite ^/x$ /y last;"
	res, err := Render(s, RenderOptions{PathRewrites: rules})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, must := range []string{
		"rewrite ^/old/(.*)$ /new/$1 permanent;",
		"rewrite ^/x$ /y last;",
	} {
		if !strings.Contains(res.SiteConf, must) {
			t.Errorf("伪静态规则未渲染，缺少 %q:\n%s", must, res.SiteConf)
		}
	}
}

// TestRawConfHeaderSurvivesLocation 自定义配置里的 add_header 不能被
// location 内的 add_header 顶掉（nginx 的 add_header 不跨层继承）。
func TestRawConfHeaderSurvivesLocation(t *testing.T) {
	s := &model.Site{
		ID:      "s3",
		Name:    "t",
		Status:  model.SiteStatusRunning,
		Domains: model.DomainList{"a.test"},
		Root:    model.RootConf{Path: "/www/a"},
		RawConf: `add_header X-Test "e2e" always;`,
		Extra:   model.ExtraConf{BrowserCache: 86400},
	}
	res, err := Render(s, RenderOptions{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf
	if !strings.Contains(conf, `add_header X-Test "e2e" always;`) {
		t.Errorf("自定义配置未注入:\n%s", conf)
	}
	// location 内不应再有重复的 Cache-Control add_header——它会顶掉继承
	locStart := strings.Index(conf, "location / {")
	if locStart >= 0 {
		locEnd := strings.Index(conf[locStart:], "\n    }")
		if locEnd > 0 {
			locBody := conf[locStart : locStart+locEnd]
			if strings.Contains(locBody, "add_header Cache-Control") {
				t.Errorf("location / 内仍有 add_header Cache-Control，会顶掉自定义 header:\n%s", locBody)
			}
		}
	}
	// expires 要保留（它本身就会设置 Cache-Control）
	if !strings.Contains(conf, "expires 86400;") {
		t.Errorf("expires 丢失:\n%s", conf)
	}
}
