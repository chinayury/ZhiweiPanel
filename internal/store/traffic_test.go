package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

func openTrafficTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestApplyTrafficBatchAndUV(t *testing.T) {
	db := openTrafficTestDB(t)
	today := time.Now().Format("2006-01-02")

	batch := &model.TrafficBatch{
		Checkpoint: model.TrafficLogOffset{SiteID: "s1", Path: "/x/access.log", Dev: 1, Ino: 2, Offset: 100, Sample: "abc"},
		Days: []*model.TrafficDay{{
			Stat: &model.TrafficStat{
				Date: today, SiteID: "s1", Requests: 3, Bytes: 300,
				Status2xx: 2, Status4xx: 1,
			},
			IPs:         []string{"1.1.1.1", "2.2.2.2", "1.1.1.1"},
			StatusCodes: map[int]int64{200: 2, 404: 1},
		}},
	}
	if err := db.ApplyTrafficBatch(batch); err != nil {
		t.Fatal(err)
	}
	stats, err := db.GetTrafficStats("s1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats=%d，期望 1", len(stats))
	}
	s := stats[0]
	if s.Requests != 3 || s.Bytes != 300 || s.Status2xx != 2 || s.Status4xx != 1 || s.UniqueIPs != 2 {
		t.Fatalf("首轮统计错误: %+v", s)
	}

	off, err := db.GetTrafficOffset("s1")
	if err != nil || off == nil {
		t.Fatalf("checkpoint 读取失败: %v %+v", err, off)
	}
	if off.Offset != 100 || off.Sample != "abc" || off.Path != "/x/access.log" {
		t.Fatalf("checkpoint=%+v", off)
	}

	// 同一天再来一批：请求累加，UV 按 IP 去重（1.1.1.1 不重复计）。
	batch2 := &model.TrafficBatch{
		Checkpoint: model.TrafficLogOffset{SiteID: "s1", Path: "/x/access.log", Offset: 200},
		Days: []*model.TrafficDay{{
			Stat:        &model.TrafficStat{Date: today, SiteID: "s1", Requests: 1, Bytes: 10, Status2xx: 1},
			IPs:         []string{"1.1.1.1", "3.3.3.3"},
			StatusCodes: map[int]int64{200: 1},
		}},
	}
	if err := db.ApplyTrafficBatch(batch2); err != nil {
		t.Fatal(err)
	}
	stats, _ = db.GetTrafficStats("s1", 7)
	s = stats[0]
	if s.Requests != 4 || s.Bytes != 310 || s.UniqueIPs != 3 {
		t.Fatalf("第二批统计错误: %+v", s)
	}

	codes, err := db.GetTrafficStatusSummary("s1", 7)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]int64{}
	for _, c := range codes {
		got[c.Code] = c.Count
	}
	if got[200] != 3 || got[404] != 1 {
		t.Fatalf("状态码汇总错误: %+v", got)
	}
}

func TestPrimeTrafficOffsetAndReset(t *testing.T) {
	db := openTrafficTestDB(t)
	if err := db.PrimeTrafficOffset("s1", "/x/access.log"); err != nil {
		t.Fatal(err)
	}
	off, err := db.GetTrafficOffset("s1")
	if err != nil || off == nil {
		t.Fatalf("预置 checkpoint 失败: %v", err)
	}
	if off.Offset != 0 {
		t.Fatalf("新站点 checkpoint 应为 0，得到 %d", off.Offset)
	}
	if err := db.PrimeTrafficOffset("s1", "/other.log"); err != nil {
		t.Fatal(err)
	}
	off, _ = db.GetTrafficOffset("s1")
	if off.Path != "/x/access.log" {
		t.Fatalf("Prime 不应覆盖已有 checkpoint: %+v", off)
	}

	today := time.Now().Format("2006-01-02")
	if err := db.ApplyTrafficBatch(&model.TrafficBatch{
		Checkpoint: model.TrafficLogOffset{SiteID: "s1", Path: "/x/access.log", Offset: 10},
		Days: []*model.TrafficDay{{
			Stat: &model.TrafficStat{Date: today, SiteID: "s1", Requests: 1},
			IPs:  []string{"1.1.1.1"}, StatusCodes: map[int]int64{200: 1},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ResetTrafficStats(); err != nil {
		t.Fatal(err)
	}
	if stats, _ := db.GetTrafficStats("s1", 7); len(stats) != 0 {
		t.Fatalf("reset 后 stats 应为空，得到 %d", len(stats))
	}
	if off, _ := db.GetTrafficOffset("s1"); off != nil {
		t.Fatalf("reset 后 checkpoint 应为空，得到 %+v", off)
	}
}

func TestPruneTrafficExtra(t *testing.T) {
	db := openTrafficTestDB(t)
	old := "2000-01-01"
	if err := db.ApplyTrafficBatch(&model.TrafficBatch{
		Checkpoint: model.TrafficLogOffset{SiteID: "s1", Path: "/x", Offset: 1},
		Days: []*model.TrafficDay{{
			Stat:        &model.TrafficStat{Date: old, SiteID: "s1", Requests: 1},
			IPs:         []string{"1.1.1.1"},
			StatusCodes: map[int]int64{200: 1},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneTrafficExtra("2026-01-01"); err != nil {
		t.Fatal(err)
	}
	// 明细清掉，但日汇总保留。
	codes, _ := db.GetTrafficStatusSummary("s1", 36500)
	if len(codes) != 0 {
		t.Fatalf("过期状态码应被清理，得到 %+v", codes)
	}
	stats, _ := db.GetTrafficStats("s1", 36500)
	if len(stats) != 1 || stats[0].Requests != 1 {
		t.Fatalf("日汇总应保留，得到 %+v", stats)
	}
}
