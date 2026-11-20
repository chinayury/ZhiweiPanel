# 智维面板 ZhiweiPanel 借鉴与实现记录（v0.7.0）

> 产出时间：2026-11-21　｜　minor 轮　｜　前几轮见 [RESEARCH-BORROW-v0.6.0.md](RESEARCH-BORROW-v0.6.0.md)
> 本轮主题：**指标持久化**——P2 待排清单里最后一个高价值项，把"重启后监控曲线清零"补成"近 72 小时趋势跨重启存在"。

## 一、来源与设计

**来源**：1Panel 的监控实现（SQLite 落库 + 可配置保留天数 MonitorSaveDays + 定时清理任务 + 手动清空；
v2 的 device_clean.go 统一清理监控数据）。1Panel 官方还提供过"数据量过大清一半"的社区工具——
落库方案最大的坑就是无界增长，保留策略必须是一等公民而不是事后补丁。

**关键设计决策**（与本仓库 v0.2.2 时的"不落库"决策正面衔接）：

| 决策 | 理由 |
|------|------|
| 双层结构：内存十秒级（实时毛刺）+ 落库分钟级（跨重启趋势） | 当年选内存环形缓冲的用途"看最近半小时"仍然成立，不动它；只补"看昨天"这一段。删掉内存层会让实时曲线粒度变粗，删掉落库层就是回到本轮之前 |
| 分钟粒度，不是十秒级 | 十秒级落库一天 8640 行；分钟级 1440 行/天，72h 保留 ≈ 4300 行上限，主库与备份体积影响可忽略。仪表盘看的是趋势不是逐秒毛刺 |
| 只落 7 个数（ts/cpu/mem/load1/disk/net_rx/net_tx），不整包序列化 Snapshot | 监控历史只是给人看曲线；完整 Snapshot 会把表撑大 5~8 倍，而 hostname/process_count 等字段对画曲线没有价值 |
| ts 用 Unix 秒做主键 + ON CONFLICT 覆盖 | 范围查询免字符串转换；重放/补写天然幂等，不留重复行 |
| 保留期写死 72 小时 + 启动/每小时清理 | 1Panel 把保留天数做成用户配置——对一个轻量面板是过度配置：72h 覆盖"重启后看昨天"，表体积恒定可控，少一个设置项就少一分误用面（配 365 天会把表拖到 50 万行） |
| 采集器（sysinfo）仍然不知道 DB 的存在 | 分层不倒退：落库是 service 层的独立一分钟节拍，从采集器取最新快照；sysinfo 保持纯采集，单测照旧不依赖 SQLite |
| 落库循环放后台 goroutine、立即返回 | 与 StartMetricsCollector 同型的硬规则——v0.2.2 首次部署"进程活着端口不在"的事故就是同步调用死循环卡住 main() 造成的 |
| 前端降采样到 ≤90 点再画 | lineChart 每个点渲染一个圆点 + 一个 X 轴标签，1440 点 = 近 3000 个 SVG 节点且标签叠死；90 点既流畅又可读，且不引图表库 |

**明确不做**：保留天数用户配置（YAGNI，挂 P3 观察）、持久化数据的管理员导出（监控是辅助设施，
审计日志已有导出通道）、主机名/进程数等维度的落库（曲线用不到）。

## 二、落地映射

| 项 | 产物 |
|----|------|
| 采样模型 | `internal/model/metrics.go`（新）：MetricsSample 七字段 |
| 表 + 存取 + 清理 | `internal/store/store.go`（schema）/ `internal/store/metrics.go`（新）：Save（幂等覆盖）/ List（区间升序）/ Prune |
| 落库节拍 | `internal/service/metrics.go`（新）：StartMetricsRecorder（立即返回）/ record（分钟对齐）/ prune（72h） |
| 读端点 | `internal/api/ops.go`：`GET /api/system/metrics/history?hours=N`（缺省 24、钳位 [1,72]）+ 路由 |
| 启动挂载 | `main.go`：StartMetricsCollector 之后 StartMetricsRecorder |
| 仪表盘"近 24 小时"曲线 | `web/js/api.js` + `web/js/views/dashboard.js`（drawDailyHistory：降采样 + 复用 lineChart） |
| 测试 | store 往返/幂等覆盖/清理、service（未采样跳过、后台写库、模拟重启后仍在、hours 钳位）、api（401/空数据/窗口外不可见/升序/非法 hours/405） |
| 文档 | README 功能表指标行、USAGE 仪表盘行、AGENT.md 只读端点、CHANGELOG v0.7.0 |

## 三、一句话结论

> 指标持久化的难点不在"往库里写一行"，而在**写多少、留多久、谁来清**：
> 分钟粒度 × 只留 7 个数 × 72 小时保留 + 周期清理，三个决定把 1Panel 踩过的
> "监控数据撑爆主库、要出社区工具手动清一半"的坑全部避开；
> 分层上落库是 service 的独立节拍，采集器保持零 DB 依赖——旧决策（不落库）
> 与新决策（持久化）各管一条数据链路，互不推翻。
