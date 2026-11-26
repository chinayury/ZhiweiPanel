//go:build unix

package logmgr

import (
	"os"
	"syscall"
)

// fileIdentity 返回文件系统身份（dev/inode）。
//
// Linux/Unix 上用于识别日志轮转：inode 变化说明 access.log 被替换，
// 旧 checkpoint 的 offset 对新文件没有意义。
func fileIdentity(info os.FileInfo) (int64, int64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return int64(st.Dev), int64(st.Ino)
}
