package service

import (
	"bytes"
	"embed"
	"fmt"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"text/template"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// defaultIndexFS 嵌入选定的默认欢迎页模板。
//
// 模板只用于"用户没主动放 index 文件时"——这意味着我们不能让模板
// 覆盖用户已有内容，所以下面 EnsureDefaultIndex 先做存在性检查。
//
//go:embed templates/site-default-index.html
var defaultIndexFS embed.FS

// defaultIndexTmpl 预编译的欢迎页模板。
var defaultIndexTmpl = template.Must(template.ParseFS(defaultIndexFS, "templates/site-default-index.html"))

// indexNames 视为"已有首页"的常见文件名。
// 命中任一则跳过模板写入，尊重用户内容。
var indexNames = []string{"index.html", "index.htm", "index.php"}

// EnsureDefaultIndex 在站点使用 index.html 且尚无首页时创建欢迎页。
func (s *Service) EnsureDefaultIndex(site *model.Site) {
	if site.Root.Path == "" {
		return
	}
	configured := site.Index.SanitizedFiles()
	if len(configured) > 0 && !slices.Contains(configured, "index.html") {
		return
	}
	// 兼顾原有的常见首页和站点配置的首页。保留已有目录项（包括符号链接）。
	for _, name := range slices.Concat(indexNames, configured) {
		if name == "." || !filepath.IsLocal(name) {
			return
		}
		if _, err := os.Lstat(filepath.Join(site.Root.Path, name)); !os.IsNotExist(err) {
			return
		}
	}
	data := map[string]any{
		"Name":      site.Name,
		"Domain":    firstDomain(site),
		"SiteID":    site.ID,
		"Timestamp": time.Now().Format("2006-01-02 15:04:05 MST"),
		"IP":        s.publicIP(),
	}
	var buf bytes.Buffer
	if err := defaultIndexTmpl.Execute(&buf, data); err != nil {
		fmt.Fprintf(os.Stderr, "[zhiweipanel] 渲染默认欢迎页失败: %v\n", err)
		return
	}
	dst := filepath.Join(site.Root.Path, "index.html")
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		return
	}
	if err != nil {
		log.Printf("[zhiweipanel] create default index failed: %v", err)
		return
	}
	_, err = f.Write(buf.Bytes())
	if err != nil {
		log.Printf("[zhiweipanel] write default index failed: %v", err)
	}
	// chown 到 root.user / root.group，让 nginx worker（通常 www-data）能读
	if site.Root.User != "" && site.Root.Group != "" {
		_ = f.Chown(userID(site.Root.User), groupID(site.Root.Group))
	}
	if err := f.Close(); err != nil {
		log.Printf("[zhiweipanel] close default index failed: %v", err)
	}
}

func firstDomain(site *model.Site) string {
	if len(site.Domains) == 0 {
		return "(未配置)"
	}
	return site.Domains[0]
}

// publicIP 解析面板主网卡 IPv4 用于模板展示。
// 取不到时返回 127.0.0.1，仅展示用不影响功能。
func (s *Service) publicIP() string {
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			return ip4.String()
		}
	}
	return "127.0.0.1"
}

// userID/groupID 把用户名/组名解析为数字 ID 用于 os.Chown。
// 解析失败返回 -1（os.Chown 会忽略负数 ID 之外的变更）。
func userID(name string) int {
	if u, err := user.Lookup(name); err == nil {
		if n, err := strconv.Atoi(u.Uid); err == nil {
			return n
		}
	}
	return -1
}

func groupID(name string) int {
	if g, err := user.LookupGroup(name); err == nil {
		if n, err := strconv.Atoi(g.Gid); err == nil {
			return n
		}
	}
	return -1
}
