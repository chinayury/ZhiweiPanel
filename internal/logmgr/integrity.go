package logmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fingerprint 文件指纹。
type Fingerprint struct {
	// Path 文件绝对路径。
	Path string
	Hash string
	Size int64
	Mode uint32
}

// FileFingerprint 计算单文件指纹。
func FileFingerprint(path string) (Fingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Fingerprint{}, err
	}
	if info.IsDir() {
		return Fingerprint{}, fmt.Errorf("%s 是目录", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return Fingerprint{}, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Fingerprint{}, err
	}
	return Fingerprint{
		Path: path,
		Hash: hex.EncodeToString(h.Sum(nil)),
		Size: info.Size(),
		Mode: uint32(info.Mode().Perm()),
	}, nil
}

// snapshotFiles 递归收集受保护文件。
func snapshotFiles(siteRoot string, targets []string) map[string]Fingerprint {
	out := map[string]Fingerprint{}
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		abs := t
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(siteRoot, t)
		}
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if fp, err := FileFingerprint(abs); err == nil {
				out[abs] = fp
			}
			continue
		}
		// 目录则递归收集（跳过缓存与运行时目录）
		_ = filepath.Walk(abs, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				base := fi.Name()
				if base == "cache" || base == "tmp" || base == ".git" || base == "node_modules" || base == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if fp, err := FileFingerprint(p); err == nil {
				out[p] = fp
			}
			return nil
		})
	}
	return out
}

// TamperDiff 篡改差异结果。
type TamperDiff struct {
	Modified []string
	Added    []string
	Deleted  []string
	// Current 当前快照，供建立基线使用。
	Current []Fingerprint
}

// IsClean 是否无篡改。
func (d TamperDiff) IsClean() bool {
	return len(d.Modified) == 0 && len(d.Added) == 0 && len(d.Deleted) == 0
}

// Total 篡改总数。
func (d TamperDiff) Total() int {
	return len(d.Modified) + len(d.Added) + len(d.Deleted)
}

// Diff 对比当前文件状态与基线。
// baseline 允许为 nil（首次建立基线，此时所有文件都算"新增"）。
func Diff(siteRoot string, targets []string, baseline map[string]Fingerprint) (TamperDiff, []Fingerprint) {
	diff := TamperDiff{}
	current := snapshotFiles(siteRoot, targets)

	for path, cur := range current {
		base, ok := baseline[path]
		if !ok {
			diff.Added = append(diff.Added, path)
			continue
		}
		if base.Hash != cur.Hash {
			diff.Modified = append(diff.Modified, path)
		}
	}
	for path := range baseline {
		if _, ok := current[path]; !ok {
			diff.Deleted = append(diff.Deleted, path)
		}
	}
	return diff, snapshotSlice(current)
}

// snapshotSlice 把指纹 map 转为切片。
func snapshotSlice(m map[string]Fingerprint) []Fingerprint {
	out := make([]Fingerprint, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// RestoreFromBackup 从备份目录恢复文件（防复改的兜底恢复）。
// 与 BackupTo 共用 backupKey，保证写入与读回的路径严格对应。
func RestoreFromBackup(backupRoot, path string) error {
	src := filepath.Join(backupRoot, filepath.FromSlash(backupKey(path)))
	dst := path

	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			// 备份中不存在，说明该文件本不该存在，删除之
			if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		return err
	}
	info, err := os.Stat(src)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

// BackupTo 将当前文件备份到 backupRoot 下。
//
// 路径映射规则：站点文件通常不在 backupRoot 之下（例如 /www/wwwroot/a.com/index.php
// 与 /opt/zhiweipanel/data/backups），此时 filepath.Rel 会返回 ../../../www/... 这种
// 带 ".." 的路径。拼接后虽然不报错，但会让备份散落到 backupRoot 之外，
// 且 RestoreFromBackup 读回时路径对不上，静默恢复失败。
// 因此统一用去掉根前缀后的绝对路径作为键，两端算法一致。
func BackupTo(src, backupRoot string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		_ = filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return nil
			}
			return copyInto(p, backupRoot, fi.Mode().Perm())
		})
		return nil
	}
	return copyInto(src, backupRoot, info.Mode().Perm())
}

// backupKey 计算源文件在备份目录中的相对键。
// 去掉盘符与前导分隔符，Windows / Linux 行为一致。
func backupKey(src string) string {
	p := filepath.ToSlash(src)
	p = strings.TrimPrefix(p, "//")
	if len(p) > 1 && p[1] == ':' {
		p = p[2:] // 去掉 Windows 盘符 "C:"
	}
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "./")
	return p
}

// copyInto 把单个文件复制到 backupRoot 下的镜像路径。
func copyInto(src, backupRoot string, mode os.FileMode) error {
	dst := filepath.Join(backupRoot, filepath.FromSlash(backupKey(src)))

	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

// sanitizePath 把绝对路径转成安全的相对形式。
// PurgeOlderThan 清理超过保留期的日志文件。
func PurgeOlderThan(dir string, days int) (int, error) {
	if days <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil
	}
	deadline := time.Now().AddDate(0, 0, -days)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(deadline) {
			if os.Remove(filepath.Join(dir, e.Name())) == nil {
				removed++
			}
		}
	}
	return removed, nil
}
