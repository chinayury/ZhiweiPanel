//go:build !unix

package logmgr

import "os"

// fileIdentity 在非 Unix 平台（Windows 等）不可用。
//
// 返回 0,0，采集器退化为"文件变小 = 截断" + "offset 前 64 字节采样"
// 两种判定；这两个判定已经能覆盖 copytruncate 与替换场景的绝大多数情况。
func fileIdentity(info os.FileInfo) (int64, int64) {
	return 0, 0
}
