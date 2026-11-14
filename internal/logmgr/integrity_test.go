package logmgr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFile 写测试文件并返回路径。
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileFingerprint(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.txt", "hello")

	fp1, err := FileFingerprint(p)
	if err != nil {
		t.Fatal(err)
	}
	if fp1.Path != p {
		t.Errorf("Path = %q, want %q", fp1.Path, p)
	}
	if fp1.Size != 5 {
		t.Errorf("Size = %d, want 5", fp1.Size)
	}
	if len(fp1.Hash) != 64 {
		t.Errorf("Hash 长度 = %d, want 64 (sha256 hex)", len(fp1.Hash))
	}

	// 内容不变 → 指纹不变
	fp2, _ := FileFingerprint(p)
	if fp1.Hash != fp2.Hash {
		t.Errorf("内容未变但指纹变化")
	}

	// 内容变化 → 指纹变化
	writeFile(t, dir, "a.txt", "hello world")
	fp3, _ := FileFingerprint(p)
	if fp1.Hash == fp3.Hash {
		t.Errorf("内容已变但指纹未变，防篡改会失效")
	}
}

// 防篡改核心：基线建立后检测到修改。
func TestDiffDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "index.php", "<?php // original")

	// 首次扫描（无基线）→ 全部视为新增
	diff, current := Diff(dir, []string{"index.php"}, nil)
	if len(diff.Added) != 1 {
		t.Errorf("首次扫描应报告 1 个新增，实际 %d", len(diff.Added))
	}
	if len(current) != 1 {
		t.Errorf("首次扫描应得到 1 个快照，实际 %d", len(current))
	}

	// 建立基线
	baseline := map[string]Fingerprint{}
	for _, fp := range current {
		baseline[fp.Path] = fp
	}

	// 内容未变 → 干净
	diff2, _ := Diff(dir, []string{"index.php"}, baseline)
	if !diff2.IsClean() {
		t.Errorf("内容未变应判定为干净，实际 modified=%v added=%v deleted=%v",
			diff2.Modified, diff2.Added, diff2.Deleted)
	}

	// 内容被篡改 → 必须检出
	writeFile(t, dir, "index.php", "<?php // hacked")
	diff3, _ := Diff(dir, []string{"index.php"}, baseline)
	if len(diff3.Modified) != 1 {
		t.Errorf("应检测到 1 个文件被修改，实际 %d: %v", len(diff3.Modified), diff3.Modified)
	}
	if diff3.Total() == 0 {
		t.Errorf("篡改后 Total() 应大于 0")
	}
}

// 文件被删除 → 检出 deleted。
func TestDiffDetectsDeletion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.php", "secret")

	_, current := Diff(dir, []string{"config.php"}, nil)
	baseline := map[string]Fingerprint{}
	for _, fp := range current {
		baseline[fp.Path] = fp
	}

	if err := os.Remove(filepath.Join(dir, "config.php")); err != nil {
		t.Fatal(err)
	}
	diff, _ := Diff(dir, []string{"config.php"}, baseline)
	if len(diff.Deleted) != 1 {
		t.Errorf("应检测到 1 个文件被删除，实际 %d", len(diff.Deleted))
	}
}

// 目录形式的目标应递归收集，但跳过缓存目录。
func TestSnapshotDirectorySkipsCache(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app/config.php", "a")
	writeFile(t, dir, "app/index.php", "b")
	writeFile(t, dir, "app/cache/tmp1.php", "should be skipped")
	writeFile(t, dir, "app/vendor/lib.php", "should be skipped")

	files := snapshotFiles(dir, []string{"app"})
	if len(files) != 2 {
		t.Errorf("应只收集 2 个文件，实际 %d: %v", len(files), files)
	}
	for p := range files {
		if contains(p, "cache") || contains(p, "vendor") {
			t.Errorf("不应收集缓存/依赖目录: %s", p)
		}
	}
}

func TestRestoreFromBackup(t *testing.T) {
	// 备份目录与站点目录必须分开，否则备份文件本身会被后续写入覆盖
	base := t.TempDir()
	siteDir := filepath.Join(base, "site")
	backup := filepath.Join(base, "backup")
	target := filepath.Join(siteDir, "index.php")

	writeFile(t, siteDir, "index.php", "original content")
	if err := BackupTo(target, backup); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 篡改
	writeFile(t, siteDir, "index.php", "hacked")

	// 恢复
	if err := RestoreFromBackup(backup, target); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "original content" {
		t.Errorf("恢复后内容 = %q, want %q", string(data), "original content")
	}
}

func TestPurgeOlderThan(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.log")
	recent := filepath.Join(dir, "recent.log")
	writeFile(t, dir, "old.log", "x")
	writeFile(t, dir, "recent.log", "y")

	past := time.Now().AddDate(0, 0, -60)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	n, err := PurgeOlderThan(dir, 30)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("应清理 1 个过期日志，实际 %d", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("过期日志未被清理")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("新日志被误删")
	}
}
