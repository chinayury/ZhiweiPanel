# 智维面板 ZhiweiPanel 版本迭代说明

## 版本策略

- 采用 SemVer：`MAJOR.MINOR.PATCH`
- **版本号锁定**：一旦声明 `vX.Y.Z`，该版本即锁定，发布流程**不得**自动递增
- 版本号单一权威源：`internal/version/version.go` 的 `const Version = "0.1.0"`
  （`main.go` 里是 `var Version = version.Version`——这样 `-ldflags "-X main.Version=..."` 也能覆盖，
  但**改版本号请只改 `internal/version/version.go` 这一处**，否则常驻门禁测试会失败）
- **常驻门禁**：`internal/version/version_test.go` 在每次 `go test ./...` 时校验
  全仓版本锚点（`internal/version/version.go`、`main.go`、`deploy/Dockerfile`、
  `deploy/docker-compose.yml`、`README.md`）与权威源一致，任何一处漂移都会让测试变红
- 历史版本单独登记在 `internal/version/version.go` 的 `History` 变量里，
  **不参与**锚点对齐校验——历史版本号是档案，不是当前版本

---

## ⚠ 版本线重置说明（2026-10-28）

本项目在 2026-10-13 发布过 `v0.1.0`（"首个正式稳定版"），2026-10-16 起进入 `v0.1.1`，
2026-10-17 起改走 `v0.2.x` 线并一路做到 `v0.2.2`（已双机上线）。

由于 `v0.2.x` 的功能集已经**远超**当年那个"v0.1.0"，继续在 `v0.2.x` 上迭代会让
"下一个稳定版到底是 v0.2.3 还是 v0.3.0"变得含糊。经确认，**版本线在此重置**：

> **`v0.1.0` = 当前代码的正式稳定版**（2026-10-28），即把 `v0.2.2` 的完整功能集
> 加上本轮全量审计与加固的结果，统一以 `v0.1.0` 发布。

因此：

- `v0.1.0`（2026-10-13）与 `v0.1.1`（2026-10-16，未发布）这两个**历史条目被本版本取代**，
  其内容保留在下方《历史版本档案》里，不删除、不篡改；
- `v0.2.0` / `v0.2.1` / `v0.2.2` 的详细记录**原样保留**在本文件后半部分，
  它们是真实发生过的发布，档案性质；
- 从本版本起，版本线只有一条：`0.1.x`。后续迭代为 `0.1.1`、`0.1.2`……

---

## v0.1.5 — 文档与交付加固：已知限制勘误、AGENT 操作说明、CI nginx 断言（2026-11-15）

**定位**：文档 + CI 改动。**无代码行为变更、无新增用户可见功能、无破坏性变更**，
版本号走 patch。

### 修正

| # | 问题 | 核实方式 | 处置 |
|---|------|---------|------|
| 1 | README「已知限制」#14（防篡改基线无 UI 入口）与实现不符 | `git grep` + 运行中的面板实机验证：`site-detail.js` 防复改页签含「建立防篡改基线」按钮（含脏检查/启用检查/确认弹窗），`api.js` 含 `establishBaseline`，后端 `POST /api/sites/{id}/baseline` 返回 `{files:n}` 与前端一致 | 从限制清单移除 |
| 2 | README「已知限制」#15（`shell` scope 只能 API 授予）与实现不符 | `ops.js` Token 弹窗含 read/write/shell 三个独立授权位（shell 蕴含 write 的派生逻辑与注释齐全），列表页以 `danger` 色标签展示 | 从限制清单移除 |

两项均为**文档漂移**：功能实现先于文档更新，限制清单没跟着改。清单从 16 条收敛为 14 条
（重新编号）。上一轮借鉴文档（`RESEARCH-BORROW-v0.1.4.md`）中「下一轮 P0」的排期据此作废，
该文件作为历史计划文档不改写，由 `RESEARCH-BORROW-v0.1.5.md` 负责勘误。

### 新增

- **`docs/AGENT.md`**：面向 AI agent / 自动化脚本的面板操作说明
  （借鉴 goinfinite/os 的 SKILL.md 范式，见 `RESEARCH-BORROW-v0.1.5.md`）。
  内容：Token 认证与 scope 语义、有副作用端点速查（★ 标注 shell 要求）、只读端点、
  8 条运行时约定（串行下发流水线 / 坏站点隔离 / 回滚不回滚 DB / 建基线顺序 / 登录限流等）、
  agent 行为红线。所有关键表述经源码核实。

### 改进

- **CI Docker job 增加 nginx 存活断言**（`ci.yml`）：面板 API 200 ≠ 容器内 nginx 活着
  （面板只 reload、从不 start nginx）。补三条断言——pid 文件指向的 master 进程存活、
  `nginx -t` 通过、启动日志无「初始配置下发失败」——堵住「CI 全绿但 nginx 没跑」的验证盲区。

### 兼容性说明

- 无破坏性变更；面板数据与配置格式不变。
- README「已知限制」从 16 条变为 14 条：**是勘误不是功能下线**，两项能力一直都在。

---

## v0.1.4 — 修复：Docker 部署开箱即用；新增发布流水线（2026-11-14）

**定位**：缺陷修复 + 交付层建设。项目公开后的**首个带 Tag 与 Release 产物的版本**
（此前 `0.1.x` 各版只有源码，没有可下载的二进制）。无新增用户可见功能、无破坏性变更。

### 修复

| # | 问题 | 根因 | 修复 |
|---|------|------|------|
| 1 | Docker 构建在 `go mod download` 阶段失败，文档里的 Docker 部署路径不可用 | `go.mod` 要求 go ≥ 1.26.10（GO-2026-6809），Dockerfile 仍是 `golang:1.24-alpine`，且官方 golang 镜像 `GOTOOLCHAIN=local` 不会自动下载工具链 | 基础镜像升到 `golang:1.26-alpine`（`d9b5f5e`） |
| 2 | 镜像内没有 nginx 进程，首屏配置下发必然失败（`invalid PID number ""`） | 面板只负责「下发配置 → `nginx -t` → reload」，从不 start nginx；容器里也没有 systemd | 新增 `deploy/docker-entrypoint.sh`：入口先拉起 nginx 再 exec 面板；nginx 启动失败只告警不阻断（`0fc117c`） |
| 3 | `nginx -t` 报 `"log_format" directive is not allowed here` | alpine 的 nginx.conf 把 `include /etc/nginx/conf.d/*.conf` 放在 root context，而面板下发的配置含 http 级指令（log_format / limit_req_zone）与 server 块 | 镜像内置 `deploy/nginx.conf`：include 移入 `http {}`，pid 显式固定 `/run/nginx/nginx.pid`（`0fc117c`） |
| 4 | `nginx -t` 报 `duplicate default server` | alpine 自带 `http.d/default.conf`（:80 上的 default_server），与面板下发的 catch-all 冲突 | 镜像构建期删除该文件（`0fc117c`） |
| 5 | 状态页把运行中的 nginx 显示成「未运行」 | 运行状态兜底只认 Debian 的 `/run/nginx.pid`，alpine 打包是 `/run/nginx/nginx.pid`；容器内没有 systemd，pid 文件是唯一探测手段 | `internal/driver/hostdriver` 增加 pid 候选路径（`0fc117c`） |
| 6 | CI 在 Windows 上 gofmt 把全部 108 个 .go 文件误报为未格式化 | GitHub Windows runner 默认 `autocrlf=true`，检出即 CRLF，而 gofmt 只认 LF | 新增 `.gitattributes`（`* text=auto eol=lf`）（`c6d61fa`） |
| 7 | CI 在 macOS 上 `TestListNewestFirst` 稳定失败（快照目录互相覆盖） | `confhistory.randomSuffix` 用 `time.Now().UnixNano()%10000` 做目录名后缀；macOS 微秒级时钟下相隔 20ms 的快照后缀完全相同，同秒内目录名撞车 | 改用 `crypto/rand` 生成 8 位十六进制后缀（`c6d61fa`） |

### 新增

- **发布流水线** `.github/workflows/release.yml`：推送 `v*` 标签自动构建
  linux amd64/arm64 静态二进制（`CGO_ENABLED=0` + SHA-256 校验和），创建 GitHub
  Release（`--generate-notes`），并删除历史 Release 的旧附件（**Release 记录与 Tag 保留**）。
  产物命名 `zhiweipanel-<version>-linux-<arch>` 与 `deploy/install.sh` 的 `BIN_URL`
  约定一致；工作流本身不写死版本号（版本从 tag 推导，受 `internal/version` 门禁约束）。
  不发布 GHCR 镜像——部署形态是源码构建，理由见
  [RESEARCH-BORROW-v0.1.4.md](RESEARCH-BORROW-v0.1.4.md) 第二节 A。

### 兼容性说明

- 无破坏性变更；直接替换二进制 + 重启即可升级（Docker 部署需重新
  `docker compose up -d --build`，镜像内 nginx 配置布局已变更）。
- 面板数据（SQLite、证书、备份）与配置格式不变。

---

## v0.1.3 — 修复：整站反代时 force_https 的 301 跳转被静默丢弃（2026-11-01）

**定位**：纯缺陷修复版本。无新功能、无破坏性变更，直接替换二进制 + 重启即可升级。

### 故障现象

站点开启「整站反代」（反代规则路径填 `/`）+「强制 HTTPS」后：
`http://` 访问不再 301 跳转到 `https://`，而是直接走反代服务。
面板上 force_https 开关仍显示"已开启"——典型的"看起来配好了、实际没生效"。

### 根因

`buildProxyScan` 置位 `rootReplacedByProxy` 后，`buildLocation` 整体提前返回，
而 force_https 的跳转指令（`if ($scheme = http) { return 301 ... }`）正是由
`buildLocation` 写进主 `location /` 的——主 location 被反代接管后，跳转
无处可写，被静默丢弃。`root_proxy_test.go` 甚至把"整站反代不应有 301"
断言成了预期行为（本次已修正该断言）。

### 修复

`internal/nginx/render.go`：`buildProxy` 在输出接管的 `location ^~ /` 时，
若 `forceRedirect` 为真则补写跳转指令（位置在 `proxy_pass` 之前）。

为什么不放 server 级：server 级的 `if(...) return` 先于 location 匹配执行，
会把 ACME 挑战目录一起 301 掉，证书续签随之失败。写在 `^~ /` 内是安全的：
`location ^~ /.well-known/acme-challenge/` 是更长前缀，优先命中不受影响。

### 回归测试

`internal/nginx/root_proxy_test.go`：

- 修正 `TestRenderRootProxyReplacesMainLocation` 的错误断言（整站反代 + force_https 必须有 301，且在 `location ^~ /` 内）；
- 新增 `TestRenderRootProxyForceHTTPSRedirect`：开/关 force_https 两种形态 + 子路径反代不受影响。

---

## v0.1.2 — 修复：未绑定证书的站点在详情页永远无法保存（2026-11-01）

**定位**：纯缺陷修复版本。无新功能、无破坏性变更，v0.1.0 / v0.1.1 的站点可直接升级（替换二进制 + 重启）。

### 故障现象（线上实录）

一个站点（未绑定证书、SSL 直接填证书文件路径建站）在详情页保存**任何**修改都返回
HTTP 400，连续 9 次 PUT 全部失败；刷新页面、重填字段均无效，且与所改字段无关。
另一个绑定过证书的站点历史保存一直正常——该故障只命中"从未绑定过证书"的站点。

### 根因

1. 前端 `web/js/views/site-detail.js` 的 `mergeDefaults` 在服务端未返回 cert 分组时，
   回填默认对象 `{cert_id:"", domains:[], not_before:"", not_after:"", ...}`；
2. 后端 `model.CertRef` 的 `NotBefore/NotAfter` 是 `time.Time`，**空字符串无法解码**
   （`parsing time "" as "2006-01-02T15:04:05Z07:00": cannot parse "" as "2006"`）；
3. 整个 PUT 在 `bindSite` 的 JSON 解码阶段就 400，根本走不到字段校验——
   所以"填什么都是 400"，报错也与用户所改的字段无关。

绑定过证书的站点 GET 返回真实 RFC3339 时间字符串，round-trip 正常，因此不中招。

### 修复（前后端双管）

| 层 | 文件 | 修复 |
|----|------|------|
| 前端 | `web/js/views/site-detail.js` | `mergeDefaults` 不再回填 cert 默认分组：服务端没返回就置 `null`。界面各处对 `site.cert` 本就判空安全（`tabSsl` 的 `site.cert && site.cert.cert_id`、`certDaysLeft` 判空、"解除绑定"流程的 `site.cert = null`） |
| 后端 | `internal/model/model.go` | 新增 `CertRef.UnmarshalJSON`：空字符串时间字段归一为零值时间（与"没有该字段"等价），兜底任意客户端；非空但非法的值仍然报错，不放松对真实脏数据的拦截 |

### 回归测试

`internal/model/cert_test.go`（新增）：

- `TestSiteUnmarshalCertEmptyTimeStrings`：线上故障形状（旧版前端 payload）必须解码成功、时间归一为零值；
- `TestSiteUnmarshalNoCertGroup`：缺 cert 分组 → `Site.Cert` 为 nil；
- `TestCertRefUnmarshalJSON`：合法 RFC3339（含纳秒）/ 空串 / 字段缺失 / null / 非空脏值报错 / marshal 往返一致。

前端逻辑用一次性脚本 `.deploy/check_mergedefaults.js` 从真实源码提取
`DEFAULTS` + `mergeDefaults` 执行验证（未绑定证书 → `cert:null` 且 payload 无空时间字符串；
已绑定证书 → 原样保留；其它分组默认值回填行为不变）。

---

## v0.1.1 — 改名：SitePanel → 智维面板 / ZhiweiPanel（2026-11-01）

**定位**：项目更名 + 运行时标识符迁移。功能与 v0.1.0 一致，本次只改名字与由此产生的标识符。

### 变更内容

| 项 | 旧 | 新 |
|----|----|----|
| 中文名 | — | 智维面板 |
| 英文名 | SitePanel | ZhiweiPanel |
| Go 模块路径 | `github.com/panel/sitepanel` | `github.com/zhiwei/panel` |
| 安装目录 | `/opt/sitepanel` | `/opt/zhiweipanel` |
| 数据库文件 | `sitepanel.db` | `zhiweipanel.db` |
| systemd 单元 | `sitepanel.service` | `zhiweipanel.service` |
| 单元口令环境变量 | `SP_ADMIN_PASS` | `ZW_ADMIN_PASS` |
| http 级 nginx 配置 | `00-sitepanel-http.conf` | `00-zhiweipanel-http.conf` |
| catch-all 配置 | `01-sitepanel-default-server.conf` | `01-zhiweipanel-default-server.conf` |
| nginx `log_format` | `sitepanel_access` | `zhiweipanel_access` |
| 配额锁变量 | `$sp_quota_lock` | `$zw_quota_lock` |
| API Token 前缀 | `sp_` | `zw_` |
| 会话 Cookie | `sitepanel_session` | `zhiweipanel_session` |
| 浏览器主题键 | `sitepanel_theme` | `zhiweipanel_theme` |

### 界面

- 侧栏品牌名与登录页显示**中文「智维面板」**，浏览器标题与文档标题用**英文 ZhiweiPanel**
- 品牌角标由 `SP` 改为 `ZW`，favicon 字母改为 `Z`

### 破坏性变更（升级必读）

- 运行时标识符**全改、不保留旧名兼容**，线上必须按 README「改名迁移」一节逐步迁移
- 旧 nginx 配置文件必须先移走（改名 `.migrated.bak`）：旧 `00-` 文件的 `limit_req_zone`
  zone 名带站点 ID（改名不变）→ 与新文件重复定义；旧 `01-` 文件带 `default_server` → 重复默认 server。
  两者都会让 `nginx -t` 失败、**整批站点一起停摆**
- API Token 前缀只是展示用（服务端比对整个 token 的 SHA-256），**已签发的 token 继续可用**

---

## v0.1.0 — 正式稳定版（2026-10-28）

> **改名说明（2026-11-01）**：本项目已由 **SitePanel** 更名为 **智维面板 / ZhiweiPanel**，
> Go 模块路径由 `github.com/panel/sitepanel` 改为 `github.com/zhiwei/panel`。
> 改名同时改了运行时标识符（安装目录、数据库文件名、systemd 单元、nginx 配置文件名等），
> **不保留旧名兼容**——线上升级必须按 README「改名迁移」一节逐步迁移。
>
> 改名是**全仓机械替换**，历史条目里的旧标识符一并被替换为新名（如 v0.2.1 当时的
> `sitepanel.v0.2.1.bak` 备份件，如今写作 `zhiweipanel.v0.2.1.bak`），
> 这些条目描述的是**当时**的操作，与当前实际部署路径已不一致，阅读时请以 README
> 「改名迁移」一节为准。旧名 → 新名的完整对照表见 README。

**定位**：可部署到生产环境的轻量建站面板正式版。本版是"全量审计 → 修复 → 加固 → 重新锁定版本号"的
成果，也是 `v0.2.2` 全功能基线 + 两轮审计共 26 项 P1 缺陷修复 + 21 项功能补齐/安全加固的合流版本。

### 功能范围

站点管理 19 项能力全部落地（域名管理、子目录绑定、网站目录、访问限制、流量限制、伪静态、
默认文档、配置文件、SSL、PHP、重定向、反向代理、防盗链、防复改、网站安全、网站日志、
网站告警、其他设置、Let's Encrypt），叠加平台运维层：

| 运维层 | 能力 |
|--------|------|
| 计划任务 | 6 种内置任务（shell / 站点备份 / 数据库备份 / 日志切割 / 内存释放 / URL 访问），标准 5 段 crontab，执行历史留痕 |
| 备份 | 站点 `tar.gz` + nginx conf 副本；数据库 `mysqldump` + gzip；保留份数自动清理 |
| 证书 | Let's Encrypt HTTP-01 自动签发/续签，续签复用原证书 ID 并自动重绑站点；手动签发/导入 |
| MySQL | 建站联动建库建用户、删站联动 drop、密码一次性回显 |
| 文件管理 | 目录浏览、读写、上传、解压、改名、删除（路径逃逸防护） |
| 机器接入 | API Token（scope 隔离，含独立 `shell` scope） |
| 合规 | 操作审计日志（写操作留痕）、系统运行指标、配置历史快照与回滚 |

双运行时支持：宿主机（systemd 管 nginx）与 Docker。

### 本版核心变化

| 维度 | 变化 |
|------|------|
| 权限模型 | 8 个有副作用的端点从 GET 改为**强制 POST**（证书续签/吊销/绑定/删除、站点启停/重载、配置下发、告警处理）；只读 API Token 不再能写。**第三轮再收口 5 个**（防篡改基线 / 篡改检测 / 登出 / 改密 / 账户类端点），并把判定标准固化成回归测试 |
| 凭据泄露 | 站点访问认证口令不再明文入库、不再明文回显（改为 apr1 哈希 + 只写字段）；异常登录提示不再回显管理员上次登录 IP |
| 客户端 IP | X-Forwarded-For 改为**受信代理白名单**模式（新增 `-trusted-proxy`）；登录限流增加账号级第二层 |
| MySQL | 服务端决定用户名/密码/字符集，客户端指定一律拒绝；驱动层标识符与字面量转义；保留用户名保护 |
| nginx 渲染 | 三道防线（域名硬校验 → 路径硬校验 → 净化降级）；单个坏站点被隔离，不再拖垮全站配置下发 |
| nginx 重载 | 降级链改为 `systemctl reload` → **`nginx -s reload`（信号重载，不影响服务）** → `systemctl restart`（最后手段）；新增 `-nginx-unit` 指定 nginx 的 systemd 单元名（自定义编译的 nginx / openresty 场景） |
| 计划任务 | shell 危险命令检查从子串黑名单升级为归一化 + 词法切分 + 命令词匹配；`shell` 独立 scope（**前端补上勾选项，此前后端有、前端没有**） |
| 备份 | 修正磁盘文件泄漏与保留顺序；`log_split` 改 copytruncate，修 nginx fd 导致的统计失真与磁盘无限涨 |
| 证书 | 续签复用原证书 ID（同时修"手动续签站点仍用旧证书"与"自动续签堆重复记录撞 Let's Encrypt 限额"） |
| 功能补齐 | **站点访问认证（HTTP Basic Auth）真正生效**：口令文件落盘 + nginx 渲染 + ACME 挑战目录豁免；**防复改页新增「建立防篡改基线」「立即检测篡改」按钮**（此前后端可用、界面无入口，导致篡改告警持续误报） |
| 依赖 | Go 1.26.10；lego v4.25.2 → v4.35.2（修 GO-2026-5593 Webroot 路径穿越任意文件写）；`govulncheck` affected=0；零新增依赖 |
| 版本机制 | 版本号改由 `internal/version/version.go` 单一权威源管理 + 常驻门禁测试 + 历史版本单独登记 |
| 仓库治理 | 清除 33 个含明文凭据的临时脚本与 5 个过期二进制（合计约 75 MB） |

### 文档清单

| 文档 | 内容 |
|------|------|
| `README.md` | 项目介绍、功能清单、快速开始、命令行参数、架构、目录结构、质量状态、已知限制 |
| `docs/DEPLOYMENT.md` | 环境要求、裸机/Docker 部署、参数表、目录结构、升级回滚、卸载、排障 |
| `docs/USAGE.md` | 功能使用说明（逐项功能：在哪配、字段含义、生效条件、注意事项） |
| `docs/BUGFIX-v0.1.0.md` | BUG 修复清单（本轮 26 项 P1 + 第三轮端点收口与前端补齐 + 回归说明） |
| `docs/OPTIMIZATION-v0.1.0.md` | 功能优化与新增日志 |
| `docs/SECURITY-v0.1.0.md` | 安全审查报告（攻击链、修复明细、渗透测试、已知限制） |
| `docs/CHANGELOG.md` | 本文件：版本策略、版本线重置说明、发布记录、历史档案 |
| `docs/audit/audit-*.md` | 四份全量代码审计原始报告（API/鉴权、调度/告警、驱动/服务/存储、前端） |

### 验证状态

| 项 | 状态 |
|----|------|
| `go build ./...` | ✅ |
| `go vet ./...` | ✅ 零告警 |
| `go test -count=1 ./...` | ✅ 17 个包全绿（39 个测试文件 / **212 个测试函数**） |
| `gofmt -l .` | ✅ 零输出 |
| `govulncheck ./...` | ✅ affected=0（须用与构建一致的 go1.26.10 工具链编译扫描器，否则会误报 11 条，详见安全报告 §3.7） |
| 前端 15 个 ES Module esbuild 校验 | ✅ 全部通过 |
| apr1 实现对齐 OpenSSL | ✅ 10 组权威向量逐字节一致 |
| 版本门禁负例 | ✅ 篡改版本锚点后门禁测试如期变红 |
| 方法闸门负例 | ✅ 摘掉 `baseline` 的 `requirePost` 后 `method_gate_test` 立即变红 |
| **线上端到端对抗测试** | ✅ **35 项全过、0 失败**（生产机 198.51.100.10，真实 nginx + 真实站点） |
| 生产部署验证 | ✅ 运行中二进制 md5 `fa4357088dc41df54e18705983cacf8e`，service active，8887/8888 监听正常，nginx 配置零变更 |

### 已知限制

见《安全审查报告》第五节。要点：

1. 面板以 root 运行，shell 计划任务的危险命令黑名单**只是护栏，不是安全边界**；
2. 站点 MySQL 密码与 API Token 明文只在创建时回显一次，库内为明文（与面板既有凭据模型一致）；
3. `raw_conf` / `rewrite` 是管理员原样注入 nginx 的特权入口，属设计内能力；
4. 通知渠道 / `access_url` 的 SSRF 防护未闭环（需自定义 DialContext 拒绝内网网段）。

---

## 历史版本档案

以下条目均为**真实发生过的开发与发布记录**，保留供追溯，不再是当前版本线：

| 版本 | 日期 | 说明 | 状态 |
|------|------|------|------|
| v0.1.0 | 2026-10-13 | 首个正式稳定版（19 项建站能力落地） | **已被 2026-10-28 的 v0.1.0 取代** |
| v0.1.1 | 2026-10-16 | MySQL 联动建库（从未发布，标记为"待发"） | 未发布，功能已并入后续版本 |
| v0.2.0 | 2026-10-17 | 计划任务 + 备份 + 站点小加项（edate / run_path / custom_404 / 批量启停） | 已上线，档案 |
| v0.2.1 | 2026-10-18 | 自定义对外端口（站点级 ListenPorts + 全局 -public-port + -disable-ipv6） | 已上线，档案 |
| v0.2.2 | 2026-10-24 | 平台运维层：系统监控 + 配置历史回滚 + API Token + 操作审计 | 已双机上线，档案 |

### v0.1.1 — MySQL 联动建库（2026-10-16，未发布）

参考宝塔面板"建站即建库"工作流扩展：

| 维度 | 变化 |
|------|------|
| 安装脚本 | `install.sh` 新增 `INSTALL_PHP=1` / `INSTALL_MYSQL=1` 开关，按 OS 装 PHP-FPM 和 MariaDB；root 凭据写到 `${DATA_DIR}/mysql-root.cnf`（0600） |
| Model | 新增 `model.Database` + `model.Site.Mysql` 字段（host 强制 localhost、charset 默认 utf8mb4、密码不参与 JSON 序列化） |
| Driver | 新增 `internal/mysql` 包：Status / CreateDatabase / CreateUser / GrantPrivileges / Drop* / Revoke / GeneratePassword |
| Service | `CreateSite` 接 `site.Mysql` 联动建库（任一步失败回滚）；`DeleteSite` 联动 drop（MySQL 失败不阻塞站点删除，记日志） |
| API | `POST /api/sites` 接 `mysql` 字段；响应里 `show_password:true` 一次性明文回显；GET/List 路径用 `SanitizeForRead` 脱敏；新增 `GET /api/system/mysql` 状态端点 |
| 前端 | 新建站点向导加"同时创建数据库"复选框（未装时禁用 + 提示）；创建成功弹"请保存密码"模态框含复制按钮；站点详情新增"数据库"标签页 |
| 安全 | 密码只明文一次；命名 `site_<12位hex>` 不可猜；host 强制 localhost 不接受 `%`；GRANT 不带 `GRANT OPTION` |
| 测试 | mysql 包 4 个用例（cnf 解析、name 推导、密码生成、SafeCopy/Masked）；service 测试全部更新到新 `New` 签名 |

> 该版本从未发布，以上功能已全部并入 v0.2.0 及之后的代码，并最终进入当前的 v0.1.0。

---

## 历史版本（v0.2.x 详细记录）

## v0.2.0 — 运维刚需（2026-10-17）

参考宝塔面板"计划任务 / 备份 / 站点到期"三大运维模块，向 ZhiweiPanel 移植并按面板自身定位收敛。

### 新增功能

| 维度 | 变化 |
|------|------|
| 计划任务子系统 | 新增 `internal/cron`：基于 `robfig/cron/v3` 的标准 5 段 crontab 解析，后台 goroutine 每 30s 扫一次 `cron_jobs` 表，落库 `cron_runs` 留最近 50 条执行历史 |
| 6 种内置任务 | shell（白名单拒绝 shutdown/init/mkfs/passwd 等）/ backup_site / backup_db / log_split / release_mem / access_url（SSRF 防护） |
| 站点/数据库备份 | 新增 `internal/backup`：站点 = `tar.gz` 根目录+nginx conf 副本；数据库 = `mysqldump` + gzip；单份 100MB 上限；支持 `retain` 保留份数自动清理 |
| 站点到期时间 | `sites.edate TIMESTAMP` 列；列表未来 7 天内角标黄，过期角标红 |
| 站点运行目录 | `site.Root.RunPath` 字段（Laravel/ThinkPHP 的 `public/` 风格）；`fastcgi_param SCRIPT_FILENAME` 拼成 `$document_root/<run_path>/$script` |
| 自定义 404 | `site.Extra.Custom404` 字段，渲染为 server 级 `error_page 404 /<path>`；路径必须相对站点根 + 拒绝 `..` 跳出 |
| 站点批量启停 | `POST /api/sites/batch {action, ids}`；UI 多选 + 底部批量操作条 |
| 全局告警 | `alert.Engine.NotifyGlobal` 接 cron_failed / backup_failed 之类无 site 上下文的告警，落库不通知 |
| Cron 即时触发 | `POST /api/cron/jobs/run` 把 `next_run_at` 改成 now，调度器 30s 内捡起来跑 |

### 配套变更

| 维度 | 变化 |
|------|------|
| Schema | 旧库自动 `ALTER TABLE sites ADD COLUMN edate`；新增 `cron_jobs` / `cron_runs` / `backups` 三张表 |
| API | `/api/cron/jobs`（CRUD + 立即执行 + 历史）；`/api/backups`（CRUD + 下载 + restore 预览 + 删除）；`/api/sites/batch` |
| 前端 | 新增 `/js/views/cron.js`（任务列表 + 6 种类型编辑 + 立即执行 + 执行历史）；新增 `/js/views/backups.js`（备份列表 + 手动备份 + 下载 + 预览 + 删除）；`sites.js` 加批量操作条 + edate 角标；`site-detail.js` 加 run_path/custom_404/edate 编辑 |
| 渲染 | `nginx/render.go` 加 `buildCustom404` + `phpFastCGI` 识别 RunPath |
| 依赖 | `github.com/robfig/cron/v3 v3.0.1`（+50KB） |
| Binary | 19.17 MB（v0.1.0 是 18.85 MB） |
| 部署 | 双机（licapp.example.com / 198.51.100.10）均已上线，md5 `f4c869cf7675de9e668024b5cb158d88` |

### 安全权衡

- **Shell 白名单** 与宝塔一致：禁用 `shutdown` / `init 0/6` / `mkfs*` / `mke2fs` / `passwd` / `chpasswd` / `--stdin` / `reboot` / `halt` / `poweroff` / fork bomb。面板以 root 运行，没有真沙箱。
- **access_url SSRF 防护**：拒绝 `127.0.0.0/8` / `10/8` / `172.16/12` / `192.168/16` / `169.254/16` / `::1`。
- **备份 100MB 上限**：防"日志站点跑一周日志 50GB 备份直接撑爆磁盘"。

### 已知限制（v0.2.0 范围外）

1. **远程存储（OSS/COS/七牛云）未做**——本地 tar.gz 已够 100MB 限内使用
2. **备份加密未做**——纯 tar.gz，泄漏即明文，留 v0.2.1
3. **Cron 全局通知渠道未做**——失败只落 `alerts` 表，"全局通知渠道"配置项留 v0.2.1
4. **站点批量删除未做**——目前只支持批量启停；批量删除太危险（联动 drop database）需要单独的二次确认流程

---

## v0.2.1 — 自定义对外端口（2026-10-18）

用户要求"3 个选项都支持"——站点级 ListenPorts + 全局 -public-port + 反代兜底文档。

### 新增功能

| 维度 | 变化 |
|------|------|
| 站点对外端口 | `model.Site.ListenPorts []int` 字段，默认 `[80]`；SSL 时自动 append 443；可多端口（如 `[80, 8080]`） |
| 全局对外端口 | 启动 flag `-public-port=8080` 覆盖所有站点（整站搬迁到非标端口的场景） |
| 关闭 IPv6 | 启动 flag `-disable-ipv6`（老机器无 IPv6 时 nginx -t 报 emerg） |
| 端口校验 | `model.ValidateListenPorts` 限制 1-65535，最多 8 端口，去重 |
| 渲染器 | `nginx/render.go effectiveListenPorts()` 按 (PublicPort / ListenPorts) 解析最终 listen 列表 |
| API | 新增 `GET /api/system/info` 返回 `public_port` / `disable_ipv6`；`POST /api/sites` 与 `PUT /api/sites/{id}` 接受 `listen_ports` |
| 前端 | 站点列表角标显示非标端口；site-create / site-detail 加"对外监听端口"输入框（默认 80） |
| 文档 | UI 提示 + README/CHANGELOG 解释三种用法的取舍 |

### 三种用法（用户原始要求"都需要支持"）

1. **按站配置** `site.ListenPorts = [80, 8080]` —— 单站点要非标端口时
2. **全局覆盖** `-public-port=8080` —— 整站搬到非标端口场景
3. **反代兜底** —— 文档明确建议"主站 80/443 + 次要服务用反代指向 127.0.0.1:<port>"（已有 `proxy` 能力）

### 配套变更

- 9 个 backup 留底（pre020 / pre021 滚动）
- `internal/service/apply_test.go` 因 GBK 编码污染重写为纯 ASCII（核心 4 个 ApplyAllConfig 测试保留：orphan 清理 / disabled 站点 / cross-site isolation / default_server rollback）
- 双机部署：licapp + production 均上线，md5 `8941baa8ae37d81fa10ff9c7a09c37c2`

### 安全权衡

- 端口列表 1-65535 + 8 个上限（防滥用）
- 同一端口只能给一个站点（nginx listen 冲突由配置渲染期报 nginx -t emerg 兜住）
- 全局 `-public-port` 模式下：API 写入站点时强制清空 `ListenPorts`（避免歧义）
- 非标端口必须云厂商安全组 + 防火墙同时放行（文档提示，UI 帮助文字里写明）

### 留待 v0.2.2

1. 备份加密（zstd + 密码）
2. 远程存储（OSS/COS/七牛云）
3. Cron/backup 失败时全局通知渠道

---

## v0.2.2 — 平台运维层（2026-10-24）

借鉴清单见 [docs/RESEARCH-BORROW-v0.2.2.md](RESEARCH-BORROW-v0.2.2.md)。

本轮做的是**平台运维层**，不是站点管理层——站点级功能（16 组配置 + 流量配额 + 限速 + WAF 类规则 +
篡改检测 + 告警）在 v0.2.1 已经做得很深，缺口集中在"运维这台机器"这一层。

### 新增功能

| # | 功能 | 借鉴来源 | 关键取舍 |
|---|------|---------|---------|
| A | **系统资源监控** | 1Panel `monitor.go`、cenvero/fleet `metrics.go` | 纯 Go 读 `/proc`，**不引入 gopsutil**（依赖链会破坏 `CGO_ENABLED=0` 单二进制）；CPU 用两次采样差值，**不用** 1Panel 的阻塞式 `cpu.Percent(3s)`（那会让 HTTP 处理器卡 3 秒）；监控历史走内存环形缓冲（120 条 ≈ 20 分钟），**不开**独立 MonitorDB |
| B | **nginx 配置历史 + 回滚 + 差异对比** | 1Panel `snapshot.go`、fleet `agent_autosync.go` | 快照**存盘不存 DB**（配置文件入 SQLite 会让主库和备份一起膨胀）；保留 30 份；回滚**只恢复 nginx 配置，不回滚数据库**；**只恢复快照里存在的文件**，当前多出来的文件不动（多出来的通常是新建站点，删掉会连带把新站点从 nginx 里摘掉）；回滚前先备份、nginx 检测失败原样退回 |
| C | **API Token 机器鉴权** | filebrowser `rules.go`、SFPanel `auth.go` | 库里**只存 SHA-256 摘要**，明文只在创建响应出现一次；吊销即删行；格式 `zw_` + 32 字节 base64url；scope 空 = 全权限，`read` 只能 GET，`write` 才能 POST/PUT/DELETE，写操作 scope 不足返回 403；**不引入 filebrowser 的规则 DSL**（只有一个使用方，抽象是纯负债） |
| D | **操作审计日志** | 1Panel `logs.go`、SFPanel `audit.go` | 只记**写操作 + 登录事件**（读操作量大价值低，全记会撑爆表、淹掉真信号）；审计写在**鉴权之后**（拿得到调用者身份与响应码）；审计写失败**绝不影响主流程**；上限 5000 条，每 200 次写清理一次；`principal` 走 request context 传递，不重复查库 |

### 配套变更

| 维度 | 变化 |
|------|------|
| Schema | 新增 `api_tokens` / `audit_logs` 两表及索引（`CREATE TABLE IF NOT EXISTS`，旧库零手工步骤） |
| API | `/api/system/metrics`；`/api/system/config-history`（列表 / 查看 / diff / restore）；`/api/tokens`（CRUD）；`/api/audit-logs` |
| 鉴权 | 新增 `principal` 抽象：session cookie 优先，其次 API Token（`Authorization: Bearer` 或 `X-API-Token`）；写操作校验 write scope |
| 前端 | 新增"运维中心"视图（`#/ops/tokens`、`#/ops/audit`、`#/ops/history`）；仪表盘新增"宿主机资源"卡片 + CPU/内存迷你趋势线 |
| 依赖 | **零新增依赖**（这是本轮的硬约束） |
| Binary | 19.35 MB（v0.2.1 是 19.18 MB，+172 KB） |

### 修复的缺陷

| 缺陷 | 影响 | 说明 |
|------|------|-----|
| `memInfo.used()` 把"读不到 MemAvailable"误报成"内存用满 100%" | 老内核或字段缺失时仪表盘显示假的 100% 占用 | 改为回退 `MemFree+Buffers+Cached`；仍读不到则返回 0 表示"未知"而不是谎报 |
| `ListAuditLogs` 的 `Scan` 漏了 `&`（`l.IP` / `l.UA`） | **`/api/audit-logs` 运行时必然 500** | 单测抓到并修复 |
| `deriveAuditAction` 把 `system` 命名空间压成 `system.action` | 配置回滚这类最需要追溯的操作，target 取成 "config-history" 而非快照 ID | 改为把第二段提升为真正的资源名：`config-history.restore` / `config.apply` |
| **`StartMetricsCollector` 同步调用死循环** | **面板进程活着但不监听任何端口，外部完全打不开** | 见下方事故记录 |
| 全仓 20 个文件未 gofmt 格式化 | 与本文档"gofmt 零输出"的说法不符 | 全仓 `gofmt -l -w .`，改后 vet / test 复跑全绿 |

### 事故记录：`StartMetricsCollector` 阻塞启动

首次部署到 198.51.100.10 时出现：进程活着、systemd 显示 `active`、日志停在"已保存配置快照"，
但 `ss -lnt` 里 8887/8888 一个端口都没有，面板从外面完全打不开。

真因：`StartMetricsCollector` 同步调用了 `sysinfo.Collector.Start` —— 那是个死循环
（与 `http.Server.ListenAndServe` 同型）。同步调用把 `main()` 卡死在那儿，HTTP 服务根本没机会启动。

修复：参照 `internal/cron` 的 `Scheduler.Start`（同步做初始化、循环用 `go` 拉起），
把采样循环放进后台 goroutine。

**已加回归测试** `internal/service/ops_test.go:TestStartMetricsCollectorDoesNotBlock`，
并已验证它在有 bug 的版本上确实失败（3.01s 超时失败）——抓不住的测试等于没有。

### 验证状态

| 项 | 状态 |
|----|------|
| `go vet ./...` | ✅ 零告警 |
| `go test ./...` | ✅ 全部通过（全仓 176 通过 + 1 跳过；本轮新增 51 个用例：sysinfo 12 / confhistory 14 / store 12 / api 8 / model 2 / service 3） |
| `gofmt -l` | ✅ 零输出 |
| esbuild 前端校验 | ✅ 15 个 JS 文件全部通过 |
| 198.51.100.10 端到端 | ✅ 监控/配置历史/diff/Token 创建/机器读写/scope 403/审计留痕/吊销 全部实测通过 |
| licapp.example.com 端到端 | ✅ 同上（本机自测，面板 8888 被云安全组过滤，见下） |
| 双机部署 | ✅ 均已升级到 v0.2.2，md5 `d1cf8a03323a76d5f89fd4e28ea9ceff`（19,349,666 字节） |

> **关于 md5**：本轮做了全仓 `gofmt` 归一化（此前有 20 个文件未格式化，与本文档
> "gofmt 零输出"的说法不符）。gofmt 只改空白与注释排版，但**源码行号会进二进制**，
> 所以归一化后二进制 md5 变了（`90f7d226…` → `d1cf8a03…`），体积不变。
> 线上两个版本功能完全一致，最终线上跑的是与仓库构建产物 md5 完全相同的 `d1cf8a03…`。

### licapp 的 SSH 端口是 9779，不是 22

排查过程中发现记录有误：licapp（198.51.100.20）的 **SSH 监听在 9779 端口**，
22 端口从外部不可达（连不上，非瞬时故障；另扫了 2222/22022/10022/8022/2022/9922/5222/12022/33022
共 10 个备用端口，全部 filtered）。

识别方法：9779 端口用 HTTP 探测会返回 `Received HTTP/0.9 when not allowed`，
用 `curl --http0.9` 能读到原始字节 `SSH-2.0-OpenSSH_8.0` —— 说明那个端口跑的是 sshd，不是面板。

主机密钥首次出现时无可比对记录（本机 PuTTY 缓存与 OpenSSH known_hosts 都没有该 IP，
会话存档里也没出现过）。采用**身份核验**代替盲信：登录后比对面板二进制 md5
= `8941baa8ae37d81fa10ff9c7a09c37c2`，与文档记录的 v0.2.1 完全一致，
且 hostname / OS（Anolis 8.10）/ systemd 状态全部吻合，才认定是目标机并继续部署。

主机密钥（供后续核对）：`ssh-ed25519 255 SHA256:OSnj1dCma36CB5pqjCxRNytpTj/QBZhrjtQeri4+UXA`

### 两台机的差异（部署时须注意）

| 项 | licapp.example.com | 198.51.100.10 |
|----|-------------------|-----------------|
| SSH 端口 | **9779** | 22 |
| nginx 可执行文件 | `/usr/sbin/aa_nginx` | `/usr/sbin/nginx` |
| 面板协议 | HTTP 8888（无 TLS） | HTTPS 8888 + HTTP 跳转 8887 |
| 初始 admin 密码 | 见运维记录（不写入文档） | 见运维记录（不写入文档） |
| 外部可达性 | 8888 **被云安全组过滤**（OS 内 firewalld/ufw 均 inactive） | 8887/8888 均可达 |

licapp 的 8888 从外部 TCP 超时，但服务本身完全正常（服务器本机自连 200）。
这是**云厂商安全组**层面的过滤，不是面板故障 —— 排查时不要误判成面板挂了。
本次 licapp 的端到端验证改从服务器本机发起。

### 回滚

两台机都保留了 `/opt/zhiweipanel/zhiweipanel.v0.2.1.bak`（v0.2.1 原二进制，19 MB）。
需要回退时：

```bash
systemctl stop zhiweipanel
install -m 0755 /opt/zhiweipanel/zhiweipanel.v0.2.1.bak /opt/zhiweipanel/zhiweipanel
systemctl start zhiweipanel
```

### 安全权衡

- **Token 只存摘要**：库文件泄露不会直接变成可用凭据；明文一次性回显，丢了只能重建
- **scope 默认全权限**：空 scope = 管理员权限。创建界面显式提示，避免用户误以为"没勾就是没权限"
- **审计只记写操作**：读操作量大价值低；但这意味着"谁看了站点配置"不留痕——等保场景若要求读审计需另开
- **审计表上限 5000 条**：审计会无限增长，长期跑着的面板必须有上限。超出后按时间淘汰最旧的
- **回滚不回滚数据库**：UI 文案、API 响应、代码注释三处都明确告知，避免用户误以为站点定义也回到了当时
- **回滚不删多余文件**：宁可残留一个旧配置文件，也不能把快照之后新建的站点从 nginx 里摘掉

---

## v0.2.0 之后规划方向

> 这份清单写于 v0.2.0 发布时。逐条核对当前代码后标注了完成情况，
> **已完成的不再是待办**，未完成的仍留在 v0.1.0 的已知限制里。

1. 通知渠道 SSRF 防护 + 敏感凭据加密存储 —— **未完成**（见《安全审查报告》6.1 / 6.2）
2. RBAC 角色强制 —— **未完成**（`role` 仍是预留字段，实际单用户无横向越权）
3. DNS-01 provider 注册（阿里云 / DNSPod / 腾讯云 / Cloudflare）—— **已完成**（`internal/acme/dns.go` 内置 4 个提供商，支持通配符证书）
4. `site_down` 探测与防篡改告警接线 —— **已完成**（`main.go` 注入 `SetSiteProbeProvider` / `SetTamperChecker`）
5. `raw_conf` 保存二次确认 —— **未完成**
6. DB schema 版本号与自动迁移 —— **未完成**
7. 站点配置导入（把现有手写 conf.d 反向解析为面板站点）—— **未完成**
