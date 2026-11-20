package model

// MetricsSample 是一条**持久化**的宿主机指标采样（分钟粒度）。
//
// 与 sysinfo.Snapshot 的关系：Snapshot 是采集器的完整内存快照
// （十秒级、字段全、重启即丢）；MetricsSample 是从中降采样落库的
// 趋势数据（分钟级、只留画曲线需要的字段、重启不丢）。
// 故意不把 Snapshot 整个序列化塞进一行——监控历史只是给人看趋势，
// 存 7 个数字足够，多余字段只会让表膨胀、让备份变慢。
type MetricsSample struct {
	// TS 采样时间（Unix 秒，分钟对齐）。主键：同一分钟重复写入覆盖。
	TS int64 `json:"ts"`
	// CPU CPU 占用率（0~100）。
	CPU float64 `json:"cpu"`
	// Mem 内存占用率（0~100，口径 Total-Available）。
	Mem float64 `json:"mem_percent"`
	// Load1 1 分钟平均负载（未按核数归一，前端归一）。
	Load1 float64 `json:"load1"`
	// Disk 磁盘占用率（0~100，统计面板数据目录所在卷）。
	Disk float64 `json:"disk_percent"`
	// NetRx 网络接收速率（字节/秒）。
	NetRx float64 `json:"net_rx_bps"`
	// NetTx 网络发送速率（字节/秒）。
	NetTx float64 `json:"net_tx_bps"`
}
