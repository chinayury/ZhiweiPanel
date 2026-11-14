package nginx

import (
	"strings"
	"testing"
)

// TestRenderDefaultServerNoSSL 没有 SSL 站点时不能 listen 443：
// 没有可用的证书，写 listen 443 ssl 会让 nginx -t 失败。
//
// 注释里也包含字面 "ssl_reject_handshake" 等关键词，因此判定时要
// 拆行并跳过以 # 开头的注释行——只匹配真实指令行。
func TestRenderDefaultServerNoSSL(t *testing.T) {
	out := RenderDefaultServer(false, DefaultServerTLS{})

	lines := directiveLines(out)
	for _, want := range []string{
		"server {",
		"listen 80 default_server;",
		"listen [::]:80 default_server;",
		"server_name _;",
		"return 444;",
		"access_log off;",
	} {
		if !containsLine(lines, want) {
			t.Errorf("无 SSL 模式下应包含指令行 %q\n实际:\n%s", want, out)
		}
	}
	if containsLine(lines, "listen 443") {
		t.Errorf("无 SSL 模式下不应 listen 443（没证书会跳认证）\n实际:\n%s", out)
	}
	if containsLine(lines, "ssl_certificate") {
		t.Errorf("无 SSL 模式下不应写 ssl_certificate:\n%s", out)
	}
}

// TestRenderDefaultServerWithSSL 有 SSL 站点时必须 listen 443 +
// 引用 dummy 证书，确保 443 上的握手也走默认拒绝分支。
func TestRenderDefaultServerWithSSL(t *testing.T) {
	out := RenderDefaultServer(true, DefaultServerTLS{
		CertFile: "/opt/zhiweipanel/data/ssl/dummy.crt",
		KeyFile:  "/opt/zhiweipanel/data/ssl/dummy.key",
	})

	lines := directiveLines(out)
	for _, want := range []string{
		"listen 80 default_server;",
		"listen 443 ssl default_server;",
		"listen [::]:443 ssl default_server;",
		"server_name _;",
		"ssl_certificate /opt/zhiweipanel/data/ssl/dummy.crt;",
		"ssl_certificate_key /opt/zhiweipanel/data/ssl/dummy.key;",
		"return 444;",
	} {
		if !containsLine(lines, want) {
			t.Errorf("有 SSL 模式下应包含指令行 %q\n实际:\n%s", want, out)
		}
	}
}

// TestRenderDefaultServerNoLogInfo catch-all 不能写入 access_log 到真实路径。
// 若 catch-all 把每个 IP 探测/端口扫描都落到面板的 access log，
// 真实日志分析会被噪声淹没（每个站点日志都是 50MB+ 噪声）。
func TestRenderDefaultServerNoLogInfo(t *testing.T) {
	out := RenderDefaultServer(true, DefaultServerTLS{CertFile: "/x", KeyFile: "/y"})
	lines := directiveLines(out)
	for _, bad := range []string{
		"access_log /var/log",
		"access_log /opt/",
		"access_log /www/",
	} {
		if containsLine(lines, bad) {
			t.Errorf("catch-all 不应写 access_log 到真实路径 %q:\n%s", bad, out)
		}
	}
	if !containsLine(lines, "access_log off;") {
		t.Errorf("catch-all 必须关 access_log:\n%s", out)
	}
}

// TestRenderDefaultServerFileNameOrder 文件名必须排在 10-site- 之前。
// 这是字母序 include 流程的关键约束。
func TestRenderDefaultServerFileNameOrder(t *testing.T) {
	if DefaultServerConfFile >= "10-site-" {
		t.Errorf("默认 server 文件名 %q 必须排在 10-site- 之前", DefaultServerConfFile)
	}
	if DefaultServerConfFile <= "00-zhiweipanel-http.conf" {
		t.Errorf("默认 server 文件名 %q 必须排在 00- 之后", DefaultServerConfFile)
	}
}

// directiveLines 拆出真实的 nginx 指令行（去注释、去空、去首尾空白）。
func directiveLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
