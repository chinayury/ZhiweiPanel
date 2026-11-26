package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// MaxRemoteDownloadSize 远程 URL 下载上限（100MB）。
const MaxRemoteDownloadSize = 100 * 1024 * 1024

// DownloadFile 返回可下载文件的绝对路径与大小。
func (s *Service) DownloadFile(_ context.Context, site *model.Site, rel string) (string, int64, error) {
	if rel == "" || rel == "/" {
		return "", 0, errors.New("不能下载目录")
	}
	if protectedRel(rel) {
		return "", 0, ErrForbiddenPath
	}
	full, err := s.resolveInRoot(site, rel)
	if err != nil {
		return "", 0, err
	}
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, ErrNotFound
		}
		return "", 0, err
	}
	if info.IsDir() {
		return "", 0, fmt.Errorf("%w: 不能下载目录", ErrIsDirectory)
	}
	return full, info.Size(), nil
}

// RemoteDownload 从 http/https URL 下载文件到站点目录。
//
// 安全边界：
//   - 只允许 http/https；
//   - 拒绝回环、链路本地（含云元数据 169.254.169.254）、未指定、组播地址；
//   - 实际拨号按解析后的 IP 进行并在拨号时再次校验，防 DNS rebinding；
//   - 100MB / 60s / 最多 5 次重定向上限；
//   - 目标文件已存在时拒绝覆盖。
func (s *Service) RemoteDownload(ctx context.Context, site *model.Site, rawURL, destRel, filename string) (string, int64, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", 0, fmt.Errorf("%w: URL 解析失败", model.ErrBadRequest)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", 0, fmt.Errorf("%w: 只支持 http/https 下载", model.ErrBadRequest)
	}
	if u.Host == "" {
		return "", 0, fmt.Errorf("%w: URL 缺少主机名", model.ErrBadRequest)
	}
	if u.User != nil {
		return "", 0, fmt.Errorf("%w: URL 不允许携带用户名/密码", model.ErrBadRequest)
	}
	if err := checkRemoteHost(ctx, u.Hostname()); err != nil {
		return "", 0, fmt.Errorf("%w: %v", model.ErrBadRequest, err)
	}

	name := strings.TrimSpace(filename)
	if name == "" {
		name = path.Base(u.Path)
		if name == "" || name == "." || name == "/" {
			name = "download-" + strconv.FormatInt(time.Now().Unix(), 10)
		}
	}
	if err := validateDownloadName(name); err != nil {
		return "", 0, fmt.Errorf("%w: %v", model.ErrBadRequest, err)
	}

	if protectedRel(destRel) {
		return "", 0, ErrForbiddenPath
	}
	destFull, err := s.resolveInRoot(site, destRel)
	if err != nil {
		return "", 0, err
	}
	info, err := os.Stat(destFull)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, ErrNotFound
		}
		return "", 0, err
	}
	if !info.IsDir() {
		return "", 0, fmt.Errorf("%w: 下载目标必须是目录", ErrIsDirectory)
	}
	target := filepath.Join(destFull, name)
	if !pathInRoot(target, destFull) {
		return "", 0, ErrInvalidPath
	}
	if _, err := os.Stat(target); err == nil {
		return "", 0, fmt.Errorf("%w: 目标文件已存在", model.ErrConflict)
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil, // 不使用环境代理，避免把请求转给不可控的中间人
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, ia := range ips {
				if forbiddenRemoteIP(ia.IP) {
					continue
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ia.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = errors.New("目标地址不允许（回环/链路本地/云元数据）")
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("重定向次数过多（上限 5）")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("重定向目标协议不允许")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "ZhiweiPanel")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("下载失败: 目标返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxRemoteDownloadSize {
		return "", 0, ErrFileTooLarge
	}

	tmpPath := target + ".partial-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = out.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	written, err := io.Copy(out, io.LimitReader(&ctxReader{ctx: ctx, r: resp.Body}, MaxRemoteDownloadSize+1))
	if err != nil {
		return "", 0, fmt.Errorf("写入下载文件失败: %w", err)
	}
	if written > MaxRemoteDownloadSize {
		return "", 0, ErrFileTooLarge
	}
	if err := out.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return "", 0, err
	}
	ok = true
	return path.Join(destRel, name), written, nil
}

func checkRemoteHost(ctx context.Context, host string) error {
	if host == "" {
		return errors.New("URL 缺少主机名")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("解析目标主机失败: %w", err)
	}
	for _, ia := range ips {
		if forbiddenRemoteIP(ia.IP) {
			return errors.New("目标地址不允许（回环/链路本地/云元数据）")
		}
	}
	return nil
}

func forbiddenRemoteIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	// 常见云元数据地址（阿里云 100.100.100.200 不在 link-local 段里）。
	if ip.Equal(net.ParseIP("100.100.100.200")) {
		return true
	}
	return false
}

func validateDownloadName(name string) error {
	if name == "" || name == "." || name == ".." {
		return errors.New("非法文件名")
	}
	if len(name) > 255 {
		return errors.New("文件名过长")
	}
	for _, r := range name {
		if r == '/' || r == '\\' || r == 0 || r < 32 {
			return errors.New("文件名不能包含路径分隔符或控制字符")
		}
	}
	return nil
}

// BatchDelete 批量删除文件/空目录；recursive=true 时递归删除目录。
//
// 先整体校验再执行：任何一个目标是站点根、受保护目录、非空目录（且未开
// recursive）时，整批都不动——避免"删了一半才发现第 7 个不合法"。
func (s *Service) BatchDelete(ctx context.Context, site *model.Site, rels []string, recursive bool) (int, error) {
	if len(rels) == 0 {
		return 0, errors.New("没有选中要删除的条目")
	}
	if len(rels) > 100 {
		return 0, errors.New("一次最多删除 100 个条目")
	}
	type item struct {
		full  string
		rel   string
		isDir bool
	}
	seen := map[string]bool{}
	var items []item
	for _, rel := range rels {
		rel = strings.TrimSpace(rel)
		if rel == "" || rel == "/" {
			return 0, errors.New("不允许删除站点根目录")
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		if protectedRel(rel) {
			return 0, ErrForbiddenPath
		}
		full, err := s.resolveInRoot(site, rel)
		if err != nil {
			return 0, err
		}
		info, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				return 0, ErrNotFound
			}
			return 0, err
		}
		if info.IsDir() && !recursive {
			entries, _ := os.ReadDir(full)
			if len(entries) > 0 {
				return 0, fmt.Errorf("目录 %s 非空：请勾选递归删除或先删除内部文件", rel)
			}
		}
		items = append(items, item{full: full, rel: rel, isDir: info.IsDir()})
	}
	// 深的先删：同时选中父目录和子目录时，先删子目录避免父目录删完后子项丢失报错。
	sort.Slice(items, func(i, j int) bool { return len(items[i].full) > len(items[j].full) })
	deleted := 0
	for _, it := range items {
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		default:
		}
		var err error
		if it.isDir && recursive {
			err = os.RemoveAll(it.full)
		} else {
			err = os.Remove(it.full)
		}
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return deleted, fmt.Errorf("删除 %s 失败: %w", it.rel, err)
		}
		deleted++
	}
	return deleted, nil
}

// Chmod 修改单个文件/目录权限（0~0777）。
func (s *Service) Chmod(_ context.Context, site *model.Site, rel, mode string) error {
	if rel == "" || rel == "/" {
		return errors.New("不能修改站点根目录权限")
	}
	if protectedRel(rel) {
		return ErrForbiddenPath
	}
	mode = strings.TrimSpace(mode)
	if len(mode) < 3 || len(mode) > 4 {
		return errors.New("权限格式应为 644 / 0755 这类八进制")
	}
	v, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || v > 0o777 {
		return errors.New("权限必须是 0~0777 的八进制数")
	}
	full, err := s.resolveInRoot(site, rel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(full); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return os.Chmod(full, os.FileMode(v))
}

// SearchFiles 在站点根内按文件名子串搜索（不区分大小写）。
//
// 遍历上限 5 万项，结果最多 200 条；跳过 logs/.well-known 与 symlink。
func (s *Service) SearchFiles(ctx context.Context, site *model.Site, keyword string, limit int) ([]FileEntry, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return []FileEntry{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rootReal, err := s.resolveInRoot(site, "")
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(keyword)
	out := make([]FileEntry, 0, limit)
	visited := 0
	err = filepath.WalkDir(rootReal, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // 单个不可读目录不阻断整次搜索
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if p == rootReal {
			return nil
		}
		visited++
		if visited > 50000 {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(rootReal, p)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if protectedRel(relSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.Contains(strings.ToLower(d.Name()), lower) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		typ := "file"
		if d.IsDir() {
			typ = "dir"
		}
		out = append(out, FileEntry{
			Name:  d.Name(),
			Path:  relSlash,
			Type:  typ,
			Size:  info.Size(),
			MTime: info.ModTime(),
			Mode:  fmt.Sprintf("%04o", info.Mode().Perm()),
		})
		if len(out) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, err
	}
	return out, nil
}
