package model

// DatabaseOverview 是「数据库中心」列表项。
//
// 只包含可安全展示的字段；密码永远不出现在这里（重置时才一次性回显）。
type DatabaseOverview struct {
	Name      string `json:"name"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
	Tables    int64  `json:"tables"`
	SizeBytes int64  `json:"size_bytes"`

	// 关联站点（库不是由建站产生时为 0 值，例如历史遗留/手工库）。
	SiteID   string `json:"site_id,omitempty"`
	SiteName string `json:"site_name,omitempty"`

	// 连接信息（面板命名规则：用户与库同名，host 固定 localhost）。
	User string `json:"user"`
	Host string `json:"host"`
	Port int    `json:"port"`
}
