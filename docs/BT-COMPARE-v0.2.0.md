# 智维面板 ZhiweiPanel vs 宝塔面板 差异分析（v0.2.0 规划）

> 基于宝塔官方文档（docs.bt.cn / docs.bt.cn/api/tags/tag-riltys） + 2026 年第三方对比（vpscost.com、blog.ctzpj.com）综合。

## 一、定位差异

| 维度 | 宝塔 | ZhiweiPanel |
|------|------|-----------|
| 体量 | 200+ 插件，胖 | 单一二进制 13MB，瘦 |
| 架构 | Python + shell 脚本，胶水风格 | Go 单进程，渲染 + 校验统一 |
| 价值主张 | 一站式全栈管理 | 只做"网站管理"这一件事 |
| 双运行时 | 仅宿主机 | 宿主机 + Docker 统一驱动 |

ZhiweiPanel 明确不做（v0.1.0 README 第 3 行）：
- FTP、Docker、邮件、PostgreSQL、Redis、Memcached、软件商店
- 这些是有意取舍，不在借鉴范围

## 二、运维刚需类：宝塔有，ZhiweiPanel 缺

按"使用频率 / 价值 / 工作量"排序，**本轮 v0.2.0 要做的**：

| # | 功能 | 宝塔做法 | 价值 | 工作量 | 本轮做? |
|---|------|---------|------|--------|---------|
| 1 | 计划任务（Cron） | 12 种类型，crontab 表达式 + 简易可视化 | **极高** | 中 | ✅ |
| 2 | 网站/数据库备份 | tar.gz 打包 + mysqldump + 保留 N 份 | **极高** | 中 | ✅ |
| 3 | 站点到期时间（edate） | 时间戳 + UI 角标 + 列表高亮 | 高 | 极小 | ✅ |
| 4 | 404 页面自定义 | `error_page 404 /404.html;` + 上传 | 高 | 小 | ✅ |
| 5 | 站点运行目录（public/） | fastcgi_param SCRIPT_FILENAME 指向子目录 | 高（Laravel/ThinkPHP 刚需） | 小 | ✅ |
| 6 | 批量启停 | 多选 + 启停/删除/备份 | 中 | 小 | ✅ |

**本轮 v0.2.0 不做**（要么不需要，要么工作量超出单次 PR 范围）：

| 功能 | 不做原因 |
|------|---------|
| WAF / 防 CC / 地区限制 | 工作量大且需规则维护，单 PR 装不下 |
| 监控历史图表 | 需要 metric 存储 + 图表库，跟"告警接线"合并到 v0.2.1 |
| 远程存储（OSS/COS） | 需要云 SDK + 凭据管理，先用本地存储 |
| 多版本 PHP 共存切换 UI | driver.PHPVersions 已能列版本，UI 留 v0.2.1 |
| Tomcat / Java 项目 | 项目定位不含 |
| phpmyadmin 集成 | 安全风险大，建议用户自建 |
| 修改 MySQL 密码 | 单端点，下次 v0.2.1 |
| 导入 SQL | 同上 |

## 三、本轮 v0.2.0 设计

### 1. 计划任务（cron_jobs 表）

```
表: cron_jobs
  id, name, type, enabled, schedule, target, payload (JSON),
  last_run_at, last_status, last_msg, next_run_at,
  created_at, updated_at

type ∈ {
  shell,           // 自由 shell（受限命令：shutdown/init 0/mkfs/passwd 等禁）
  backup_site,     // 备份指定站点
  backup_db,       // 备份指定数据库
  log_split,       // 切割站点访问日志
  release_mem,     // 释放 PHP/MySQL/Nginx 内存（drop_caches）
  access_url,      // 访问 URL
}
schedule: 标准 5 段 crontab 表达式（m h d M w）
```

后台 goroutine 每 30s 扫 `next_run_at <= now AND enabled=1`，把命中任务加锁、设 `last_status=running`、fork 执行。
执行结束写 `last_status=ok|failed`、`last_msg`、算下次 `next_run_at`、写 `cron_runs`（执行历史）。

为避免自己实现 cron 解析出错，用 `github.com/robfig/cron/v3`（被广泛使用，small deps）。

### 2. 备份（backups 表 + 手动入口）

```
表: backups
  id, kind ('site'|'database'), target (site_id|db_name),
  target_name, size, path, encrypted, note, created_at
存储: /opt/zhiweipanel/data/backup/{site,database}/<id>.tar.gz
```

- 站点备份 = tar -czf 整个 site.Root.Path + nginx conf 副本
- 数据库备份 = mysqldump --single-transaction + gzip（密码从 cnf 读）
- 保留份数：cron_job.payload 配 `retain: 3`，按 created_at 留 3 删旧
- 恢复：解包到 site.Root.Path，**前端按钮 + 二次确认**

### 3. 站点小加项

- **edate（到期时间）**: `sites` 表加 `edate TIMESTAMP`；列表里未来 7 天内的标黄，过期标红
- **404 page**: `site.Extra.Custom404 string` + `error_page 404` 渲染
- **run_path（运行目录）**: `site.Root.RunPath string`（相对 root.path），用于 `fastcgi_param SCRIPT_FILENAME $document_root/run_path$fastcgi_script_name`
- **批量启停**: `POST /api/sites/batch` 接 `{action: "start|stop|delete", ids: [...]}`

## 四、v0.2.0 交付物

1. DB schema 升级（向后兼容：旧库 CREATE IF NOT EXISTS + ALTER 补列）
2. `internal/cron` 新包：解析器 + 调度器 + 5 种内置 handler
3. `internal/backup` 新包：备份/恢复
4. `internal/api/cron.go` + `internal/api/backup.go`：REST 端点
5. `web/js/views/cron.js` + `web/js/views/backup.js`：UI
6. `docs/CHANGELOG.md` + `README.md`：更新

## 五、风险与权衡

- **crontab 解析**: 选 `robfig/cron/v3` 第三方库而不是自己写。代价是 +50KB 二进制；收益是不会因边界 case 误执行任务。
- **shell 命令白名单**: v0.2.0 禁掉 `shutdown / init 0 / mkfs / passwd / chpasswd / reboot / halt / poweroff`，跟宝塔策略一致。其它命令允许。
- **备份加密**: v0.2.0 只做"普通 tar.gz"，加密（zstd + password）留 v0.2.1。
- **备份容量**: 不限制总量（用户自己用保留份数控）；但加 100MB 单份上限防爆盘。
- **执行日志**: 5 类任务最近 50 条执行记录，超出滚动删除。

## 六、不影响 v0.2.0 主轴的决策

- 保留 v0.1.0 现有前端 hash 路由；cron/backup 视图加进 `app.js` 路由表
- 通知渠道复用 `internal/alert` 现有通道，cron 失败直接 alert.send
- 数据迁移：所有新表 `CREATE TABLE IF NOT EXISTS`，旧库不需要任何手工步骤
