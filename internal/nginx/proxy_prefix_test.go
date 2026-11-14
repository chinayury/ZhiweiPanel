package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestProxyLocationUsesCaretPrefix 锁死反代 location 修饰符：
// nginx 的正则 location 优先于普通前缀 location，不加强修饰符时
// 反代 /api/ 下的 .php 会被 location ~ \.php$ 抢走到 FPM，
// deny_paths 的正则也会把反代路径整个 403。
func TestProxyLocationUsesCaretPrefix(t *testing.T) {
	s := &model.Site{
		ID:      "px1",
		Name:    "px",
		Status:  model.SiteStatusRunning,
		Domains: model.DomainList{"px.test"},
		Root:    model.RootConf{Path: "/www/px"},
		PHP:     model.PHPConf{Enabled: true, Socket: "/run/php/php8.1-fpm.sock"},
		Proxy: model.ProxyConf{
			Enabled: true,
			Rules: []model.ProxyRule{
				{Path: "/api", Target: "http://127.0.0.1:8888", Enabled: true, Timeout: 30},
			},
		},
	}
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	conf := res.SiteConf

	if !strings.Contains(conf, "location ^~ /api {") {
		t.Errorf("反代 location 未使用 ^~ 修饰符，正则 location 会抢走它的请求:\n%s", conf)
	}
	if !strings.Contains(conf, "proxy_pass http://127.0.0.1:8888;") {
		t.Errorf("缺少 proxy_pass:\n%s", conf)
	}
	// 不能出现不带 ^~ 的同名 location（那会 duplicate location 导致 nginx -t 失败）
	if strings.Contains(conf, "location /api {") {
		t.Errorf("同时存在不带 ^~ 的 location /api，会 duplicate location:\n%s", conf)
	}
}

// TestProxyLocationTargetTrailingSlashNormalized 锁死反代 target 尾斜杠归一化：
// 用户填的 target 是否带 "/" 是 UI 习惯差异，渲染时不能让 target 的尾斜杠污染
// proxy_pass。path=/mag（无尾斜杠）+ target=http://x:port/（带尾斜杠）会渲染出
// proxy_pass http://x:port/; 配 location ^~ /mag → nginx 把前缀 /mag 替换为 /，
// 剩下的 "/" 还在前面 → 上游收到 "//" 触发 ERR_INVALID_URL。
func TestProxyLocationTargetTrailingSlashNormalized(t *testing.T) {
	cases := []struct {
		name        string
		path        string
		target      string
		wantPassHas string // proxy_pass 后面必须出现的部分
		banPassHas  string // proxy_pass 后面绝不能出现的部分
	}{
		{
			name:        "path-no-slash, target-with-slash → proxy_pass 绝不能带尾斜杠",
			path:        "/mag",
			target:      "http://127.0.0.1:19902/",
			wantPassHas: "proxy_pass http://127.0.0.1:19902;",
			banPassHas:  "proxy_pass http://127.0.0.1:19902/;",
		},
		{
			name:        "path-no-slash, target-no-slash → 保持无尾斜杠",
			path:        "/ws",
			target:      "http://127.0.0.1:9000",
			wantPassHas: "proxy_pass http://127.0.0.1:9000;",
			banPassHas:  "proxy_pass http://127.0.0.1:9000/;",
		},
		{
			name:        "path-with-slash, target-no-slash → 必须有尾斜杠（剥离前缀语义）",
			path:        "/api/",
			target:      "http://127.0.0.1:8000",
			wantPassHas: "proxy_pass http://127.0.0.1:8000/;",
			banPassHas:  "proxy_pass http://127.0.0.1:8000;;", // 出现 ;; 就废了
		},
		{
			name:        "path-with-slash, target-with-slash → 仍必须有尾斜杠，不重复加",
			path:        "/api/",
			target:      "http://127.0.0.1:8000/",
			wantPassHas: "proxy_pass http://127.0.0.1:8000/;",
			banPassHas:  "proxy_pass http://127.0.0.1:8000//;", // 双斜杠也算污染
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &model.Site{
				ID: "pxts", Name: "pxts", Status: model.SiteStatusRunning,
				Domains: model.DomainList{"pxts.test"},
				Root:    model.RootConf{Path: "/www/pxts"},
				Proxy: model.ProxyConf{
					Enabled: true,
					Rules: []model.ProxyRule{
						{Path: tc.path, Target: tc.target, Enabled: true, Timeout: 30},
					},
				},
			}
			res, err := Render(s, RenderOptions{SiteID: s.ID})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			conf := res.SiteConf
			if !strings.Contains(conf, tc.wantPassHas) {
				t.Errorf("缺少 %q:\n%s", tc.wantPassHas, conf)
			}
			if strings.Contains(conf, tc.banPassHas) {
				t.Errorf("污染的 proxy_pass 出现 %q:\n%s", tc.banPassHas, conf)
			}
		})
	}
}
