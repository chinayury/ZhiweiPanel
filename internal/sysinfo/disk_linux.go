//go:build linux

package sysinfo

import "syscall"

// readDiskUsage 用 Statfs 统计挂载点用量。
//
// used 用 (Blocks-Bfree) 而不是 (Blocks-Bavail)：Bavail 是留给普通用户
// 的量（扣掉 root 保留块），拿它算 "已用" 会让 100% 的盘看起来还有 5%。
//
// Statfs 是纯 Go（golang.org/x/sys / syscall），不需要 cgo，
// 因此 CGO_ENABLED=0 的单文件二进制照样能编出来。
func readDiskUsage(path string) (total, used uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bsize := uint64(st.Bsize)
	total = st.Blocks * bsize
	free := st.Bfree * bsize
	if total == 0 {
		return 0, 0, false
	}
	used = total - free
	return total, used, true
}

// FreeDiskBytes 返回指定路径上当前用户可用的字节数（用于备份前判空间）。
func FreeDiskBytes(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}
