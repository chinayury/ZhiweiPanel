package store

import (
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// 指标持久化存取测试：写入/区间读取/过期清理。

func TestMetricsSamplesRoundTrip(t *testing.T) {
	db := newTestDB(t)
	base := time.Now().Add(-3 * time.Hour).Unix()

	samples := []model.MetricsSample{
		{TS: base, CPU: 12.5, Mem: 40.1, Load1: 0.3, Disk: 21.7, NetRx: 1024, NetTx: 2048},
		{TS: base + 60, CPU: 33.3, Mem: 41.2, Load1: 0.5, Disk: 21.7, NetRx: 4096, NetTx: 8192},
		{TS: base + 120, CPU: 7.7, Mem: 39.9, Load1: 0.1, Disk: 21.8, NetRx: 128, NetTx: 256},
	}
	for _, m := range samples {
		if err := db.SaveMetricsSample(m); err != nil {
			t.Fatalf("SaveMetricsSample: %v", err)
		}
	}

	// 全量读取，顺序必须旧 → 新
	got, err := db.ListMetricsSamples(0, 0)
	if err != nil {
		t.Fatalf("ListMetricsSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].TS <= got[i-1].TS {
			t.Errorf("顺序错乱: %v", got)
		}
	}
	if got[0].CPU != 12.5 || got[1].NetRx != 4096 || got[2].Mem != 39.9 {
		t.Errorf("字段回读不对: %+v", got)
	}

	// 同一分钟重复写入 → 覆盖（幂等），不新增行
	over := model.MetricsSample{TS: base + 60, CPU: 99.9, Mem: 50, Load1: 2, Disk: 30, NetRx: 1, NetTx: 2}
	if err := db.SaveMetricsSample(over); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}
	got, _ = db.ListMetricsSamples(0, 0)
	if len(got) != 3 {
		t.Fatalf("重复写同一分钟应覆盖而非新增，实际 %d 条", len(got))
	}
	if got[1].CPU != 99.9 {
		t.Errorf("覆盖未生效: %+v", got[1])
	}

	// 区间查询：只取 (base, base+120) 之间的
	got, err = db.ListMetricsSamples(base+1, base+120)
	if err != nil {
		t.Fatalf("区间查询: %v", err)
	}
	if len(got) != 1 || got[0].TS != base+60 {
		t.Errorf("区间查询结果不对: %+v", got)
	}
}

func TestMetricsSamplesPrune(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().Unix()

	old := model.MetricsSample{TS: now - 8*24*3600, CPU: 1}
	recent := model.MetricsSample{TS: now - 60, CPU: 2}
	for _, m := range []model.MetricsSample{old, recent} {
		if err := db.SaveMetricsSample(m); err != nil {
			t.Fatalf("SaveMetricsSample: %v", err)
		}
	}

	n, err := db.PruneMetricsSamples(now - 72*3600)
	if err != nil {
		t.Fatalf("PruneMetricsSamples: %v", err)
	}
	if n != 1 {
		t.Fatalf("应删除 1 条过期数据，实际 %d", n)
	}

	got, _ := db.ListMetricsSamples(0, 0)
	if len(got) != 1 || got[0].TS != recent.TS {
		t.Errorf("清理后应只剩最近一条: %+v", got)
	}

	// 幂等：同样的保留期再清一次，应 (0, nil) 且不再删任何东西
	if n, err := db.PruneMetricsSamples(now - 72*3600); err != nil || n != 0 {
		t.Errorf("重复清理应 (0, nil)，实际 (%d, %v)", n, err)
	}
	got, _ = db.ListMetricsSamples(0, 0)
	if len(got) != 1 || got[0].TS != recent.TS {
		t.Errorf("幂等清理后样本不应被动: %+v", got)
	}
}
