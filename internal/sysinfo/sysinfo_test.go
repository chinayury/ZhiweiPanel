package sysinfo

import (
	"runtime"
	"strings"
	"testing"
)

// TestParseCPUTimes 验证 /proc/stat 的 cpu 聚合行解析。
func TestParseCPUTimes(t *testing.T) {
	in := `cpu  100 20 30 400 10 0 5 0 0 0
cpu0 50 10 15 200 5 0 2 0 0 0
intr 12345
ctxt 6789
`
	got, ok := parseCPUTimes(strings.NewReader(in))
	if !ok {
		t.Fatal("expected parse success")
	}
	// total = 100+20+30+400+10+0+5 = 565
	if got.total != 565 {
		t.Errorf("total = %d, want 565", got.total)
	}
	// idle = idle(400) + iowait(10) = 410
	if got.idle != 410 {
		t.Errorf("idle = %d, want 410", got.idle)
	}
}

// TestParseCPUTimesNegative 非 cpu 行开头、字段不足、脏数据都必须安全返回。
func TestParseCPUTimesNegative(t *testing.T) {
	cases := []string{
		"",                // 空
		"intr 12345\n",    // 没有 cpu 行
		"cpu 100 20 30\n", // 字段不足
		"cpu a b c d e\n", // 非数字
	}
	for _, in := range cases {
		if _, ok := parseCPUTimes(strings.NewReader(in)); ok {
			t.Errorf("expected failure for input %q", in)
		}
	}
}

// TestCPUUsagePercent 验证占用率计算，含"首次采样无基线"的负例。
func TestCPUUsagePercent(t *testing.T) {
	cases := []struct {
		name string
		prev cpuTimes
		cur  cpuTimes
		want float64
	}{
		{"全空闲", cpuTimes{total: 100, idle: 90}, cpuTimes{total: 200, idle: 190}, 0},
		{"全忙", cpuTimes{total: 100, idle: 0}, cpuTimes{total: 200, idle: 0}, 100},
		{"一半忙", cpuTimes{total: 100, idle: 50}, cpuTimes{total: 200, idle: 100}, 50},
		{"首次采样无基线", cpuTimes{}, cpuTimes{total: 200, idle: 100}, 0},
		{"计数器回退", cpuTimes{total: 200, idle: 100}, cpuTimes{total: 100, idle: 50}, 0},
	}
	for _, c := range cases {
		got := cpuUsagePercent(c.prev, c.cur)
		if got != c.want {
			t.Errorf("%s: got %.2f, want %.2f", c.name, got, c.want)
		}
	}
}

// TestParseMemInfo 验证内存口径。
func TestParseMemInfo(t *testing.T) {
	in := `MemTotal:       16000 kB
MemFree:         2000 kB
MemAvailable:    8000 kB
Buffers:          500 kB
Cached:          6000 kB
SwapTotal:       4000 kB
SwapFree:        3000 kB
`
	m := parseMemInfo(strings.NewReader(in))
	if m.total != 16000*1024 {
		t.Errorf("total = %d", m.total)
	}
	// used 必须是 Total-Available（现代口径），不是 Total-Free
	if m.used() != 8000*1024 {
		t.Errorf("used = %d, want %d", m.used(), 8000*1024)
	}
	if m.swapUsed() != 1000*1024 {
		t.Errorf("swapUsed = %d, want %d", m.swapUsed(), 1000*1024)
	}
	if m.used() != 0 && m.used() > m.total {
		t.Errorf("used must not exceed total")
	}
}

// TestParseMemInfoNegative 缺 MemAvailable 时 used 退化为 0，而不是负数。
func TestParseMemInfoNegative(t *testing.T) {
	in := "MemTotal: 1000 kB\n"
	m := parseMemInfo(strings.NewReader(in))
	if m.used() != 0 {
		t.Errorf("used = %d, want 0 (no MemAvailable)", m.used())
	}
}

// TestParseMemInfoFallback 老内核（< 3.14）没有 MemAvailable，
// 退回到 MemFree+Buffers+Cached 估算，而不是直接谎报"内存用满"。
func TestParseMemInfoFallback(t *testing.T) {
	in := `MemTotal:       16000 kB
MemFree:         2000 kB
Buffers:          500 kB
Cached:          6000 kB
`
	m := parseMemInfo(strings.NewReader(in))
	if m.hasAvail {
		t.Error("hasAvail should be false when MemAvailable is absent")
	}
	// avail = 2000+500+6000 = 8500 → used = 16000-8500 = 7500 kB
	if want := uint64(7500 * 1024); m.used() != want {
		t.Errorf("used = %d, want %d", m.used(), want)
	}
}

// TestParseLoadAvg 验证负载解析。
func TestParseLoadAvg(t *testing.T) {
	l1, l5, l15 := parseLoadAvg("0.52 0.58 0.59 1/234 5678")
	if l1 != 0.52 || l5 != 0.58 || l15 != 0.59 {
		t.Errorf("got %v %v %v", l1, l5, l15)
	}
	// 脏数据必须返回 0 而不是 panic
	if a, b, c := parseLoadAvg("garbage"); a != 0 || b != 0 || c != 0 {
		t.Errorf("expected zeros for garbage, got %v %v %v", a, b, c)
	}
	if a, _, _ := parseLoadAvg(""); a != 0 {
		t.Errorf("expected 0 for empty")
	}
}

// TestParseNetCounters 验证网络计数器：跳过 lo，累加多接口。
func TestParseNetCounters(t *testing.T) {
	in := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 1000 10 0 0 0 0 0 0
  eth0: 5000 50 0 0 0 0 0 0 7000 70 0 0 0 0 0 0
  eth1:  500 5 0 0 0 0 0 0  600 6 0 0 0 0 0 0
`
	n := parseNetCounters(strings.NewReader(in))
	if n.rx != 5500 {
		t.Errorf("rx = %d, want 5500", n.rx)
	}
	if n.tx != 7600 {
		t.Errorf("tx = %d, want 7600", n.tx)
	}
}

// TestParseNetCountersNegative 字段不足的行必须跳过而不是误加。
func TestParseNetCountersNegative(t *testing.T) {
	in := `  eth0: 100 1
  eth1: 200 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16
`
	n := parseNetCounters(strings.NewReader(in))
	if n.rx != 200 || n.tx != 9 {
		t.Errorf("rx=%d tx=%d, want rx=200 tx=9", n.rx, n.tx)
	}
}

// TestParseUptime 验证运行时长解析。
func TestParseUptime(t *testing.T) {
	if got := parseUptime("123456.78 987654.32"); got != 123456 {
		t.Errorf("got %d, want 123456", got)
	}
	if got := parseUptime(""); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	if got := parseUptime("abc"); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

// TestCollectorSample 验证采集器能产出一份完整快照且字段自洽。
//
// 只在 Linux 上有意义（依赖 /proc）；其它平台跳过，
// 但解析逻辑仍由上面的纯函数用例覆盖。
func TestCollectorSample(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("需要 /proc，仅 Linux 可跑")
	}
	c := NewCollector("/")
	c.sample()
	s := c.Snapshot()
	if s.Timestamp.IsZero() {
		t.Error("timestamp not set")
	}
	if s.CPUCount <= 0 {
		t.Error("cpu count should be > 0")
	}
	if s.MemTotal == 0 {
		t.Error("mem total should be > 0 on Linux")
	}
	if s.MemUsed > s.MemTotal {
		t.Errorf("mem used %d > total %d", s.MemUsed, s.MemTotal)
	}
	if s.DiskTotal == 0 {
		t.Error("disk total should be > 0")
	}
	if s.DiskUsed > s.DiskTotal {
		t.Errorf("disk used %d > total %d", s.DiskUsed, s.DiskTotal)
	}
}

// TestCollectorHistoryRing 验证历史环形缓冲满了以后顺序不乱、长度不超上限。
func TestCollectorHistoryRing(t *testing.T) {
	c := NewCollector("/")
	c.historyMax = 3
	for i := 0; i < 10; i++ {
		c.sample()
	}
	h := c.History()
	if len(h) != 3 {
		t.Fatalf("history len = %d, want 3", len(h))
	}
	// 顺序必须是旧 → 新：时间戳递增
	for i := 1; i < len(h); i++ {
		if h[i].Timestamp.Before(h[i-1].Timestamp) {
			t.Fatalf("history not ordered at %d: %v then %v", i, h[i-1].Timestamp, h[i].Timestamp)
		}
	}
}
