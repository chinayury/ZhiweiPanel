# SQLite 驱动与旧库兼容性

面板持久层使用 database/sql 和 WAL，限制为单个连接。SQLite 驱动升至 v1.63.0，并按上游要求将 modernc.org/libc 对齐到 v1.80.1。该版可区分空 BLOB 和 NULL，查询时减少逐列读取的锁操作。沿用面板原有的同步、事务和数据库连接设置。[驱动发行说明](https://gitlab.com/cznic/sqlite/-/blob/v1.63.0/CHANGELOG.md)

| 项目 | 取舍 |
| --- | --- |
| [1Panel](https://github.com/1Panel-dev/1Panel) | 数据库管理已有独立入口，此次升级只涉及面板自身的持久层。 |
| [HestiaCP](https://github.com/hestiacp/hestiacp) | 文件管理独立于内部数据库驱动，沿用现有文件接口。 |
| [Infinite OS](https://github.com/goinfinite/os) | 保留单容器入口，继续以静态二进制分发。 |

x/crypto 的候选更新主要涉及 SSH 与 OpenPGP。面板直接导入 bcrypt，此包在两个标签间没有源码差异，留待单独评估。[上游变更](https://github.com/golang/crypto/compare/v0.58.0...v0.59.0)
