# 智维面板 ZhiweiPanel 同类项目借鉴要点清单（v0.2.2）

> 产出时间：2026-10-24　｜　检索范围：GitHub + Gitee　｜　两阶段筛选
> 本文是**借鉴清单**（用户第一阶段指定产出物），末尾附 v0.2.2 的落地映射。

## 一、检索范围与方法

### 两阶段

| 阶段 | 做法 | 产出 |
|------|------|------|
| 第一阶段 · 初筛 | 12 个候选仓库全量下载 → 关键词能力矩阵（18 类能力 × 文件命中数）→ 按「架构同构度 / 成熟度 / 参考价值」淘汰 | 5 个进入深读 |
| 第二阶段 · 深读 | 定点读具体实现文件，核对「它怎么做的」而不只是「它有没有」 | 本文第二、三节 |

### 踩过的坑（方法论本身也是产出）

- **GitHub 未认证 API 只有 60 次/小时**，实测用到 36 次就耗尽 → 改用 `api.github.com` 树 API + `codeload.github.com` tarball + `raw.githubusercontent.com` 直取。
- **Gitee API v5 匿名 search 恒返回 `[]`**，极易被误判成「Gitee 上没有同类项目」→ 改走 `web_search site:gitee.com` 定位。
- **`curl` 被网络策略拦截，但 Python `urllib` 可直连** `api.github.com` → 检索脚本走 urllib 而非 curl。

### 候选清单与淘汰结论

| 仓库 | 语言 | 结论 | 理由 |
|------|------|------|------|
| `1Panel-dev/1Panel` | Go + Vue | ✅ 深读 | 架构同构：Go 后端 + 独立前端 + 面板式管理 |
| `cenvero/fleet` | Go | ✅ 深读 | 采集 agent + 配置漂移检测，运维层思路可移植 |
| `goinfinite/os` | Go | ✅ 深读 | ★360，小型 Go 面板，「agent 可操作面板」范式最贴近 |
| `filebrowser/filebrowser` | Go | ✅ 深读 | 权限规则 DSL，是 API Token scope 的另一种解法 |
| `svrforum/SFPanel` | Go | ✅ 深读 | 审计中间件 + 路由组织 |
| `aaPanel/aaPanel` | **Python**（1081 个 .py） | ❌ 淘汰 | 核心是 Python 不是 Go，借鉴价值只剩产品思路 |
| `caddyserver/caddy` | Go | ❌ 淘汰 | HTTP 服务器不是面板 |
| `traefik/traefik` | Go | ❌ 淘汰 | 反向代理不是面板 |
| `Run-Panel/RunPanel` | Go | ❌ 淘汰 | LXC/Incus 虚拟化方向，产品定位不重叠 |
| `sailboxhq/sailbox` | Go | ❌ 淘汰 | 容器编排方向 |
| `momaek/tolato` | Go | ❌ 淘汰 | 项目体量与定位差异过大 |
| `louislam/uptime-kuma` | TS | ❌ 淘汰 | 只做可用性监控，非建站面板 |

**Gitee 候选**：`qkpanel/qkpanel`、`PushengHe/1Panel`（1Panel 镜像，冗余）、`ten/ServerStatus`、`todayliao/quick-env-deploy-go-edition`、`yanfanVIP/fnproxy-panel` —— 均未发现 ZhiweiPanel 未覆盖且架构相容的实现。

### 关键初筛结论

> **aaPanel 看着像同类，其实核心是 Python。** 它的「进程管理」「计划任务」在 Go 里对应的是完全不同的技术栈。
> 真正值得借鉴的是 **1Panel / cenvero-fleet / goinfinite-os** 这类**小型 Go 面板**——它们与 ZhiweiPanel 的
> 「单二进制 + 少量依赖 + 运维层能力」约束最接近。

---

## 二、可借鉴要点（深读提炼）

### A. 系统监控

**来源**：`1Panel/backend/app/service/monitor.go`、`cenvero/fleet/internal/agent/metrics.go`

| 要点 | 同类项目做法 | 对 ZhiweiPanel 的价值 |
|------|-------------|-------------------|
| 速率用**两次采样差值** | 1Panel 存累计计数器，查询时用 `now - 上次` 求增量 | ✅ 直接借鉴 |
| Swap **单独取**，不混进内存 | fleet 的 `metrics.go` 把 Swap 从内存指标里拆出来 | ✅ 直接借鉴 |
| 监控历史**有保留期上限** | 1Panel `MonitorStoreDays` 定期清理 MonitorDB | ✅ 借鉴思想、**改实现方式** |
| 采样与查询**分离** | 1Panel：cron 写库，接口读库 | ⚠️ 部分借鉴 |

**明确不借鉴的**：
- ❌ **gopsutil**：会拖进 `net/ebpf/cilium` 等依赖链，与 `CGO_ENABLED=0` 单二进制约束冲突。Linux 上 CPU/内存/负载/网络全在 `/proc`，直接读最省。
- ❌ **1Panel 的 `cpu.Percent(3*time.Second)`**：**阻塞式**采样，会卡住调用方 3 秒。1Panel 放在定时任务里能忍，ZhiweiPanel 要放在 HTTP 处理器里就绝对不行。
- ❌ **独立 MonitorDB**：ZhiweiPanel 已有 SQLite 主库，再开一个库要多一套连接管理；而监控只是「看个当前值 + 一小段曲线」，不值得。

**ZhiweiPanel 的取舍**：内存环形缓冲（120 条 × 10 秒 ≈ 最近 20 分钟），落 DB 的三个代价（主库膨胀 / 备份变慢 / 再写一套清理逻辑）全免。**代价是重启后曲线从头开始** —— 对这个用途够用。

### B. 配置历史与回滚

**来源**：`1Panel/backend/app/api/v1/snapshot.go`（snapshot 表）、`cenvero/fleet/internal/core/agent_autosync.go`（配置漂移检测）

| 要点 | 同类项目做法 | 对 ZhiweiPanel 的价值 |
|------|-------------|-------------------|
| 配置下发后留**可回退的历史** | 1Panel snapshot 表存整份配置 | ✅ 直接借鉴（ZhiweiPanel 原先只有内存级 rollback，无历史可浏览） |
| **检测配置漂移** | fleet 对比期望值与实际值 | ✅ 借鉴思想 → v0.2.2 用「与当前生效配置 diff」实现 |
| 回滚**只回滚配置**，不动业务数据 | — | ✅ ZhiweiPanel 采纳：只恢复 nginx 配置文件，**不回滚数据库** |

**ZhiweiPanel 的取舍**：
- 快照**存盘不存 DB**（配置文件几十 KB，入 SQLite 会让主库和备份一起膨胀）；保留最近 30 份。
- 回滚**只恢复快照里存在的文件，当前多出来的文件不动** —— 多出来的通常是快照之后新建的站点配置，删掉会连带把新站点从 nginx 里摘掉。
- 回滚前先备份当前内容，nginx 检测失败则原样退回。
- 快照**只在配置下发成功后生成** —— 失败的中间态没有参考价值，反而会让人回滚时选错版本。

### C. API Token / 机器鉴权

**来源**：`filebrowser/rules/rules.go`（权限规则 DSL）、`SFPanel/internal/api/middleware/auth.go`

| 要点 | 同类项目做法 | 对 ZhiweiPanel 的价值 |
|------|-------------|-------------------|
| 凭据**只存摘要** | 通行做法 | ✅ 直接借鉴 |
| 机器凭据与浏览器会话**分离** | SFPanel 用不同中间件 | ✅ 直接借鉴 |
| 权限**规则化** | filebrowser 用 DSL 表达规则 | ⚠️ **过度设计，未采纳** |

**ZhiweiPanel 的取舍**：
- 只存 **SHA-256 摘要**，另存 prefix（前 8 位）仅供展示；明文只在创建响应出现一次；吊销即删行不做软删除。
- 格式 `zw_` + 32 字节 base64url（无 padding，能安全放进 URL / HTTP 头 / shell 变量）。
- scope 语义：**空 = 全权限**；`read` 只能 GET；`write` 才能 POST/PUT/DELETE；写操作 scope 不足返回 403。
- ❌ **不引入 filebrowser 的规则 DSL**：ZhiweiPanel 只有一个使用者（管理员本人 + 自动化脚本），DSL 是纯负债。这符合「只有一个使用方时抽象层是纯负债」的取舍。

### D. 操作审计

**来源**：`1Panel/backend/app/model/logs.go`、`SFPanel/internal/api/middleware/audit.go`

| 要点 | 同类项目做法 | 对 ZhiweiPanel 的价值 |
|------|-------------|----------------|
| 审计作为**中间件**统一挂载 | SFPanel | ✅ 直接借鉴 |
| 审计写在**鉴权之后** | 拿到调用者身份 | ✅ 直接借鉴 |
| 记录**响应码** | SFPanel `statusRecorder` | ✅ 直接借鉴 |

**ZhiweiPanel 的取舍**：
- **只记写操作（POST/PUT/PATCH/DELETE）+ 登录事件**。读操作量大价值低，全记会撑爆表、淹掉真信号。
- 审计写失败**绝不影响主流程**（辅助设施不能把用户的正常操作搞失败）。
- 上限 5000 条，每 200 次写做一次清理（避免每次都 `COUNT(*)`）。
- `principal` 走 request context 传递，审计中间件不重复查库。

### E. 迁移与幂等

**来源**：`1Panel/backend/init/migration/migrate.go`

- gormigrate 的**有序幂等迁移列表** —— 每一步独立、可重复执行、失败可定位。
- ZhiweiPanel 的 `store.migrate()` 采用同构思路：`CREATE TABLE IF NOT EXISTS` + 索引同批建，旧库零手工步骤。

### F. Agent 可操作面板（暂未实现，v0.2.3+ 候选）

**来源**：`goinfinite/os/SKILL.md` + `AGENTS.md`

该项目的核心主张是：**面板不只给人用，也要能被 AI agent 安全操作**。做法是把「这个面板能做什么、有哪些坑、怎么操作」写成一份结构化的 `SKILL.md` + `AGENTS.md`，让 agent 能安全地驱动面板而不需要把整套运维知识塞进提示词。

**价值**：ZhiweiPanel 已有 API Token（机器可调用），缺的正是「一份权威的操作说明书」。这是 v0.2.3 的高价值候选。

---

## 三、明确不借鉴的（连同理由）

| 能力 | 同类项目都有 | ZhiweiPanel 不做 | 理由 |
|------|------------|--------------|------|
| 终端 / PTY | 1Panel、fleet、SFPanel | ❌ | 高危攻击面（root shell 暴露到 Web），与「只做网站管理」定位冲突 |
| 文件管理器 | 全部 | ❌ | 已有站点文件管理，完整 FM 是独立产品 |
| Docker / Compose | 1Panel、fleet | ❌ | 定位取舍（README 已声明不做） |
| 防火墙管理 | 1Panel、SFPanel | ❌ | 与宿主机安全组职责重叠，面板越权管防火墙风险大 |
| WAF | 1Panel、SFPanel | ❌ | 规则维护成本高，v0.2.0 已有站点级 WAF 类规则 |
| 2FA / TOTP | 1Panel、fleet、SFPanel | ❌ | v0.2.3+ 候选，当前单管理员场景优先级低于运维层 |
| S3 / 远程存储 | 1Panel、fleet | ❌ | 需云 SDK + 凭据管理 |
| 多租户 / RBAC | SFPanel、fleet | ❌ | 定位是单机单管理员 |

---

## 四、v0.2.2 落地映射

本轮（v0.2.2）实现的 4 项功能，全部来自上表，且**零新依赖**。

| 功能 | 借鉴来源 | 关键实现文件 | 偏离之处（及原因） |
|------|---------|------------|------------------|
| **A. 系统资源监控** | 1Panel monitor.go、fleet metrics.go | `internal/sysinfo/`（sysinfo.go / disk_linux.go / disk_other.go） | 不用 gopsutil（依赖冲突）；不用阻塞采样（HTTP 处理器不能卡 3 秒）；不落 MonitorDB（内存环形缓冲） |
| **B. 配置历史 + 回滚 + diff** | 1Panel snapshot.go、fleet autosync | `internal/confhistory/`、`internal/service/ops.go` | 快照存盘不存 DB；只恢复 nginx 配置不回滚 DB；保留 30 份 |
| **C. API Token** | filebrowser rules、SFPanel auth | `internal/model/ops.go`、`internal/store/ops_store.go`、`internal/api/principal.go`、`internal/api/tokens.go` | 不用规则 DSL（单使用者，抽象是负债）；scope 只做 read/write 两级 |
| **D. 操作审计** | 1Panel logs.go、SFPanel audit middleware | `internal/api/audit.go`、`internal/store/ops_store.go` | 只记写操作 + 登录；上限 5000 条 |

### 新增 API 契约

```
GET    /api/system/metrics                      → {current, history}
GET    /api/system/config-history               → {history: [...]}
GET    /api/system/config-history/{id}          → {snapshot, contents}
GET    /api/system/config-history/{id}/diff     → {diffs: [...]}
POST   /api/system/config-history/{id}/restore  → {restored, message}
GET    /api/tokens                              → {tokens: [...]}
POST   /api/tokens                              → {token:{...,token}, show_token:true}
DELETE /api/tokens/{id}                         → 204
GET    /api/audit-logs?limit=&action=           → {logs, total, limit}
```

### 测试覆盖

> 用例数为 `go test -v` 实测（2026-10-24），非估算。

| 包 | 用例数 | 覆盖要点 |
|----|-------|---------|
| `internal/sysinfo` | 12 | /proc 各解析器 + 负例（脏数据、字段不足、计数器回退、首次采样无基线、MemAvailable 缺失、老内核回退口径） |
| `internal/confhistory` | 14 | 快照/列表/回滚/裁剪 + **路径穿越防护** + diff 各形态 |
| `internal/store` | 12（新增） | Token 摘要存取（读路径不返回明文）、审计表增删查、裁剪 |
| `internal/api` | 8 | 审计动作推导、scope 判定、token 提取各种写法、随机 token 格式 |
| `internal/model` | 2 | scope 语义、过期判定 |
| `internal/service` | 3（新增） | **启动阻塞回归测试**（`StartMetricsCollector` 必须立刻返回）+ 后台采集产出样本 + 未采样安全查询 |

全仓合计 176 通过 + 1 跳过（本轮新增 51 个用例）。`internal/sysinfo` 的 `TestCollectorSample`
需要 `/proc`，仅 Linux 可跑，在 Windows 上跳过（解析逻辑由同包的纯函数用例覆盖）。

---

## 五、后续候选（v0.2.3+）

| 候选 | 来源 | 价值 | 工作量 |
|------|------|------|--------|
| **SKILL.md / AGENTS.md 操作说明书** | goinfinite/os | 高 —— 让 AI agent 能安全驱动面板，配合 API Token 打通闭环 | 小 |
| 2FA / TOTP | 1Panel、fleet | 中 —— 等保要求 | 中 |
| 监控历史落盘（可选） | 1Panel MonitorDB | 低 —— 20 分钟窗口够用，除非用户要求长期留存 | 中 |
| 配置漂移主动告警 | fleet autosync | 中 —— 快照 diff 已具备能力，差一个定时比对 + 通知 | 中 |
| 审计导出（CSV / JSON） | 通用 | 中 —— 等保报表要交 | 小 |

---

## 六、一句话结论

> 同类项目（1Panel / fleet / goinfinite-os）在**站点管理**层已经做得很深，ZhiweiPanel 在这一层并不落后；
> 真正的差距集中在**平台运维层**——监控、配置历史、机器鉴权、操作审计。
> v0.2.2 一次性补齐这四项，且**零新依赖、保持单二进制**。
> 下一批值得借鉴的，是 goinfinite/os 的「面板可被 agent 操作」范式。
