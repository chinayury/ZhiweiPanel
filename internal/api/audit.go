package api

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// ---------------------------------------------------------------------------
// 操作审计
//
// 面板以 root 运行，能改任意配置、删任意文件。没有审计的话，
// 出事之后无法回答"谁在什么时候动了什么"——等保场景下的硬要求。
//
// 设计取舍：
//   - 只记写操作（POST/PUT/PATCH/DELETE）+ 登录事件。
//     读操作量大且价值低，全记只会把表撑爆、把真信号淹掉。
//   - 审计写在鉴权**之后**：这样拿得到调用者身份，也拿得到响应码。
//   - 写审计失败绝不影响主流程——审计是辅助设施，
//     不能因为它把用户的正常操作搞失败。
// ---------------------------------------------------------------------------

// auditLimit 是审计表保留条数上限。
const auditLimit = 5000

// auditCounter 用于每 N 次写入做一次清理，避免每次都 COUNT(*)。
var auditCounter atomic.Int64

// statusRecorder 包装 ResponseWriter 以捕获状态码。
//
// 只实现需要的方法：流式响应（Flusher/Hijacker）没透传，
// 但本包所有写接口都是普通 JSON 响应，不涉及 SSE/WebSocket
// （WebSocket 走的是单独的 authWS 路径，不经过这里）。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wrote {
		sr.status = code
		sr.wrote = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wrote {
		sr.status = http.StatusOK
		sr.wrote = true
	}
	return sr.ResponseWriter.Write(b)
}

// auditMiddleware 记录写操作。
//
// 放在 authMiddleware 之后使用：principal 已经进 context，
// 拿得到用户名与"是否用 Token"。
func (s *Server) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAuditedMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		s.recordAudit(r, rec.status)
	})
}

// isAuditedMethod 判断该方法是否需要留痕。
func isAuditedMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// recordAudit 写一条审计记录。失败只记服务端日志。
func (s *Server) recordAudit(r *http.Request, status int) {
	p, _ := principalFrom(r.Context())
	action, target := deriveAuditAction(r.Method, r.URL.Path)

	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}

	entry := &model.AuditLog{
		Username: p.Username,
		Action:   action,
		Target:   target,
		Method:   r.Method,
		Path:     r.URL.Path,
		Status:   status,
		IP:       s.clientIP(r),
		UA:       ua,
		ViaToken: p.ViaToken,
	}
	if err := s.svc.DB().SaveAuditLog(entry); err != nil {
		log.Printf("[audit] 写审计日志失败: %v", err)
		return
	}

	// 每 200 条做一次清理：审计表会无限增长，长期跑着的面板必须有上限。
	if auditCounter.Add(1)%200 == 0 {
		if err := s.svc.DB().TrimAuditLogs(auditLimit); err != nil {
			log.Printf("[audit] 清理旧记录失败: %v", err)
		}
	}
}

// deriveAuditAction 从方法与路径推导动作名与目标。
//
// 规则：
//   - 去掉 /api/ 前缀，第一段是资源名（sites / certs / cron / ...）
//   - system 是命名空间：第二段才是真正的资源名
//   - 有 ID 段：动作 = 资源.动词；ID 段就是 target
//   - 有子动作段（toggle / bind / issue / restore ...）：动作 = 资源.子动作
//   - POST 无 ID：动作 = 资源.create
//
// 例：
//
//	POST   /api/sites                 → site.create
//	PUT    /api/sites/abc123          → site.update      target=abc123
//	DELETE /api/sites/abc123          → site.delete      target=abc123
//	POST   /api/sites/abc123/toggle   → site.toggle      target=abc123
//	POST   /api/certs/issue           → cert.issue
//	DELETE /api/tokens/xyz            → token.delete     target=xyz
//	POST   /api/system/config-apply   → config.apply
//	POST   /api/system/config-history/{id}/restore → config-history.restore  target={id}
func deriveAuditAction(method, path string) (action, target string) {
	p := strings.TrimPrefix(path, "/api/")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "unknown", ""
	}
	resource := parts[0]

	// 资源名归一：sites → site，certs → cert，cron/jobs → cron
	singular := map[string]string{
		"sites":   "site",
		"certs":   "cert",
		"cron":    "cron",
		"backups": "backup",
		"tokens":  "token",
		"alerts":  "alert",
	}
	if s, ok := singular[resource]; ok {
		resource = s
	}

	rest := parts[1:]
	// verb 是已经确定的动作词（目前只有 system 命名空间会预置）。
	verb := ""

	// system 是命名空间，第二段才是真正的资源名。
	//
	// 不做这一步的话，/api/system/config-history/{id}/restore 会被推成
	// "system.action"、target 还取成 "config-history" —— 恰好把最需要追溯的
	// "谁回滚了哪一版配置"给抹平了。
	if resource == "system" && len(rest) > 0 {
		if m, ok := systemActions[rest[0]]; ok {
			resource, verb = m.resource, m.verb
		} else {
			resource = rest[0]
		}
		rest = rest[1:]
	}

	if len(rest) == 0 {
		if verb != "" {
			return resource + "." + verb, ""
		}
		switch method {
		case http.MethodPost:
			return resource + ".create", ""
		case http.MethodPut:
			return resource + ".update", ""
		case http.MethodDelete:
			return resource + ".delete", ""
		}
		return resource + "." + strings.ToLower(method), ""
	}

	first := rest[0]
	// 固定的子动作（不是 ID）
	subActions := map[string]bool{
		"toggle": true, "bind": true, "issue": true, "revoke": true,
		"import": true, "run": true, "restore": true, "unzip": true,
		"rename": true, "mkdir": true, "upload": true, "reload": true,
		"resolve": true, "test": true, "apply": true, "batch": true,
		"reset": true, "rotate": true,
	}
	if subActions[first] {
		return resource + "." + first, ""
	}

	// 把 ID 段当目标
	target = first
	if len(rest) > 1 && subActions[rest[1]] {
		return resource + "." + rest[1], target
	}
	switch method {
	case http.MethodPost:
		return resource + ".action", target
	case http.MethodPut:
		return resource + ".update", target
	case http.MethodDelete:
		return resource + ".delete", target
	case http.MethodPatch:
		return resource + ".patch", target
	}
	return resource + "." + strings.ToLower(method), target
}

// systemActions 映射 /api/system/ 下的第二段路径。
//
// 值分别是（资源名, 动作词）。动作词为空表示该路径没有固定动词，
// 由后面的 ID / 子动作段决定（如 config-history/{id}/restore）。
var systemActions = map[string]struct{ resource, verb string }{
	"config-apply":   {"config", "apply"},
	"config-history": {"config-history", ""},
	"nginx-config":   {"nginx", "config"},
	"nginx-test":     {"nginx", "test"},
	"mysql":          {"mysql", "status"},
	"logs":           {"log", "tail"},
}

// handleAuditLogs GET /api/audit-logs —— 列出操作审计记录。
//
// 支持 ?limit=&action= 两个过滤参数。
func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	action := strings.TrimSpace(r.URL.Query().Get("action"))

	logs, err := s.svc.DB().ListAuditLogs(limit, action)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	total, _ := s.svc.DB().CountAuditLogs()
	writeJSON(w, http.StatusOK, map[string]any{
		"logs":  logs,
		"total": total,
		"limit": auditLimit,
	})
}

// handleAuditLogin 记录登录事件。
//
// 登录不在 protected 中间件覆盖范围内（登录接口本来就要免鉴权），
// 所以这里显式调用。
func (s *Server) handleAuditLogin(r *http.Request, username, action, detail string, status int) {
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	entry := &model.AuditLog{
		Username:  username,
		Action:    action,
		Detail:    detail,
		Method:    r.Method,
		Path:      r.URL.Path,
		Status:    status,
		IP:        s.clientIP(r),
		UA:        ua,
		CreatedAt: time.Now(),
	}
	if err := s.svc.DB().SaveAuditLog(entry); err != nil {
		log.Printf("[audit] 写登录审计失败: %v", err)
	}
}
