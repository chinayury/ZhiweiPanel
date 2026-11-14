package nginx

import (
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// authSite 构造一个开启了访问认证的测试站点。
func authSite() *model.Site {
	return &model.Site{
		ID:      "abc123",
		Name:    "auth",
		Domains: model.DomainList{"auth.test"},
		Root:    model.RootConf{Path: "/www/wwwroot/auth.test"},
		Access: model.AccessConf{
			AuthEnabled: true,
			AuthUser:    "admin",
			AuthHash:    "$apr1$abcdefgh$EfExgQSMBXioDhIVk8IOb1",
		},
	}
}

// authFile 是 nginx 会读到的口令文件路径（正斜杠，与 Linux 上一致）。
const authFile = "/opt/zhiweipanel/data/htpasswd/abc123"

// hasBasicAuth 判断配置里有没有真正的 auth_basic 指令。
//
// 不能直接用 strings.Contains(conf, "auth_basic")：
// ACME 挑战 location 里固定有一行 "auth_basic off;"，
// 它与"站点开启了访问认证"是两回事。
func hasBasicAuth(conf string) bool {
	return strings.Contains(conf, "auth_basic \"") || strings.Contains(conf, "auth_basic_user_file")
}

// TestRenderBasicAuthEnabled 确认访问认证会被真正渲染进 nginx 配置。
//
// 这条测试锁的是"功能残缺"这个真实缺陷：模型里有 AuthEnabled/AuthUser/AuthPass，
// 前端也画了输入框，但全仓没有任何一处渲染它们——
// 用户配完，站点对外访问毫无变化。
func TestRenderBasicAuthEnabled(t *testing.T) {
	s := authSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID, AccessFile: authFile})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	conf := res.SiteConf

	if !strings.Contains(conf, `auth_basic "auth.test";`) {
		t.Errorf("站点配置缺少 auth_basic:\n%s", conf)
	}
	want := `auth_basic_user_file "` + authFile + `";`
	if !strings.Contains(conf, want) {
		t.Errorf("站点配置缺少 %q:\n%s", want, conf)
	}
}

// TestRenderBasicAuthDisabled 确认未开启认证时不输出 auth_basic 指令。
func TestRenderBasicAuthDisabled(t *testing.T) {
	s := authSite()
	s.Access.AuthEnabled = false
	res, err := Render(s, RenderOptions{SiteID: s.ID, AccessFile: authFile})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if hasBasicAuth(res.SiteConf) {
		t.Errorf("未开启认证却输出了 auth_basic:\n%s", res.SiteConf)
	}
}

// TestRenderBasicAuthNoHash 确认没有口令哈希时不输出 auth_basic。
//
// 关键：nginx 在**解析配置时**就打开 auth_basic_user_file 指向的文件，
// 文件不存在 → nginx -t 报 "open() ... failed" → ApplyAllConfig 整体回滚 →
// **所有站点**的配置都停不下来。为一个站点的认证把整台面板拖下水，
// 代价完全不成比例，所以宁可这个站点暂时没有认证。
func TestRenderBasicAuthNoHash(t *testing.T) {
	s := authSite()
	s.Access.AuthHash = ""
	res, err := Render(s, RenderOptions{SiteID: s.ID, AccessFile: authFile})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if hasBasicAuth(res.SiteConf) {
		t.Errorf("没有口令哈希却输出了 auth_basic:\n%s", res.SiteConf)
	}
}

// TestRenderBasicAuthNoAccessFile 确认没有口令文件路径时不输出 auth_basic。
func TestRenderBasicAuthNoAccessFile(t *testing.T) {
	s := authSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if hasBasicAuth(res.SiteConf) {
		t.Errorf("没有口令文件路径却输出了 auth_basic:\n%s", res.SiteConf)
	}
}

// TestRenderBasicAuthUnsafePath 确认路径不安全时不输出 auth_basic。
//
// 数据目录里带空格会让 nginx -t 失败，宁可不出，
// 也不要渲染出一份会让整批配置回滚的配置。
func TestRenderBasicAuthUnsafePath(t *testing.T) {
	s := authSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID, AccessFile: "/opt/zhiwei panel/htpasswd/abc123"})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if hasBasicAuth(res.SiteConf) {
		t.Errorf("路径不安全却输出了 auth_basic:\n%s", res.SiteConf)
	}
}

// TestRenderBasicAuthWindowsPathRejected 确认反斜杠路径被拒绝。
//
// nginx 只认正斜杠；用 filepath.Join 在 Windows 上拼出来的路径会把
// 反斜杠写进配置，nginx -t 直接失败。
func TestRenderBasicAuthWindowsPathRejected(t *testing.T) {
	s := authSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID, AccessFile: `C:\zhiweipanel\data\htpasswd\abc123`})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if hasBasicAuth(res.SiteConf) {
		t.Errorf("反斜杠路径却输出了 auth_basic:\n%s", res.SiteConf)
	}
}

// TestRenderBasicAuthDisablesACMEChallenge 确认 ACME 挑战目录显式关闭认证。
//
// server 级开了 auth_basic 之后，location 会继承认证要求；
// Let's Encrypt 的验证请求拿不到挑战文件 → 续签失败 → 证书过期 →
// 站点 HTTPS 全面不可用。这是最容易被忽略、后果又最严重的一处连带影响。
func TestRenderBasicAuthDisablesACMEChallenge(t *testing.T) {
	s := authSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID, AccessFile: authFile})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	conf := res.SiteConf
	idx := strings.Index(conf, "location ^~ /.well-known/acme-challenge/ {")
	if idx < 0 {
		t.Fatalf("没有输出 ACME 挑战 location:\n%s", conf)
	}
	end := strings.Index(conf[idx:], "}")
	if end < 0 {
		t.Fatalf("ACME 挑战 location 没有闭合:\n%s", conf)
	}
	block := conf[idx : idx+end]
	if !strings.Contains(block, "auth_basic off;") {
		t.Errorf("ACME 挑战 location 未关闭访问认证（会导致证书续签失败）:\n%s", block)
	}
}
