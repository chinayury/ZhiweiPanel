package service

import (
	"context"
	"log"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// ---------------------------------------------------------------------------
// 指标持久化（v0.7.0）
//
// 采集器（sysinfo.Collector）的十秒级历史只在内存里，重启即丢。
// 本组件每分钟从采集器取一次最新快照，降采样成一行落 SQLite，
// 让仪表盘的"近 24 小时"曲线跨重启存在。
//
// 为什么独立一条 1 分钟节拍而不是让采集器顺手写库：
//   - 采集器在 internal/sysinfo，不知道 DB 的存在（分层干净）；
//   - 1 分钟粒度是刻意的：十秒级落库一天 8640 行，把主库和备份都拖累；
//     分钟级 1440 行/天 × 72 小时保留 ≈ 4300 行上限，量级可忽略。
//   - "看毛刺"由内存环形缓冲负责（实时曲线），"看趋势跨重启"由本表负责，
//     两条数据链路各管一件事。
// ---------------------------------------------------------------------------

// metricsKeepHours 持久化采样的保留时长（小时）。
// 72 小时 = 三天，覆盖"重启后还能看昨天"的用途且表极小。
const metricsKeepHours = 72

// StartMetricsRecorder 启动后台指标落库循环。立即返回（循环放 goroutine），
// 与 StartMetricsCollector 同型——main() 绝不能被阻塞。
// db 为 nil（部分单测直接构造 Service）时静默不启动。
func (s *Service) StartMetricsRecorder(ctx context.Context) {
	s.startMetricsRecorder(ctx, time.Minute)
}

// startMetricsRecorder 是可注入间隔的内部版本（单测用）。
func (s *Service) startMetricsRecorder(ctx context.Context, interval time.Duration) {
	if s.db == nil || interval <= 0 {
		return
	}
	go func() {
		s.pruneMetrics()
		if interval >= time.Minute {
			// 启动后先立刻落一条，让"升级完马上看"就有数据点。
			s.recordMetricsSample()
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				i++
				s.recordMetricsSample()
				// 每小时清一次过期数据（保留期 > 1h，清理频率 1h 足够）。
				if i%60 == 0 {
					s.pruneMetrics()
				}
			}
		}
	}()
}

// recordMetricsSample 取最新快照，按分钟对齐落一行。
//
// 失败只记日志——监控是辅助设施，不能因为它打扰主流程。
// 未采样过（Timestamp 为零）时跳过：没数据就别写一行 0 值占位。
func (s *Service) recordMetricsSample() {
	snap := s.metrics.Snapshot()
	if snap.Timestamp.IsZero() {
		return
	}
	m := model.MetricsSample{
		TS:    snap.Timestamp.Unix() / 60 * 60,
		CPU:   snap.CPUPercent,
		Mem:   snap.MemPercent,
		Load1: snap.Load1,
		Disk:  snap.DiskPercent,
		NetRx: snap.NetRxBps,
		NetTx: snap.NetTxBps,
	}
	if err := s.db.SaveMetricsSample(m); err != nil {
		log.Printf("[metrics] 保存指标采样失败: %v", err)
	}
}

// pruneMetrics 删除保留期之前的过期采样。
func (s *Service) pruneMetrics() {
	keepBefore := time.Now().Add(-metricsKeepHours * time.Hour).Unix()
	if n, err := s.db.PruneMetricsSamples(keepBefore); err != nil {
		log.Printf("[metrics] 清理过期指标失败: %v", err)
	} else if n > 0 {
		log.Printf("[metrics] 已清理过期指标采样 %d 条", n)
	}
}

// MetricsPersistedHistory 返回最近 hours 小时的**持久化**采样（旧 → 新）。
//
// 命名刻意与 MetricsHistory（内存十秒级环形缓冲）区分开：
// 两个"历史"是两条数据链路，前者看实时毛刺，本方法看跨重启趋势。
// hours 钳位到 [1, metricsKeepHours]：超出保留期的数据根本不存在，
// 不如把请求压回有效区间。
func (s *Service) MetricsPersistedHistory(hours int) ([]model.MetricsSample, error) {
	if hours < 1 {
		hours = 1
	}
	if hours > metricsKeepHours {
		hours = metricsKeepHours
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	return s.db.ListMetricsSamples(since, 0)
}
