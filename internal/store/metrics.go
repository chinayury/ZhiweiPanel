package store

import (
	"github.com/zhiwei/panel/internal/model"
)

// ---------------------------------------------------------------------------
// 宿主机指标持久化（v0.7.0）
//
// 内存环形缓冲（sysinfo.Collector）保留十秒级细节，重启即丢；
// 本组方法存取**分钟级**降采样趋势，目的是让"近 24 小时"曲线跨重启存在。
// 设计取舍见 model.MetricsSample 与 migrate() 里 metrics_samples 的注释。
// ---------------------------------------------------------------------------

// SaveMetricsSample 写入一条分钟级采样。ts（Unix 秒）是主键，
// 同一分钟重复写入覆盖旧行——重放/补写都幂等。
func (d *DB) SaveMetricsSample(m model.MetricsSample) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`
INSERT INTO metrics_samples (ts, cpu, mem, load1, disk, net_rx, net_tx)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(ts) DO UPDATE SET
  cpu=excluded.cpu, mem=excluded.mem, load1=excluded.load1,
  disk=excluded.disk, net_rx=excluded.net_rx, net_tx=excluded.net_tx`,
		m.TS, m.CPU, m.Mem, m.Load1, m.Disk, m.NetRx, m.NetTx)
	return err
}

// ListMetricsSamples 返回 [since, until) 区间内的采样（旧 → 新）。
// until <= 0 表示到最新。
func (d *DB) ListMetricsSamples(since, until int64) ([]model.MetricsSample, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.db.Query(`
SELECT ts, cpu, mem, load1, disk, net_rx, net_tx
FROM metrics_samples
WHERE ts >= ? AND (? <= 0 OR ts < ?)
ORDER BY ts ASC`, since, until, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MetricsSample{}
	for rows.Next() {
		var m model.MetricsSample
		if err := rows.Scan(&m.TS, &m.CPU, &m.Mem, &m.Load1, &m.Disk, &m.NetRx, &m.NetTx); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneMetricsSamples 删除 keepBefore（Unix 秒）之前的采样，返回删除行数。
//
// 采集器每分钟写一行，表按分钟线性增长——没有清理就会无限膨胀，
// 把主库撑大、拖慢备份。写入方（service 采集器）按固定保留期调用本方法。
func (d *DB) PruneMetricsSamples(keepBefore int64) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`DELETE FROM metrics_samples WHERE ts < ?`, keepBefore)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
