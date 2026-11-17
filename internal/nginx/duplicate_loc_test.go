package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestNoDuplicateLocationPrefixes 锁死 duplicate location 缺陷。
//
// 线上实例：启动时 ApplyAllConfig 渲染出的配置报
//
//	nginx: [emerg] duplicate location "/" in .../10-site-xxx.conf:80
//
// 而同一份配置在后续重写下又变正常，说明渲染结果在某些输入组合下
// 会产出重复的 location 前缀。nginx -t 失败会导致整份配置回滚，
// 全部站点的变更都无法生效，因此必须在渲染层拦住。
func TestNoDuplicateLocationPrefixes(t *testing.T) {
	cases := []struct {
		name string
		site *model.Site
	}{
		{
			name: "反代 + 主location",
			site: &model.Site{
				ID: "d1", Name: "d1", Status: model.SiteStatusRunning,
				Domains: model.DomainList{"a.test"},
				Root:    model.RootConf{Path: "/www/a"},
				Proxy: model.ProxyConf{Enabled: true, Rules: []model.ProxyRule{
					{Path: "/api", Target: "http://127.0.0.1:8888", Enabled: true},
				}},
			},
		},
		{
			name: "反代根路径 + 主location",
			site: &model.Site{
				ID: "d2", Name: "d2", Status: model.SiteStatusRunning,
				Domains: model.DomainList{"a.test"},
				Root:    model.RootConf{Path: "/www/a"},
				Proxy: model.ProxyConf{Enabled: true, Rules: []model.ProxyRule{
					{Path: "/", Target: "http://127.0.0.1:8888", Enabled: true},
				}},
			},
		},
		{
			name: "重定向到根 + 主location",
			site: &model.Site{
				ID: "d3", Name: "d3", Status: model.SiteStatusRunning,
				Domains: model.DomainList{"a.test"},
				Root:    model.RootConf{Path: "/www/a"},
				Redirect: model.RedirectConf{Rules: []model.RedirectRule{
					{From: "/", To: "https://b.test", Status: 301, Enable: true},
				}},
			},
		},
		{
			name: "子目录绑定 / + 主location",
			site: &model.Site{
				ID: "d4", Name: "d4", Status: model.SiteStatusRunning,
				Domains: model.DomainList{"a.test"},
				Root:    model.RootConf{Path: "/www/a"},
				SubDir: model.SubDirConf{Enabled: true, Rules: []model.SubDirBindingRule{
					{Path: "/", Root: "/data/root"},
				}},
			},
		},
		{
			name: "两条同 From 重定向",
			site: &model.Site{
				ID: "d5", Name: "d5", Status: model.SiteStatusRunning,
				Domains: model.DomainList{"a.test"},
				Root:    model.RootConf{Path: "/www/a"},
				Redirect: model.RedirectConf{Rules: []model.RedirectRule{
					{From: "/x", To: "/y", Status: 301, Enable: true},
					{From: "/x", To: "/z", Status: 302, Enable: true},
				}},
			},
		},
	}

	for _, c := range cases {
		res, err := Render(c.site, RenderOptions{SiteID: c.site.ID})
		if err != nil {
			t.Errorf("%s: Render failed: %v", c.name, err)
			continue
		}
		if dups := duplicateLocationPrefixes(res.SiteConf); len(dups) > 0 {
			t.Errorf("%s: 渲染出重复的 location 前缀 %v，nginx -t 会报 duplicate location:\n%s",
				c.name, dups, res.SiteConf)
		}
	}
}

// duplicateLocationPrefixes 找出配置里重复出现的 location 前缀。
// 只比较 location 的匹配串（去掉 ^~ / ~* / ~ 等修饰符后的部分），
// 因为 nginx 的 duplicate 判定基于最终匹配串。
//
// nginx 的 duplicate location 只在**同一 server 块内**判定；P2 把 80/443
// 拆成两个 server 块后，同一份配置里会有多个 server 块，因此每遇到新的
// server { 就重置查重表，否则会把「80 块的 location /」与「443 块的
// location /」误报成重复。
func duplicateLocationPrefixes(conf string) []string {
	var dups []string
	seen := map[string]bool{}
	for _, line := range strings.Split(conf, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "server {") {
			seen = map[string]bool{}
			continue
		}
		if !strings.HasPrefix(t, "location ") {
			continue
		}
		// 去掉 "location" 与末尾的 " {"
		spec := strings.TrimSpace(strings.TrimPrefix(t, "location"))
		spec = strings.TrimSpace(strings.TrimSuffix(spec, "{"))
		// 去掉修饰符，取最终匹配串
		for _, mod := range []string{"^~", "~*", "~", "="} {
			if strings.HasPrefix(spec, mod) {
				spec = strings.TrimSpace(strings.TrimPrefix(spec, mod))
				break
			}
		}
		if seen[spec] {
			dups = append(dups, spec)
		}
		seen[spec] = true
	}
	return dups
}
