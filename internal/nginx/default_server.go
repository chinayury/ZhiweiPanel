package nginx

import (
	"fmt"
	"strings"
)

// DefaultServerConfFile 防跨站隔离的 catch-all server 配置文件名。
//
// 文件名前缀 01- 是必须的，原因有两个：
//
//  1. nginx 按文件名升序加载 conf.d/*.conf，第一个 listen 80/443 的 server
//     自动成为该端口的 default_server。01- 排在 10-site-*.conf 之前，
//     default_server 标记才能抢先于真实站点的 server 块生效。
//
//  2. 必须排在 00-zhiweipanel-http.conf 之后：本文件引用的 server_name、return
//     等不需要 log_format，但 default_server 块若与 http 级定义在同一文件，
//     字母序虽然正常（00- < 01-），职责混杂不便维护；分开成两个文件，
//     各管一摊，回滚也独立。
const DefaultServerConfFile = "01-zhiweipanel-default-server.conf"

// DefaultServerTLS 默认 server 用的 TLS 上下文（dummy 自签证书路径）。
//
// nginx 1.18 (Ubuntu 22.04 默认) 不支持 ssl_reject_handshake（1.19.4+），
// 因此 443 端口必须配一份证书才能让 SSL 握手成功、然后被 return 444 立即断开。
// 用自签 dummy 证书而不是用户的真实站证书，避免"IP 直连者拿到用户站点的
// 真实证书/品牌"的信息泄露。
type DefaultServerTLS struct {
	CertFile string
	KeyFile  string
}

// RenderDefaultServer 生成 catch-all server 块，拦截任何 Host 不匹配
// 已配置 server_name 的请求。
//
// 触发场景（必须全部覆盖）：
//   - 用户用 IP 直连（http://103.x.x.x/）：nginx 默认行为是命中 listen 80
//     顺序上的第一个 server 块——若没有这个 catch-all，用户会看到真实站点
//     的内容（实测：访问 IP 返回 toutiao.example.com 的首页）。
//   - 用户伪造 Host 头指向别人域名：同样落到 default_server 路径。
//   - 用户多站点配置下访问"另一个站点在另一个 server 上的路径"：
//     如果新站点还没生成配置，IP 直连会暴露老站点根目录。
//
// 实现：
//   - server_name _ 通配所有未匹配请求
//   - listen 80 default_server + return 444：明文 HTTP 直接断开连接
//   - listen 443 ssl default_server + cert/key + return 444：
//     nginx <1.19.4 没有 ssl_reject_handshake，靠 dummy 自签证书让握手
//     成功，再用 return 444 立即断开——客户端看到"连接被重置"
//
// hasSSL 为 true 且 tls.CertFile/KeyFile 非空时同时输出 443 监听；
// 其他情况下不 listen 443，避免在没有 dummy 证书的环境里被迫配证书。
//
// 返回值是完整 server 块（含注释），调用方负责写入 *.conf 文件。
func RenderDefaultServer(hasSSL bool, tls DefaultServerTLS) string {
	listenSSL := hasSSL && tls.CertFile != "" && tls.KeyFile != ""

	var sb strings.Builder
	sb.WriteString("# ============================================\n")
	sb.WriteString("# ZhiweiPanel catch-all server\n")
	sb.WriteString("# 防跨站隔离：拦截未匹配任何已配置 server_name 的请求\n")
	sb.WriteString("# 80/443 都走 default_server；命中后 return 444 立即断开\n")
	sb.WriteString("# 443 用自签 dummy 证书而非用户站点证书——避免 IP 直连者看到\n")
	sb.WriteString("# 真实站点证书品牌\n")
	sb.WriteString("# 文件名前缀 01- 确保 default_server 标记先于 10-site-*.conf 生效\n")
	sb.WriteString("# ============================================\n\n")
	sb.WriteString("server {\n")
	sb.WriteString("    listen 80 default_server;\n")
	sb.WriteString("    listen [::]:80 default_server;\n")
	if listenSSL {
		sb.WriteString("    listen 443 ssl default_server;\n")
		sb.WriteString("    listen [::]:443 ssl default_server;\n")
	}
	sb.WriteString("    server_name _;\n")
	// 不记 catch-all 的访问/错误日志：
	//   - 任意 IP 探测、端口扫描都会被记进 access.log；
	//   - 真实站点的日志分析会被海量噪声淹没。
	sb.WriteString("    access_log off;\n")
	sb.WriteString("    error_log /dev/null crit;\n")
	if listenSSL {
		fmt.Fprintf(&sb, "    ssl_certificate %s;\n", tls.CertFile)
		fmt.Fprintf(&sb, "    ssl_certificate_key %s;\n", tls.KeyFile)
	}
	// return 444 在明文 80 与已握手完成的 443 上都生效。
	sb.WriteString("    return 444;\n")
	sb.WriteString("}\n")
	return sb.String()
}
