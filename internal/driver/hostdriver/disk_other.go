//go:build !linux

package hostdriver

import "github.com/zhiwei/panel/internal/driver"

// diskUsageOf 非 Linux 平台不支持 statfs，返回空值。
// 面板的正式运行目标是 Linux，Windows 仅用于开发期编译校验。
func diskUsageOf(string) driver.DiskInfo { return driver.DiskInfo{} }
