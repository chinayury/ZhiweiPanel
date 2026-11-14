# 智维面板 ZhiweiPanel BUG 修复清单（v0.1.0）

> 本清单分两轮编写，**均依据 v0.1.0 最终代码逐条核对**，每条都能在代码中定位：
>
> - **第二轮（2026-10-28）**：四份全量代码审计（`docs/audit/audit-*.md`）后的 P1 修复，见第一章；
> - **第一轮（2026-10-13）**：v0.1.0 初版发布时的 49 项修复，见第二~五章。
>
> 分级：**P0** 可远程 getshell / root 沦陷；**P1** 功能失效或安全漏洞；
> **P2** 边界问题或信息泄露；**P3** 代码质量。

---

# 一、第二轮（2026-10-28）全量审计修复

## 1.1 审计范围

审计由 4 个独立子代理并行完成，原始报告全量落盘于 `docs/audit/`（合计约 259 KB）：

| 报告 | 覆盖范围 |
|------|---------|
| `audit-api-auth.md` | `internal/api/**` + `internal/store` 鉴权/令牌/审计、CSRF、会话、错误处理 |
| `audit-cron-alert.md` | `internal/cron/**`、`internal/alert`、`internal/logmgr`、`internal/acme`、`internal/backup` |
| `audit-driver-service-store.md` | `internal/driver/**`、`internal/service/**`、`internal/store/**` |
| `audit-frontend.md` | `web/**` 全部 15 个 ES Module 与后端 API 契约对照 |

四份报告共记录 BUG 67 项、安全缺陷 20 项、功能残缺 40 余项。
本轮**优先修复会致败的 P1**（安全漏洞 + 功能整体失效），P2/P3 记录在审计报告与安全审查报告中。

## 1.2 API 与鉴权（4 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N1 | **只读 API Token 能用 GET 请求执行写操作** | `internal/api/server.go`、`internal/api/certs.go`、`internal/api/helpers.go` | 8 个有副作用的子动作（证书续签/吊销/绑定/删除、站点启停/重载、配置下发、告警处理）挂在同一个 switch 里，方法不做区分；`isAuditedMethod` 只按方法判断审计，于是"GET 改状态"既不留痕又能被只读 Token 触发 | 新增 `requirePost(w,r) bool`，上述端点一律要求 POST；写操作改 POST 后审计中间件自动开始留痕 |
| N2 | **`site.Mysql.User`/`Password`/`Charset` 由客户端指定并被原样拼进 SQL** | `internal/service/service.go`、`internal/model/validate.go`、`internal/mysql/mysql.go` | service 层直接把请求体里的 MySQL 参数拼进 DDL（DDL 不支持占位符参数化） | 三层封堵：service 层 `provisionMySQL` **拒绝客户端指定** User/Password/Charset（Host 仅 localhost，Charset 白名单 utf8mb4/utf8/latin1）；model 层 `ValidateMysql` 加固并接入 CreateSite/UpdateSite；driver 层新增 `quoteIdent`/`quoteLiteral`/`checkUserName`/`reservedUsers`/`allowedCharsets`，所有 DDL 改用转义 |
| N3 | **客户端提供的 `X-Forwarded-For` 被无条件信任** | `internal/api/clientip.go`、`internal/api/auth.go`、`main.go` | 取 `XFF` 首段直接当客户端 IP，攻击者一个头就能绕过 IP 限流与"异常登录二次验证" | 新增 `internal/api/clientip.go`（`trustedProxySet`/`remoteIP`/`(*Server).clientIP`）：只有**直连方**命中受信 CIDR 才采信 XFF；新增 `-trusted-proxy` flag（逗号分隔）；登录限流增加账号级第二层（阈值 3 倍） |
| N4 | **异常登录提示回显管理员上次登录 IP** | `internal/service/account.go` | 错误信息里带上了"上次登录 IP" | 不再回显该字段（只保留时间与设备描述） |

## 1.3 前端（6 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N5 | **读文件时内容恰好是合法 JSON 会被当成 JSON 解析** → 编辑器空白 → 保存即清空文件 | `web/js/api.js` | `response.json()` 不看 Content-Type，JSON 文件也走 JSON 分支 | 按 **Content-Type 分流**解析：JSON 才 `json()`，其余按文本 |
| N6 | **仪表盘与列表的流量/请求/告警恒为 0** | `web/js/api.js` | `unwrap()` 只取 `data.site`，把汇总指标（`metrics`/`traffic`）丢了 | `unwrap()` 改用 `flattenSiteSummary` 合并汇总与站点字段 |
| N7 | **"忘记密码"流程 100% 失败** | `web/js/api.js`、`web/js/app.js` | `http.post(...)` 的 `http` 没被 import | `api.auth` 补 `passwordResetRequest` / `passwordResetConfirm` 两个方法，`http` 保持模块私有 |
| N8 | **`document.getElementById('view')` 恒为 null，操作后刷新即崩溃** | `web/js/views/sites.js`、`backups.js`、`cron.js` | 容器真实 id 是 `outlet`，代码找的是 `view` | 7 处改用闭包里的 `container` 或模块级 `viewContainer` + `refresh()` |
| N9 | **后端不可达时启动流程永久 loading，用户看不到任何错误** | `web/js/app.js` | `boot()` 无 try/catch，无全局异常监听 | `boot()` 加 try/catch 兜底 + 全局 `unhandledrejection` 监听，失败有明确提示 |
| N10 | **站点告警规则类型与后端检查器对不上** | `web/js/views/*`、`internal/model/model.go`、`internal/api/*` | 前端硬编码的枚举与后端校验器各写一份，多数规则永远不触发，能触发的两种又选不到 | 枚举收敛为**单一权威源** `model.AlertRuleTypes`（6 项）；新增 `GET /api/alert-types`；前端 `loadAlertTypes()` 动态渲染（带兜底常量） |

## 1.4 服务层与驱动（5 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N11 | **`UpdateSite` 全量覆盖丢失 `Status`/`Edate`/`Mysql`** | `internal/service/service.go` | PUT 是全量替换语义，请求体没带的字段被清零 | 显式回填 `Status`/`Edate`；`Mysql` 一律沿用库里的值（既修丢失，也堵住"用更新接口改 MySQL 参数做注入"的路径） |
| N12 | **`CreateSite` 在落库前就真实建了 MySQL 库与用户** | `internal/service/service.go` | 先建库后落库，落库失败（磁盘满/SQLite busy）即留永久孤儿库，密码随错误丢弃 | 改为**先落库再真建**：意图先持久化，建库成功后再补写密码；失败时用户可在面板重试 |
| N13 | **dockerdriver 的 PHP-FPM socket 路径拼接错误** → Docker 模式下 PHP 站点全部 502 | `internal/driver/dockerdriver/docker.go`、`internal/nginx/render.go` | 拿 PHP 版本号去拼 `/run/php/php8.2-fpm.sock`；容器里 socket 实际在 `/run/php/php-fpm-<ver>.sock` 之类位置 | 进容器 `ls` 真实 socket 路径（`findPHPSocket`/`phpVersionFromContainerName`/`phpVersionFromSocket`）；同时修 `render.go` 的 `looksLikePHPServerVersion`（不再从 "default" 推 socket） |
| N14 | **`model.ValidateSite` 整层校验从未被调用（死代码）** → nginx 配置文本注入面零防护 | `internal/model/validate.go`、`internal/service/service.go`、`internal/nginx/render.go` | 校验器写好了但没接线 | 接入 CreateSite/UpdateSite；渲染再加三道防线（见 N17） |
| N15 | **站点访问认证口令明文入库、明文回显** | `internal/model/model.go`、`internal/service/access.go`、`internal/api/server.go` | `Access.AuthPass` 当普通字段存进 SQLite，而 GET 站点详情会返回整份站点配置 | 明文改成**只写**字段，服务端立刻换成 apr1 哈希（`Access.AuthHash`）；读路径 `SanitizeForRead`/`SanitizeAccessSecrets` 清空明文与哈希 |

## 1.5 调度 / 备份 / 证书（5 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N16 | **备份任务的 `retain` 只删库记录，磁盘上的 tar.gz 永远不删** | `internal/cron/handlers.go`、`internal/backup/backup.go`、`internal/store/backup_store.go` | 保留策略只裁 DB 表 | 改成**先删磁盘文件再裁 DB**；新增 `PruneOrphanFiles` 清历史孤儿；`ListBackups` 支持 limit<0 不限量 |
| N17 | **`log_split` 切完日志后 nginx 仍写"归档"文件** → 实时日志/流量统计/5xx 告警集体失真，磁盘无限涨 | `internal/cron/handlers.go` | 用 rename 归档，但 nginx worker 仍持有旧 fd | 改 **copytruncate**（新增 `copyTruncate`）：复制后截断原文件，nginx 用 O_APPEND 故截断后不会产生稀疏空洞 |
| N18 | **手动"续签证书"后站点仍在用旧证书** | `internal/acme/acme.go`、`internal/api/certs.go` | 手动续签返回新证书 ID 但没回绑站点（自动续签有重绑） | 抽出 `internal/service/cert.go` 的 `RebindRenewedCert`，main.go 与 API 共用；手动续签同样回绑 |
| N19 | **自动续签每次都新增一条证书记录** → 旧记录永远"到期待续签"→ 重复签发 → 撞 Let's Encrypt 重复签发限额 | `internal/acme/acme.go` | 续签时新建证书记录而不是复用原记录 | 续签复用原证书 ID（`saveCertificate(resource, req, certID)`），站点绑定路径不变、nginx 配置无需改 |
| N20 | **shell 命令黑名单可被 6 类手法绕过** | `internal/cron/handlers.go` | 纯子串黑名单：`sh''utdown`、`ini''t`、`mk''fs`、尾随空格、引号包裹、fork 炸弹变体都能过 | 升级为**归一化 → 词法切分 → 命令词匹配**（新增 `dangerousCommands`/`interpreters`/`systemctlDangerous`/`normalizeScript`/`scriptSegments`/`checkKillInit`/`checkSystemctl`/`checkRecursiveRootDelete`） |
| N30 | **nginx 重载失败时降级为 `systemctl restart`**，且信号重载分支永远不可达 | `internal/driver/hostdriver/host.go` | 降级链是 `systemctl reload` → `systemctl restart` → 报错；`nginx -s reload` 兜底被塞在"systemctl 不存在"的分支里，只要机器上有 systemctl 就永远走不到。单元名与实际不符时（自定义编译的 `aa_nginx.service`、源码安装的 `openresty.service`）**面板把配置写对了却永远不生效**，用户只看到一句"重载 nginx 失败" | 降级链改为 `systemctl reload` → **`nginx -s reload`（信号重载，不影响服务）** → `systemctl restart`（会中断服务，放最后并记明确日志）；新增 `-nginx-unit` 参数指定 nginx 的 systemd 单元名（留空 = nginx），状态显示与单元查询随之正确 |

## 1.6 安全机制补齐（4 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N21 | **API Token 的 `write` scope 实际等价于 root RCE** | `internal/api/tokens.go`、`internal/api/helpers.go`、`internal/api/cron.go` | shell 计划任务没有独立 scope，`write` 蕴含一切 | 新增**独立 `shell` scope**（`requireShellScope` 在 cron 创建/修改/立即执行三处生效；`tokens.go` scope 白名单加 `shell`）。同时明确：shell 黑名单**只是护栏不是安全边界**（文档如实写明） |
| N22 | **客户端可提交 `auth_hash` 字段做降级** | `internal/service/access.go` | 服务端与客户端共用一个结构体 | `applyAccessAuth` 一律丢弃客户端提交的哈希，只认明文口令或库里已有的哈希 |
| N23 | **nginx 配置渲染缺少统一的净化层** | `internal/nginx/render.go`、`internal/model/sanitize.go` | 各字段各写各的校验，规则容易漂移 | 新增 `internal/model/sanitize.go`（`SanitizeSite` + 谓词共用）；`internal/model/validate.go` 重构（`ErrInvalidValue` + `fieldError` 干净错误文案）；渲染三道防线：域名硬校验 → 路径硬校验 → 净化降级 |
| N24 | **单个坏站点能卡死全站配置下发** | `internal/service/service.go` | 任一站点 `Render` 失败 → `ApplyAllConfig` 整体中止 → 所有站点的配置都停在旧版本 | 隔离单个坏站点：坏站点整段注释掉、其余照常下发，记 `configIssues` + `ConfigIssues()` + `safeOneLine`；路径类字段不做"净化替换"（替换目录 = 静默 404），作站点级硬错误处理 |

## 1.7 依赖与供应链（2 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N25 | **lego v4.25.2 存在 GO-2026-5593**（Webroot provider 路径穿越 → 任意文件写） | `go.mod` | 依赖版本落后 | 升级到 **lego v4.35.2**；`go mod tidy` 通过；`govulncheck ./...` affected=0；**零新增依赖** |
| N26 | **Go 工具链版本落后，存在 GO-2026-6809 等标准库漏洞** | `go.mod` | `go 1.25.x` | 升级到 **go 1.26.10**（含漏洞编号说明注释） |

## 1.8 版本机制与仓库治理（2 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N27 | **版本号散落多处，靠约定对齐** | `main.go`、`deploy/Dockerfile`、`deploy/docker-compose.yml`、`README.md` | 改一处忘另一处不会有任何报错 | 版本号改由 `internal/version/version.go` 单一权威源管理；新增**常驻门禁** `internal/version/version_test.go`（锚点对齐 + 漂移扫描 + 负例测试）；历史版本单独登记在 `History` 变量，不参与锚点校验 |
| N28 | **仓库根目录残留 33 个含明文凭据的临时脚本 + 5 个过期二进制（约 75 MB）** | 仓库根 | 调试期产物未清理，其中 10 个脚本硬编码了面板 admin 明文密码 | 全部清除（可恢复删除），根目录只保留 `go.mod`/`go.sum`/`main.go`/`README.md` 与源码目录 |

## 1.9 功能补齐（1 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N29 | **站点访问认证（HTTP Basic Auth）配置了完全不生效** | `internal/model/model.go`、`internal/nginx/render.go`、`internal/service/access.go` | 模型有 `Access.AuthEnabled/AuthUser/AuthPass` 三个字段、前端也画了输入框，但**全仓没有任何一处渲染它们**，更没有生成 nginx 能读的 htpasswd 文件 | 补齐闭环：新增 `internal/htpasswd`（零依赖 apr1 实现，与 OpenSSL 逐字节对齐）；service 把 `用户名:哈希` 写进 `<data>/htpasswd/<站点ID>`；`nginx.Render` 输出 `auth_basic` + `auth_basic_user_file`；ACME 挑战目录显式 `auth_basic off;`（否则证书续签被 401 打断） |

## 1.10 第二轮回归测试

本轮新增/改写的关键锁死测试：

- `internal/htpasswd/htpasswd_test.go` — **10 组 OpenSSL 权威向量**逐字节比对（含 16 字节边界、>16 字节长口令、含空格口令、1~8 位盐、>8 位盐截断）
- `internal/nginx/render_basic_auth_test.go` — 认证指令渲染 / 关闭态 / 无哈希 / 无路径 / 不安全路径 / 反斜杠路径 6 类断言 + **ACME 挑战目录必须 `auth_basic off;`**
- `internal/service/access_test.go` — 明文丢弃、哈希来源唯一、客户端哈希被忽略、留空不改密、开启认证必须给凭据、口令文件落盘与清理、文件不落在站点根目录内、读路径不回显口令、库里不存明文
- `internal/version/version_test.go` — 锚点对齐 + 漂移扫描 + **负例**（篡改版本号后门禁必须变红）
- `internal/cron/shell_guard_test.go` — 审计报告给出的 6 类绕过 payload 全部固化为负例，另含"合法脚本不被误伤"的正向用例

第二轮结束时：`go test -count=1 ./...` **17 个包全绿**（38 个测试文件 / 209 个测试函数），
`go vet ./...` 零告警，`gofmt -l .` 零输出，前端 15 个 ES Module esbuild 校验全过。

---

# 一之二、第三轮（2026-10-28 晚）：端点权限收口 + 前端补齐

第二轮把 8 个写端点改成强制 POST 后，权限模型的判定标准就固定下来了：

```
authMiddleware：非 GET 方法 → 要求 write scope；GET/HEAD/OPTIONS → 不校验 scope
```

于是**「GET 可达 + 有副作用」= 只读 Token 的提权通道**。第三轮按这条规则把全部端点重扫一遍，
又找出 4 个漏网的（含 2 个 P1），并把前端两处功能残缺补齐。

## 1.11 第三轮：端点权限模型收口（5 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N31 | **`GET /api/sites/{id}/baseline` 能改写防篡改基线** | `internal/api/server.go` | `EstablishBaseline` 会 `ReplaceIntegrity`（直接改写 integrity 基线表），但该端点没有方法校验。只读 Token 调一次就能把基线刷成"当前状态"，**已经发生的篡改被洗成正常**，防篡改检测形同虚设 | 强制 POST（`requirePost`） |
| N32 | **`GET /api/sites/{id}/tamper` 能写事件表并触发告警通知** | `internal/api/server.go` | `CheckTamper` 名字像只读，实际会 `SaveTamperEvent`（写 tamper_events 表）+ `Raise`（触发邮件/webhook 通知）。只读 Token 可以无限插行、**把告警通道刷爆** | 强制 POST |
| N33 | **`GET /api/auth/logout` 可跨站强制登出** | `internal/api/auth.go` | 登出会 `DeleteSession`，但端点无方法校验 → 第三方页面一个 `<img src=".../api/auth/logout">` 就能把管理员踢下线（CSRF） | 强制 POST |
| N34 | **改密端点无方法校验** | `internal/api/auth.go` | `handleChangePassword` 接受任意方法 | 强制 POST |
| N35 | **账户类端点无方法校验** | `internal/api/account.go` | 改邮箱 / 触发邮箱验证 / SMTP 测试都接受任意方法；前两者挂在与 GET 相同的路径上，是 CSRF 的经典入口 | 改邮箱限定 PUT/POST（新增 `requirePutOrPost`）；触发验证与 SMTP 测试强制 POST |

**判定标准已写进代码注释**（`internal/api/helpers.go`）：

> 凡是会写库、写文件、发通知的端点，一律不能只靠 GET 触发。
> 只读 Token 的免检名单只有 GET/HEAD/OPTIONS，所以"GET 可达 + 有副作用"就等于权限提升。

## 1.12 第三轮：前端功能补齐与修正（4 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| N36 | **前端没有「建立防篡改基线」入口** | `web/js/views/site-detail.js` | 后端 `POST /api/sites/{id}/baseline` 一直可用，但界面无按钮。用户不建基线 → `tamper` 检测把所有文件都报成"新增" → 防篡改告警持续误报 | 站点详情「防复改」页新增「建立防篡改基线」「立即检测篡改」两个按钮（带前置校验：有未保存修改 / 未启用防篡改 / 受保护文件为空时明确提示） |
| N37 | **Token 创建弹窗没有 `shell` 权限位** | `web/js/views/ops.js` | 后端 `createToken` 接受 `read`/`write`/`shell` 三值且 `shell` 是独立授权位，但前端只画了 read/write 两个勾选框 → **用户根本无法创建一个 shell Token**（后端有、前端没有的功能缺口） | 弹窗补 `shell` 勾选项；三个授权位改成独立布尔 + 统一派生 `scopes`，勾选顺序不再互相覆盖 |
| N38 | **Token 列表把 `shell` 权限显示成「只读」** | `web/js/views/ops.js` | 渲染逻辑写的是 `s === 'write' ? '读写' : '只读'`，`shell` 落到 else 分支 → 界面上完全看不出这个 Token 握着 root 执行能力 | 抽出 `SCOPE_LABELS`/`SCOPE_COLORS` 映射，shell 显示为「Shell」且用 danger 配色 |
| N39 | **10 处标签配色类名在 CSS 里不存在** | `web/js/views/ops.js`、`web/css/app.css` | 用了 `blue`/`orange`/`purple`/`green`/`red`，而样式表只定义了 `default`/`primary`/`success`/`warning`/`danger`/`info` → 这些标签没有任何底色 | 全部映射到真实存在的类名 |

顺带修掉一个前端运行期 bug：`components.js` 的 `alertDialog` 里 `footer` 回调引用了未定义的 `m`
（`button('关闭', () => m.close())`），一点"关闭"就抛 `ReferenceError`。

## 1.13 第三轮回归测试

新增 `internal/api/method_gate_test.go`：**15 个有副作用的端点，逐个断言非 POST 一律 405**。

该测试用**空 `Server`** 直连 handler——方法闸门必须排在触碰数据库之前，否则这里会直接 nil panic。
换句话说，闸门一旦被挪到 `GetSite` 之后，测试立刻红（已用负例实测验证过这一点）。

同时补 `TestRequirePost` / `TestRequirePutOrPost` 两个方法闸门自身的用例。

第三轮结束时：`go test -count=1 ./...` **17 个包全绿**（39 个测试文件 / **212 个测试函数**），
`go vet ./...` 零告警，`gofmt -l .` 零输出，前端 15 个 ES Module esbuild 校验全过。

线上端到端：**35 项对抗用例全过、0 失败**（详见 `docs/SECURITY-v0.1.0.md` 第五节）。

---

# 二、第一轮：安全类（17 项）

> 2026-10-13 初版发布时修复，代码中依然保留。

### P0

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| S1 | 客户端可控 `site.ID` 导致 root 任意文件写 + 持久 nginx 注入 | `service.go` CreateSite | ID 为空才生成，请求体传值即采用；`SiteConfName` 直接拼路径，`filepath.Join` 的 clean 反而帮助 `..` 穿越生效 | 恒重新生成 ID；新增 `model.ValidateID` 十六进制白名单 |
| S2 | WebSocket 日志端点无会话校验 | `main.go` authWS | 只检查 cookie 是否存在，不查库；`HandleWebSocket` 自身也不校验 | 新增 `Server.Authorize(r)`，authWS 改为真实校验 |
| S3 | `Root.Path` 零校验，nginx 可静态伺服任意目录 | `service.go` | 只判空不校验，`ErrInvalidPath` 建了却从未使用 | 新增 `model.ValidatePath`（绝对路径/禁 `..`/禁系统目录/禁控制字符/长度上限），在建站与更新时校验 Root.Path、SubDir.Root、Lock.Files |

### P1

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| S4 | `set_real_ip_from 0.0.0.0/0` 信任全网，一个 `X-Forwarded-For` 头同时绕过 IP 白名单与按 IP 限速 | `render.go` buildRealIP | 可信代理范围硬编码全网 | 改为用户显式配置的 `Extra.TrustedProxies` CIDR 列表；留空则整段 real_ip 不输出 |
| S5 | nginx 配置注入面：`raw_conf`/`rewrite`/`redirect`/`proxy`/`hotlink` 等 20+ 处零转义 | `render.go` | 渲染器把配置字符串当裸值 | 域名已有白名单；其余字段补 `normalizeExactPath`/`sanitizeResolver`/白名单校验，`nginx -t` + 回滚兜底保留 |
| S6 | 删除站点可删面板数据目录（`id=".."`） | `service.go` DeleteSite | `RemoveAll(join(backupRoot,id))` 未校验 | 随 S1 的 ID 白名单一并修复（非法 ID 无法落库） |
| S7 | `handleSiteLogs` 的 `type` 参数路径穿越，root 读任意 `.log` | `api/misc.go` | `logType` 无白名单，直接拼路径 | 限定枚举 `{access,error}`；并断言最终路径仍在站点 logs 目录内 |
| S8 | 防篡改 `Lock.Files` 可指向任意绝对路径/全盘 Walk | `service.go` | 无 containment 校验 | `validateSitePaths` 统一校验，拒绝绝对路径与 `..` |
| S9 | 登录无速率限制，可暴力破解 + bcrypt CPU 耗尽 | `api/auth.go` | 认证入口无限流 | 新增 `loginLimiter`（IP+用户名维度，5 次/分钟窗口，触顶锁 15 分钟），且在 bcrypt 计算之前判断 |

### P2

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| S10 | 日志分页 `limit*page` 无上限，可 OOM | `api/misc.go` | `page` 只要求 >0 | `page ≤ 500`、`limit ≤ 1000`，且 `limit*page ≤ 100000` |
| S11 | 错误响应回显内部路径、nginx stderr、SQLite 报错 | `api/helpers.go` | default 分支直接回传 `err.Error()` | 改为 `errors.Is` 匹配领域错误；未知错误只记日志，对外返回统一"内部错误" |
| S12 | Webhook/dingtalk/wecom target 无 SSRF 防护 | `alert/engine.go` | target 是自由 URL | 已在审查报告中标注；当前为单管理员内网场景，v0.1.0 保留并在文档中给出加固建议 |
| S13 | HTTP Server 缺 `ReadTimeout`/`IdleTimeout`，Slowloris 可耗尽连接 | `main.go` | 超时配置不全 | 补 `ReadTimeout: 30s`、`IdleTimeout: 120s`（WriteTimeout 不设，避免影响 WebSocket） |
| S14 | `logmgr.Tail` 单行读取无长度上限 | `logmgr.go` | `ReadString` 不设上限 | 统一走 `maxLogLineBytes` 常量与 Scanner buffer |
| S15 | WebSocket Origin 校验用 `strings.Contains`，`evil.com` 包含即绕过 | `api/misc.go` | 包含判断而非相等 | 改为 `origin == scheme://r.Host` 精确比较 |
| S16 | 登录响应体回传 session token，HttpOnly 保护减半 | `api/auth.go` | 多余字段 | 删除 `token` 字段，只返回 `username`/`role` |
| S17 | 改密码不失效其他会话 | `api/auth.go` | 缺踢下线逻辑 | 新增 `store.DeleteUserSessionsExcept`，改密后删除该用户其它会话并记日志 |

# 三、第一轮：功能失效类（12 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| F1 | 流量采集从未被调用，统计/配额/配额告警全是死数据 | `main.go` | `CollectTraffic` 实现了却没挂后台任务 | 新增 `runTrafficCollector`，每 5 分钟遍历站点采集 |
| F2 | 证书续签成功后新证从不绑定回站点，nginx 继续 serve 旧证 | `main.go` | `Renew` 返回全新 ID/新路径的证书，三个调用方都丢弃返回值 | 新增 `rebindRenewedCert`，按域名反查站点并更新 `site.Cert` 与 `SSL.CertFile/KeyFile` |
| F3 | `ReadAccessLog` 文档承诺"读末尾"实现却从头扫描 | `logmgr.go` | Scanner 从 offset 0 顺序读 + 提前 break | 先按 `limit*512B` 估算窗口 seek 到尾部，再向前补齐 |
| F4 | `Tail` 第一个 EOF 就退出 goroutine，"实时日志"读完成存量即死 | `logmgr.go` | EOF 被当作终止条件 | EOF 后 sleep 300ms 重试同一 fd；启动时 seek 到末尾只推新行；`stop` 用 `sync.Once` 防重复 close |
| F5 | 配额告警单位错误，任何流量都瞬间超阈值 | `alert/checker.go` | 分子是字节、分母是 MB，差 1024² | 统一换算为字节比较；GB 展示改浮点 |
| F6 | 配额锁定变量只 set 不读，`enforce_quota` 是空操作 | `render.go` | 只置位无消费 | 补 `if (ssp_quota_lock) { return 403; }` |
| F7 | 回滚不覆盖 http 级配置与已删孤儿文件 | `service.go` | 快照只记录 per-site conf | http conf 旧内容与孤儿删除均纳入 `backups`；`rollback` 按 `httpConfKey` 区分处理；驱动新增 `RemoveConf` |
| F8 | `ListSites` 静默跳过损坏配置，孤儿清理反把站点配置删掉 | `store.go` | 读路径吞错 + 写路径把"读不到"当"不存在" | 解析失败时记日志并返回占位 Site（保留 ID），使其进入 `known` 集合不被清理 |
| F9 | `UpdateSite` 全量替换丢 `cert` 字段，SSL 配置静默丢失 | `service.go` | PUT 语义 + `omitempty` | `if site.Cert == nil { site.Cert = old.Cert }` |
| F10 | 前端实时跟踪 100% 失败（tail 路由不存在） | `web/js/api.js` | 请求 `/sites/{id}/logs/tail`，实际端点应是 `/system/logs/tail?site_id=` | 改为正确路由 |
| F11 | 前端历史日志永远"暂无记录"（漏 `records` 键） | `web/js/views/logs.js` | 猜成 `logs`/`lines`/`items` | 改为 `res.records` |
| F12 | 设置保存必 400（通知渠道数组、数字设置） | `web/js/views/alerts.js`、`settings.js` | 后端 settings 是 `string->string`，前端传数组/number | 值统一 `String()`；渠道列表 JSON 序列化后存 `alert_channels_json` 键 |

# 四、第一轮：渲染正确性类（10 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| R1 | SSL 就绪时只监听 443 丢掉 80 | `render.go` | if/else 二选一 | 同时监听 80 与 443 |
| R2 | HTTPS 跳转写在 server 级，把 ACME 挑战也 301 掉 | `render.go` | rewrite 阶段先于 location 匹配 | 跳移入 `location /` 内；新增 `location ^~ /.well-known/acme-challenge/` 放行 |
| R3 | `root` 只在 `location /` 内，PHP location 的 `sdocument_root` 取错 → `.php` 全 404 | `render.go` | 变量作用域 | root 提到 server 级 |
| R4 | 访问日志在 web 根下且默认无拒绝规则，`/logs/access.log` 可被公开下载 | `render.go` | 缺默认拒绝 | 固定输出 `location ^~ /logs/ { deny all; }` |
| R5 | 反代/黑名单的 location 被正则 location 抢走 | `render.go` | 普通前缀 location 不挡正则 | 反代 location 改用 `^~` |
| R6 | 静态缓存 location 的 `add_header` 抹掉继承的安全响应头 | `render.go` | nginx 的 add_header 不跨层继承 | 抽出 `securityHeaders(indent)`，缓存 location 内重复输出 |
| R7 | 黑名单/重定向路径被补尾斜杠，精确文件路径整条失效 | `render.go` | 复用 `normalizePathPrefix` | 新增 `normalizeExactPath`（只规范前导斜杠） |
| R8 | 重定向/反代路径为 `/` 时与主 location 重复 → nginx -t 失败全量回滚 | `render.go` | 不去重 | 子目录绑定跳过 `/`；渲染测试断言无重复 location |
| R9 | WAF 正则漏掉 URL 编码（`%3Cscript%3E` 直接放过） | `render.go` | `srequest_uri` 未解码 | 新增 `wafPattern` 扩展编码变体，`\s+` 放宽为 `(?:\s|%20|\+)+` |
| R10 | 子目录 map 是死代码且 key 语义错误 | `render.go` | nginx map key 是字面量不插值；无消费方 | 整段删除 |

# 五、第一轮：健壮性与质量类（10 项）

| # | 问题 | 位置 | 根因 | 修复 |
|---|------|------|------|------|
| Q1 | 日志解析遇超长行整次读取失败，5xx 告警与统计同时静默停摆 | `logmgr.go` | `sc.Err()` 直接 return | `ErrTooLong` 时跳过该行继续 |
| Q2 | `Aggregate` 的 Date 取最后一条（最旧）记录 | `logmgr.go` | 循环内覆盖 | 保留按 `r.Time` 分组的语义并在注释中明确 |
| Q3 | `SiteSummaries` 把当月流量当"今日流量"展示 | `service.go` | `TodayBytes = monthBytes` | 保留字段但注释标明近似语义，避免误读 |
| Q4 | `DeleteSite` 不清理 integrity/tamper_events | `service.go`/`store.go` | 只删 sites 行 | 删除站点时同事务清两表 |
| Q5 | 域名校验不规范化存储值，带空格域名可绕过冲突检查 | `model.go` | 校验 trim 后存原值 | 校验时 trim + 小写再存储 |
| Q6 | PHP-FPM socket 探测匹配不到 Ubuntu 命名（`php8.1-fpm.sock` 无连字符） | `hostdriver/host.go` | glob/正则写死 `php-*-fpm.sock` | glob 改 `php*fpm.sock`，正则连字符可选 |
| Q7 | 告警通知失败被完全吞掉 | `alert/engine.go` | `if err != nil { continue }` | 记录日志；冷却态与失败重留在文档标注 |
| Q8 | `site_down` 与防篡改告警是空钩子 | `alert/checker.go` | 无注入点/不调用 | 已在文档中标注为已知限制，给出改造方案（**后续版本已完成接线**：`main.go` 注入 `SetSiteProbeProvider` 真实 HTTP 探测与 `SetTamperChecker` 真实文件比对，`internal/alert/hooks_test.go` 锁死链路） |
| Q9 | 前端 WS 离开视图不关闭、切换站点缓冲不清空 | `web/js/views/logs.js` | 无卸载钩子 | `render()` 开头 `stopTail()` + 清空 `lineBuf` |
| Q10 | 创建向导域名输入每敲一字失焦；PHP 版本数组当对象用；吊销证书不刷新；仪表盘单点失败打穿整页 | `web/js/views/*` | 多处 | 逐一修复（`syncName` 不重绘、`Array.isArray(status.php)`、吊销后重渲、非关键接口 `.catch` 降级） |

# 六、第一轮：依赖漏洞（14 项，全部清零）

`govulncheck ./...` 修复前报告 **14 个影响代码的漏洞**，修复后 **0 个**。

| 模块 | 修复前 | 修复后 | 涉及 CVE |
|------|--------|--------|----------|
| github.com/go-acme/lego/v4 | v4.21.0 | **v4.25.2**（第二轮再升到 v4.35.2） | GO-2025-3847（ACME API 不强制 HTTPS，可中间人）、GO-2026-5593（Webroot 路径穿越） |
| golang.org/x/net | v0.33.0 | **v0.60.0** | GO-2026-6617/6612/6611/6610/6603/5026 |
| golang.org/x/text | v0.23.0 | **v0.43.0** | GO-2026-5970 |
| github.com/go-jose/go-jose/v4 | v4.0.4 | **v4.1.4** | GO-2026-4945 |
| modernc.org/sqlite | v1.34.5 | **v1.60.1** | 跟进上游修复 |
| golang.org/x/crypto | v0.36.0 | **v0.58.0** | — |
| Go 工具链 | go1.25.14 | **go1.26.9 → go1.26.10** | net/http、net/textproto、crypto/tls、crypto/x509 共 8 个标准库漏洞、GO-2026-6809 |

# 七、回归测试总览

| 轮次 | 测试规模 | 关键锁死测试 |
|------|---------|-------------|
| 第一轮（2026-10-13） | 56 个用例 | `internal/nginx/ssl_listen_test.go`（双端口监听、跳转不拦 ACME、server 级 root）、`render_fix_test.go`（WAF 编码变体、精确路径匹配、伪静态渲染）、`waf_compile_test.go`（模拟 nginx 双引号反转义后再编译正则）、`proxy_prefix_test.go`（反代 location 必须 `^~`）、`full_feature_test.go`（40+ 断言，含"禁止信任全网代理"）、`service/apply_test.go`（孤儿清理、停用逐行注释）、`store/store_test.go`、`hostdriver/host_test.go` |
| 第二轮（2026-10-28） | 17 个包 / 38 个测试文件 / **209 个测试函数** | `internal/htpasswd/htpasswd_test.go`（10 组 OpenSSL 权威向量）、`internal/nginx/render_basic_auth_test.go`（6 类渲染断言 + ACME 豁免）、`internal/service/access_test.go`（9 类凭据生命周期断言）、`internal/version/version_test.go`（版本门禁 + 负例）、`internal/cron/shell_guard_test.go`（6 类绕过 payload 负例 + 正向用例） |
| 第三轮（2026-10-28 晚） | 39 个测试文件 / **212 个测试函数** | `internal/api/method_gate_test.go`（**15 个有副作用的端点 × 6 种方法**逐个断言非 POST 一律 405；用空 `Server` 直连 handler，闸门被挪到读库之后会立刻 panic）+ `TestRequirePost` / `TestRequirePutOrPost` |

**当前状态**：`go build ./...` ✅、`go vet ./...` ✅ 零告警、`go test -count=1 ./...` ✅ 全绿、
`gofmt -l .` ✅ 零输出、`govulncheck ./...` ✅ affected=0（须用与构建一致的 go1.26.10 工具链编译扫描器）、
前端 15 个 ES Module esbuild 校验 ✅ 全过、
**线上端到端对抗测试 ✅ 35 项全过、0 失败**（生产机 198.51.100.10）。
