package api

import (
	"net"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// 客户端 IP 的可信判定
//
// 早期版本无条件采信 `X-Forwarded-For` 首段：
//
//	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
//	    ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
//	}
//
// 面板默认直接监听 0.0.0.0:8888，客户端可以随意伪造这个头，于是：
//
//  1. **登录限流被废掉**：限流键是 "XFF|用户名"，攻击者每次换一个
//     X-Forwarded-For 就永远命中新条目，5 次锁定 / 15 分钟封禁形同虚设，
//     可以对管理员口令做无限次在线猜测。
//  2. **异常登录二次验证被绕过**：异常判定是"同子网 + 相似 UA 即放行"。
//     攻击者先用正确密码登录一次，服务端在响应里回显了管理员上次的
//     登录 IP；然后带上 `X-Forwarded-For: <那个IP>` 重放，
//     同子网判定成立 → 不再需要邮箱验证码 → 双因素被降级成单因素。
//  3. **审计日志的 IP 可伪造**：事后溯源失真，还能栽赃第三方。
//
// 正确做法：**只有当直连方（RemoteAddr）落在受信代理网段里时，
// 才采信 X-Forwarded-For**；否则一律用直连地址。
//
// 这样面板直连时（最常见的部署形态）完全不受影响；
// 面板挂在 Nginx / Cloudflare 后面时，用 -trusted-proxy 指定代理网段即可。
// ---------------------------------------------------------------------------

// trustedProxySet 保存受信反向代理的网段集合。
type trustedProxySet struct {
	nets []*net.IPNet
}

// newTrustedProxySet 解析 CIDR 列表，忽略非法项。
//
// 非法项直接忽略而不是报错：启动参数写错一个网段不该让整个面板起不来，
// 但也不能因此放宽信任——忽略意味着该网段不被信任，属于安全侧的失败模式。
func newTrustedProxySet(cidrs []string) *trustedProxySet {
	set := &trustedProxySet{}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			set.nets = append(set.nets, n)
			continue
		}
		// 也接受单个 IP（等价于 /32 或 /128）
		if ip := net.ParseIP(c); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			set.nets = append(set.nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return set
}

// empty 判断集合是否为空（= 不信任任何代理）。
func (t *trustedProxySet) empty() bool {
	return t == nil || len(t.nets) == 0
}

// contains 判断 ip 是否落在受信代理网段内。
func (t *trustedProxySet) contains(ip string) bool {
	if t.empty() {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range t.nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// remoteIP 从 RemoteAddr 里取出 IP 部分。
//
// RemoteAddr 形如 "1.2.3.4:5678" 或 "[::1]:5678"；
// 也可能没有端口（测试里常见）。
func remoteIP(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	// 没有端口：去掉 IPv6 的方括号
	return strings.Trim(addr, "[]")
}

// clientIP 返回可信的客户端 IP。
//
// 规则：直连方在受信代理网段内 → 采信 X-Forwarded-For 首段；
// 否则 → 用直连地址。
func (s *Server) clientIP(r *http.Request) string {
	return clientIPFor(r, s.trustedProxies)
}

// clientIPFor 是 clientIP 的包级实现。
//
// 抽出来是为了让面板入口守卫（panel_guard.go）与鉴权/限流共用同一套
// 信任规则：守卫挂在最外层，拿不到 Server 的方法，但必须与
// auth/audit 看到同一个客户端 IP，否则"白名单放行、审计记成代理 IP"
// 这类不一致会让事故溯源失真。
func clientIPFor(r *http.Request, trusted *trustedProxySet) string {
	peer := remoteIP(r.RemoteAddr)
	if trusted.contains(peer) {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			if ip := strings.TrimSpace(strings.Split(fwd, ",")[0]); ip != "" {
				return ip
			}
		}
	}
	return peer
}
