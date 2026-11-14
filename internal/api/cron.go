package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/cron"
	"github.com/zhiwei/panel/internal/model"
)

// ---------- 计划任务 ----------

// handleCronJobs 列表 + 创建。
//
// GET /api/cron/jobs         → { jobs: [...] }
// POST /api/cron/jobs        → 新建任务，返回 { id, ...job }
func (s *Server) handleCronJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jobs, err := s.svc.DB().ListCronJobs()
		if err != nil {
			writeModelError(w, err)
			return
		}
		if jobs == nil {
			jobs = []*model.CronJob{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	case http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Schedule string `json:"schedule"`
			Target   string `json:"target"`
			Payload  string `json:"payload"`
			Enabled  bool   `json:"enabled"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			writeError(w, http.StatusBadRequest, errors.New("name 不能为空"))
			return
		}
		if _, err := cron.ParseSchedule(body.Schedule, time.Now()); err != nil {
			writeError(w, http.StatusBadRequest, errors.New("crontab 表达式不合法: "+err.Error()))
			return
		}
		// shell 任务需要独立的 shell 权限（write 不蕴含它）。
		if !requireShellScope(w, r, body.Type) {
			return
		}
		j := &model.CronJob{
			ID:       newCronID(),
			Name:     body.Name,
			Type:     model.CronJobType(body.Type),
			Enabled:  body.Enabled,
			Schedule: body.Schedule,
			Target:   body.Target,
			Payload:  body.Payload,
		}
		// 立即算 next_run_at，避免面板启动前第一次扫描就漏。
		if next, err := cron.ParseSchedule(body.Schedule, time.Now()); err == nil {
			j.NextRunAt = &next
		}
		if err := s.svc.DB().SaveCronJob(j); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, j)
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// handleCronJobByID 详情 / 修改 / 删除。
//
// GET    /api/cron/jobs/{id}            → { job, runs }
// PUT    /api/cron/jobs/{id}            → 修改
// DELETE /api/cron/jobs/{id}            → 删除
func (s *Server) handleCronJobByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/cron/jobs/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		j, err := s.svc.DB().GetCronJob(id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		runs, _ := s.svc.DB().ListCronRuns(id, 50)
		writeJSON(w, http.StatusOK, map[string]any{"job": j, "runs": runs})
	case http.MethodPut:
		j, err := s.svc.DB().GetCronJob(id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		var body struct {
			Name     *string `json:"name"`
			Type     *string `json:"type"`
			Schedule *string `json:"schedule"`
			Target   *string `json:"target"`
			Payload  *string `json:"payload"`
			Enabled  *bool   `json:"enabled"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if body.Name != nil {
			j.Name = *body.Name
		}
		if body.Type != nil {
			j.Type = model.CronJobType(*body.Type)
		}
		if body.Schedule != nil {
			if _, err := cron.ParseSchedule(*body.Schedule, time.Now()); err != nil {
				writeError(w, http.StatusBadRequest, errors.New("crontab 表达式不合法: "+err.Error()))
				return
			}
			j.Schedule = *body.Schedule
			// 重算 next_run_at
			if next, err := cron.ParseSchedule(j.Schedule, time.Now()); err == nil {
				j.NextRunAt = &next
			}
		}
		if body.Target != nil {
			j.Target = *body.Target
		}
		if body.Payload != nil {
			j.Payload = *body.Payload
		}
		if body.Enabled != nil {
			j.Enabled = *body.Enabled
		}
		// 改完之后如果变成（或仍然是）shell 任务，需要独立的 shell 权限。
		// 注意必须在应用 body 之后判定：把一个普通任务改成 shell
		// 同样属于"取得 root 执行能力"，不能只拦新建。
		if !requireShellScope(w, r, string(j.Type)) {
			return
		}
		if err := s.svc.DB().SaveCronJob(j); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, j)
	case http.MethodDelete:
		if err := s.svc.DB().DeleteCronJob(id); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// handleCronJobRun 立即触发一次（不等待 cron）。
//
// POST /api/cron/jobs/run  body={job_id: "..."}
//
// 用于"按钮立即跑一次"——不会持久化 last_run_at（那由调度器写）。
func (s *Server) handleCronJobRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body struct {
		JobID string `json:"job_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// 当前 v0.2.0 简化：只标记 next_run_at=now，让调度器在 30s 内捡起来跑。
	j, err := s.svc.DB().GetCronJob(body.JobID)
	if err != nil {
		writeModelError(w, err)
		return
	}
	// "立即执行"同样是在触发执行：shell 任务需要 shell 权限。
	if !requireShellScope(w, r, string(j.Type)) {
		return
	}
	now := time.Now()
	j.NextRunAt = &now
	if err := s.svc.DB().SaveCronJob(j); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next_run_at": now})
}

// newCronID 生成 12 字节随机 hex。
func newCronID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// strconvItoaSafe 兼容 int == 0 的占位
func strconvItoaSafe(n int) string { return strconv.Itoa(n) }

var _ = strconvItoaSafe
