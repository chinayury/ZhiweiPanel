//go:build linux

package hostdriver

import (
	"syscall"

	"github.com/zhiwei/panel/internal/driver"
)

// diskUsageOf 通过 statfs 读取磁盘占用。
func diskUsageOf(path string) driver.DiskInfo {
	var du driver.DiskInfo
	if path == "" {
		return du
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return du
	}
	du.Total = st.Blocks * uint64(st.Bsize)
	du.Free = st.Bavail * uint64(st.Bsize)
	du.Used = du.Total - du.Free
	return du
}
