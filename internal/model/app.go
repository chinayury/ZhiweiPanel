package model

// AppStatus 软件商店里一个软件条目的安装状态。
type AppStatus string

const (
	// AppInstalled 已安装（探测到二进制/子系统且可用）。
	AppInstalled AppStatus = "installed"
	// AppAvailable 未安装，可安装。
	AppAvailable AppStatus = "available"
)

// App 软件商店中的一个软件条目。
//
// 与宝塔「软件商店」同型：区分「已安装了哪些」与「能安装哪些」。
// 本版本只做目录 + 已安装检测，不自动执行安装/卸载命令——安装动作
// 以 InstallHint 命令提示的形式给出，由运维在服务器上自行执行。
type App struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"` // web / runtime / database / cache / dev
	Description string    `json:"description"`
	Status      AppStatus `json:"status"`       // installed / available
	Version     string    `json:"version"`      // 已安装版本（未安装为空）
	InstallHint string    `json:"install_hint"` // 安装提示命令
}
