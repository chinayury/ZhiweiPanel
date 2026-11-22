package nginx

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestGlobalNginxConfSSLSessionCacheSizeMatchesSiteRender 锁死一个只在
// docker 部署路径出现的跨文件冲突：
//
//	deploy/nginx.conf（镜像内全局配置，http 级）声明 ssl_session_cache
//	shared:SSL:<size>，站点渲染（buildTLS）在 server 级也声明同名 zone。
//	nginx 要求同名 shared zone 尺寸一致，否则直接 emerg：
//	  "the size 10485760 of shared memory zone "SSL" conflicts with
//	   already declared size 2097152"
//	该冲突在宿主部署上永不触发（发行版 nginx.conf 无此行），因此只在
//	docker 环境的 nginx -t 阶段爆炸——2026-11-22 在 docker 部署的测试环境
//	实测踩坑（全局 2m vs 站点 10m），且 bindCertToSite 当时把失败静默吞掉。
//	本测试把两处尺寸锁成同一个值，任何人改其中一处都会立刻变红。
func TestGlobalNginxConfSSLSessionCacheSizeMatchesSiteRender(t *testing.T) {
	re := regexp.MustCompile(`ssl_session_cache\s+shared:SSL:(\d+)([kmg]?)`)

	// 站点渲染出的尺寸（权威值来源：render.go buildTLS）
	res, err := Render(testSite(), RenderOptions{SiteID: testSite().ID})
	if err != nil {
		t.Fatalf("渲染站点失败: %v", err)
	}
	m := re.FindStringSubmatch(res.SiteConf)
	if m == nil {
		t.Fatalf("站点渲染结果里没有 ssl_session_cache shared:SSL 声明:\n%s", res.SiteConf)
	}
	siteSize := m[1] + m[2]

	// deploy/nginx.conf 的尺寸
	confPath := filepath.Join("..", "..", "deploy", "nginx.conf")
	b, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", confPath, err)
	}
	global := re.FindAllStringSubmatch(string(b), -1)
	if len(global) == 0 {
		// 全局配置不声明该 zone 也是合法形态（站点级自足），
		// 此时无冲突可言，直接通过。
		return
	}
	for _, g := range global {
		gSize := g[1] + g[2]
		if gSize != siteSize {
			t.Errorf("deploy/nginx.conf 的 shared:SSL:%s 与站点渲染的 shared:SSL:%s 尺寸不一致，"+
				"docker 部署下启用 SSL 站点会让 nginx -t 直接 emerg", gSize, siteSize)
		}
	}
}
