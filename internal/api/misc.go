package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/zhiwei/panel/internal/logmgr"
	"github.com/zhiwei/panel/internal/model"
)

// maxLogScanLines 单次日志读取的行数上限。
// limit*page 超过该值直接 400，避免超大 page 把内存撑爆。
const maxLogScanLines = 100000

// contextTODO 提供一个后台上下文（供绑定证书后的异步重载使用）。
func contextTODO() context.Context { return context.Background() }

// readFileString 读取文件为字符串。
func readFileString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------- 设置 ----------

// phpVersionRe 默认 PHP 版本格式（主版本.次版本）。
var phpVersionRe = regexp.MustCompile(`^\d+\.\d+$`)

// panelSecurityKeys 改动后必须立即刷新入口守卫、且仅允许浏览器会话修改的键。
//
// 为什么仅限会话：这些键能把管理员自己锁在门外（改端口/入口/白名单）。
// 一个泄漏的 write Token 不应该具备"让面板失联"的能力——与 2FA 管理端点
// 同一条设计原则（见 internal/api/twofa.go）。
var panelSecurityKeys = []string{"panel_path", "panel_ip_allowlist", "panel_port", "panel_bind"}

func touchesPanelSecurity(m map[string]string) bool {
	for _, k := range panelSecurityKeys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// handleSettings 读写面板设置。
//
// 历史实现把任意键原样写库、随后无条件 ApplyAllConfig。v0.9.0 起：
//   - 面板安全键做格式校验、事务写入、立即刷新入口守卫；
//   - 其余键只做最小格式校验（默认站点根目录/PHP 版本等）；
//   - 不再触发 nginx 全量下发——现有设置键都不参与既有站点的渲染，
//     "改个默认 PHP 版本就 reload 全部站点"是没有意义的副作用。
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		all, err := s.svc.DB().AllSettings()
		if err != nil {
			writeModelError(w, err)
			return
		}
		resp := map[string]any{"settings": all}
		if s.panelGuard != nil {
			resp["panel_access"] = s.panelGuard.Effective()
		}
		writeJSON(w, http.StatusOK, resp)

	case http.MethodPut, http.MethodPost:
		var body struct {
			Settings map[string]string `json:"settings"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(body.Settings) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("settings 不能为空"))
			return
		}
		// 面板安全设置仅限浏览器会话：API Token 改入口/端口无法交互式验证新地址，
		// 一旦写错就是把管理员锁在门外。
		if touchesPanelSecurity(body.Settings) {
			p, ok := principalFrom(r.Context())
			if !ok || p.ViaToken {
				writeError(w, http.StatusForbidden, errors.New("面板安全设置仅限浏览器会话修改（API Token 无权限）"))
				return
			}
		}
		normalized, err := s.normalizeSettings(r, body.Settings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.svc.DB().SetSettings(normalized); err != nil {
			writeModelError(w, err)
			return
		}
		if touchesPanelSecurity(normalized) && s.panelGuard != nil {
			s.panelGuard.Reload()
		}
		resp := map[string]any{"ok": true}
		if v, ok := normalized["panel_path"]; ok {
			resp["panel_path"] = v
		}
		writeJSON(w, http.StatusOK, resp)

	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// normalizeSettings 校验并归一化设置键值。
func (s *Server) normalizeSettings(r *http.Request, in map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}

	if v, ok := out["panel_path"]; ok {
		p, err := normalizePanelPath(v)
		if err != nil {
			return nil, err
		}
		out["panel_path"] = p
	}
	if v, ok := out["panel_port"]; ok {
		if strings.TrimSpace(v) == "" {
			// 空 = 清除设置，启动时回落到 -port 参数。
			out["panel_port"] = ""
		} else {
			n, err := parsePanelPort(v)
			if err != nil {
				return nil, err
			}
			out["panel_port"] = strconv.Itoa(n)
		}
	}
	if v, ok := out["panel_bind"]; ok {
		b, err := normalizePanelBind(v)
		if err != nil {
			return nil, err
		}
		out["panel_bind"] = b
	}
	if v, ok := out["panel_ip_allowlist"]; ok {
		entries, nets, invalid := parseIPAllowlist(v)
		if len(invalid) > 0 {
			return nil, fmt.Errorf("IP 白名单包含非法条目: %s", strings.Join(invalid, ", "))
		}
		out["panel_ip_allowlist"] = strings.Join(entries, "\n")
		// 自锁防护：保存后当前客户端将无法访问的配置直接拒绝。
		// 回环地址永远放行，因此经 SSH 隧道/本机 curl 操作时不会被这条拦住。
		if len(nets) > 0 {
			ip := s.clientIP(r)
			if !isLoopbackIP(ip) && !ipInNets(nets, ip) {
				return nil, fmt.Errorf("当前客户端 IP %s 不在白名单内，保存后将无法访问面板；请把该 IP 加入白名单，或经 SSH 隧道从本机操作", ip)
			}
		}
	}
	if v, ok := out["default_site_root"]; ok {
		if strings.TrimSpace(v) != "" {
			if err := model.ValidatePath(v); err != nil {
				return nil, fmt.Errorf("默认站点根目录不合法: %v", err)
			}
		}
	}
	if v, ok := out["default_php_version"]; ok {
		if strings.TrimSpace(v) != "" && !phpVersionRe.MatchString(strings.TrimSpace(v)) {
			return nil, errors.New("默认 PHP 版本格式应为 8.2 这类主次版本号")
		}
	}
	if v, ok := out["log_retention_days"]; ok {
		if strings.TrimSpace(v) != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 3650 {
				return nil, errors.New("日志保留天数必须是 0~3650 的整数")
			}
			out["log_retention_days"] = strconv.Itoa(n)
		}
	}
	return out, nil
}

// handleTrafficReset 清空流量统计与采集 checkpoint。
//
// v0.8.1 及以前的累加逻辑会把同一批日志反复计入，历史数字偏高；
// 升级后新采集是准确的，但旧数据不会自动消失。本端点提供显式清理，
// 必须提交 confirm:"RESET"，避免误触。
func (s *Server) handleTrafficReset(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Confirm != "RESET" {
		writeError(w, http.StatusBadRequest, errors.New(`请输入 RESET 确认清空流量统计`))
		return
	}
	if err := s.svc.DB().ResetTrafficStats(); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 日志 ----------

// handleSiteLogs 读取站点历史日志。
func (s *Server) handleSiteLogs(w http.ResponseWriter, r *http.Request, siteID string) {
	site, err := s.svc.DB().GetSite(siteID)
	if err != nil {
		writeModelError(w, err)
		return
	}
	// type 只允许白名单枚举：早期直接拼进文件路径，
	// type=../../../../etc/passwd 之类的值可穿越读取任意 .log 文件。
	logType := r.URL.Query().Get("type")
	switch logType {
	case "", "access", "error":
	default:
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	if logType == "" {
		logType = "access"
	}

	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			page = n
		}
	}
	// limit*page 是读取行数上限，必须封顶：早期 page 无上限，
	// limit=5000&page=100000 会把数千万条记录读进内存直接 OOM。
	if limit*page > maxLogScanLines {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}

	logPath := filepath.Join(site.Root.Path, "logs", logType+".log")
	// 双重保险：断言最终路径仍在站点 logs 目录内
	cleanPath := filepath.Clean(logPath)
	logsDir := filepath.Clean(filepath.Join(site.Root.Path, "logs")) + string(os.PathSeparator)
	if !strings.HasPrefix(cleanPath, logsDir) {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}

	if logType == "access" {
		records, err := logmgr.ReadAccessLog(logPath, limit*page)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, http.StatusOK, map[string]any{"records": []any{}, "page": page})
				return
			}
			writeModelError(w, err)
			return
		}
		start := (page - 1) * limit
		if start > len(records) {
			start = len(records)
		}
		end := start + limit
		if end > len(records) {
			end = len(records)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"records": records[start:end],
			"page":    page,
			"total":   len(records),
		})
		return
	}

	// 错误日志按纯文本返回
	content := readFileString(logPath)
	if content == "" {
		writeJSON(w, http.StatusOK, map[string]any{"records": []any{}, "page": page})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"records": []map[string]string{{"line": content}},
		"page":    page,
	})
}

// handleTail 返回 WebSocket 实时日志地址。
func (s *Server) handleTail(w http.ResponseWriter, r *http.Request) {
	siteID := r.URL.Query().Get("site_id")
	if siteID == "" {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	if _, err := s.svc.DB().GetSite(siteID); err != nil {
		writeModelError(w, err)
		return
	}
	wsURL := "/ws/logs?site_id=" + siteID
	if r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil {
		wsURL = "wss://" + r.Host + wsURL
	} else {
		wsURL = "ws://" + r.Host + wsURL
	}
	writeJSON(w, http.StatusOK, map[string]any{"ws_url": wsURL})
}

// upgrader WebSocket 升级器。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// 同源检查必须用相等判断，不能用 strings.Contains：
	// origin="https://panel.example.com.evil.com" 含有
	// r.Host="panel.example.com"，Contains 会放行。
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // 非浏览器客户端
		}
		return origin == "https://"+r.Host || origin == "http://"+r.Host
	},
}

// HandleWebSocket 处理实时日志推送。
func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	siteID := r.URL.Query().Get("site_id")
	site, err := s.svc.DB().GetSite(siteID)
	if err != nil {
		writeError(w, http.StatusNotFound, model.ErrNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	logPath := filepath.Join(site.Root.Path, "logs", "access.log")
	if _, err := os.Stat(logPath); err != nil {
		_ = conn.WriteJSON(map[string]any{"type": "error", "message": "日志文件不存在"})
		return
	}

	stop, err := logmgr.Tail(logPath, func(rec *model.LogRecord) {
		_ = conn.WriteJSON(map[string]any{"type": "line", "line": rec})
	})
	if err != nil {
		_ = conn.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	defer stop()

	// 读取客户端关闭信号，避免 goroutine 泄漏
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	<-done
}

// handleStatic 服务前端静态资源（由 main 注册到 /）。
func (s *Server) handleStatic(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	}
}

// jsonBytes 序列化为 JSON 字节。
func jsonBytes(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
