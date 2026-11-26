package service

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// 压缩相关限制。压缩是流式的，限制的是条目数与原始字节总量，
// 防止一个误选把整站/超大目录打成包耗尽磁盘。
const (
	maxArchiveEntries = 20000
	maxArchiveBytes   = int64(2 << 30) // 2GB 原始字节
)

type archiveEntry struct {
	full string
	rel  string // 相对 base 的 URL 风格路径（压缩包内名字）
	info fs.FileInfo
}

// CompressPaths 把 base 目录下的选中条目压缩为 zip / tar.gz。
//
// baseRel 是当前浏览目录（相对站点根）；names 是选中的站点相对路径
// （必须位于 base 内，防止"选 A 目录把 B 目录也打进去"的歧义）。
// 返回新压缩包的站点相对路径。
func (s *Service) CompressPaths(ctx context.Context, site *model.Site, baseRel string, names []string, format string) (string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	ext := ".zip"
	switch format {
	case "", "zip":
		format = "zip"
	case "tar.gz", "tgz":
		format = "tar.gz"
		ext = ".tar.gz"
	default:
		return "", fmt.Errorf("不支持的压缩格式 %q（仅 zip / tar.gz）", format)
	}
	if len(names) == 0 {
		return "", errors.New("没有选中要压缩的条目")
	}
	if len(names) > maxArchiveEntries {
		return "", fmt.Errorf("一次最多压缩 %d 个条目", maxArchiveEntries)
	}
	if protectedRel(baseRel) {
		return "", ErrForbiddenPath
	}
	rootReal, err := s.resolveInRoot(site, "")
	if err != nil {
		return "", err
	}
	baseFull, err := s.resolveInRoot(site, baseRel)
	if err != nil {
		return "", err
	}
	baseInfo, err := os.Stat(baseFull)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	if !baseInfo.IsDir() {
		return "", fmt.Errorf("%w: 压缩基准路径必须是目录", ErrIsDirectory)
	}

	// 先把条目收集完，再创建压缩包：否则压缩包自己可能落在被遍历的目录里，
	// 把自己也压进去（无限增长的经典 bug）。
	var entries []archiveEntry
	var totalBytes int64
	addPath := func(full string, info fs.FileInfo) error {
		relSite, err := filepath.Rel(rootReal, full)
		if err != nil {
			return err
		}
		relSite = filepath.ToSlash(relSite)
		if relSite == "." || relSite == "" {
			return errors.New("不能压缩站点根目录")
		}
		if protectedRel(relSite) {
			return nil
		}
		relBase, err := filepath.Rel(baseFull, full)
		if err != nil {
			return err
		}
		relBase = filepath.ToSlash(relBase)
		if relBase == "." {
			return nil
		}
		if strings.HasPrefix(relBase, "../") || filepath.IsAbs(relBase) {
			return fmt.Errorf("%w: 只能压缩当前目录内的条目", ErrInvalidPath)
		}
		totalBytes += info.Size()
		if !info.IsDir() && totalBytes > maxArchiveBytes {
			return fmt.Errorf("压缩内容超过 %d GB 上限", maxArchiveBytes/(1<<30))
		}
		entries = append(entries, archiveEntry{full: full, rel: relBase, info: info})
		if len(entries) > maxArchiveEntries {
			return fmt.Errorf("条目数超过 %d 上限", maxArchiveEntries)
		}
		return nil
	}

	for _, name := range names {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		full, err := s.resolveInRoot(site, name)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				return "", ErrNotFound
			}
			return "", err
		}
		if protectedRel(name) {
			// 全选根目录时必然包含 logs；压缩是"尽量打包用户内容"，
			// 受保护目录直接跳过，而不是让整次压缩失败。
			continue
		}
		if !info.IsDir() {
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if err := addPath(full, info); err != nil {
				return "", err
			}
			continue
		}
		// 目录：遍历（不跟随 symlink）；受保护子目录整棵跳过。
		if err := filepath.WalkDir(full, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			relSite, rerr := filepath.Rel(rootReal, p)
			if rerr != nil {
				return rerr
			}
			relSite = filepath.ToSlash(relSite)
			if protectedRel(relSite) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() && p == full {
				info, ierr := d.Info()
				if ierr != nil {
					return ierr
				}
				return addPath(p, info)
			}
			if d.IsDir() {
				info, ierr := d.Info()
				if ierr != nil {
					return ierr
				}
				return addPath(p, info)
			}
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			return addPath(p, info)
		}); err != nil {
			return "", err
		}
	}
	if len(entries) == 0 {
		return "", errors.New("没有可压缩的内容（选中项均为空或受保护目录）")
	}

	archiveName := "archive-" + time.Now().Format("20060102-150405") + ext
	archiveFull := filepath.Join(baseFull, archiveName)
	for i := 1; ; i++ {
		if _, err := os.Stat(archiveFull); os.IsNotExist(err) {
			break
		}
		if i > 100 {
			return "", errors.New("无法生成不冲突的压缩包名")
		}
		archiveName = fmt.Sprintf("archive-%s-%d%s", time.Now().Format("20060102-150405"), i, ext)
		archiveFull = filepath.Join(baseFull, archiveName)
	}
	tmpPath := archiveFull + ".partial"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if format == "zip" {
		if err := writeZipEntries(ctx, f, entries); err != nil {
			return "", err
		}
	} else {
		if err := writeTarGzEntries(ctx, f, entries); err != nil {
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, archiveFull); err != nil {
		return "", err
	}
	ok = true
	rel, err := filepath.Rel(rootReal, archiveFull)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func writeZipEntries(ctx context.Context, out io.Writer, entries []archiveEntry) error {
	zw := zip.NewWriter(out)
	for _, e := range entries {
		select {
		case <-ctx.Done():
			_ = zw.Close()
			return ctx.Err()
		default:
		}
		hdr, err := zip.FileInfoHeader(e.info)
		if err != nil {
			_ = zw.Close()
			return err
		}
		hdr.Name = e.rel
		if e.info.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = zw.Close()
			return err
		}
		if e.info.IsDir() {
			continue
		}
		if err := copyExact(ctx, w, e.full, e.info.Size()); err != nil {
			_ = zw.Close()
			return err
		}
	}
	return zw.Close()
}

func writeTarGzEntries(ctx context.Context, out io.Writer, entries []archiveEntry) error {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	closeAll := func() error {
		if err := tw.Close(); err != nil {
			_ = gz.Close()
			return err
		}
		return gz.Close()
	}
	for _, e := range entries {
		select {
		case <-ctx.Done():
			_ = tw.Close()
			_ = gz.Close()
			return ctx.Err()
		default:
		}
		hdr, err := tar.FileInfoHeader(e.info, "")
		if err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
		hdr.Name = e.rel
		if e.info.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
		if e.info.IsDir() {
			continue
		}
		if err := copyExact(ctx, tw, e.full, e.info.Size()); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
	}
	return closeAll()
}

// copyExact 精确复制 size 字节，并在读取过程中响应 ctx 取消。
func copyExact(ctx context.Context, dst io.Writer, srcPath string, size int64) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	n, err := io.CopyN(dst, &ctxReader{ctx: ctx, r: in}, size)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", srcPath, err)
	}
	if n != size {
		return fmt.Errorf("%s 压缩期间被修改（预期 %d 字节，实际 %d）", srcPath, size, n)
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	default:
	}
	return c.r.Read(p)
}

// ExtractArchive 按扩展名解压 zip / tar / tar.gz。
func (s *Service) ExtractArchive(ctx context.Context, site *model.Site, archiveRel, destRel string) (int, error) {
	lower := strings.ToLower(archiveRel)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return s.UnzipArchive(ctx, site, archiveRel, destRel)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return s.extractTar(ctx, site, archiveRel, destRel, true)
	case strings.HasSuffix(lower, ".tar"):
		return s.extractTar(ctx, site, archiveRel, destRel, false)
	default:
		return 0, fmt.Errorf("不支持的压缩格式（仅 zip / tar / tar.gz）")
	}
}

func (s *Service) extractTar(ctx context.Context, site *model.Site, archiveRel, destRel string, gz bool) (int, error) {
	archiveFull, err := s.resolveInRoot(site, archiveRel)
	if err != nil {
		return 0, err
	}
	if protectedRel(archiveRel) {
		return 0, ErrForbiddenPath
	}
	if destRel == "" {
		destRel = path.Dir(archiveRel)
		if destRel == "." || destRel == "/" {
			destRel = ""
		}
	}
	if protectedRel(destRel) {
		return 0, ErrForbiddenPath
	}
	destFull, err := s.resolveInRoot(site, destRel)
	if err != nil {
		return 0, err
	}
	if _, err := os.Stat(archiveFull); err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if err := os.MkdirAll(destFull, 0o755); err != nil {
		return 0, err
	}
	f, err := os.Open(archiveFull)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var src io.Reader = f
	if gz {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return 0, fmt.Errorf("打开 gzip 失败: %w", err)
		}
		defer zr.Close()
		src = zr
	}
	tr := tar.NewReader(src)
	var totalBytes int64
	extracted := 0
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("读取 tar 失败: %w", err)
		}
		extracted++
		if extracted > maxUnzipEntries {
			return 0, fmt.Errorf("压缩包条目数超过上限 %d", maxUnzipEntries)
		}
		switch hdr.Typeflag {
		case tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			return 0, fmt.Errorf("%w: 拒绝解压特殊文件: %s", ErrInvalidArchiveEntry, hdr.Name)
		case tar.TypeDir, tar.TypeReg, tar.TypeRegA:
		default:
			return 0, fmt.Errorf("%w: 不支持的 entry 类型: %s", ErrInvalidArchiveEntry, hdr.Name)
		}
		raw := filepath.ToSlash(hdr.Name)
		for _, seg := range strings.Split(raw, "/") {
			if seg == ".." {
				return 0, fmt.Errorf("%w: 拒绝解压含 .. 的 entry: %s", ErrInvalidArchiveEntry, hdr.Name)
			}
		}
		if strings.HasPrefix(raw, "/") || filepath.IsAbs(hdr.Name) {
			return 0, fmt.Errorf("%w: 拒绝解压绝对路径 entry: %s", ErrInvalidArchiveEntry, hdr.Name)
		}
		relName := strings.TrimPrefix(path.Clean("/"+raw), "/")
		if protectedRel(relName) {
			return 0, fmt.Errorf("%w: 拒绝解压到受保护子目录: %s", ErrArchiveTargetForbidden, hdr.Name)
		}
		target := filepath.Join(destFull, filepath.FromSlash(relName))
		if !pathInRoot(target, destFull) {
			return 0, fmt.Errorf("%w: 拒绝解压越界 entry: %s", ErrInvalidArchiveEntry, hdr.Name)
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return 0, err
			}
			continue
		}
		if hdr.Size < 0 || hdr.Size > maxUnzipEntrySize {
			return 0, fmt.Errorf("entry %s 大小 %d 超过单文件上限", hdr.Name, hdr.Size)
		}
		if totalBytes+hdr.Size > maxUnzipBytes {
			return 0, fmt.Errorf("累计解压字节数超过上限 %d", maxUnzipBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return 0, err
		}
		if _, err := io.CopyN(out, &ctxReader{ctx: ctx, r: tr}, hdr.Size); err != nil {
			_ = out.Close()
			return 0, fmt.Errorf("写入 %s 失败: %w", target, err)
		}
		if err := out.Close(); err != nil {
			return 0, err
		}
		totalBytes += hdr.Size
	}
	return extracted, nil
}
