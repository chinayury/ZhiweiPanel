# 数据库连接与部署版本

[MySQL 驱动发行说明](https://github.com/go-sql-driver/mysql/releases/tag/v1.10.2)记录了初始握手包不完整时的异常退出修复。面板数据库管理直接使用该驱动，本次采用补丁升级，沿用原有配置。

| 来源 | 取舍 |
| --- | --- |
| [1Panel](https://github.com/1Panel-dev/1Panel) | 站点、数据库与文件管理已有对应入口。应用市场超出本次数据库连接修复范围。 |
| [HestiaCP](https://hestiacp.com/docs/server-administration/file-manager) | 站点目录已有文件管理，引入独立 SFTP 子系统会增加部署依赖。 |
| [Infinite OS](https://github.com/goinfinite/os) | 项目已有容器入口与调用方文档，沿用镜像内置版本，避免每次发布同步多个默认值。 |
