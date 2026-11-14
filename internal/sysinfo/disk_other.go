//go:build !linux

package sysinfo

// readDiskUsage 非 Linux 平台没有 statfs，返回空值。
//
// 面板的正式运行目标是 Linux；Windows 仅用于开发期的编译校验与单测，
// 所以这里给一个安全降级而不是让整个包编不过。
func readDiskUsage(string) (total, used uint64, ok bool) {
	return 0, 0, false
}

// FreeDiskBytes 非 Linux 平台不支持，返回 false。
func FreeDiskBytes(string) (uint64, bool) {
	return 0, false
}
