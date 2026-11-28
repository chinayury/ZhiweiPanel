# 站点数据库授权与容器运行环境

站点库名包含下划线。在数据库级授权中，MariaDB 和采用默认设置的 MySQL 会将下划线解释为通配符。面板为新站点授权时按字面值处理库名，并兼容 MySQL 开启 `partial_revokes` 后的匹配规则。[MySQL GRANT](https://dev.mysql.com/doc/refman/8.4/en/grant.html) · [MariaDB GRANT](https://mariadb.com/docs/server/reference/sql-statements/account-management-sql-statements/grant)

Alpine 3.20 的常规支持已于 2026 年 4 月 1 日结束。面板 Docker 镜像使用 Alpine 提供的 nginx、curl 和系统库，选择 3.24 分支后可继续获取软件包更新。该分支的支持截止日期为 2028 年 6 月 1 日。[Alpine 分支支持表](https://www.alpinelinux.org/releases/)

| 来源 | 项目取舍 |
| --- | --- |
| [HestiaCP 单库授权修正](https://github.com/hestiacp/hestiacp/pull/6063) | 创建站点时对目标库精确授权，读取服务器匹配规则失败时向调用方返回错误。 |
| [HestiaCP 单节点 DNSSEC](https://github.com/hestiacp/hestiacp/pull/6084) | 适用于托管权威 DNS 区域的服务。面板通过 DNS 提供商完成 ACME 验证，区域签名由提供商管理。 |
| [1Panel 建站时保留目录内容](https://github.com/1Panel-dev/1Panel/pull/14042) | 建站和重新下发配置时已检查已有首页。 |
| [Infinite OS 操作说明](https://github.com/goinfinite/os/blob/main/SKILL.md) | 项目已有面板 API 权限和写操作说明。 |
