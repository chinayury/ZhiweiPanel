// Package cron 提供面板的计划任务子系统。
//
// 设计目标（v0.2.0 借鉴宝塔计划任务）：
//   - 标准 5 段 crontab 表达式 + 内置 6 种任务类型
//   - 后台 goroutine 每 30s 扫一次 DB，把"已到期"任务入队
//   - 任务执行异步、不阻塞扫描；同一任务不会重叠执行
//   - 执行结果写库 + 失败时调 alert 通知
//   - shell 任务执行前过禁用词黑名单（shutdown/init/mkfs/passwd 等禁）
package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/zhiwei/panel/internal/alert"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/service"
	"github.com/zhiwei/panel/internal/store"
)

// TickInterval 调度器扫描间隔。
//
// 30s 是"分钟级 cron 也能接受"和"DB 压力"的折中：单次扫描只查 enabled=1 AND
// next_run_at<=now 的少量行，索引已建立（idx_cron_jobs_next）。
const TickInterval = 30 * time.Second

// MaxConcurrent 单时间点最大并发任务数。
//
// 防止 panel 重启后一批任务堆积同时启动把机器打爆。
const MaxConcurrent = 4

// ErrAlreadyRunning 同一任务正在执行，重复触发直接拒。
var ErrAlreadyRunning = errors.New("job already running")

// Scheduler 是计划任务调度器。
type Scheduler struct {
	svc      *service.Service
	db       *store.DB
	alertEng *alert.Engine
	parser   cron.Parser

	mu      sync.Mutex
	running map[string]struct{} // job_id -> 当前正在跑
	cancel  context.CancelFunc
}

// New 构造调度器。svc 可为 nil（DB 仍要传）；alert 失败时仅记日志，不阻塞任务。
func New(svc *service.Service, db *store.DB, eng *alert.Engine) *Scheduler {
	return &Scheduler{
		svc:      svc,
		db:       db,
		alertEng: eng,
		parser: cron.NewParser(
			cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
		),
		running: make(map[string]struct{}),
	}
}

// Start 启动后台循环。返回值：第一次入队的 "tick" channel，单元测试用它触发。
//
// ctx 是父上下文（main 的 cancel）。调度器退出时 close 它内部的 goroutine。
func (s *Scheduler) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	// 启动时先把所有 enabled 任务的 next_run_at 算出来——
	// v0.1.0 之前没有这个字段，旧任务列出来 first_run_at=NULL，扫描器不会触发它们。
	// 这一步对 v0.2.0 全新部署可省略，但保证兼容。
	if err := s.bootstrapNextRuns(ctx); err != nil {
		log.Printf("计划任务初始化 next_run_at 失败: %v", err)
	}
	go s.loop(ctx)
}

// Stop 优雅退出。
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// loop 是主循环。
func (s *Scheduler) loop(ctx context.Context) {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	// 启动先扫一次，避免新装的任务等满 30s 才触发。
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now()
	jobs, err := s.db.ListDueCronJobs(now, 32)
	if err != nil {
		log.Printf("计划任务扫描失败: %v", err)
		return
	}
	for _, j := range jobs {
		if !s.tryStart(j.ID) {
			continue
		}
		go s.runOne(ctx, j)
	}
}

func (s *Scheduler) tryStart(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.running[id]; busy {
		return false
	}
	if len(s.running) >= MaxConcurrent {
		return false
	}
	s.running[id] = struct{}{}
	return true
}

func (s *Scheduler) done(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}

// runOne 执行单个任务。错误时写库 + 通知。
func (s *Scheduler) runOne(ctx context.Context, j *model.CronJob) {
	defer s.done(j.ID)
	start := time.Now()
	run := &model.CronRun{JobID: j.ID, StartedAt: start}

	h, err := handlerFor(j.Type)
	if err != nil {
		run.Status = "failed"
		run.Message = err.Error()
		run.EndedAt = time.Now()
		_, _ = s.db.InsertCronRun(run)
		s.markFinished(j, run)
		s.sendAlert(j, run)
		return
	}

	// 给任务一个独立 timeout：默认 1 小时（防 shell 卡死挂起整个调度器）。
	// 备份类可能更慢，由 handler 内部 ctx 继承更长 timeout。
	runCtx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()

	if err := h.Run(runCtx, j, s.svc); err != nil {
		run.Status = "failed"
		run.Message = truncateMsg(err.Error(), 1000)
	} else {
		run.Status = "ok"
		run.Message = "ok"
	}
	run.EndedAt = time.Now()
	_, _ = s.db.InsertCronRun(run)
	// 滚动清理：每任务最多 50 条
	_ = s.db.TrimCronRuns(j.ID, 50)

	s.markFinished(j, run)
	if run.Status == "failed" {
		s.sendAlert(j, run)
	}
}

// markFinished 把 last_* / next_run_at 写回。
func (s *Scheduler) markFinished(j *model.CronJob, run *model.CronRun) {
	j.LastRunAt = &run.StartedAt
	j.LastStatus = model.CronJobStatus(run.Status)
	j.LastMsg = run.Message
	sched, err := s.parser.Parse(j.Schedule)
	if err == nil {
		nxt := sched.Next(run.EndedAt.Add(time.Second))
		j.NextRunAt = &nxt
	}
	if err := s.db.SaveCronJob(j); err != nil {
		log.Printf("计划任务回写失败: job=%s err=%v", j.ID, err)
	}
}

func (s *Scheduler) sendAlert(j *model.CronJob, run *model.CronRun) {
	if s.alertEng == nil {
		return
	}
	msg := fmt.Sprintf("计划任务 %q (%s) 执行失败：%s", j.Name, j.Type, run.Message)
	if err := s.alertEng.NotifyGlobal("cron_failed", "warning", msg); err != nil {
		log.Printf("计划任务告警发送失败: %v", err)
	}
}

// bootstrapNextRuns 启动时给所有 enabled 任务算 first next_run_at。
func (s *Scheduler) bootstrapNextRuns(ctx context.Context) error {
	jobs, err := s.db.ListCronJobs()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		if j.NextRunAt != nil {
			continue
		}
		sched, err := s.parser.Parse(j.Schedule)
		if err != nil {
			continue
		}
		nxt := sched.Next(time.Now())
		j.NextRunAt = &nxt
		if err := s.db.SaveCronJob(j); err != nil {
			log.Printf("bootstrap next_run_at 失败: job=%s err=%v", j.ID, err)
		}
	}
	return nil
}

// truncateMsg 截断错误信息避免 DB 字段溢出。
func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// ParseSchedule 解析 crontab 表达式并返回 next 触发时间。
// 给 API 用：保存/更新前先 validate。
func ParseSchedule(expr string, now time.Time) (time.Time, error) {
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := p.Parse(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(now), nil
}

// EncodePayload 序列化任务参数 JSON。
func EncodePayload(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodePayload 反序列化任务参数。
func DecodePayload(s string, v any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}
