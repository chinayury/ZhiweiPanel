// Package backup 提供网站/数据库的备份与恢复。
//
// v0.2.0 设计（参考宝塔面板"备份网站/备份数据库"任务）：
//   - 站点备份 = tar.gz 站点根目录 + 渲染好的 nginx conf 副本
//   - 数据库备份 = mysqldump --single-transaction + gzip
//   - 文件落地到 ${DATA_DIR}/backup/{site,database}/<id>.tar.gz
//   - 保留份数由 cron 任务 payload.retain 控制，超出删除
//   - 单文件最大 100MB（防爆盘；超出则报错让用户处理）
//
// 不做（v0.2.0 范围外，留 v0.2.1）：
//   - OSS/COS/七牛云远程存储
//   - zstd + 密码加密
//   - 备份完整性自检（CRC）
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/service"
)

// MaxBackupSize 单份备份最大字节数（100MB）。
//
// 防止"日志站点跑了一周日志 50GB 备份直接撑爆磁盘"这种事故。
// 超出报 ErrTooLarge，调用方转告用户"请先清理站点日志再备份"。
const MaxBackupSize = 100 * 1024 * 1024

// ErrTooLarge 备份超出 100MB 限制。
var ErrTooLarge = errors.New("备份内容超过 100MB 上限，请先清理大文件/日志")

// BackupSite 备份整个站点（目录 + 渲染的 conf）。
//
// 流程：
//  1. 在 ${DATA_DIR}/backup/site/ 下创建临时目录 + tar.gz
//  2. 把站点根目录 tar 进去（排除 logs 防止巨量访问日志）
//  3. 把渲染好的 nginx conf 副本写入 tar.gz 根目录的 nginx.conf
//  4. close gzip，原子 rename 为 .tar.gz
//  5. 查文件大小：>100MB 直接删掉并返回 ErrTooLarge
//  6. 返回 *model.Backup 让调用方写 DB
func BackupSite(ctx context.Context, svc *service.Service, site *model.Site) (*model.Backup, error) {
	if site.Root.Path == "" {
		return nil, fmt.Errorf("站点根目录为空")
	}
	if _, err := os.Stat(site.Root.Path); err != nil {
		return nil, fmt.Errorf("站点根目录不存在: %w", err)
	}

	dataDir := svc.DataDir()
	outDir := filepath.Join(dataDir, "backup", "site")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, err
	}
	id := newID()
	tmpPath := filepath.Join(outDir, id+".tar.gz.partial")
	finalPath := filepath.Join(outDir, id+".tar.gz")

	f, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	if err := backupSiteTarGz(ctx, f, site, svc); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	// 查大小
	st, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if st.Size() > MaxBackupSize {
		_ = os.Remove(tmpPath)
		return nil, ErrTooLarge
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	return &model.Backup{
		ID:         id,
		Kind:       model.BackupSite,
		Target:     site.ID,
		TargetName: site.Name,
		Size:       st.Size(),
		Path:       finalPath,
	}, nil
}

// backupSiteTarGz 实际写 tar.gz。
//
// 站点根目录的 logs 子目录会被跳过（access.log 经常几十 MB，
// 备份价值低反而让备份包迅速膨胀）。要备份日志请用单独的
// "备份目录"任务（v0.2.0 暂未实现）。
func backupSiteTarGz(ctx context.Context, out *os.File, site *model.Site, svc *service.Service) error {
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// 1. 写一份渲染好的 nginx conf 进 tar，方便恢复时直接 cp 到 conf.d
	rendered, err := svc.RenderSiteConfig(site)
	if err == nil {
		body := []byte(rendered)
		hdr := &tar.Header{
			Name:    "nginx.conf",
			Mode:    0644,
			Size:    int64(len(body)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}

	// 2. tar 站点根目录
	root := site.Root.Path
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 单个文件读不到时**不**让整个备份失败（站点里有 FIFO / socket
			// / 损坏 inode 不奇怪）。仅记日志跳过。
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// 跳过 logs 子目录
		if info.IsDir() && (path == filepath.Join(root, "logs") || strings.HasPrefix(path, filepath.Join(root, "logs")+"/")) {
			return filepath.SkipDir
		}
		// 不打包 socket / 设备文件
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			rel = ""
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(filepath.Join("site", rel))
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return nil
		}
		return nil
	})
}

// BackupDatabase 用 mysqldump 备份一个数据库。
//
// 必须 svc.MySQL() != nil（MySQL 子系统启用）。
// 走 mysqldump + gzip：单文件 ~10x 压缩，5GB 库压到 500MB。
//
// 限制 100MB 仍然适用（gzip 后）。
func BackupDatabase(ctx context.Context, svc *service.Service, dbName string) (*model.Backup, error) {
	if svc.MySQL() == nil {
		return nil, fmt.Errorf("MySQL 子系统未启用")
	}
	// mysqldump 命令位置
	mysqldump, err := exec.LookPath("mysqldump")
	if err != nil {
		return nil, fmt.Errorf("找不到 mysqldump 命令: %w", err)
	}
	// 读 cnf 拿 root 凭据
	cnfPath := filepath.Join(svc.DataDir(), "mysql-root.cnf")
	args := []string{
		"--defaults-extra-file=" + cnfPath,
		"--single-transaction",
		"--quick",
		"--routines",
		"--triggers",
		"--default-character-set=utf8mb4",
		dbName,
	}

	dataDir := svc.DataDir()
	outDir := filepath.Join(dataDir, "backup", "database")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, err
	}
	id := newID()
	tmpPath := filepath.Join(outDir, id+".sql.gz.partial")
	finalPath := filepath.Join(outDir, id+".sql.gz")

	out, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(out)
	cmd := exec.CommandContext(ctx, mysqldump, args...)
	cmd.Stdout = gz
	stderrBuf := &strings.Builder{}
	cmd.Stderr = stderrBuf
	if err := cmd.Run(); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("mysqldump 失败: %w (stderr=%s)", err, stderrBuf.String())
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	st, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	if st.Size() > MaxBackupSize {
		_ = os.Remove(tmpPath)
		return nil, ErrTooLarge
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	return &model.Backup{
		ID:         id,
		Kind:       model.BackupDatabase,
		Target:     dbName,
		TargetName: dbName,
		Size:       st.Size(),
		Path:       finalPath,
	}, nil
}

// FindStaleBackups 返回超出保留份数的最旧备份（按 created_at 升序）。
//
// 由 cron 任务 retention 时调用，先 TrimBackups 删了 DB 记录，
// 再调这里拿磁盘路径做 os.Remove。
func FindStaleBackups(svc *service.Service, kind model.BackupKind, target string, keepN int) ([]*model.Backup, error) {
	if keepN <= 0 {
		return nil, nil
	}
	// limit=-1 = 不限量：必须看到全部记录才能判断谁超出了保留份数。
	// 早期传 0，被 store 层解释成"默认 100"，于是超过 100 份的站点
	// 永远只能裁剪到看起来像 100 的样子。
	all, err := svc.DB().ListBackups(string(kind), target, -1)
	if err != nil {
		return nil, err
	}
	// all 按 created_at DESC；超出 keepN 的就是 stale
	if len(all) <= keepN {
		return nil, nil
	}
	return all[keepN:], nil
}

// PruneOrphanFiles 删除备份目录里已经没有数据库记录的文件，返回删除个数。
//
// 为什么需要它：早期版本的保留逻辑顺序错了——先裁剪数据库记录，
// 再去找"超出保留份数"的备份文件。由于记录已经被删掉，
// 从数据库列出来的永远只剩 keepN 条，那个 os.Remove 循环一次都不执行，
// 磁盘上的 tar.gz / sql.gz 一个都删不掉。
//
// 结果是：界面上备份稳定在 N 份，磁盘却一天涨一份。
// 站点备份动辄几十上百 MB，几天就能把根分区写满，
// 接着 nginx 写日志失败、SQLite 写入失败，整个面板不可用。
//
// 记录已经被删掉之后，这些文件就彻底成了孤儿——没人能说出它们属于谁，
// 只能靠目录扫描清理。这里做这件事，让历史遗留也能被收走。
func PruneOrphanFiles(svc *service.Service, kind model.BackupKind) (int, error) {
	known := map[string]bool{}
	all, err := svc.DB().ListBackups("", "", -1)
	if err != nil {
		return 0, err
	}
	for _, b := range all {
		if b.Path != "" {
			known[b.Path] = true
		}
	}

	var dir string
	switch kind {
	case model.BackupSite:
		dir = filepath.Join(svc.DataDir(), "backup", "site")
	case model.BackupDatabase:
		dir = filepath.Join(svc.DataDir(), "backup", "database")
	default:
		return 0, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// .partial 是写入中的临时文件，不归这里管（由写入方自己清理）。
		if strings.HasSuffix(name, ".partial") {
			continue
		}
		p := filepath.Join(dir, name)
		if known[p] {
			continue
		}
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	return removed, nil
}

// newID 生成 16 字节随机 hex。
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
