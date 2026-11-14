package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestPHPSocketDefaultPath 锁死 render.go 默认 fastcgi socket 路径格式：
// Debian/Ubuntu PHP-FPM 实际 socket 是 /run/php/phpX.Y-fpm.sock（php 和版本号之间无连字符），
// 不是 /run/php/php-X.Y-fpm.sock。后者指向不存在的文件，根域所有 PHP 请求
// （首页、/api/*、favicon.ico 经 try_files 落到 index.php）全 502 Bad Gateway。
func TestPHPSocketDefaultPath(t *testing.T) {
	for _, ver := range []string{"7.4", "8.0", "8.1", "8.2", "8.3"} {
		t.Run("ver="+ver, func(t *testing.T) {
			s := &model.Site{
				ID: "phps", Name: "phps", Status: model.SiteStatusRunning,
				Domains: model.DomainList{"phps.test"},
				Root:    model.RootConf{Path: "/www/phps"},
				PHP:     model.PHPConf{Enabled: true, Version: ver},
			}
			res, err := Render(s, RenderOptions{SiteID: s.ID})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			conf := res.SiteConf
			want := "fastcgi_pass unix:/run/php/php" + ver + "-fpm.sock;"
			if !strings.Contains(conf, want) {
				t.Errorf("PHP %s: 缺少 %q（Debian/Ubuntu 实际 socket 是这个名）:\n%s", ver, want, conf)
			}
			// 反向断言：错误的带连字符格式不能再出现
			bad := "fastcgi_pass unix:/run/php/php-" + ver + "-fpm.sock;"
			if strings.Contains(conf, bad) {
				t.Errorf("PHP %s: 出现错误格式 %q，会 502:\n%s", ver, bad, conf)
			}
		})
	}
}
