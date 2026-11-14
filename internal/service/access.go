package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhiwei/panel/internal/driver"
	"github.com/zhiwei/panel/internal/htpasswd"
	"github.com/zhiwei/panel/internal/model"
)

// 本文件实现站点的 HTTP Basic Auth（访问认证）闭环。
//
// 背景：模型里 Access.AuthEnabled / AuthUser / AuthPass 三个字段从一开始就存在，
// 前端也画了"访问认证"输入框，但**全仓没有任何一处把它们渲染进 nginx**，
// 更没有任何一处生成 nginx 能读的 htpasswd 文件——
// 用户在界面上配完，站点对外访问毫无变化。
//
// 闭环由三段组成：
//
//  1. 规范化（applyAccessAuth）：明文口令 → apr1 哈希，明文即刻丢弃；
//  2. 落盘（syncAccessFile）：把 "用户名:哈希" 写进数据目录下的口令文件；
//  3. 渲染（nginx.buildBasicAuth）：输出 auth_basic + auth_basic_user_file。
//
// 三段缺一不可，且**顺序不能换**：nginx 在解析配置时就会打开
// auth_basic_user_file 指向的文件，文件不存在时 nginx -t 直接失败，
// 而一次 nginx -t 失败会让 ApplyAllConfig 整体回滚 ——
// 为了一个站点的认证把整台面板的所有站点拖下水，代价完全不成比例。

// accessFileDir 返回 htpasswd 口令文件目录。
//
// 位置很关键：必须在**站点根目录之外**，否则 nginx 会把它当静态文件伺服出去
// （等价于把口令哈希公开下载）。
// 数据目录默认是 /opt/zhiweipanel/data，与站点根目录 /www/wwwroot 完全分离。
func (s *Service) accessFileDir() string {
	return filepath.Join(s.dataDir, "htpasswd")
}

// accessFilePath 返回某站点的口令文件路径。
func (s *Service) accessFilePath(siteID string) string {
	return filepath.Join(s.accessFileDir(), siteID)
}

// applyAccessAuth 规范化站点的访问认证配置。
//
//   - keepHash：更新站点时传入旧哈希。请求体里没带口令（auth_pass 为空）
//     表示"不修改密码"，此时沿用旧哈希。
//   - 客户端提交的 auth_hash 一律丢弃：否则调用方可以塞进明文口令或
//     其它弱格式（例如 {PLAIN}）做降级，绕过面板的哈希策略。
//
// 明文口令只在这一次调用里出现：函数返回前就被清空，
// 之后既不入库、也不回显、也不落任何文件。
func (s *Service) applyAccessAuth(site *model.Site, keepHash string) error {
	a := &site.Access
	pass := a.AuthPass
	a.AuthPass = ""
	a.AuthHash = ""

	if pass != "" {
		h, err := htpasswd.Hash(pass, "")
		if err != nil {
			return fmt.Errorf("生成访问认证口令哈希失败: %w", err)
		}
		a.AuthHash = h
	} else {
		a.AuthHash = keepHash
	}

	if a.AuthEnabled {
		if strings.TrimSpace(a.AuthUser) == "" {
			return fmt.Errorf("%w: 开启访问认证时必须填写认证用户名", model.ErrInvalidValue)
		}
		if a.AuthHash == "" {
			return fmt.Errorf("%w: 开启访问认证时必须设置访问密码", model.ErrInvalidValue)
		}
		// 口令文件路径会原样写进 nginx 配置：路径里带空格/引号/分号
		// 会改变配置结构，宁可明确报错也不要渲染出一份坏配置。
		if !model.NginxSafeConfPath(s.accessFilePath(site.ID)) {
			return fmt.Errorf("%w: 面板数据目录路径包含 nginx 不支持的字符，无法启用访问认证", model.ErrInvalidValue)
		}
	}
	return nil
}

// syncAccessFile 同步站点的 htpasswd 口令文件。
//
// 时序要求：文件必须在 nginx 解析配置**之前**存在。
// 幂等：内容一致时不重写（nginx 解析配置时会读这个文件，
// 无意义的重写只会让文件 mtime 一直变）。
func (s *Service) syncAccessFile(site *model.Site) error {
	if site == nil || site.ID == "" {
		return nil
	}
	path := s.accessFilePath(site.ID)
	a := site.Access

	if !a.AuthEnabled || a.AuthUser == "" || a.AuthHash == "" {
		// 关闭认证（或还没设过口令）时删掉残留文件：
		// 配置里已经不引用它了，但盘上留一份没人知道的口令哈希不是好事。
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除访问认证口令文件失败: %w", err)
		}
		return nil
	}

	if err := os.MkdirAll(s.accessFileDir(), 0o755); err != nil {
		return fmt.Errorf("创建访问认证口令目录失败: %w", err)
	}
	content := a.AuthUser + ":" + a.AuthHash + "\n"
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return nil
	}
	// 权限 0644：nginx master 以 root 解析配置时能读，
	// 部分发行版的 worker 也会按请求读这个文件，0644 保证两种情况都工作。
	// 文件内容是加盐哈希，不是明文口令。
	return driver.WriteFileAtomic(path, content, 0o644)
}

// removeAccessFile 删除站点的口令文件（删站时调用，避免残留）。
func (s *Service) removeAccessFile(siteID string) error {
	if siteID == "" {
		return nil
	}
	path := s.accessFilePath(siteID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除访问认证口令文件失败: %w", err)
	}
	return nil
}
