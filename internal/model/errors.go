package model

import "errors"

// 领域层统一错误。
var (
	ErrNotFound      = errors.New("资源不存在")
	ErrInvalidDomain = errors.New("域名格式非法")
	ErrInvalidPath   = errors.New("路径非法")
	ErrDuplicate     = errors.New("资源已存在")
	ErrUnauthorized  = errors.New("未授权")
	ErrForbidden     = errors.New("拒绝访问")
	ErrBadRequest    = errors.New("请求参数错误")
	ErrConflict      = errors.New("配置冲突")
	ErrUnavailable   = errors.New("依赖不可用")
	ErrIntegrity     = errors.New("完整性校验失败")
	// ErrMySQLNotAvailable 站点要求建库但面板未配置 MySQL 子系统。
	// 与 ErrUnavailable 区别：明确指向 MySQL 缺失，前端可以给出"装 MySQL"
	// 引导；后者更泛，含义不清。
	ErrMySQLNotAvailable = errors.New("MySQL 子系统未配置，请先在安装脚本启用（INSTALL_MYSQL=1）")
)
