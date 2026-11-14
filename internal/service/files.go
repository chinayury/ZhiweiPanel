package service

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// 站点文件管理：所有路径必须在 site.Root 之内，禁 ../ 逃逸。
//
// 核心安全约束：
//  1. resolveInRoot：filepath.Clean + EvalSymlinks 后必须仍在 root 内
//  2. 禁访问 logs/ 子目录（nginx 已 deny all，但面板再守一道）
//  3. 禁访问 .well-known/（ACME 挑战目录）
//  4. 单文件 ≤ MaxFileSize 字节，防止 OOM
//  5. 路径字符白名单：拒绝包含 NUL/控制字符
//
// 错误约定：sentinel 都通过 fmt.Errorf %w 包装 model.ErrXxx，
// 写 model sentinel 已被 api.writeModelError 识别为正确 HTTP 状态码。
// errors.Is 链让"加额外上下文"与"按状态码映射"两件事互不干扰。

// MaxFileSize 单文件读/写上限（50MB）。
const MaxFileSize = 50 * 1024 * 1024

// 路径逃逸站点根 — 视为 403 Forbidden。
var ErrPathEscapesRoot = model.ErrForbidden

// 受保护子目录（logs/、.well-known/）— 也视为 403。
var ErrForbiddenPath = model.ErrForbidden

// 路径含非法字符 — 400。
var ErrInvalidPath = model.ErrInvalidPath

// 路径不存在 — 404。
var ErrNotFound = model.ErrNotFound

// 文件超过 MaxFileSize — 413。
var ErrFileTooLarge = errors.New("文件大小超过限制")

// 解压包内的 entry 非法（zip-slip、绝对路径、symlink）— 400。
// 与 ErrInvalidPath 同源，因为本质都是路径不安全。
var ErrInvalidArchiveEntry = model.ErrInvalidPath

// 解压目标本身非法（受保护子目录、越界）— 403。
var ErrArchiveTargetForbidden = model.ErrForbidden

// 期望文件但收到目录（或反过来）— 400。
var ErrIsDirectory = model.ErrBadRequest

// FileEntry 文件浏览器一项。
type FileEntry struct {
	Name  string    `json:"name"`
	Type  string    `json:"type"` // "file" | "dir" | "symlink"
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
	Mode  string    `json:"mode"` // 八进制字符串，如 "0644"
}

// 站点文件操作结果。
type fileOps struct {
	root string
}

// resolveInRoot 把用户给的相对路径解析为绝对路径，并校验仍在 root 内。
//
// 实现要点：
//  1. 拒绝任何含 ".." 路径段的相对输入——比 EvalSymlinks 更早拦截
//     跨目录逃逸，且避免 path.Clean / filepath.Join 在不同 OS 下
//     路径分隔符不一致导致的前缀检查绕过
//  2. EvalSymlinks 解所有符号链接：realpath 必须仍以 root 开头
//  3. root 也 EvalSymlinks 解一次，避免 root 自身被 symlink 指向他处
func (s *Service) resolveInRoot(site *model.Site, rel string) (string, error) {
	if site == nil || site.Root.Path == "" {
		return "", ErrPathEscapesRoot
	}
	// 字符白名单
	for _, r := range rel {
		if r == 0 || r < 32 {
			return "", ErrInvalidPath
		}
	}
	// 早期阻断 .. 段（任何位置）
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", ErrPathEscapesRoot
		}
	}
	// 业务约定：相对路径以 / 分隔（URL 风格），空视为根
	rel = path.Clean("/" + rel)
	if rel == "/" {
		rel = "."
	}
	rootReal, err := filepath.EvalSymlinks(site.Root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: 站点根目录", ErrNotFound)
		}
		return "", err
	}
	// 把业务路径转成本机原生分隔符再 Join——否则在 Windows 上
	// filepath.Join("C:\xxx", "/etc/passwd") 拼出 "C:\xxx\/etc/passwd"
	// 仍然字面以 root 前缀开头，绕过 pathInRoot 检查。
	relNative := filepath.FromSlash(rel)
	full := filepath.Join(rootReal, relNative)
	fullReal, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			if !pathInRoot(full, rootReal) {
				return "", ErrPathEscapesRoot
			}
			return full, nil
		}
		return "", err
	}
	if !pathInRoot(fullReal, rootReal) {
		return "", ErrPathEscapesRoot
	}
	return fullReal, nil
}

// pathInRoot 判定 full 是否处于 root 内（含等于 root 自身）。
func pathInRoot(full, root string) bool {
	if full == root {
		return true
	}
	prefix := root + string(filepath.Separator)
	return strings.HasPrefix(full, prefix)
}

// protectedRel 相对路径命中受保护子目录则返回 true。
//
// 用 path 包而非 filepath——业务路径是 URL 风格（/logs），不分平台
// 都要按 POSIX 解析。filepath 在 Windows 上会把 / 转成 \，导致
// TrimPrefix 失败、保护失效。
func protectedRel(rel string) bool {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	for _, prefix := range []string{"logs", ".well-known"} {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
}

// ListFiles 列出目录内容。
func (s *Service) ListFiles(_ context.Context, site *model.Site, rel string) ([]FileEntry, error) {
	full, err := s.resolveInRoot(site, rel)
	if err != nil {
		return nil, err
	}
	if protectedRel(rel) {
		return nil, ErrForbiddenPath
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: 期望目录", ErrIsDirectory)
	}
	dirEntries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		fi, err := de.Info()
		if err != nil {
			continue // 跳过无法 stat 的（断链/竞态）
		}
		typ := "file"
		if de.IsDir() {
			typ = "dir"
		} else if de.Type()&fs.ModeSymlink != 0 {
			typ = "symlink"
		}
		out = append(out, FileEntry{
			Name:  de.Name(),
			Type:  typ,
			Size:  fi.Size(),
			MTime: fi.ModTime(),
			Mode:  fmt.Sprintf("%04o", fi.Mode().Perm()),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		// 目录优先 + 字母序
		if (out[i].Type == "dir") != (out[j].Type == "dir") {
			return out[i].Type == "dir"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ReadFile 读文件内容。文本文件返回原 bytes；二进制文件 base64 编码。
func (s *Service) ReadFile(_ context.Context, site *model.Site, rel string) (content []byte, isText bool, mimeType string, err error) {
	full, err := s.resolveInRoot(site, rel)
	if err != nil {
		return nil, false, "", err
	}
	if protectedRel(rel) {
		return nil, false, "", ErrForbiddenPath
	}
	fi, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, "", ErrNotFound
		}
		return nil, false, "", err
	}
	if fi.IsDir() {
		return nil, false, "", fmt.Errorf("%w: 期望文件", ErrIsDirectory)
	}
	if fi.Size() > MaxFileSize {
		return nil, false, "", ErrFileTooLarge
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, false, "", err
	}
	// 文本探测：检查前 8KB 是否含 NUL 字节
	isText = true
	for i := 0; i < len(data) && i < 8192; i++ {
		if data[i] == 0 {
			isText = false
			break
		}
	}
	mt := mime.TypeByExtension(filepath.Ext(full))
	if mt == "" {
		mt = "application/octet-stream"
	}
	return data, isText, mt, nil
}

// WriteFile 写文件。覆盖式写入；自动创建父目录。
func (s *Service) WriteFile(_ context.Context, site *model.Site, rel string, data []byte) error {
	if len(data) > MaxFileSize {
		return ErrFileTooLarge
	}
	if rel == "" || rel == "/" {
		return errors.New("不允许直接覆盖根目录")
	}
	if protectedRel(rel) {
		return ErrForbiddenPath
	}
	full, err := s.resolveInRoot(site, rel)
	if err != nil {
		return err
	}
	// 父目录必须存在
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

// DeleteFile 删除文件或空目录。递归删除请用 Zip-and-remove 工具，
// 当前不暴露，避免误删整站。
func (s *Service) DeleteFile(_ context.Context, site *model.Site, rel string) error {
	if rel == "" || rel == "/" {
		return errors.New("不允许删除根目录")
	}
	if protectedRel(rel) {
		return ErrForbiddenPath
	}
	full, err := s.resolveInRoot(site, rel)
	if err != nil {
		return err
	}
	fi, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if fi.IsDir() {
		// 目录非空拒绝
		entries, _ := os.ReadDir(full)
		if len(entries) > 0 {
			return errors.New("目录非空：请先删除内部文件")
		}
		return os.Remove(full)
	}
	return os.Remove(full)
}

// Rename 在同站点根内移动/重命名。
func (s *Service) Rename(_ context.Context, site *model.Site, from, to string) error {
	if from == "" || to == "" || from == "/" || to == "/" {
		return errors.New("from / to 不能为空或根")
	}
	if protectedRel(from) || protectedRel(to) {
		return ErrForbiddenPath
	}
	src, err := s.resolveInRoot(site, from)
	if err != nil {
		return err
	}
	dst, err := s.resolveInRoot(site, to)
	if err != nil {
		return err
	}
	if src == dst {
		return errors.New("源与目标相同")
	}
	// 父目录建好
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// 目标已存在 → 拒绝覆盖（避免静默覆盖）
	if _, err := os.Stat(dst); err == nil {
		return errors.New("目标已存在")
	}
	return os.Rename(src, dst)
}

// Mkdir 新建目录。
func (s *Service) Mkdir(_ context.Context, site *model.Site, rel string) error {
	if rel == "" || rel == "/" {
		return errors.New("路径不能为空或根")
	}
	if protectedRel(rel) {
		return ErrForbiddenPath
	}
	full, err := s.resolveInRoot(site, rel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(full); err == nil {
		return errors.New("已存在")
	}
	return os.MkdirAll(full, 0o755)
}

// ZipDir 把目录压缩成 zip 流。占位未实现，留待后续。
func (s *Service) ZipDir(_ context.Context, _ *model.Site, _ string, _ io.Writer) error {
	return errors.New("暂未实现")
}

// 解压相关限制（防 zip-slip + 资源耗尽）。
const (
	maxUnzipBytes     = 500 * 1024 * 1024 // 累计解压 500MB
	maxUnzipEntries   = 5000              // 单 zip ≤ 5000 个 entry
	maxUnzipEntrySize = 100 * 1024 * 1024 // 单文件 ≤ 100MB
)

// UnzipArchive 把 zip 文件解压到 destRel（相对站点根）。
//
// 强安全约束：
//   - 拒绝绝对路径、含 .. 的 entry（防 zip slip）
//   - 拒绝 symlink entry（防 symlink 指向站外）
//   - 总字节数 / entry 数 / 单文件 size 三重上限
//   - 拒绝写到 logs/、.well-known/ 受保护子目录
//
// destRel 留空表示解压到 zip 所在目录。
func (s *Service) UnzipArchive(_ context.Context, site *model.Site, zipRel, destRel string) (extracted int, err error) {
	zipFull, err := s.resolveInRoot(site, zipRel)
	if err != nil {
		return 0, err
	}
	if protectedRel(zipRel) {
		return 0, ErrForbiddenPath
	}

	// dest 默认与 zip 同目录。zipRel="site.zip" → path.Dir 返回 "."，
	// 视为 root（与 zip 同级目录就是 root）。
	if destRel == "" {
		destRel = path.Dir(zipRel)
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
	if _, err := os.Stat(zipFull); err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if err := os.MkdirAll(destFull, 0o755); err != nil {
		return 0, err
	}

	r, err := zip.OpenReader(zipFull)
	if err != nil {
		return 0, fmt.Errorf("打开 zip 失败: %w", err)
	}
	defer r.Close()
	if len(r.File) > maxUnzipEntries {
		return 0, fmt.Errorf("zip 包含 %d 个 entry，超过上限 %d", len(r.File), maxUnzipEntries)
	}

	var totalBytes int64
	for _, f := range r.File {
		// 安全：拒绝 symlink。zip 里 symlink 标记写在 ExternalAttrs 高 16 位
		// （unix mode bits 0xA000），SetMode() 不会写入 ExternalAttrs，
		// 必须直接查原始字段。f.Mode() 的反推依赖 CreatorVersion=Unix，
		// 某些工具写出的 zip 不带 CreatorVersion，导致 Mode() 反推失败——
		// 双重检查：raw ExternalAttrs 与 f.Mode() 命中其一即拒。
		if f.ExternalAttrs>>16&0xA000 == 0xA000 || f.Mode()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("%w: 拒绝解压 symlink: %s", ErrInvalidArchiveEntry, f.Name)
		}
		// 路径安全：先按 / 分段查 raw name 含 ".."（path.Clean 会把 "../x" 归一成
		// "/x" 漏判），再查绝对路径，再 Clean 防隐藏 trick。
		raw := filepath.ToSlash(f.Name)
		slashParts := strings.Split(raw, "/")
		for _, p := range slashParts {
			if p == ".." {
				return 0, fmt.Errorf("%w: 拒绝解压含 .. 的 entry: %s", ErrInvalidArchiveEntry, f.Name)
			}
		}
		if strings.HasPrefix(raw, "/") || filepath.IsAbs(f.Name) {
			return 0, fmt.Errorf("%w: 拒绝解压绝对路径 entry: %s", ErrInvalidArchiveEntry, f.Name)
		}
		cleanName := path.Clean("/" + raw)
		if cleanName == "/.." {
			return 0, fmt.Errorf("%w: 拒绝解压越界 entry: %s", ErrInvalidArchiveEntry, f.Name)
		}
		// 拒绝写到受保护子目录
		relName := strings.TrimPrefix(cleanName, "/")
		if protectedRel(relName) {
			return 0, fmt.Errorf("%w: 拒绝解压到受保护子目录: %s", ErrArchiveTargetForbidden, f.Name)
		}
		target := filepath.Join(destFull, filepath.FromSlash(relName))

		// 再次校验：解压后的实际路径仍必须在 destFull 内
		// （防 entry 路径里含 .. 但 path.Clean 后未完全归一化的边缘情况）
		if !strings.HasPrefix(target, destFull+string(filepath.Separator)) && target != destFull {
			return 0, fmt.Errorf("%w: 拒绝解压越界 entry: %s", ErrInvalidArchiveEntry, f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return 0, err
			}
			continue
		}

		// 单文件大小限制
		if f.UncompressedSize64 > maxUnzipEntrySize {
			return 0, fmt.Errorf("entry %s 大小 %d 超过单文件上限 %d", f.Name, f.UncompressedSize64, maxUnzipEntrySize)
		}
		// 累计字节限制
		if totalBytes+int64(f.UncompressedSize64) > maxUnzipBytes {
			return 0, fmt.Errorf("累计解压字节数超过上限 %d", maxUnzipBytes)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, err
		}
		if err := extractZipEntry(f, target); err != nil {
			return 0, err
		}
		totalBytes += int64(f.UncompressedSize64)
		extracted++
	}
	return extracted, nil
}

// extractZipEntry 打开 entry 写入目标文件，限流防止 OOM。
func extractZipEntry(f *zip.File, target string) error {
	in, err := f.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	// 限流：单 entry 不超过 maxUnzipEntrySize
	_, err = io.CopyN(out, in, maxUnzipEntrySize+1)
	if err == nil {
		return errors.New("entry 实际大小超过声明大小")
	}
	if err != io.EOF {
		return err
	}
	return nil
}
