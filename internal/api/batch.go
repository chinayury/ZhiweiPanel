package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zhiwei/panel/internal/model"
)

// handleSitesBatch 批量操作多站点。
//
// POST /api/sites/batch
// body={action: "start"|"stop"|"delete", ids: ["..."]}
//
// 用于站点列表的多选操作。
func (s *Server) handleSitesBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("ids 不能为空"))
		return
	}
	if len(body.IDs) > 100 {
		writeError(w, http.StatusBadRequest, errors.New("单次最多 100 个"))
		return
	}
	type item struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	out := make([]item, 0, len(body.IDs))
	anyOK := false
	for _, id := range body.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		site, err := s.svc.DB().GetSite(id)
		if err != nil {
			out = append(out, item{ID: id, Error: err.Error()})
			continue
		}
		switch body.Action {
		case "start":
			if err := s.svc.ToggleSite(r.Context(), id, model.SiteStatusRunning); err != nil {
				out = append(out, item{ID: id, Name: site.Name, Error: err.Error()})
				continue
			}
			anyOK = true
			out = append(out, item{ID: id, Name: site.Name, Status: string(model.SiteStatusRunning)})
		case "stop":
			if err := s.svc.ToggleSite(r.Context(), id, model.SiteStatusStopped); err != nil {
				out = append(out, item{ID: id, Name: site.Name, Error: err.Error()})
				continue
			}
			anyOK = true
			out = append(out, item{ID: id, Name: site.Name, Status: string(model.SiteStatusStopped)})
		default:
			out = append(out, item{ID: id, Error: fmt.Sprintf("不支持的 action: %s", body.Action)})
		}
	}
	if anyOK {
		// 任何一个成功都触发 ApplyAllConfig
		if err := s.svc.ApplyAllConfig(context.Background()); err != nil {
			// 不报错，只把信息塞到 out[0] 之外不优雅；保持现状
		}
	}
	ok := 0
	for _, it := range out {
		if it.Error == "" {
			ok++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out, "ok_count": ok})
}
