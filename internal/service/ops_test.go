package service

import (
	"context"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/sysinfo"
)

// TestStartMetricsCollectorDoesNotBlock 回归测试，锁死一个真实的线上事故：
//
// v0.2.2 首次部署到 198.51.100.10 时，面板进程活着、systemd 显示 active、
// 日志停在"已保存配置快照"，但 ss 里 8887/8888 一个端口都没监听，
// 面板从外面完全打不开。
//
// 真因：StartMetricsCollector 同步调用了 sysinfo.Collector.Start ——
// 那是个死循环（和 http.Server.ListenAndServe 同型）。同步调用把 main()
// 卡死在那儿，HTTP 服务根本没机会启动。
//
// 这条测试要求 StartMetricsCollector **立刻返回**，采样循环放后台 goroutine。
func TestStartMetricsCollectorDoesNotBlock(t *testing.T) {
	s := &Service{metrics: sysinfo.NewCollector(t.TempDir())}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.StartMetricsCollector(ctx)
		close(done)
	}()

	select {
	case <-done:
		// 正常：立即返回
	case <-time.After(3 * time.Second):
		t.Fatal("StartMetricsCollector 阻塞了：采样循环是死循环，必须放后台 goroutine，" +
			"否则 main() 会被卡住，HTTP 服务永远起不来")
	}
}

// TestMetricsCollectorRunsInBackground 采集器确实在后台跑起来了，
// 且能产出样本（不只是"没卡住"）。
func TestMetricsCollectorRunsInBackground(t *testing.T) {
	s := &Service{metrics: sysinfo.NewCollector(t.TempDir())}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.StartMetricsCollector(ctx)

	// 首轮采样是同步发生的（Collector.Start 里先采一次再进循环），
	// 但它跑在后台 goroutine 里，所以这里给一点时间。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !s.Metrics().Timestamp.IsZero() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("后台采集器没有产出样本")
}

// TestMetricsSnapshotIsSafeBeforeFirstSample 未采样时查询不能 panic。
func TestMetricsSnapshotIsSafeBeforeFirstSample(t *testing.T) {
	s := &Service{metrics: sysinfo.NewCollector(t.TempDir())}
	snap := s.Metrics()
	if !snap.Timestamp.IsZero() {
		t.Error("未采样时不应有时间戳")
	}
	if len(s.MetricsHistory()) != 0 {
		t.Error("未采样时历史应为空")
	}
}
