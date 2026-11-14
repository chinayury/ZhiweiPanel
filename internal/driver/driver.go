// Package driver 定义运行时驱动抽象。
// 同一套业务逻辑既能跑在宿主机（systemd 管 nginx），也能跑在 Docker 里，
// 差异全部收敛在这个接口的实现里。
package driver

import (
	"context"
	"os"
)

// Runtime 运行时类型。
type Runtime string

const (
	RuntimeHost   Runtime = "host"
	RuntimeDocker Runtime = "docker"
)

// NginxStatus nginx 运行状态。
type NginxStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Version   string `json:"version"`
	ConfigOK  bool   `json:"config_ok"`
	Error     string `json:"error"`
}

// PHPVersion 一个可用 PHP 版本。
type PHPVersion struct {
	Version string `json:"version"`
	Socket  string `json:"socket"`
	Running bool   `json:"running"`
}

// DockerContainer 容器信息。
type DockerContainer struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	State    string `json:"state"`
	Ports    string `json:"ports"`
	Networks string `json:"networks"`
}

// DiskInfo 磁盘信息。
type DiskInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}

// SystemStatus 系统总览。
type SystemStatus struct {
	Nginx   NginxStatus  `json:"nginx"`
	PHP     []PHPVersion `json:"php"`
	Runtime Runtime      `json:"runtime"`
	Docker  DockerInfo   `json:"docker"`
	Disk    DiskInfo     `json:"disk"`
}

// DockerInfo docker 相关信息。
type DockerInfo struct {
	Available  bool              `json:"available"`
	Containers []DockerContainer `json:"containers"`
}

// Paths 面板使用的关键路径。
type Paths struct {
	// ConfDir 站点配置目录（http 级 include 位置）。
	ConfDir string
	// SiteConfDir 站点 vhost 目录。
	SiteConfDir string
	// CertDir 证书存储目录。
	CertDir string
	// LogDir 面板自身日志目录。
	LogDir string
	// DataDir 面板数据目录。
	DataDir string
}

// SiteConfPrefix 站点配置文件名前缀。
//
// 前缀是必须的：nginx 按文件名升序加载 conf.d 下的文件，
// log_format / limit_req_zone 等 http 级指令必须先于站点配置加载，
// 否则站点配置引用它们时会报 unknown log format。
// 因此 http 级配置用 00-，站点配置用 10-site-。
//
// 这个常量同时用于识别"conf.d 里哪些文件归面板管"，
// 以便安全清理已删除站点的残留配置（不碰手写的其他 .conf）。
const SiteConfPrefix = "10-site-"

// SiteConfName 返回站点配置文件名。
func SiteConfName(siteID string) string {
	return SiteConfPrefix + siteID + ".conf"
}

// Driver 运行时驱动接口。
type Driver interface {
	// Runtime 返回运行时类型。
	Runtime() Runtime

	// Paths 返回关键路径。
	Paths() Paths

	// NginxStatus 查询 nginx 状态。
	NginxStatus(ctx context.Context) (NginxStatus, error)

	// NginxTest 执行 nginx -t 配置检测。
	NginxTest(ctx context.Context) (bool, string, error)

	// Reload 重载 nginx 配置。
	Reload(ctx context.Context) error

	// WriteConf 写入站点配置（HTTP 级）。
	WriteConf(ctx context.Context, name, content string) error

	// RemoveConf 删除 http 级配置文件。
	// 独立于 WriteConf 存在，供回滚"原先并不存在的 http 配置"使用。
	RemoveConf(ctx context.Context, name string) error

	// WriteSiteConf 写入站点 vhost 配置。
	WriteSiteConf(ctx context.Context, siteID, content string) error

	// RemoveSiteConf 删除站点配置。
	RemoveSiteConf(ctx context.Context, siteID string) error

	// ReadConf 读取配置内容。
	ReadConf(ctx context.Context, name string) (string, error)

	// ListSiteConfs 列出已存在的站点配置文件。
	ListSiteConfs(ctx context.Context) ([]string, error)

	// PHPVersions 返回可用 PHP 版本。
	PHPVersions(ctx context.Context) ([]PHPVersion, error)

	// SystemStatus 返回系统总览。
	SystemStatus(ctx context.Context) (SystemStatus, error)
}

// WriteFileAtomic 原子写文件：先写临时文件再 rename，避免 nginx 读到半截配置。
func WriteFileAtomic(path, content string, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
