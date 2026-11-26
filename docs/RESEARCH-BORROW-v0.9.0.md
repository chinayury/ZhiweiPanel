# 智维面板 ZhiweiPanel 借鉴与实现记录（v0.9.0）

> 产出时间：2026-11-25　｜　minor 轮　｜　前几轮见 [RESEARCH-BORROW-v0.8.0.md](RESEARCH-BORROW-v0.8.0.md)
> 本轮主题：**平台级整体优化**——站点级功能已经比宝塔更细，真正落后的是宝塔的
> 面板安全、文件管理、数据库中心与可观测性闭环。

## 一、来源与缺口判断

**来源**：宝塔面板（面板设置/安全、文件管理、数据库页、网站监控报表）+ 本轮对
`BT-COMPARE-v0.2.0.md` 的复核。对照结论：

- 站点级 19 个页签、配置下发回滚、防篡改、2FA、审计、证书、计划任务、备份都不缺；
- 缺的是平台级高频闭环：**安全入口/IP 白名单、文件管理增强、数据库中心、流量统计准确性**；
- 两处「已承诺但没兑现」必须先修：`panel_port` / `panel_path` 只存库不生效；
  流量采集每 5 分钟重读 5 万行并累加，统计只会虚高。

**明确不做**（不符合"只做建站管理"或本轮装不下）：一键部署模板、站点克隆、
远程备份（S3/OSS/WebDAV）、Web 终端、系统防火墙、文件回收站、phpMyAdmin 表编辑、
多用户/权限、在线升级、面板 TLS 可视化。

## 二、关键设计决策

| 决策 | 理由 |
|------|------|
| 安全入口只遮蔽 UI，`/api` 与 `/ws` 保持无前缀 | 既有 API Token、CI、Docker HEALTHCHECK 全走 `/api/auth/me`；给 API 加前缀是破坏性变更。入口是遮挡不是认证 |
| IP 白名单对所有路径生效，回环永远放行 | 白名单是真正的网络层限制；回环是管理员 SSH 隧道/本机 curl 的自救通道 |
| 安全设置仅浏览器会话可改 | 一个泄漏的 write Token 不应能把管理员锁在门外（与 2FA 管理端点同一原则） |
| 保存时用真实客户端 IP 做自锁校验 | 配错白名单的代价是永久失联；服务端在写入前拒绝"当前 IP 不在名单内" |
| 数据库只管理 `site_*`，系统库在任何动作前 404 | 宁可少做，也不让 mysql/sys 进入 SQL/备份路径 |
| 导入/导出走 `mysqldump` / `mysql` 客户端 | 大文件、DELIMITER、存储过程用驱动逐条执行既慢又易错；复用宿主机既有客户端，不引新依赖 |
| 流量采集用 `dev/ino + offset + 64 字节采样` | 只比 offset 会漏掉 copytruncate 后长回原大小；采样能在不读全文的情况下识别改写 |
| 首次采集从文件尾起步 | 升级前的历史数字已被旧逻辑重复计入，重算只会更糟；新站点在建站时预置 offset=0，从文件头开始 |
| 统计/IP/状态码/checkpoint 同事务 | 分开提交会在失败后重复累加；checkpoint 必须跟着数据一起推进 |
| 真 UV 落 `traffic_ips` 行 | 旧 `unique_ips` 只是单批末尾 5 万行的去重数且被覆盖，不是日 UV |
| 压缩先收集条目再写盘 | 否则压缩包自己可能落在被遍历的目录里，把自己压进去 |
| 远程下载拒绝回环/链路本地/元数据，允许私网 | 内网镜像与对象存储是真实需求；云元数据与回环是明确的 SSRF 高危目标 |

## 三、落地映射

| 项 | 产物 |
|----|------|
| 安全入口 + IP 白名单中间件 | `internal/api/panel_guard.go`、`internal/api/clientip.go`（复用客户 IP 信任规则）、`main.go`（启动读取 settings、`srv.Guard(mux)`） |
| 设置校验/事务写入/自锁 | `internal/api/misc.go`、`internal/store/store.go`（`SetSettings`） |
| 设置页安全区 + 默认项接线 | `web/js/views/settings.js`、`web/js/views/site-create.js`、`internal/service/service.go`（`CreateSite` 读 default_*） |
| 文件管理 2.0 | `internal/service/file_archive.go`（压缩/解压）、`internal/service/file_ops.go`（下载/远程/批量/chmod/搜索）、`internal/api/files.go`、`web/js/views/site-files.js` |
| 数据库中心 | `internal/mysql/databases.go`、`internal/service/databases.go`、`internal/api/databases.go`、`web/js/views/databases.js`、`web/js/app.js` |
| 流量准确化 | `internal/logmgr/incremental.go`（偏移+采样+按日聚合）、`internal/store/traffic.go`（事务批次/UV/状态码/reset）、`internal/service/service.go`（CollectTraffic）、`web/js/views/dashboard.js`、`web/js/views/site-detail.js` |
| 测试 | `panel_guard_test.go`、`traffic_test.go`（api/store/service）、`incremental_test.go`、`file_ops_test.go`、`databases_test.go` |
| 文档 | README 功能/参数/已知限制、USAGE 4.2/4.11/11.x/12/13、AGENT 端点与运行时约定、CHANGELOG v0.9.0 |

## 四、边界与已知代价

- **安全入口不是认证**：知道入口的人仍要过密码/2FA；入口只降低扫描器命中率。
- **API 不被入口遮蔽**：这是兼容性取舍；API 侧仍由 session/Token/限流/2FA 保护。
- **远程下载仍可能访问私网服务**：已拒绝回环/链路本地/元数据，但私网段是允许的；
  只应对可信 URL 使用（README 已知限制 #15）。
- **流量历史不自动重算**：升级后新数据准确，旧虚高需显式 reset（#17）。
- **数据库中心不碰系统库**：Docker 镜像无 mysql 客户端时导入/导出会明确报错（#16）。
- **文件操作不可逆**：批量递归删除有二次确认，但仍应只对测试站点做冒烟（#18）。

## 五、一句话结论

> 宝塔值得借鉴的不是"更多功能"，而是**把高频动作做成闭环**：入口要能藏、
> 设置要真生效、文件要能压缩解压、数据库要能改密导入导出、统计要经得起看。
> 本轮四块都只做闭环最小集，把"看起来有、实际不能用"的假功能换成
> 有边界、有测试、可回滚的真能力。
## 来源复核与取舍

- [宝塔访问控制](https://docs.bt.cn/user-guide/config/safe/login-and-access-control/)将安全入口和授权 IP 分开设置。面板分别校验 UI 路径和来源 IP，保留现有 API 地址。
- [1Panel](https://github.com/1Panel-dev/1Panel)将文件、数据库与主机监控放在同一管理界面。当前站点文件页和数据库页已覆盖这两类日常操作，容器应用市场超出建站管理范围。
- [File Browser](https://github.com/filebrowser/filebrowser)以指定目录作为文件管理范围。站点文件管理沿用站点根目录，避免另设文件服务和独立账户。
- Filebeat 的 filestream 输入为单条消息设置字节上限。增量流量采集沿用项目的 1 MiB 上限，分段跳过超长行，读到换行才推进偏移，保留后续请求。[Filebeat 文档](https://www.elastic.co/docs/reference/beats/filebeat/filebeat-input-filestream)
