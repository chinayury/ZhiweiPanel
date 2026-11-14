package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// ---------- cron_jobs ----------

// SaveCronJob 新增或更新计划任务。NextRunAt 由调度器在启用时算好，保存时直接落盘。
func (d *DB) SaveCronJob(j *model.CronJob) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	j.UpdatedAt = now

	enabled := 0
	if j.Enabled {
		enabled = 1
	}
	var lastRun, nextRun sql.NullTime
	if j.LastRunAt != nil && !j.LastRunAt.IsZero() {
		lastRun = sql.NullTime{Time: *j.LastRunAt, Valid: true}
	}
	if j.NextRunAt != nil && !j.NextRunAt.IsZero() {
		nextRun = sql.NullTime{Time: *j.NextRunAt, Valid: true}
	}

	_, err := d.db.Exec(`
INSERT INTO cron_jobs (id,name,type,enabled,schedule,target,payload,last_run_at,last_status,last_msg,next_run_at,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, type=excluded.type, enabled=excluded.enabled,
  schedule=excluded.schedule, target=excluded.target, payload=excluded.payload,
  last_run_at=excluded.last_run_at, last_status=excluded.last_status, last_msg=excluded.last_msg,
  next_run_at=excluded.next_run_at, updated_at=excluded.updated_at`,
		j.ID, j.Name, string(j.Type), enabled, j.Schedule, j.Target, j.Payload,
		lastRun, string(j.LastStatus), j.LastMsg, nextRun,
		j.CreatedAt, j.UpdatedAt)
	return err
}

// GetCronJob 按 ID 读取。
func (d *DB) GetCronJob(id string) (*model.CronJob, error) {
	row := d.db.QueryRow(`SELECT id,name,type,enabled,schedule,target,payload,last_run_at,last_status,last_msg,next_run_at,created_at,updated_at FROM cron_jobs WHERE id=?`, id)
	return scanCronJob(row)
}

// ListCronJobs 列出全部任务，按创建时间倒序。
func (d *DB) ListCronJobs() ([]*model.CronJob, error) {
	rows, err := d.db.Query(`SELECT id,name,type,enabled,schedule,target,payload,last_run_at,last_status,last_msg,next_run_at,created_at,updated_at FROM cron_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.CronJob
	for rows.Next() {
		j, err := scanCronJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// DeleteCronJob 删除。
func (d *DB) DeleteCronJob(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM cron_jobs WHERE id=?`, id)
	return err
}

// ListDueCronJobs 拉取所有 enabled=1 且 next_run_at<=now 的任务。
//
// 调度器每 30s 调一次。它会拿到一个"应当跑"的快照，然后逐个执行。
// 这里不直接执行，是为了让"扫描"和"执行"解耦——前者是 DB 读，后者
// 可能耗时（shell 跑 10 分钟、备份跑 5 分钟），不该阻塞扫描。
func (d *DB) ListDueCronJobs(now time.Time, limit int) ([]*model.CronJob, error) {
	rows, err := d.db.Query(`
SELECT id,name,type,enabled,schedule,target,payload,last_run_at,last_status,last_msg,next_run_at,created_at,updated_at
FROM cron_jobs
WHERE enabled=1 AND next_run_at IS NOT NULL AND next_run_at <= ?
ORDER BY next_run_at ASC
LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.CronJob
	for rows.Next() {
		j, err := scanCronJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// scanCronJob 解析一行。
//
// row 接受 *sql.Row 和 *sql.Rows，两者都实现了 Scan，因此传 interface。
// 用 sql.NullTime 接收可空时间列；非空时回填到 *time.Time。
func scanCronJob(row interface {
	Scan(...any) error
}) (*model.CronJob, error) {
	var j model.CronJob
	var typ, lastStatus string
	var enabled int
	var lastRun, nextRun, created, updated sql.NullTime
	var payload sql.NullString
	err := row.Scan(
		&j.ID, &j.Name, &typ, &enabled, &j.Schedule, &j.Target, &payload,
		&lastRun, &lastStatus, &j.LastMsg, &nextRun, &created, &updated,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrNotFound
		}
		return nil, err
	}
	j.Type = model.CronJobType(strings.TrimSpace(typ))
	j.Enabled = enabled != 0
	j.LastStatus = model.CronJobStatus(strings.TrimSpace(lastStatus))
	if payload.Valid {
		j.Payload = payload.String
	}
	if lastRun.Valid {
		t := lastRun.Time
		j.LastRunAt = &t
	}
	if nextRun.Valid {
		t := nextRun.Time
		j.NextRunAt = &t
	}
	if created.Valid {
		j.CreatedAt = created.Time
	}
	if updated.Valid {
		j.UpdatedAt = updated.Time
	}
	return &j, nil
}

// ---------- cron_runs ----------

// InsertCronRun 写一条执行历史。
func (d *DB) InsertCronRun(r *model.CronRun) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`
INSERT INTO cron_runs (job_id,started_at,ended_at,status,message)
VALUES (?,?,?,?,?)`,
		r.JobID, r.StartedAt, r.EndedAt, r.Status, r.Message)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListCronRuns 拉取某任务最近 N 条执行记录。
//
// 用于任务详情页的"执行历史"列表。limit 默认 50，scheduler 写入时
// 也按此阈值清理更早的记录（每写一条新记录就 trim 一次到 50）。
func (d *DB) ListCronRuns(jobID string, limit int) ([]*model.CronRun, error) {
	rows, err := d.db.Query(`
SELECT id,job_id,started_at,ended_at,status,message
FROM cron_runs
WHERE job_id=?
ORDER BY started_at DESC
LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.CronRun
	for rows.Next() {
		var r model.CronRun
		var started, ended sql.NullTime
		if err := rows.Scan(&r.ID, &r.JobID, &started, &ended, &r.Status, &r.Message); err != nil {
			return nil, err
		}
		if started.Valid {
			r.StartedAt = started.Time
		}
		if ended.Valid {
			r.EndedAt = ended.Time
		}
		out = append(out, &r)
	}
	return out, nil
}

// TrimCronRuns 保留某任务最近 keepN 条，超出删除。
func (d *DB) TrimCronRuns(jobID string, keepN int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`
DELETE FROM cron_runs
WHERE job_id=? AND id NOT IN (
  SELECT id FROM cron_runs WHERE job_id=? ORDER BY started_at DESC LIMIT ?
)`, jobID, jobID, keepN)
	return err
}
