// Package sysinfo 采集宿主机运行指标。
//
// 设计取舍（借鉴 1Panel / cenvero-fleet 的监控实现后做的调整）：
//
//  1. 采样与查询分离。1Panel 用 cron 定时把指标写进独立的 MonitorDB，
//     查询时再从库里按时间区间取。对 ZhiweiPanel 来说过重——面板本来
//     就有 SQLite 主库，再开一个库要多一套连接管理。
//     这里改成"后台定时采样 + 内存缓存最新快照"，查询是 O(1) 且零 IO。
//
//  2. CPU 占用率用「两次采样的 jiffies 差值」算，不用阻塞式采样。
//     gopsutil 的 cpu.Percent(3*time.Second) 会**卡住调用方 3 秒**。
//     1Panel 把它放在定时任务里所以能忍；ZhiweiPanel 要把它放在 HTTP
//     处理器里就绝对不行——否则每次开面板都要等 3 秒。
//     delta 法第一次采样没有基线，返回 0；之后每轮都有值。
//
//  3. 纯 Go 实现，不引入 gopsutil。gopsutil 会拖进一串依赖
//     （net/ebpf/cilium/...），而 ZhiweiPanel 的硬约束是
//     CGO_ENABLED=0 的单文件二进制，依赖越少越可控。
//     Linux 上需要的 CPU/内存/负载/网络全在 /proc 里，直接读最省。
//
//  4. 磁盘占用用 syscall.Statfs，不用 df 或 gopsutil。
//     syscall.Statfs 是纯 Go 的（golang.org/x/sys），无 cgo。
package sysinfo

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Snapshot 是一次采样得到的宿主机指标快照。
type Snapshot struct {
	Timestamp time.Time `json:"timestamp"`

	// CPU
	CPUPercent float64 `json:"cpu_percent"`
	CPUCount   int     `json:"cpu_count"`

	// 负载（1/5/15 分钟平均），需要按 CPU 核数归一才有可比性
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	// 内存。Used 采用 Linux 现代口径 Total-Available，
	// 而不是 Total-Free（后者会把 page cache 算成已用，虚高）。
	MemTotal   uint64  `json:"mem_total"`
	MemUsed    uint64  `json:"mem_used"`
	MemPercent float64 `json:"mem_percent"`

	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`

	// 磁盘
	DiskPath    string  `json:"disk_path"`
	DiskTotal   uint64  `json:"disk_total"`
	DiskUsed    uint64  `json:"disk_used"`
	DiskPercent float64 `json:"disk_percent"`

	// 网络速率（字节/秒），由两次采样的累计字节差算出
	NetRxBps float64 `json:"net_rx_bps"`
	NetTxBps float64 `json:"net_tx_bps"`

	UptimeSeconds int64  `json:"uptime_seconds"`
	ProcessCount  int    `json:"process_count"`
	Hostname      string `json:"hostname"`
}

// cpuTimes 是 /proc/stat 里的一行累计 CPU 时间（单位 jiffy）。
type cpuTimes struct {
	total uint64
	idle  uint64
}

// netCounters 是 /proc/net/dev 的累计收发字节。
type netCounters struct {
	rx uint64
	tx uint64
}

// Collector 采集器。
//
// 只有一个后台 goroutine 写缓存，读侧走 RWMutex。
// 首轮采样后 CPU/网络速率必然是 0——没有基线就没有差值。
// 这是刻意的：宁可显示 0，也不要为了拿一个数阻塞 3 秒。
type Collector struct {
	mu    sync.RWMutex
	last  Snapshot
	ready bool

	// history 是最近若干次采样的环形缓冲。
	//
	// 为什么要留历史但不落库：1Panel 把监控数据写进独立的 MonitorDB，
	// 还要额外做保留天数清理。ZhiweiPanel 的监控只是给人看个当前值和
	// 一小段趋势曲线，落库会带来三个问题：主库膨胀、备份变慢、
	// 还要再写一套清理逻辑。内存环形缓冲把这三个问题全免了——
	// 代价是面板重启后曲线从头开始，这对"看最近半小时"的用途完全够。
	history    []Snapshot
	historyMax int

	diskPath string
	hostname string

	prevCPU cpuTimes
	prevNet netCounters
	prevAt  time.Time
}

// NewCollector 创建采集器。diskPath 指定统计哪个挂载点的磁盘用量，
// 留空则用根分区（面板自身的数据目录通常也在根分区上）。
//
// historyMax 是保留的历史采样条数（默认 120）。配合 10 秒的采样间隔，
// 相当于保留最近 20 分钟的趋势。
func NewCollector(diskPath string) *Collector {
	if diskPath == "" {
		diskPath = "/"
	}
	host, _ := os.Hostname()
	return &Collector{
		diskPath:   diskPath,
		hostname:   host,
		historyMax: 120,
		history:    make([]Snapshot, 0, 120),
	}
}

// Start 启动后台采样循环，直到 ctx 被取消。
//
// interval 建议 5~15 秒。太短会让采样本身产生可观的 CPU 开销，
// 也会把 /proc/stat 的 jiffies 差值压到噪声范围内。
func (c *Collector) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	// 先立刻采一次，让 API 第一次调用就有数据（CPU/网络速率此时为 0）
	c.sample()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sample()
		}
	}
}

// Snapshot 返回最近一次采样的结果。尚未采到样时返回零值。
func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last
}

// History 返回最近的历史采样（旧的在前），最多 historyMax 条。
func (c *Collector) History() []Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Snapshot, 0, len(c.history))
	out = append(out, c.history...)
	return out
}

// sample 采一轮并更新缓存。
func (c *Collector) sample() {
	now := time.Now()

	cpu, cpuOK := readCPUTimes()
	mem := readMemInfo()
	load1, load5, load15 := readLoadAvg()
	net := readNetCounters()
	uptime := readUptime()
	procs := countProcesses()

	s := Snapshot{
		Timestamp:     now,
		CPUCount:      runtime.NumCPU(),
		Load1:         load1,
		Load5:         load5,
		Load15:        load15,
		MemTotal:      mem.total,
		MemUsed:       mem.used(),
		MemPercent:    percent(mem.used(), mem.total),
		SwapTotal:     mem.swapTotal,
		SwapUsed:      mem.swapUsed(),
		UptimeSeconds: uptime,
		ProcessCount:  procs,
		Hostname:      c.hostname,
	}

	// CPU 占用率：本次与上次的 jiffies 差值
	if cpuOK {
		s.CPUPercent = cpuUsagePercent(c.prevCPU, cpu)
	}
	c.prevCPU = cpu

	// 网络速率：本次与上次的累计字节差 / 时间差
	if c.prevAt.IsZero() {
		c.prevNet = net
		c.prevAt = now
	} else {
		if dt := now.Sub(c.prevAt).Seconds(); dt > 0 {
			if net.rx >= c.prevNet.rx {
				s.NetRxBps = float64(net.rx-c.prevNet.rx) / dt
			}
			if net.tx >= c.prevNet.tx {
				s.NetTxBps = float64(net.tx-c.prevNet.tx) / dt
			}
		}
		c.prevNet = net
		c.prevAt = now
	}

	// 磁盘
	if total, used, ok := readDiskUsage(c.diskPath); ok {
		s.DiskPath = c.diskPath
		s.DiskTotal = total
		s.DiskUsed = used
		s.DiskPercent = percent(used, total)
	}

	c.mu.Lock()
	c.last = s
	c.ready = true
	// 环形缓冲：满了就丢最旧的一条，保持"旧 → 新"的顺序。
	// 用滑动而不是"按 idx 覆盖"——后者会让数组顺序错乱，
	// 画出来的曲线会是乱的。n=120 时每次拷贝成本可忽略。
	if c.historyMax > 0 {
		if len(c.history) >= c.historyMax {
			copy(c.history, c.history[1:])
			c.history = c.history[:len(c.history)-1]
		}
		c.history = append(c.history, s)
	}
	c.mu.Unlock()
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// readCPUTimes 解析 /proc/stat 的 aggregate "cpu" 行。
//
// 格式：cpu  user nice system idle iowait irq softirq steal guest guest_nice
// idle = idle + iowait（iowait 期间 CPU 空闲）。
func readCPUTimes() (cpuTimes, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, false
	}
	defer f.Close()
	return parseCPUTimes(f)
}

// parseCPUTimes 是 readCPUTimes 的可注入版本，便于单测。
func parseCPUTimes(r io.Reader) (cpuTimes, bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return cpuTimes{}, false
		}
		var vals []uint64
		for _, f := range fields[1:] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			vals = append(vals, v)
		}
		var total uint64
		for _, v := range vals {
			total += v
		}
		// vals[3]=idle, vals[4]=iowait
		idle := vals[3]
		if len(vals) > 4 {
			idle += vals[4]
		}
		return cpuTimes{total: total, idle: idle}, true
	}
	return cpuTimes{}, false
}

// cpuUsagePercent 由两次 CPU 采样算出占用率。
//
// 第一次采样（没有基线）返回 0 —— 没有差值就没有百分比，
// 宁可显示 0 也不要编一个数出来。
func cpuUsagePercent(prev, cur cpuTimes) float64 {
	if prev.total == 0 || cur.total <= prev.total {
		return 0
	}
	dTotal := cur.total - prev.total
	dIdle := cur.idle - prev.idle
	if dIdle > dTotal {
		return 0
	}
	return float64(dTotal-dIdle) / float64(dTotal) * 100
}

type memInfo struct {
	total     uint64
	available uint64
	hasAvail  bool
	free      uint64
	buffers   uint64
	cached    uint64
	swapTotal uint64
	swapFree  uint64
}

// used 返回已用内存字节。
//
// 口径：Total - Available。
// **不能用** Total - Free —— 那会把 page cache 算成已用，
// 一台刚做完备份的机器会显示 90% 内存占用，实际上一分没多占。
//
// 老内核（< 3.14）没有 MemAvailable，退回 MemFree+Buffers+Cached 估算。
// 如果连这些都读不到，返回 0 表示"未知"，而不是谎报 100%。
func (m memInfo) used() uint64 {
	avail := m.available
	if !m.hasAvail {
		avail = m.free + m.buffers + m.cached
	}
	if avail == 0 || avail > m.total {
		return 0
	}
	return m.total - avail
}

// swapUsed 返回已用 swap 字节。
func (m memInfo) swapUsed() uint64 {
	if m.swapFree >= m.swapTotal {
		return 0
	}
	return m.swapTotal - m.swapFree
}

// readMemInfo 解析 /proc/meminfo。
func readMemInfo() memInfo {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return memInfo{}
	}
	defer f.Close()
	return parseMemInfo(f)
}

// parseMemInfo 是 readMemInfo 的可注入版本，便于单测。
func parseMemInfo(r io.Reader) memInfo {
	var m memInfo
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// /proc/meminfo 的单位是 kB
		switch key {
		case "MemTotal":
			m.total = v * 1024
		case "MemAvailable":
			m.available = v * 1024
			m.hasAvail = true
		case "MemFree":
			m.free = v * 1024
		case "Buffers":
			m.buffers = v * 1024
		case "Cached":
			m.cached = v * 1024
		case "SwapTotal":
			m.swapTotal = v * 1024
		case "SwapFree":
			m.swapFree = v * 1024
		}
	}
	return m
}

// readLoadAvg 解析 /proc/loadavg，返回 1/5/15 分钟平均负载。
func readLoadAvg() (float64, float64, float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	return parseLoadAvg(string(b))
}

// parseLoadAvg 是 readLoadAvg 的可注入版本，便于单测。
func parseLoadAvg(s string) (float64, float64, float64) {
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return 0, 0, 0
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return l1, l5, l15
}

// readNetCounters 汇总 /proc/net/dev 里所有非 lo 接口的收发字节。
func readNetCounters() netCounters {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return netCounters{}
	}
	defer f.Close()
	return parseNetCounters(f)
}

// parseNetCounters 是 readNetCounters 的可注入版本，便于单测。
func parseNetCounters(r io.Reader) netCounters {
	var n netCounters
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		// 回环接口的流量没有对外意义，排除掉。
		if name == "lo" || strings.HasPrefix(name, "lo:") {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(fields[0], 10, 64)
		tx, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 == nil {
			n.rx += rx
		}
		if err2 == nil {
			n.tx += tx
		}
	}
	return n
}

// readUptime 读取 /proc/uptime 第一段（秒）。
func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	return parseUptime(string(b))
}

// parseUptime 是 readUptime 的可注入版本，便于单测。
func parseUptime(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

// countProcesses 统计 /proc 下的数字目录（即进程数）。
func countProcesses() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err == nil {
			n++
		}
	}
	return n
}

// DiskUsageOf 供外部查询指定路径的磁盘用量（独立于采集器缓存）。
func DiskUsageOf(path string) (total, used uint64, ok bool) {
	return readDiskUsage(path)
}

// DataDirOf 从路径推断一个适合做磁盘统计的目录（取其挂载点）。
// 备份前判空间要用真实所在卷，而不是路径字符串的前缀。
func DataDirOf(path string) string {
	if path == "" {
		return "/"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
