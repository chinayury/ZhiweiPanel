package model

import "time"

// TrafficLogOffset 访问日志增量采集 checkpoint。
//
// SiteID 是主键；Path 记录上次采集的文件路径（站点根目录变更时
// 路径不一致，采集器会从新文件末尾重新开始，避免把旧文件重复计入）。
// Dev/Ino 是文件系统身份（Linux 有效）；Offset 是"已成功落库到哪个字节"。
type TrafficLogOffset struct {
	SiteID string
	Path   string
	Dev    int64
	Ino    int64
	Offset int64
	// Sample 是 offset 之前最后 64 字节的 SHA-256（hex）。
	// 用于识别"文件被截断后又长回原大小"（copytruncate 的固有窗口）：
	// 仅比较 inode 与 size 会漏掉这种情况，导致漏读新日志。
	Sample    string
	UpdatedAt time.Time
}

// TrafficDay 一天的增量聚合结果。
//
// IPs 是该日出现过的去重 IP（落 traffic_ips 后由 store 统计真 UV）；
// StatusCodes 是精确状态码 → 次数。
type TrafficDay struct {
	Stat        *TrafficStat
	IPs         []string
	StatusCodes map[int]int64
}

// TrafficBatch 一次采集的原子落库单元。
//
// "统计 + IP 去重 + 状态码 + checkpoint"必须同事务提交：
// 否则中途失败会留下"统计已加、checkpoint 没推进"，下一轮重复累加。
type TrafficBatch struct {
	Checkpoint TrafficLogOffset
	Days       []*TrafficDay
	// StartedAtEnd 表示本次是首次采集、从文件末尾开始（历史日志不重算）。
	StartedAtEnd bool
}

// TrafficStatusCount 状态码汇总（给统计 API 用）。
type TrafficStatusCount struct {
	Code  int   `json:"code"`
	Count int64 `json:"count"`
}
