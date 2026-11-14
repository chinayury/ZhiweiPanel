// Package security 提供跨模块复用的安全校验原语。
package security

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
)

// CheckExternalURL 校验 URL 是否安全可对外访问。
//
// 拒绝三类：
//  1. 非 http/https 协议；
//  2. 字面内网 / loopback / link-local IP；
//  3. 域名解析后任一 IP 落在内网 / loopback / link-local（防 DNS 重绑定）。
//
// 为什么必须解析域名再复查：早期实现只对"字面量是 IP"的 host 做校验，
// 域名一律放行。攻击者用 `127.0.0.1.nip.io` 这类解析到内网的域名，
// 或自建 DNS 把域名指向 169.254.169.254 云元数据，就能绕过 SSRF 防护。
func CheckExternalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL 解析失败: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("只允许 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL 缺少 host")
	}
	return checkHost(host)
}

// checkHost 校验 host 是否指向公网地址。字面 IP 直接判定；域名解析后逐一复查。
func checkHost(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if isForbiddenIP(ip) {
			return fmt.Errorf("不允许访问内网/loopback 地址: %s", host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("域名解析失败: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("域名无解析结果: %s", host)
	}
	for _, ip := range ips {
		if isForbiddenIP(ip) {
			return fmt.Errorf("域名 %s 解析到内网/loopback 地址 %s，已拒绝", host, ip)
		}
	}
	return nil
}

// isForbiddenIP 判定 IP 是否属于内网 / loopback / link-local / 未指定地址。
func isForbiddenIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// NoRedirectToInternal 作为 http.Client.CheckRedirect 回调。
//
// http.Client 默认跟随重定向，且每一跳都不会再走 SSRF 校验——
// 攻击者用 `http://evil.com`（公网）302 跳转到 `http://127.0.0.1:3306`
// 就能绕过首次 URL 校验打内网。这里对每一跳目标重新做 CheckExternalURL，
// 同时限制最多 10 次重定向。
func NoRedirectToInternal(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("重定向次数过多")
	}
	if err := CheckExternalURL(req.URL.String()); err != nil {
		return fmt.Errorf("重定向目标不安全: %w", err)
	}
	return nil
}
