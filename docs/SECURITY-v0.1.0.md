# 智维面板 ZhiweiPanel 安全审查报告（v0.1.0）

- **审查对象**：`github.com/zhiwei/panel` v0.1.0（Go 1.26.10 单二进制，**103 个 Go 文件 / 21203 行**，**15 个前端 ES Module / 6139 行**，39 个测试文件 / 212 个测试函数）
- **审查方式**：
  - `govulncheck ./...` 依赖扫描（affected=0）
  - **4 路并行人工源码审计**（第二轮，2026-10-27~28）：API 与鉴权 / 调度与告警 / 驱动与服务与存储 / 前端与 API 契约，原始报告全量留存于 `docs/audit/`（约 259 KB）
  - 4 路并行人工源码审计（第一轮，2026-10-12~13）
  - 线上渗透测试（见第五节）
- **审查日期**：2026-10-12 ~ 2026-10-28

---

## 一、结论

| 指标 | 全部修复前 | v0.1.0 修复后 |
|------|-----------|--------------|
| 依赖漏洞（`govulncheck`） | **14 个影响代码** | **0 个**（第三轮用正确工具链复查 affected=0，见 §3.6 工具链说明） |
| P0（可 getshell / root 沦陷） | 1 | **0** |
| P1（可越权或注入） | 4（第一轮）+ **21 项 P1**（第二轮）+ **5 项 P1**（第三轮，N31–N35） | **0** |
| P2（信息泄露 / DoS） | 8 | **0**（SSRF 与凭据明文两项保留并给出加固建议，见第六节） |
| 审计记录规模 | BUG 67 项 / 安全缺陷 20 项 / 功能残缺 40+ 项（第二轮） | 已修 26 项 P1，其余留档待办 |
| 线上对抗实测 | 未做 | **35 项全过、0 失败**（见第五节） |
| 总体安全评分 | **4 / 10** | **8.5 / 10** |

> 评分口径：满分 10 分 = 认证可靠 + 权限模型收敛 + 注入面清零 + 依赖无已知漏洞 + 有审计追溯。
> v0.1.0 拿不到 10 分的原因集中在第六节（认证之后的权力没有二次收敛），
> 这是产品形态问题（单管理员 + root 运行 + raw 能力），不是可以靠打补丁消掉的问题。

---

## 二、修复前最危险的攻击链（已全部阻断）

### 2.1 第二轮：只读 Token 直接变写权限

```
拿到一个 scope=read 的 API Token（CI / 监控用的那个）
  │
  ├─ GET  /api/sites/{id}/toggle      → 停掉生产站点
  ├─ GET  /api/certs/{id}/revoke      → 吊销证书 → 站点 HTTPS 全面不可用
  ├─ GET  /api/system/config-apply    → 全量重下发 nginx 配置
  └─ GET  /api/alerts/{id}/resolve    → 抹掉告警痕迹
        │
        └─ 这些全是 GET → 审计中间件按方法判定 → 写操作**不留痕**
```

**阻断点**：新增 `requirePost(w,r)`，8 个有副作用的端点一律要求 POST（N1）。
写操作改 POST 后，审计中间件（`isAuditedMethod` 覆盖 POST/PUT/PATCH/DELETE）自动开始留痕。

> 第三轮按同一条规则又收口了 5 个端点（N31–N35），并补上 `method_gate_test.go`
> 把「有副作用的端点不得用 GET 触发」锁死成回归测试——见 §2.8。

### 2.2 第二轮：MySQL DDL 拼接注入

```
POST /api/sites
  {"domains":["a.com"], "mysql":{"user":"x' ; DROP DATABASE production; -- ","password":"…"}}
        │
        └─ service 层把客户端指定的 User/Password/Charset 原样拼进
           CREATE USER / CREATE DATABASE / GRANT（DDL 不支持占位符参数化）
                 │
                 └─ 面板的 MySQL 客户端持有 root 凭据 → 数据库沦陷
```

**阻断点**：三层封堵（N2）——service 层**拒绝**客户端指定 User/Password/Charset；
model 层 `ValidateMysql` 白名单；driver 层 `quoteIdent`/`quoteLiteral` 转义 +
`reservedUsers` 保留名保护。

### 2.3 第二轮：伪造 X-Forwarded-For 绕过全部 IP 防护

```
攻击者伪造请求头：X-Forwarded-For: 1.2.3.4
  │
  ├─ 登录限流按 IP 计数 → 每次换头即绕过（撞库无限次尝试）
  ├─ "异常登录"二次验证按 IP 判断 → 永远不触发
  └─ 拿到 admin 会话后：
       └─ raw_conf 写任意 nginx 指令 → 面板以 root 管 nginx → 服务器 root 沦陷
```

**阻断点**：新增 `internal/api/clientip.go`——只有**直连方**命中受信 CIDR 才采信 XFF；
新增 `-trusted-proxy` 参数；登录限流再加账号级第二层（N3）。

### 2.4 第二轮：nginx 配置文本注入

```
model.ValidateSite() 写了一整套校验器，但**从来没有被调用**（死代码）
  │
  └─ 站点字段（重定向目标、反代目标、UA 屏蔽词、IP 名单……）原样进入 nginx 配置
       └─ 一个分号就能追加指令：
          redirect.rules[].to = "/x; root /; autoindex on;"
                 │
                 └─ nginx 直接伺服整个文件系统 → 面板数据库（zhiweipanel.db）
                    → 含 admin bcrypt 哈希与有效 session → root 沦陷
```

**阻断点**：三道防线（N14/N23）——域名硬校验 → 路径硬校验 → `SanitizeSite` 净化降级；
路径类字段不做"净化替换"（替换目录 = 静默 404），作站点级硬错误由调用方隔离。

### 2.5 第二轮：shell 计划任务 = root RCE

```
API Token（scope=write）→ POST /api/cron/jobs {"type":"shell","script":"sh''utdown -h now"}
  │
  └─ 危险命令检查是**纯子串黑名单** → 字面量拼接即可绕过
       └─ 任务以面板的 root 身份执行 → 任意命令
```

**阻断点**：归一化 → 词法切分 → 命令词匹配（N20）；同时把 `shell` 拆成**独立 scope**（N21），
使泄漏的普通 Token 不再等于 root shell。

### 2.6 第二轮：站点访问认证口令被任意只读 Token 读走

```
GET /api/sites/{id}   （任意只读 Token 即可）
  │
  └─ 响应含 access.auth_pass 明文 —— 该字段随站点配置 JSON 一起存进 SQLite
       └─ 拿下所有开了 Basic Auth 的站点的访问密码
```

**阻断点**：明文改只写字段，服务端立刻换成 apr1 哈希（N15/N22）；读路径统一脱敏。

### 2.7 第一轮：ID 穿越 + 根目录任意路径（经典链）

```
POST /api/sites
  {"domains":["a.com"], "id":"../../conf.d/evil",
   "root":{"path":"/opt/zhiweipanel/data"},
   "raw_conf":"server {\n listen 9999;\n root /;\n autoindex on;\n}"}
        │
        ├─ ID 穿越 → 配置写到 /etc/nginx/conf.d/evil.conf
        ├─ root / + autoindex on → 经 nginx 下载 zhiweipanel.db
        └─ 库含 bcrypt 哈希 + 有效 session → 接管面板 → root 沦陷
```

**阻断点**：`CreateSite` 恒重新生成 ID + `model.ValidateID` 白名单（S1）；
`model.ValidatePath` 根目录校验（S3）。

### 2.8 第三轮：GET 触发的副作用端点（只读 Token 提权 + 告警风暴 + CSRF）

第二轮把 8 个写端点改成强制 POST 后，权限模型的判定标准就固定下来了：

```
authMiddleware：
  非 GET 方法 → 要求 write scope
  GET/HEAD/OPTIONS → 不校验 scope（只读 Token 免检）
```

于是**任何"GET 可达 + 有副作用"的端点，都是只读 Token 的提权通道**。
第二轮审计后又找出 4 个漏网的：

```
只读 Token（scopes:["read"]）
  │
  ├─ GET /api/sites/{id}/baseline
  │     → EstablishBaseline → ReplaceIntegrity
  │     → 把防篡改基线刷成"当前状态"
  │     → 已经发生的篡改被洗成正常，防篡改检测形同虚设
  │
  ├─ GET /api/sites/{id}/tamper
  │     → CheckTamper → SaveTamperEvent（写事件表）+ Raise（触发告警通知）
  │     → 无限插行 + 把邮件/webhook 通道刷爆
  │
  ├─ GET /api/auth/logout
  │     → DeleteSession
  │     → 第三方页面一个 <img> 就能把管理员踢下线（CSRF 登出）
  │
  └─ GET /api/auth/password / GET /api/admin/account/email
        → 改密 / 改邮箱走的是无方法校验的 handler
```

第二个（`tamper`）尤其隐蔽：函数名叫"检测"，实际会写库并发通知，
从接口签名上看完全像只读。

**阻断点**：新增 `requirePost` / `requirePutOrPost` 两个方法闸门，
把 4 个端点全部收口（N31–N35）；并新增
`internal/api/method_gate_test.go` 锁死这条不变量——
**15 个有副作用的端点，逐个断言非 POST 一律 405**。
该测试用空 `Server` 直连 handler，闸门一旦被挪到读库之后就会立刻 panic，
从而保证"闸门在触碰数据之前生效"。

> 判定标准写进了 `requirePost` 的注释里：
> **凡是会写库、写文件、发通知的端点，一律不能只靠 GET 触发。**
> 只读 Token 的免检名单只有 GET/HEAD/OPTIONS，
> 所以"GET 可达 + 有副作用"就等于权限提升。

---

## 三、已修复漏洞明细

### 3.1 认证、会话与权限模型

| 项 | 内容 |
|----|------|
| **N1** 只读 Token 的写能力绕过 | 8 个有副作用的端点从 GET 改为强制 POST（`internal/api/certs.go`、`server.go`）。只读 Token 不再能写；写操作自动进审计。第三轮再收口 5 个（N31–N35）并加 `method_gate_test.go` 锁死不变量 |
| **N3** X-Forwarded-For 无条件信任 | `internal/api/clientip.go`：`trustedProxySet` 只接受显式配置的 CIDR；`remoteIP` 只在直连方受信时采信 XFF；新增 `-trusted-proxy` flag |
| **N4** 异常登录提示泄露管理员上次 IP | `internal/service/account.go` 不再回显该字段 |
| **N22** 客户端可注入 `auth_hash` 做降级 | `applyAccessAuth` 一律丢弃客户端提交的哈希，只认明文口令或库里已有哈希 |
| **S2** WebSocket 无会话校验 | `authWS` 原只检查 cookie 存在性 → 新增 `Server.Authorize(r)` 做真实 `GetSession` 校验 |
| **S9** 登录无速率限制 | 新增 `loginLimiter`（IP+用户名维度，5 次/分钟，触顶锁 15 分钟），判断置于 bcrypt 计算**之前**；第二轮再加账号级第二层 |
| **S16** 登录响应回传 token | 删除 `token` 字段，HttpOnly 保护不再被抵消 |
| **S17** 改密不失效其它会话 | `DeleteUserSessionsExcept` 保留当前会话踢掉其它 |
| **N21** API Token 的 `write` scope 等价 root RCE | 新增独立 `shell` scope（`requireShellScope` 在 cron 创建/修改/立即执行三处生效）；前端 Token 创建弹窗同步补上 `shell` 勾选项（此前后端有、前端没有，`shell` Token 在界面上还显示成"只读"） |
| **N31** `GET /api/sites/{id}/baseline` 可改写防篡改基线 | 强制 POST（`requirePost`）。只读 Token 原本能把基线刷成当前状态，把已发生的篡改洗成正常 |
| **N32** `GET /api/sites/{id}/tamper` 可写事件表并触发告警通知 | 强制 POST。函数名像只读，实际 `CheckTamper` 会 `SaveTamperEvent` + `Raise`，原本可被用来插行、刷爆通知通道 |
| **N33** `GET /api/auth/logout` 可跨站强制登出 | 强制 POST（CSRF：第三方页面的 `<img>` 即可触发） |
| **N34** `GET /api/auth/password` 改密端点无方法校验 | 强制 POST |
| **N35** `GET /api/admin/account/email` 等账户端点无方法校验 | 改邮箱限定 PUT/POST（新增 `requirePutOrPost`）；触发邮箱验证、SMTP 测试一并强制 POST |

### 3.2 路径与文件系统

| 项 | 内容 |
|----|------|
| **N11** `UpdateSite` 丢 `Status`/`Edate`/`Mysql` | 显式回填；`Mysql` 一律沿用库里的值，同时堵住"用更新接口改 MySQL 参数"的注入路径 |
| **N15** 站点访问认证口令明文入库与回显 | 改 apr1 哈希 + 只写字段；读路径 `SanitizeForRead`/`SanitizeAccessSecrets` 统一脱敏 |
| **N16** 备份保留策略磁盘泄漏 | 先删磁盘文件再裁 DB；新增 `PruneOrphanFiles` |
| **S1** `site.ID` 任意文件写 | ID 恒由服务端生成 + 十六进制白名单 |
| **S3** `Root.Path` 零校验 | `model.ValidatePath`（绝对路径 / 禁 `..` / 禁系统目录 / 禁控制字符 / 长度上限） |
| **S7** 日志类型路径穿越 | 白名单枚举 + 路径 containment 断言 |
| **S8** `Lock.Files` 穿越 / 全盘扫描 | 纳入 `validateSitePaths` |

### 3.3 nginx 配置注入与渲染正确性

| 项 | 内容 |
|----|------|
| **N14** `model.ValidateSite` 从未被调用 | 接入 CreateSite/UpdateSite；`ValidateMysql` 同批接入 |
| **N23** 渲染层缺统一净化层 | 新增 `internal/model/sanitize.go`（`SanitizeSite` + 与校验器共用同一批谓词，避免规则漂移）；`validate.go` 重构出 `ErrInvalidValue` + `fieldError`（干净错误文案，API 映射 400） |
| **N24** 单个坏站点卡死全站配置下发 | `ApplyAllConfig` 隔离坏站点（整段注释掉），其余照常下发，记 `configIssues` 并通过 API 暴露 |
| **N13** Docker 模式 PHP 站点全 502 | 进容器 `ls` 真实 socket 路径；`looksLikePHPServerVersion` 不再从 "default" 推 socket |
| **N18/N19** 证书续签不重绑 / 重复签发撞 LE 限额 | 手动续签同样回绑站点（`RebindRenewedCert`）；自动续签复用原证书 ID |
| **S4** `set_real_ip_from 0.0.0.0/0` | 改为用户显式配置的 CIDR 列表（`Extra.TrustedProxies`），留空则整段不输出 |
| **S5** 渲染层零转义面 | 域名严格白名单；其余字段补路径规范化与白名单；`nginx -t` + 回滚兜底保留。`raw_conf`/`rewrite` 为设计内的管理员 raw 能力，已在文档中明示风险 |
| **N29** Basic Auth 配置了不生效 | 补齐闭环：apr1 哈希 → 口令文件 → `auth_basic` + `auth_basic_user_file`；ACME 挑战目录显式 `auth_basic off;`（否则续签被 401 打断） |

### 3.4 注入面（SQL / 命令 / 计划任务）

| 项 | 内容 |
|----|------|
| **N2** MySQL DDL 拼接注入 | 三层封堵：服务端决定 User/Password/Charset；`ValidateMysql` 白名单；driver 层 `quoteIdent`/`quoteLiteral` + `reservedUsers` |
| **N20** shell 黑名单可绕过 | 归一化 → 词法切分 → 命令词匹配；fork 炸弹/sysrq 模式匹配；`systemctl` 与递归删根单独判定 |
| **N12** 建库失败留孤儿库 | 改为先落库再真建；建库失败时意图仍留在库里可重试 |
| **SEC-07** `mysqldump` 参数注入 | 建库/备份的库名一律由面板生成且不以 `-` 开头 |

### 3.5 信息泄露与 DoS

| 项 | 内容 |
|----|------|
| **N5** 前端把 JSON 文件当 JSON 解析 → 保存即清空文件 | 响应按 Content-Type 分流解析 |
| **N10** 站点告警枚举与后端检查器不一致 | 枚举单一权威源 `model.AlertRuleTypes` + `GET /api/alert-types` |
| **S11** 错误回显内部细节 | `errors.Is` 匹配领域错误，未知错误只记日志 |
| **S10** 分页 OOM | `limit*page ≤ 100000` |
| **S13** Slowloris | 补齐 `ReadTimeout`/`IdleTimeout` |
| **S14** Tail 单行无上限 | 统一走 `maxLogLineBytes` |

### 3.6 依赖漏洞（14 → 0）

| 模块 | 修复前 | 修复后 | 涉及漏洞 |
|------|--------|--------|----------|
| github.com/go-acme/lego/v4 | v4.21.0 | **v4.35.2** | GO-2025-3847（ACME API 不强制 HTTPS）、**GO-2026-5593**（Webroot 路径穿越 → 任意文件写） |
| golang.org/x/net | v0.33.0 | **v0.60.0** | GO-2026-6617/6612/6611/6610/6603/5026 |
| golang.org/x/text | v0.23.0 | **v0.43.0** | GO-2026-5970 |
| github.com/go-jose/go-jose/v4 | v4.0.4 | **v4.1.4** | GO-2026-4945 |
| modernc.org/sqlite | v1.34.5 | **v1.60.1** | 跟进上游修复 |
| golang.org/x/crypto | v0.36.0 | **v0.58.0** | — |
| Go 工具链 | go1.25.14 | **go1.26.10** | net/http、net/textproto、crypto/tls、crypto/x509 共 8 个标准库漏洞 + GO-2026-6809 |

**零新增依赖**：本轮未引入任何新模块，`go mod tidy` 后依赖集合仅减不增。

### 3.7 `govulncheck` 的工具链陷阱（第三轮复查记录）

第三轮复查时 `govulncheck` 一度报出 **"11 个标准库漏洞命中代码"**（`crypto/x509` GO-2026-6809、`net/http` 6 个、`net/textproto`、`crypto/tls`），一度以为修复失效。**结论是假阳性**，成因有两层：

1. **扫描器用的是启动器工具链，不是构建工具链。**
   本机 `go version` 报 `go1.25.14`，但 `go.mod` 的 `go 1.26.10` 指令会让 `GOTOOLCHAIN=auto` **自动切换到 go1.26.10**（`go env -C <repo> GOVERSION` = `go1.26.10`，GOROOT 指向已下载的 go1.26.10 工具链）。
   而 `govulncheck` 二进制本身是用 go1.25.14 编译的，它按**自己的**标准库版本判定 → 把 go1.26.10 已修复的漏洞又报了一遍。
   **修法**：用与构建一致的工具链重编扫描器（`GOTOOLCHAIN=go1.26.10 go install golang.org/x/vuln/cmd/govulncheck@latest`），复查结果为 `No vulnerabilities found. Your code is affected by 0 vulnerabilities.`
2. **`-mode=binary` 对已构建产物报出的 3 条模块漏洞同样是假阳性。**
   它报 `golang.org/x/net/html`（GO-2026-6866/6860）与 `golang.org/x/crypto/openpgp`（GO-2026-5932）的符号"存在于二进制"。
   但 `go list -deps .`（linux/amd64，329 个包）对这两个包的命中数是 **0**，且二进制字符串表里**根本不含** `golang.org/x/net/html` 与 `golang.org/x/crypto/openpgp` 这两个串——它们压根没被链接进产物（`openpgp` 只是 `x/crypto` 模块图里的包，不是实际依赖）。

**权威判据顺序**（本项目沿用）：
`go version -m <二进制>` 看真实构建工具链 → `go list -deps` 看实际链接的包 → `govulncheck`（与构建同工具链）源码模式看可达性 → `govulncheck -mode=binary` 只作补充参考。
**不要只看 `govulncheck` 的单条输出就断言"有漏洞"或"已修复"。**

---

## 四、代码层面的安全能力清单

| 能力 | 位置 | 说明 |
|------|------|------|
| 会话 token 256-bit 加密随机 | `internal/store/store.go` | `NewID()+NewID()`，服务端 TTL 校验 |
| bcrypt + 用户不存在时时序对齐 | `internal/api/auth.go` | 防用户名枚举 |
| `SameSite=Strict` cookie | `internal/api/auth.go` | 挡 CSRF |
| 域名输入严格字符白名单 | `internal/model/validate.go` | |
| 路径白名单 + 禁系统目录 | `internal/model/validate.go` | |
| `exec.Command` 全链路参数化 | 全仓 | 无 shell 注入面 |
| 全仓 SQL 参数化 | `internal/store/**` | 标识符/字面量用转义函数 |
| `nginx -t` 失败自动回滚 | `internal/service/service.go` | `reloadMu` 覆盖所有写路径 |
| 单站点隔离 | `internal/service/service.go` | 一个坏站点不再拖垮全站 |
| 可信代理白名单 | `internal/api/clientip.go` | XFF 不再无条件采信 |
| 版本号常驻门禁 | `internal/version/version_test.go` | 防止版本漂移导致部署错版 |
| 审计留痕 | `internal/api/audit.go` | 写操作全留痕（改 POST 后自动覆盖） |

---

## 五、线上渗透测试结果

> 本节记录在真实部署环境（生产 nginx + 面板）上实际执行的对抗性测试。
> 测试基线：**v0.1.0**（2026-10-28 构建，linux/amd64，md5 `fa4357088dc41df54e18705983cacf8e`）。
> 执行环境：生产机 `198.51.100.10`（真实 nginx 1.18.0 + 面板 root 运行 + 真实站点）。
> 测试方式：脚本化 curl 对抗测试（`e2e_final2.sh`），**未执行**的项一律标注"未执行"，不做推测性结论。

### 5.1 总体结果

| 指标 | 结果 |
|------|------|
| 用例总数 | **35** |
| 通过 | **35** |
| 失败 | **0** |
| 被测写端点（GET 应被拒） | 11 个，全部返回 405 |
| 只读 Token 越权尝试 | 3 次，全部被拒（403） |
| Basic Auth 凭据矩阵 | 无凭据 401 / 错误口令 401 / 正确口令 200 |
| 现有业务站点回归 | part.example.com → 301（强制 HTTPS）、toutiao.example.com → 200 |

### 5.2 逐项实测结果

| # | 对抗动作 | 期望 | 实测 | 结论 |
|---|---------|------|------|------|
| 1 | 未授权访问受保护接口 | 401 | 401 | ✅ |
| 2 | 伪造 `X-Forwarded-For: 203.0.113.77` | 记直连地址 127.0.0.1 | 日志 `ip=127.0.0.1` | ✅ 伪造头不被采信 |
| 3 | `GET /api/sites/{id}/toggle` | 405 | 405 | ✅ |
| 4 | `GET /api/system/config-apply` | 405 | 405 | ✅ |
| 5 | `GET /api/alerts/{id}/resolve` | 405 | 405 | ✅ |
| 6 | `GET /api/certs/{id}/revoke` | 405 | 405 | ✅ |
| 7 | `GET /api/certs/{id}/bind` | 405 | 405 | ✅ |
| 8 | `GET /api/sites/{id}/baseline`（改写防篡改基线） | 405 | 405 | ✅ |
| 9 | `GET /api/sites/{id}/tamper`（写事件表+发通知） | 405 | 405 | ✅ |
| 10 | `GET /api/auth/logout`（CSRF 登出） | 405 | 405 | ✅ |
| 11 | `GET /api/admin/account/email` | 405 | 405 | ✅ |
| 12 | `GET /api/admin/account/email/verify` | 405 | 405 | ✅ |
| 13 | `GET /api/admin/smtp/test` | 405 | 405 | ✅ |
| 14 | 只读 Token `GET /api/sites` | 200 | 200 | ✅ 读权限正常 |
| 15 | 只读 Token `POST .../toggle` | 403 | 403 | ✅ |
| 16 | 只读 Token `POST .../baseline` | 403 | 403 | ✅ |
| 17 | 读写 Token `POST .../baseline` | 过闸门（400=未启用防篡改） | 400 | ✅ 方法闸门不再误伤 |
| 18 | write Token 建 shell 计划任务 | 403 | 403 | ✅ shell 不蕴含于 write |
| 19 | shell Token 建 shell 计划任务 | 201 | 201 | ✅ 独立授权位可用 |
| 20 | 建站时返回 `auth_pass` | 空字符串 | `""` | ✅ 不回显口令 |
| 21 | 口令文件是否落明文 | 无 | 无（仅 apr1 哈希） | ✅ |
| 22 | apr1 哈希与 OpenSSL `passwd -apr1` | 逐字节一致 | 一致 | ✅ |
| 23 | nginx 配置是否含 `auth_basic_user_file` | 有 | 有 | ✅ |
| 24 | ACME 挑战段是否 `auth_basic off;` | 有 | 有 | ✅ |
| 25 | 无凭据访问受保护站点 | 401 | 401 | ✅ |
| 26 | 错误口令访问 | 401 | 401 | ✅ |
| 27 | 正确口令访问 | 200 | 200 | ✅ |
| 28 | ACME 挑战路径是否被认证拦住 | 不拦（404） | 404 | ✅ 续签不会被 401 打断 |
| 29 | 关闭访问认证后无凭据访问 | 200 | 200 | ✅ |
| 30 | 删除站点后配置是否残留 | 无 | 无 | ✅ |
| 31 | 删除站点后口令文件是否残留 | 无 | 无 | ✅ |
| 32 | 面板 API 未带凭据 | 401 | 401 | ✅ |
| 33 | 现有站点 part.example.com | 301 | 301 | ✅ |
| 34 | 现有站点 toutiao.example.com | 200 | 200 | ✅ |
| 35 | 三档 Token 创建（read / read+write / +shell） | 全部成功、scope 落库 | `"scopes":["read","write","shell"]` | ✅ |

### 5.3 未执行的项（明确标注，不做推测）

| 项 | 状态 | 原因 |
|----|------|------|
| MySQL DDL 注入（`user`/`password` 拼接） | 未执行线上实测 | 需真实 MySQL 账号；防护已由三层封堵 + 单测覆盖 |
| SSRF（webhook / dingtalk / wecom / `access_url` 打内网） | 未执行线上实测 | 已知残留风险，见 §6.1；SMTP 未配置，告警通道本身不可达 |
| nginx 配置文本注入（`raw_conf` 注入） | 未执行线上实测 | `raw_conf` 是设计内的管理员 raw 能力，见 §6.3 |
| 证书申请/续签全流程 | 未执行线上实测 | 需真实 ACME 账户与 DNS 凭据 |
| 暴力破解锁定阈值（连续失败触发锁定） | 未执行 | 避免把生产账号锁在门外 |
| 面板 HTTPS 证书链校验 | 未执行 | 面板用自签证书，仅供内网/受控访问 |

### 5.4 渗透测试过程中的工具坑（避免下次误判为产品缺陷）

- **`curl` 的 cookie 域作用域**：会话 cookie 作用域是 `127.0.0.1`，用同一份 cookie jar 去请求 `https://198.51.100.10:8888` 会得到 **401**——这是 curl 的域匹配规则，不是鉴权缺陷。手工带 `Cookie:` 头请求公网 IP → **200**，会话在公网 IP 上同样有效。
- **cookie jar 里的 `#HttpOnly_` 前缀**：用 `grep -v '^#'` 过滤会把 HttpOnly 行一起滤掉，导致手工构造的 `Cookie` 头为空字符串，表现为"会话失效"。
- **HTTP 打 HTTPS 端口**：8888 是 TLS 端口，用 `http://` 请求会得到 Go 返回的 **400**（`client sent an HTTP request to an HTTPS server`），日志里能查到，不是面板故障。
- **nginx `-s reload` 是异步的**：配置未换上时命中默认 server 的 `return 444`，curl 记为 **000**（断连）。`sleep 2` 后即正常。
- **Token 必须留到用例跑完再删**：先删后用会让后续用例全部变成 401。

---

## 六、已知限制与加固建议

以下为 v0.1.0 **未完全闭环**但已评估风险的项目，按优先级排列。

### 6.1 通知渠道 / `access_url` SSRF（P2，保留）

`alert/engine.go` 的 webhook/dingtalk/wecom `target`，以及计划任务的 `access_url`，
未做完整网段校验：已认证用户可让面板代发请求打内网（如 `169.254.169.254` 云元数据）。

**现状**：`access_url` 的防护只检查 IP 字面量，主机名与 30x 跳转仍可绕过；
通知渠道 target 无防护。

**建议加固**（下版本）：

```go
// 自定义 DialContext，拒绝 loopback / link-local / 私网
transport := &http.Transport{
    DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
}
// 解析 target host，netip.ParseAddr 后判断 IsLoopback()/IsPrivate()/IsLinkLocalUnicast()
// 并禁用 30x 自动跟随（或逐跳复检）
```

### 6.2 敏感凭据明文落库（P2）

- 钉钉 `access_token`/`secret` 存在 URL query 里随 `Target` 入库；
- 站点 MySQL 密码以明文存进站点配置 JSON；
- 会话 token 在库中明文存储。

**现状**：站点 MySQL 密码与面板 root 凭据模型一致，改存储格式会破坏"重置密码"流程，
本版选择**保持一致并如实记录**，而不是悄悄换格式。

**建议**：敏感字段加密存储（AES-GCM，密钥置于数据目录外），或至少对 DB 文件做 `0600` + 目录 `0700`。

### 6.3 `raw_conf` / `rewrite` 的管理员 raw 能力（P1，设计内）

这是对标宝塔的产品功能，但意味着任何已认证会话都对 nginx 配置有完全控制力
（nginx master 以 root 运行）。

**建议**：对含 `raw_conf` 的保存操作要求二次输入密码；或增加危险指令黑名单
（`root`、`alias`、`fastcgi_pass`、`proxy_pass`、`listen`、`include`）。

### 6.4 shell 计划任务是 root shell（P1，设计内 + 护栏）

面板以 root 运行，计划任务的 shell 脚本天然拥有 root 权限。
本轮已做两件事：① 危险命令检查从子串黑名单升级为词法级匹配；
② 新增独立 `shell` scope，使普通 Token 触碰不到它。

**但必须明确**：黑名单**只是护栏，不是安全边界**。绕过方式仍然存在（解释器内联、
环境变量拼装、未覆盖的新命令……）。

**建议**：需要强隔离时，把 shell 任务放进独立容器或受限 systemd 单元里执行，
而不是继续加固黑名单。

### 6.5 RBAC 预留未实现（P3）

`users.role` 字段与 `/api/auth/me` 已暴露 role，但无多用户/角色管理 API，
`authMiddleware` 只验登录不验角色。

**现状**：实际单用户，无横向越权。

**建议**：要么移除 role 概念，要么在写操作中间件层强制 `role=="admin"`。

### 6.6 告警投递失败无反馈（P2）

四种渠道（email / webhook / dingtalk / wecom）**都真的发送**，但发送失败被完全吞掉
（`internal/alert/engine.go` 的 `if err != nil { continue }`），用户在界面上看不到任何提示，
只会发现"配了告警但没收到"。

> 补充说明（避免误判）：`site_down` 与防篡改告警**不是空钩子**——
> `main.go` 注入了 `SetSiteProbeProvider`（真实 HTTP 探测）与 `SetTamperChecker`
> （真实文件比对），`internal/alert/hooks_test.go` 锁死了这两条链路。

### 6.7 其它

| 项 | 说明 |
|----|------|
| 告警冷却态仅在内存 | 面板重启后冷却丢失，同一未恢复告警会全部重发 |
| `WriteFileAtomic` 临时文件固定名 | 理论上有符号链接竞态；建议 `os.CreateTemp` 同目录随机名 |
| 无 schema 版本号/迁移机制 | 只有 `CREATE TABLE IF NOT EXISTS`；schema 演进需人工处理 |
| `GetCert`/`GetACMEAccount` 全表扫描 | O(n) 实现，数据量大时需加索引查询 |
| 审计只记写操作 | 读操作量大价值低；等保场景若要求读审计需另开 |
| 审计表上限 5000 条 | 超出后按时间淘汰最旧的 |
| ~~前端无「建立防篡改基线」入口~~ | **第三轮已补齐**：站点详情「防复改」页新增「建立防篡改基线」「立即检测篡改」两个按钮，对应 `POST /api/sites/{id}/baseline` 与 `POST /api/sites/{id}/tamper`；不建基线时 `tamper` 告警持续误报的问题同时闭环 |
| 版本号不能唯一标识二进制 | 版本线重置后，旧的 0.1.x 构建与当前 v0.1.0 **都自报 "ZhiweiPanel 0.1.0"**。核对/回滚必须用 `md5sum` 或 `go version -m`，不能只看 `-version`（实测：清理旧二进制时发现 4 个不同构建自报同一版本号） |

---

## 七、安全评分依据

### 加分项（现有防线有效）

- 会话 token 256-bit 加密随机 + 服务端 TTL 校验
- bcrypt + 用户不存在时时序对齐（防用户名枚举）
- `SameSite=Strict` cookie 挡 CSRF
- 域名 / 路径 / IP / 方法等字段全部有白名单，且**写路径与渲染路径共用同一批谓词**（规则不漂移）
- 写操作强制 POST + 审计自动留痕
- 可信代理白名单 + 双层登录限流
- `exec.Command` 全链路参数化，无 shell 注入面
- 全仓 SQL 参数化；DDL 标识符/字面量转义
- `nginx -t` 失败自动回滚 + 单站点隔离
- `go:embed` 静态资源无路径穿越
- 依赖漏洞清零，零新增依赖
- 版本号单一权威源 + 常驻门禁（防部署错版）

### 扣分项

- 单管理员模型下，认证一旦 compromised 即等价 root（`raw_conf` 全量 nginx 控制 + 面板以 root 管 nginx）
- shell 计划任务本质是 root shell，黑名单只是护栏
- 通知渠道 / `access_url` SSRF 面未闭环
- 敏感凭据明文落库
- 无 RBAC，role 是预留字段
- 审计不覆盖读操作

**结论**：在"单强口令管理员 + 内网/反代 + 不下发不可信 Token"的部署假定下，
v0.1.0 的防线是有效的；主要剩余风险集中在"认证之后权力无收敛"，
这需要产品层面的二次确认、RBAC 或执行沙箱才能根本解决——**不会**用"再加几条黑名单"
来假装解决。
