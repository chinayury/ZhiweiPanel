package api

import (
	"net/http"
	"strings"

	"github.com/zhiwei/panel/internal/model"
)

// ---------------------------------------------------------------------------
// 系统运行指标 + nginx 配置历史
// ---------------------------------------------------------------------------

// handleMetrics GET /api/system/metrics
//
// 返回当前快照 + 最近的历史采样（内存环形缓冲，无 DB 写入）。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current": s.svc.Metrics(),
		"history": s.svc.MetricsHistory(),
	})
}

// handleConfigHistory GET /api/system/config-history
//
// 列出 nginx 配置的历史快照。
func (s *Server) handleConfigHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	entries, err := s.svc.ListConfigHistory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": entries})
}

// handleConfigHistoryByID 处理 /api/system/config-history/{id} 与其子路径。
//
//   - GET    /api/system/config-history/{id}            查看快照内容
//   - GET    /api/system/config-history/{id}/diff       与当前生效配置对比
//   - POST   /api/system/config-history/{id}/restore   回滚到该快照
func (s *Server) handleConfigHistoryByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/system/config-history/"), "/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		s.showConfigSnapshot(w, r, id)
	case action == "diff" && r.Method == http.MethodGet:
		s.diffConfigSnapshot(w, r, id)
	case action == "restore" && r.Method == http.MethodPost:
		s.restoreConfigSnapshot(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// showConfigSnapshot 返回快照元数据与文件内容。
func (s *Server) showConfigSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	entry, contents, err := s.svc.GetConfigSnapshot(id)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": entry,
		"contents": contents,
	})
}

// diffConfigSnapshot 把快照与当前生效配置对比。
func (s *Server) diffConfigSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	diffs, err := s.svc.DiffConfigSnapshot(id)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"diffs": diffs})
}

// restoreConfigSnapshot 回滚到指定快照。
//
// 回滚只恢复 nginx 配置文件，**不回滚数据库**。
// 这一点必须对用户讲清楚：面板里的站点定义仍然是"当前"那一份，
// 恢复的是"当时生效在 nginx 上的配置"。若要连站点定义一起回退，
// 应当配合站点备份使用。
func (s *Server) restoreConfigSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	restored, err := s.svc.RestoreConfigSnapshot(r.Context(), id)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"restored": restored,
		"message":  "配置已回滚并重载。注意：数据库中的站点定义未回滚。",
	})
}
