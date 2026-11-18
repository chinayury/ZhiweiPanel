package service

import (
	"path/filepath"
	"testing"
)

// TestSetBackupDir 锁死 -backup-dir 的三段语义（README 已知限制 #14 曾长期挂账）：
//
//  1. 默认（flag 空）= <data>/backup——老部署的备份记录都指向那里，
//     改默认值会让旧记录的下载/恢复失效，绝不能动；
//  2. 显式设置 → BackupRoot 返回注入值（备份落盘/孤儿清理随之迁移）；
//  3. 再设空值 → 不覆盖已设置的值（main.go 的 SetBackupDir 只在非空时生效）。
func TestSetBackupDir(t *testing.T) {
	s := newDriftTestService(t)

	if got, want := s.BackupRoot(), filepath.Join(s.DataDir(), "backup"); got != want {
		t.Errorf("默认备份根目录应为 %s，实际 %s", want, got)
	}

	custom := filepath.Join(t.TempDir(), "custom-backup-root")
	s.SetBackupDir(custom)
	if got := s.BackupRoot(); got != custom {
		t.Errorf("设置后备份根目录应为 %s，实际 %s", custom, got)
	}

	s.SetBackupDir("")
	if got := s.BackupRoot(); got != custom {
		t.Errorf("空值不应覆盖已设置的备份根目录，实际 %s", got)
	}
}
