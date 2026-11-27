# 裸机安装的发行版兼容性

裸机安装器允许系统包管理器在首选 PHP-FPM 版本不可用时安装默认包。不同发行版的默认 PHP 版本、二进制名称和 systemd 单元名可能不同。安装器需要根据实际包选择服务，否则机器重启后 PHP 站点可能返回 502。

MySQL 驱动 v1.11.0 将支持下限提高到 MySQL 8.0 和 MariaDB 10.11。安装器使用系统提供的 MariaDB，其中 Debian 11 提供 10.5。本版保留 v1.10.2，后续升级需先明确面板支持的数据库版本。[驱动发行说明](https://github.com/go-sql-driver/mysql/releases/tag/v1.11.0) · [Debian 软件包](https://packages.debian.org/bullseye/mariadb-server)

| 来源 | 项目取舍 |
| --- | --- |
| [1Panel 的客户端 IP 处理](https://github.com/1Panel-dev/1Panel/pull/14343) | 面板的登录和审计已经共用可信代理解析。 |
| [HestiaCP 的备份机制](https://github.com/hestiacp/hestiacp/blob/main/docs/docs/server-administration/backup-restore.md) | 面板保留本地备份与保留份数配置。远程增量备份涉及额外的存储配置和工具，暂缓。 |
| [Infinite OS](https://github.com/goinfinite/os) | 现有 Docker 部署已覆盖单容器交付。 |
