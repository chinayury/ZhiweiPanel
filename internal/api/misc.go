package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

// handleSettings 读写面板设置。
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		all, err := s.svc.DB().AllSettings()
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": all})

	case http.MethodPut, http.MethodPost:
		var body struct {
			Settings map[string]string `json:"settings"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		for k, v := range body.Settings {
			if err := s.svc.DB().SetSetting(k, v); err != nil {
				writeModelError(w, err)
				return
			}
		}
		// 设置变更后重新下发配置（例如默认 PHP 版本、站点根目录模板）
		_ = s.svc.ApplyAllConfig(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
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
