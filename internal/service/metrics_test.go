package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
	"github.com/zhiwei/panel/internal/sysinfo"
)

// 指标持久化（v0.7.0）服务层测试：
// 采集器未就绪时静默跳过、后台循环真的写库、历史区间钳位。

// newMetricsTestService 构造带真实 SQLite 与采集器的 Service。
func newMetricsTestService(t *testing.T) (*Service, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Service{
		db:      db,
		dataDir: dir,
		metrics: sysinfo.NewCollector(dir),
	}, db
}

// TestMetricsRecorderNoDBNoPanic db 为 nil（部分单测直接构造 Service）时
// 必须静默不启动，绝不能 panic。
func TestMetricsRecorderNoDBNoPanic(t *testing.T) {
	s := &Service{metrics: sysinfo.NewCollector(t.TempDir())}
	s.StartMetricsRecorder(context.Background()) // 不应 panic、应立即返回
}

// TestMetricsRecorderWritesPersistedSamples 落库循环真的把采样写进 SQLite，
// 且重启（新 Service 实例 + 同一个库）后还能读出来——这是持久化的核心承诺。
func TestMetricsRecorderWritesPersistedSamples(t *testing.T) {
	s, db := newMetricsTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 采集器先跑起来（首轮采样立即发生，但在后台 goroutine 里）
	s.StartMetricsCollector(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !s.Metrics().Timestamp.IsZero() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s.Metrics().Timestamp.IsZero() {
		t.Fatal("采集器 3 秒内没有产出样本")
	}

	// 用短间隔驱动落库循环（1 分钟的真实间隔不适合放进单测）
	s.startMetricsRecorder(ctx, 20*time.Millisecond)

	var got []model.MetricsSample
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := db.ListMetricsSamples(0, 0)
		if err != nil {
			t.Fatalf("ListMetricsSamples: %v", err)
		}
		if len(rows) >= 1 {
			got = rows
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(got) < 1 {
		t.Fatal("3 秒内未落库任何采样")
	}
	// 分钟对齐：ts 必须是 60 的倍数（同分钟重复采样会被主键覆盖，行数不涨）
	for _, m := range got {
		if m.TS%60 != 0 {
			t.Errorf("ts %d 不是分钟对齐", m.TS)
		}
	}

	// 新 Service（模拟重启）+ 同一个库 → 历史还在
	s2 := &Service{db: db, dataDir: "x", metrics: sysinfo.NewCollector(t.TempDir())}
	rows, err := s2.MetricsPersistedHistory(24)
	if err != nil {
		t.Fatalf("重启后读取历史: %v", err)
	}
	if len(rows) < 1 {
		t.Fatal("重启后历史应仍在")
	}
}

// TestMetricsHistoryClamp hours 钳位：<=0 按最小 1 小时、超出保留期按上限，
// 落在窗口外的采样无论怎么请求都拿不到。
func TestMetricsHistoryClamp(t *testing.T) {
	s, db := newMetricsTestService(t)

	now := time.Now().Unix()
	// 100 小时前的采样：超出 72h 保留期之外，任何请求都取不到
	if err := db.SaveMetricsSample(model.MetricsSample{TS: now - 100*3600, CPU: 111}); err != nil {
		t.Fatal(err)
	}
	// 窗口内的采样
	if err := db.SaveMetricsSample(model.MetricsSample{TS: now - 600, CPU: 22.5}); err != nil {
		t.Fatal(err)
	}

	// hours=0 → 视为 1 小时 → 只有一条；hours=1000 → 钳到 72 → 同样一条
	for _, hours := range []int{0, -5, 1, 24, 1000} {
		rows, err := s.MetricsPersistedHistory(hours)
		if err != nil {
			t.Fatalf("MetricsHistory(%d): %v", hours, err)
		}
		if len(rows) != 1 || rows[0].CPU != 22.5 {
			t.Errorf("hours=%d 应恰好返回窗口内那条，实际 %+v", hours, rows)
		}
	}
}
