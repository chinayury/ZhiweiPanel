package service

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/mysql"
)

// ListDatabases 返回面板管理的数据库列表（site_*），并补上站点映射。
func (s *Service) ListDatabases(ctx context.Context) ([]model.DatabaseOverview, error) {
	if s.mysql == nil {
		return nil, mysql.ErrNotInstalled
	}
	infos, err := s.mysql.ListManagedDatabases(ctx)
	if err != nil {
		return nil, err
	}
	sites, err := s.db.ListSites()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*model.Site, len(sites))
	for _, site := range sites {
		if site != nil && site.Mysql != nil && site.Mysql.Name != "" {
			byName[site.Mysql.Name] = site
		}
	}

	out := make([]model.DatabaseOverview, 0, len(infos))
	for _, info := range infos {
		ov := model.DatabaseOverview{
			Name:      info.Name,
			Charset:   info.Charset,
			Collation: info.Collation,
			Tables:    info.Tables,
			SizeBytes: info.SizeBytes,
			User:      info.Name,
			Host:      "localhost",
			Port:      3306,
		}
		if site := byName[info.Name]; site != nil {
			ov.SiteID = site.ID
			ov.SiteName = site.Name
			if site.Mysql.User != "" {
				ov.User = site.Mysql.User
			}
			if site.Mysql.Host != "" {
				ov.Host = site.Mysql.Host
			}
			if site.Mysql.Port > 0 {
				ov.Port = site.Mysql.Port
			}
		}
		out = append(out, ov)
	}
	return out, nil
}

// GetDatabase 按库名查找一个受管数据库。
func (s *Service) GetDatabase(ctx context.Context, name string) (*model.DatabaseOverview, error) {
	if err := mysql.ValidateManagedDatabaseName(name); err != nil {
		return nil, model.ErrNotFound
	}
	list, err := s.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, model.ErrNotFound
}

// ResetDatabasePassword 重置站点数据库用户的密码。
//
// 返回一次性明文（调用方负责只回显一次）。用户与库同名是面板的命名规则；
// 老数据里若站点配置记录了不同用户名，以站点配置为准。
func (s *Service) ResetDatabasePassword(ctx context.Context, name string) (string, error) {
	if s.mysql == nil {
		return "", mysql.ErrNotInstalled
	}
	ov, err := s.GetDatabase(ctx, name)
	if err != nil {
		return "", err
	}
	pwd := mysql.GeneratePassword()
	user := ov.User
	if user == "" {
		user = ov.Name
	}
	if err := s.mysql.SetUserPassword(ctx, user, pwd, "localhost"); err != nil {
		return "", err
	}
	if ov.SiteID != "" {
		site, err := s.db.GetSite(ov.SiteID)
		if err != nil {
			return "", fmt.Errorf("密码已重置，但读取站点失败: %w", err)
		}
		if site.Mysql != nil {
			site.Mysql.Password = pwd
			if site.Mysql.User == "" {
				site.Mysql.User = user
			}
			if err := s.db.SaveSite(site); err != nil {
				return "", fmt.Errorf("密码已重置，但保存站点配置失败: %w", err)
			}
		}
	}
	return pwd, nil
}

// OptimizeDatabase 优化库内所有表，返回处理表数。
func (s *Service) OptimizeDatabase(ctx context.Context, name string) (int, error) {
	if s.mysql == nil {
		return 0, mysql.ErrNotInstalled
	}
	if _, err := s.GetDatabase(ctx, name); err != nil {
		return 0, err
	}
	return s.mysql.OptimizeDatabase(ctx, name)
}

// ImportDatabase 把 SQL 脚本导入受管数据库。
func (s *Service) ImportDatabase(ctx context.Context, name string, src io.Reader) error {
	if s.mysql == nil {
		return mysql.ErrNotInstalled
	}
	if _, err := s.GetDatabase(ctx, name); err != nil {
		return err
	}
	cnf := filepath.Join(s.dataDir, "mysql-root.cnf")
	return mysql.ImportSQL(ctx, cnf, name, src)
}
