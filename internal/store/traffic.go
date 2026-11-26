package store

import (
	"database/sql"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// ---------- v0.9.0: 流量统计增量采集 ----------

// GetTrafficOffset 读取站点日志采集 checkpoint；不存在返回 nil。
func (d *DB) GetTrafficOffset(siteID string) (*model.TrafficLogOffset, error) {
	var o model.TrafficLogOffset
	var updated sql.NullTime
	err := d.db.QueryRow(`SELECT site_id,path,dev,ino,offset,sample,updated_at
FROM traffic_log_offsets WHERE site_id=?`, siteID).
		Scan(&o.SiteID, &o.Path, &o.Dev, &o.Ino, &o.Offset, &o.Sample, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if updated.Valid {
		o.UpdatedAt = updated.Time
	}
	return &o, nil
}

// PrimeTrafficOffset 为新建站点预置 checkpoint（offset=0）。
//
// 新站点还没有历史日志，首轮从 0 开始不会重复计数；若不预置，
// ReadIncremental 的"首次从末尾开始"会漏掉建站后到首轮采集之间的流量。
func (d *DB) PrimeTrafficOffset(siteID, path string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT OR IGNORE INTO traffic_log_offsets
(site_id,path,dev,ino,offset,sample,updated_at) VALUES (?,?,0,0,0,'',?)`,
		siteID, path, time.Now())
	return err
}

// ApplyTrafficBatch 原子提交一次采集结果。
//
// 统计累加、IP 去重、状态码、checkpoint 必须同事务：任何一步失败都不推进
// checkpoint，下一轮从旧位置重读。反之若分开提交，失败后会重复累加。
func (d *DB) ApplyTrafficBatch(b *model.TrafficBatch) error {
	if b == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	for _, day := range b.Days {
		if day == nil || day.Stat == nil {
			continue
		}
		s := day.Stat
		if _, err := tx.Exec(`
INSERT INTO traffic_stats (date,site_id,requests,bytes,status_2xx,status_3xx,status_4xx,status_5xx,unique_ips)
VALUES (?,?,?,?,?,?,?,?,0)
ON CONFLICT(date,site_id) DO UPDATE SET
  requests=requests+excluded.requests, bytes=bytes+excluded.bytes,
  status_2xx=status_2xx+excluded.status_2xx, status_3xx=status_3xx+excluded.status_3xx,
  status_4xx=status_4xx+excluded.status_4xx, status_5xx=status_5xx+excluded.status_5xx`,
			s.Date, s.SiteID, s.Requests, s.Bytes, s.Status2xx, s.Status3xx,
			s.Status4xx, s.Status5xx); err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, ip := range day.IPs {
			if ip == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO traffic_ips (date,site_id,ip) VALUES (?,?,?)`,
				s.Date, s.SiteID, ip); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		// UV 以 traffic_ips 的实际行数为准，不在采集器里估算。
		if _, err := tx.Exec(`UPDATE traffic_stats SET unique_ips=(
  SELECT COUNT(*) FROM traffic_ips WHERE date=? AND site_id=?
) WHERE date=? AND site_id=?`, s.Date, s.SiteID, s.Date, s.SiteID); err != nil {
			_ = tx.Rollback()
			return err
		}
		for code, count := range day.StatusCodes {
			if code <= 0 || count <= 0 {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO traffic_status (date,site_id,code,count)
VALUES (?,?,?,?)
ON CONFLICT(date,site_id,code) DO UPDATE SET count=count+excluded.count`,
				s.Date, s.SiteID, code, count); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	ck := b.Checkpoint
	if _, err := tx.Exec(`INSERT INTO traffic_log_offsets
(site_id,path,dev,ino,offset,sample,updated_at) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(site_id) DO UPDATE SET
  path=excluded.path, dev=excluded.dev, ino=excluded.ino,
  offset=excluded.offset, sample=excluded.sample, updated_at=excluded.updated_at`,
		ck.SiteID, ck.Path, ck.Dev, ck.Ino, ck.Offset, ck.Sample, time.Now()); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// DeleteTrafficSite 删除一个站点的全部流量统计与采集状态。
// 删站时调用，避免 site_id 永远不再复用时留下无主明细行。
func (d *DB) DeleteTrafficSite(siteID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	for _, table := range []string{"traffic_stats", "traffic_ips", "traffic_status", "traffic_log_offsets"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE site_id=?`, siteID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// GetTrafficStatusSummary 返回最近 days 天的精确状态码汇总。
func (d *DB) GetTrafficStatusSummary(siteID string, days int) ([]model.TrafficStatusCount, error) {
	since := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	rows, err := d.db.Query(`SELECT code, COALESCE(SUM(count),0)
FROM traffic_status WHERE site_id=? AND date>=? GROUP BY code ORDER BY code`, siteID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TrafficStatusCount
	for rows.Next() {
		var c model.TrafficStatusCount
		if err := rows.Scan(&c.Code, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneTrafficExtra 清理过期的 UV / 状态码明细。
//
// traffic_stats 本身很小（每站点每天一行），长期保留；traffic_ips 与
// traffic_status 会随 IP 数与状态码数增长，默认保留 90 天即可。
func (d *DB) PruneTrafficExtra(beforeDate string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM traffic_ips WHERE date<?`, beforeDate); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM traffic_status WHERE date<?`, beforeDate); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ResetTrafficStats 清空流量统计与 checkpoint。
//
// 用于升级后清理历史虚高数据：v0.8.1 及以前的累加逻辑会让历史数字偏高，
// 本函数不自动调用，必须由管理员显式确认（POST /api/system/traffic/reset）。
// 清空 checkpoint 后下一轮采集从文件末尾起步，不会把旧日志重新算一遍。
func (d *DB) ResetTrafficStats() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	for _, table := range []string{"traffic_stats", "traffic_ips", "traffic_status", "traffic_log_offsets"} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
