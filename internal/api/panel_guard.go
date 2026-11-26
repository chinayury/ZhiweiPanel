package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhiwei/panel/internal/store"
)

// 面板入口守卫：安全入口路径 + IP 白名单。
//
// 宝塔「面板设置/安全」的两项核心能力，本面板 v0.9.0 才真正接线：
// 早期设置页虽然能填 panel_path，但后端从未读取，填了等于没填。
//
// 设计边界（重要）：
//   - 安全入口只拦浏览器 UI（静态资源 + SPA 回退），**不拦 /api 与 /ws**。
//     原因：已有 API Token、CI、Docker HEALTHCHECK 都固定访问 /api/auth/me，
//     给 API 加前缀会破坏所有既有自动化；入口路径是"藏 UI"的遮挡，
//     不是认证——真正的认证仍是 session / Bearer token。
//   - IP 白名单对所有路径生效（含 /api、/ws），这是真正的网络层限制。
//   - 回环地址永远放行：SSH 隧道 / 本机 curl 是管理员自救通道。

const (
	// 设置变更后最长 2 秒重新加载一次。设置页保存时会主动 Reload，
	// 这里的 TTL 只是兜底（例如直接改 DB 的场景）。
	panelGuardReloadInterval = 2 * time.Second

	panelPathMinLen = 2
	panelPathMaxLen = 32
)

// panelGuardConfig 一次加载后的不可变配置快照。
type panelGuardConfig struct {
	path string
	// allowAll = 白名单为空（不限制）。
	allowAll bool
	// allow 为空且 allowAll=false 表示"设置了白名单但全部非法"，
	// 按失败关闭处理：除回环外一律拒绝。
	allow []*net.IPNet
}

// panelGuard 入口守卫。
type panelGuard struct {
	db      *store.DB
	trusted *trustedProxySet

	mu       sync.RWMutex
	cfg      panelGuardConfig
	loadedAt time.Time
}

func newPanelGuard(db *store.DB, trusted *trustedProxySet) *panelGuard {
	g := &panelGuard{db: db, trusted: trusted}
	g.Reload()
	return g
}

// Reload 从 settings 表重新读取面板入口配置。
func (g *panelGuard) Reload() {
	cfg := panelGuardConfig{allowAll: true}
	if g.db != nil {
		if v, err := g.db.GetSetting("panel_path"); err == nil {
			if p, perr := normalizePanelPath(v); perr == nil {
				cfg.path = p
			}
		}
		if v, err := g.db.GetSetting("panel_ip_allowlist"); err == nil {
			if strings.TrimSpace(v) != "" {
				_, nets, _ := parseIPAllowlist(v)
				cfg.allowAll = false
				cfg.allow = nets
			}
		}
	}
	g.mu.Lock()
	g.cfg = cfg
	g.loadedAt = time.Now()
	g.mu.Unlock()
}

// refreshIfStale 在 TTL 到期后重新加载。并发请求可能重复加载，
// 结果幂等，代价只是一次 SQLite 查询。
func (g *panelGuard) refreshIfStale() {
	g.mu.RLock()
	stale := time.Since(g.loadedAt) > panelGuardReloadInterval
	g.mu.RUnlock()
	if stale {
		g.Reload()
	}
}

// Path 返回当前生效的安全入口路径（不含斜杠，空表示未启用）。
func (g *panelGuard) Path() string {
	g.refreshIfStale()
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cfg.path
}

// Effective 返回给设置页展示的入口状态。
func (g *panelGuard) Effective() map[string]any {
	g.refreshIfStale()
	g.mu.RLock()
	defer g.mu.RUnlock()
	return map[string]any{
		"path":                 g.cfg.path,
		"ip_allowlist_enabled": !g.cfg.allowAll,
		"ip_allowlist_count":   len(g.cfg.allow),
	}
}

// Middleware 包装整个面板 handler。
func (g *panelGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.refreshIfStale()
		g.mu.RLock()
		cfg := g.cfg
		g.mu.RUnlock()

		// 1) IP 白名单：作用于所有路径。
		ip := clientIPFor(r, g.trusted)
		if !cfg.allowAll && !isLoopbackIP(ip) && !ipInNets(cfg.allow, ip) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		// 2) 安全入口：只拦 UI；API / WS 保持原有路径。
		if cfg.path == "" || isAPIPath(r.URL.Path) || isWSPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		prefix := "/" + cfg.path
		switch {
		case r.URL.Path == "/":
			// 根路径不重定向到入口——重定向等于把入口明文告诉扫描器。
			http.NotFound(w, r)
			return
		case r.URL.Path == prefix:
			target := prefix + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusMovedPermanently)
			return
		case strings.HasPrefix(r.URL.Path, prefix+"/"):
			r2 := r.Clone(r.Context())
			u2 := *r.URL
			u2.Path = strings.TrimPrefix(r.URL.Path, prefix)
			if u2.Path == "" {
				u2.Path = "/"
			}
			// 清掉 RawPath，避免编码路径与改写后的 Path 不一致。
			u2.RawPath = ""
			r2.URL = &u2
			next.ServeHTTP(w, r2)
			return
		default:
			http.NotFound(w, r)
			return
		}
	})
}

func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

func isWSPath(p string) bool {
	return p == "/ws" || strings.HasPrefix(p, "/ws/")
}

// isLoopbackIP 判断客户端 IP 是否为回环地址（含 IPv4-mapped IPv6）。
func isLoopbackIP(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.IsLoopback()
	}
	return false
}

// ipInNets 判断 IP 是否落在网段集合内。
func ipInNets(nets []*net.IPNet, raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range nets {
		if n == nil {
			continue
		}
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ---------- 设置解析 / 校验 ----------

// 安全入口路径的保留名。这些路径已有用途，拿来做入口会与静态资源、
// API 或未来的 WS 端点冲突。
var panelPathReserved = map[string]bool{
	"api":         true,
	"ws":          true,
	"css":         true,
	"js":          true,
	"assets":      true,
	"static":      true,
	"index.html":  true,
	"favicon.ico": true,
}

// normalizePanelPath 归一化并校验安全入口路径。
//
// 空字符串是合法值（= 不启用入口遮挡）。非空值只接受
// 2~32 位 [A-Za-z0-9_-]，不接受斜杠、点、空格。
func normalizePanelPath(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	v = strings.Trim(v, "/")
	if v == "" {
		return "", nil
	}
	if len(v) < panelPathMinLen || len(v) > panelPathMaxLen {
		return "", fmt.Errorf("面板路径长度必须为 %d~%d 个字符", panelPathMinLen, panelPathMaxLen)
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "", errors.New("面板路径只能包含字母、数字、下划线和短横线")
		}
	}
	if panelPathReserved[strings.ToLower(v)] {
		return "", fmt.Errorf("面板路径 %q 是保留路径，请换一个", v)
	}
	return v, nil
}

// parseIPAllowlist 解析 IP/CIDR 白名单。
//
// 支持换行、逗号、空格、分号分隔；裸 IP 自动补 /32 或 /128。
// 返回归一化后的条目（给设置页回显）、可匹配的网段、以及非法条目。
func parseIPAllowlist(raw string) (normalized []string, nets []*net.IPNet, invalid []string) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case '\n', '\r', ',', ';', ' ', '\t':
			return true
		}
		return false
	})
	seen := map[string]bool{}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		if _, n, err := net.ParseCIDR(f); err == nil {
			normalized = append(normalized, f)
			nets = append(nets, n)
			continue
		}
		if ip := net.ParseIP(f); ip != nil {
			bits := 128
			if v4 := ip.To4(); v4 != nil {
				ip = v4
				bits = 32
			}
			normalized = append(normalized, f)
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		invalid = append(invalid, f)
	}
	return
}

// normalizePanelBind 校验面板绑定地址。空 = 所有网卡。
func normalizePanelBind(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if net.ParseIP(v) == nil {
		return "", fmt.Errorf("绑定地址 %q 不是合法 IP", v)
	}
	return v, nil
}

// parsePanelPort 校验面板监听端口。
func parsePanelPort(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > 65535 {
		return 0, errors.New("面板端口必须是 1~65535 的整数")
	}
	return n, nil
}
