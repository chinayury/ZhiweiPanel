# 智维面板 ZhiweiPanel 功能优化与新增日志（v0.1.0）

> 本文记录从初始版本迭代到 v0.1.0 正式版的全部功能优化与新增能力，
> **所有条目均对照 v0.1.0 最终代码编写**。
>
> 分两轮：
> - **第二轮（2026-10-28）**：四份全量代码审计后的修复、补齐与机制化，见第一章；
> - **第一轮（2026-10-13）**：初版发布时的优化，见第二~七章。

---

# 一、第二轮（2026-10-28）全量审计后的新增与优化

## 1.1 功能补齐：站点访问认证（HTTP Basic Auth）终于生效

这是本轮**最主要的功能补齐**。模型里 `Access.AuthEnabled / AuthUser / AuthPass`
三个字段从一开始就存在，前端也画了"访问认证"输入框，但全仓**没有任何一处**把它们
渲染进 nginx，也没有生成 nginx 能读的口令文件——用户配完毫无反应。

本轮补齐完整闭环（三段，顺序不能换）：

| 环节 | 实现 | 要点 |
|------|------|------|
| ① 规范化 | `internal/service/access.go` 的 `applyAccessAuth` | 明文口令立刻换成 apr1 哈希，明文**不入库、不回显、不落文件**；客户端提交的 `auth_hash` 一律丢弃（防止塞明文或弱格式做降级） |
| ② 落盘 | `syncAccessFile` | 写入 `<data>/htpasswd/<站点ID>`（**在站点根目录之外**，否则会被当静态文件伺服出去）；内容 `用户名:哈希`；内容不变不重写；关闭认证时删除文件 |
| ③ 渲染 | `internal/nginx/render.go` 的 `buildBasicAuth` | 输出 `auth_basic "<域名>"` + `auth_basic_user_file "<路径>"`；缺哈希/缺路径/路径含 nginx 危险字符时**不输出**（宁可这个站点暂时没认证，也不要让 nginx -t 失败拖垮全站） |

配套新增 `internal/htpasswd` 包：**零依赖 apr1 实现**（只用标准库 `crypto/md5`），
与 OpenSSL `passwd -apr1` / Apache `htpasswd -m` **逐字节一致**（10 组权威向量锁定）。
实现中三个易错点已在代码注释里写明：crypt(3) 的**交错字节序**编码、
1000 次迭代每轮的**四项**输入、以及口令分块重复段。

同时补上一个容易漏掉的连带影响：server 级开了 `auth_basic` 后，
ACME 挑战 location 会继承认证要求 → Let's Encrypt 验证请求拿到 401 →
**证书续签失败 → 站点 HTTPS 全面不可用**。因此挑战目录固定输出 `auth_basic off;`。

前端同步调整：密码框改为**只写**语义（留空 = 不修改已设置的密码），并给出提示文案。

## 1.2 机制化：版本号不再靠约定

| 项 | 之前 | 之后 |
|----|------|------|
| 版本号位置 | `main.go` 的 `const Version`，另外在 Dockerfile / compose / README 各写一份 | `internal/version/version.go` 的 `const Version = "0.1.0"` 单一权威源；`main.go` 用 `var Version = version.Version` |
| 漂移防护 | 无（改一处忘另一处不会报错） | `internal/version/version_test.go` **常驻门禁**：锚点对齐 + 全仓漂移扫描 + 负例测试，任意一处不一致即测试变红 |
| 历史版本 | 混在文档里 | `internal/version/version.go` 的 `History` 变量单独登记，不参与锚点校验 |

门禁的锚点正则只认"面板版本号"形态（`v?X.Y.Z` 出现在版本语境的固定锚点上），
避免把 SVG 坐标、PHP 版本示例、nginx 版本、Chrome UA 里的第三方版本号误报成漂移。
负例测试用 `strconv.Itoa` 拼装假版本号，避免门禁匹配到自己。

## 1.3 nginx 配置渲染：三道防线 + 净化层

| 层 | 做法 | 为什么 |
|----|------|--------|
| 第一道 | 域名硬校验 | 没有合法域名就没有 `server_name`，站点无法对外服务 → 站点级致命错误 |
| 第二道 | 路径硬校验（`ValidateSitePaths`） | 路径**没有"安全降级"语义**（替换成别的目录 = 静默 404），必须报错而不是替换 |
| 第三道 | `model.SanitizeSite` 净化降级 | 其余字段（IP 名单、UA 屏蔽词、反代目标、重定向……）非法时丢弃或回落，**不让渲染失败** |

关键设计：**不能在渲染层硬失败**。`Render` 失败 → `ApplyAllConfig` 整体中止 →
**所有站点**的配置都停在旧版本——一个站点的历史脏字段能把整台面板的其它站点一起卡住。

为此本轮新增 `ApplyAllConfig` 的**单站点隔离**能力：坏站点的整段配置被注释掉
（等价于停用），其余站点照常下发；被隔离的站点记入 `configIssues` 并通过 API 暴露，
前端能看到"哪个站点的配置有问题"。

配套：`internal/model/validate.go` 重构出 `ErrInvalidValue` + `fieldError`
（带字段名的干净错误文案 + `Unwrap`），API 层据此把校验失败映射成 **400** 而不是 500。

## 1.4 权限模型收紧

| 项 | 说明 |
|----|------|
| 写操作强制 POST | 8 个有副作用的端点（证书续签/吊销/绑定/删除、站点启停/重载、配置下发、告警处理）一律要求 POST。**只读 API Token 不再等于写权限**。第三轮按同一规则再收口 5 个（防篡改基线 / 篡改检测 / 登出 / 改密 / 账户类端点），并新增 `method_gate_test.go` 把这条不变量锁死 |
| 审计自动覆盖 | 写操作改 POST 后，审计中间件（`isAuditedMethod` 覆盖 POST/PUT/PATCH/DELETE）自动开始留痕 |
| API Token 独立 `shell` scope | 面板以 root 运行，计划任务里的 shell 脚本 = 交出 root shell。`write` 蕴含它会让泄漏的 CI/监控 Token 直接等于整机 root。现在 `shell` 需单独勾选（**前端创建弹窗同步补上这个勾选项**——此前只有后端支持，界面上根本创建不出来；Token 列表里 `shell` 也不再显示成「只读」） |
| 受信代理白名单 | 新增 `-trusted-proxy`（CIDR 逗号分隔）：只有直连方落在受信网段才采信 `X-Forwarded-For`，否则一律用直连地址 |
| 登录限流双层 | IP 层之外增加账号层（阈值 3 倍），单账号被撞库时也能兜住 |
| 凭据不回显 | 异常登录提示不再回显管理员上次登录 IP；站点访问认证口令不入库不回显 |

## 1.5 MySQL 联动加固

| 项 | 说明 |
|----|------|
| 参数服务端决定 | 用户名、密码、字符集一律由面板生成，客户端指定**直接拒绝**（字符集白名单 utf8mb4/utf8/latin1，Host 仅 localhost） |
| 驱动层转义 | DDL 不支持占位符参数化 → 新增 `quoteIdent` / `quoteLiteral`，所有标识符与字面量转义后拼接 |
| 保留用户名保护 | 新增 `reservedUsers`，防止删站时把面板自己的 MySQL 账号删掉 |
| 建库顺序修正 | **先落库再真建**：落库失败不会留下孤儿库+孤儿用户；建库失败时"要建库"的意图仍留在库里，用户可在面板重试 |

## 1.6 运维链路修复

| 项 | 修复前 | 修复后 |
|----|--------|--------|
| 备份保留策略 | 只裁 DB 记录，磁盘上的 tar.gz 永远不删 → 磁盘泄漏 | 先删磁盘文件再裁 DB；新增 `PruneOrphanFiles` 清历史孤儿；`ListBackups` 支持 limit<0 不限量 |
| 日志切割 | 用 rename 归档，但 nginx worker 仍持有旧 fd → 实时日志/流量统计/5xx 告警集体失真，磁盘无限涨 | 改 **copytruncate**（复制后截断原文件），nginx 用 O_APPEND 故截断后无稀疏空洞 |
| 手动续签 | 新证不回绑站点，站点仍用旧证书 | 抽出 `internal/service/cert.go` 的 `RebindRenewedCert`，main.go 与 API 共用 |
| 自动续签 | 每次新增证书记录 → 旧记录永远"到期待续签"→ 重复签发撞 LE 限额 | 续签复用原证书 ID；站点绑定路径不变，nginx 配置无需改 |
| shell 危险命令 | 纯子串黑名单，`sh''utdown` / `ini''t` / `mk''fs` / 尾随空格 / 引号包裹 / fork 炸弹变体均可绕过 | 归一化（去引号/反斜杠/`s`/反引号）→ 词法切分 → 按命令词匹配；fork 炸弹与 sysrq 改模式匹配；`systemctl` 单独判定；递归删根目录单独判定 |
| Docker PHP socket | 拿版本号拼 `/run/php/php8.2-fpm.sock`，容器里实际路径不同 → Docker 模式 PHP 站点全 502 | 进容器 `ls` 真实 socket 路径；`looksLikePHPServerVersion` 不再从 "default" 推 socket |
| nginx 重载降级链 | `systemctl reload` 失败就直接 `systemctl restart`（**会中断服务**），而 `nginx -s reload` 兜底被放在"systemctl 不存在"的分支里永远不可达 | 降级链改为 `systemctl reload` → **`nginx -s reload`（信号重载，不影响服务）** → `systemctl restart`（最后手段 + 明确日志）；新增 `-nginx-unit` 参数指定 nginx 的 systemd 单元名（自定义编译的 `aa_nginx.service` 这类场景，单元名不对会导致"配置写对了却永远不生效"） |

## 1.7 前端修复（6 项）

| 问题 | 修复 |
|------|------|
| 读文件时内容恰好是合法 JSON → 编辑器空白 → 保存即清空文件 | 响应按 **Content-Type 分流**解析：JSON 才 `json()`，其余按文本 |
| 仪表盘与列表的流量/请求/告警恒为 0 | `unwrap()` 改用 `flattenSiteSummary` 合并汇总与站点字段 |
| "忘记密码"流程 100% 失败（`http` 未 import） | `api.auth` 补 `passwordResetRequest` / `passwordResetConfirm` |
| `getElementById('view')` 恒 null，操作后刷新即崩溃（7 处） | 改用闭包 `container` 或模块级 `viewContainer` + `refresh()` |
| 后端不可达时永久 loading，看不到任何错误 | `boot()` 加 try/catch 兜底 + 全局 `unhandledrejection` 监听 |
| 告警规则类型与后端检查器对不上 | 枚举单一权威源 `model.AlertRuleTypes`；新增 `GET /api/alert-types`；前端动态渲染（带兜底常量） |

## 1.8 供应链与仓库治理

| 项 | 说明 |
|----|------|
| 依赖升级 | lego v4.25.2 → **v4.35.2**（修 GO-2026-5593 Webroot 路径穿越任意文件写）；Go 工具链 → **1.26.10**（GO-2026-6809 等标准库漏洞）；`govulncheck` affected=0；**零新增依赖** |
| 仓库清理 | 删除 33 个临时脚本（其中 10 个硬编码了面板 admin 明文密码）与 5 个过期二进制（约 75 MB）；清掉 `tmp/`、`dist/`、`cmd/` 等垃圾目录 |
| 可追溯性 | 全部修复逐条落到 `docs/BUGFIX-v0.1.0.md`，四份审计原始报告保留在 `docs/audit/` |

---

# 二、第一轮：安全能力新增

| 能力 | 说明 |
|------|------|
| **站点 ID 服务端强校验** | 新增 `model.ValidateID`，ID 必须为 8-64 位十六进制；`CreateSite` 恒重新生成，客户端传值被忽略。从根上消除"ID 穿越写任意 `.conf`"的攻击面 |
| **站点路径白名单校验** | 新增 `model.ValidatePath`：必须绝对路径、禁 `..`、禁指向 `/etc`、`/root`、`/usr`、`/var/log` 等系统目录、禁控制字符、长度上限 255。覆盖站点根目录、子目录绑定目标、防篡改文件列表 |
| **登录限流与锁定** | 新增 `loginLimiter`，按 IP+用户名 滑动窗口限流（5 次/分钟），触顶锁定 15 分钟，响应带 `Retry-After`。限流判断置于 bcrypt 计算之前，避免 CPU 被打满 |
| **WebSocket 真实鉴权** | 新增 `Server.Authorize(r)`，`authWS` 从"检查 cookie 存在"升级为真实会话校验 |
| **错误信息最小化** | `writeModelError` 改为 `errors.Is` 匹配领域错误；未知错误只进服务端日志，对外返回统一"内部错误"，不再泄露绝对路径 / nginx stderr / SQLite 报错 |
| **Origin 精确校验** | WebSocket 同源检查从 `strings.Contains` 改为相等比较，堵住 `panel.example.com.evil.com` 这类绕过 |
| **日志目录默认拒绝外网** | 渲染器固定输出 `location ^~ /logs/ { deny all; }`，`/logs/access.log` 不再可被公开下载 |
| **可信代理显式配置** | 新增 `Extra.TrustedProxies`（CIDR 列表）。不配置则完全不输出 real_ip，杜绝 `set_real_ip_from 0.0.0.0/0` 导致 `X-Forwarded-For` 可伪造 |
| **改密踢下线** | 新增 `store.DeleteUserSessionsExcept`，改密后失效该用户其它会话 |
| **删除 `h()` 的 `html` prop** | 移除前端唯一的 innerHTML 开口，全部用户可控数据走 `textContent`/`createTextNode` |

---

# 三、第一轮：核心功能修复（此前静默失效）

| 功能 | 修复前 | 修复后 |
|------|--------|--------|
| **流量统计 / 月配额强制 / 配额告警** | `CollectTraffic` 从未被调用，`traffic_stats` 恒为空，三项功能全是 0 的死数据 | 新增 `runTrafficCollector` 后台任务，每 5 分钟采集 |
| **证书自动续签** | `Renew` 产出的新证从不绑定回站点，nginx 继续 serve 旧证；日志全绿但旧证到期即全站 HTTPS 下线，且每 12 小时泄漏一份孤儿证书文件 | 新增 `rebindRenewedCert`，按域名反查站点并更新 `site.Cert` 与证书路径 |
| **访问日志查看** | `ReadAccessLog` 从头顺序读，繁忙站点显示远古记录；5xx 告警窗口过滤后恒为 0（越忙越不告警） | 改为 seek 到尾部窗口后读取，真正"读末尾" |
| **实时日志跟踪** | `Tail` 第一个 EOF 就退出 goroutine，读完成存量即死；且把整个历史日志一次性推给前端 | EOF 后轮询等待新行；启动 seek 到末尾只推新行；`stop` 防重复 close |
| **配额超额拒绝** | `ssp_quota_lock` 只 set 不读，界面显示"已锁定"实际流量照常放行 | 补 `if (ssp_quota_lock) { return 403; }` |
| **防篡改基线** | `diff, _ := logmgr.Diff(...)` 丢掉真正的快照返回值，基线恒为空；每次检测都把文件报成"新增"，真正的"内容被改"反而漏掉 | 改用第二返回值；新增回归测试锁定 |
| **站点证书绑定** | `UpdateSite` 全量替换丢 `cert` 字段，任何不含该字段的更新都让 SSL 配置静默丢失 | `if site.Cert == nil { site.Cert = old.Cert }` |
| **PHP 执行** | `root` 只在 `location /` 内，PHP location 的 `sdocument_root` 取错 → `.php` 全部 404 "File not found."（而直连 FPM 正常，极难排查） | root 提到 server 级 |
| **配置回滚完整性** | 只快照 per-site conf，http 级配置与孤儿删除不在回滚范围；失败后磁盘留下混合态，此后任何外部 reload 都失败 | http conf 与孤儿删除均纳入快照，`rollback` 按 key 区分处理 |
| **损坏配置的站点** | `ListSites` 静默跳过 + 孤儿清理反把它的 nginx 配置删掉 → 站点静默 404 且无日志 | 记日志 + 返回占位 Site 保留 ID，进入 `known` 集合不被清理 |
| **配额告警准确性** | 分子是字节、分母是 MB，1KB 流量对 1GB 配额都能算出 100% | 统一换算为字节；GB 展示改浮点 |
| **PHP 版本探测** | glob 写死 `php-*-fpm.sock`，匹配不到 Ubuntu 实际的 `php8.1-fpm.sock` → PHP 版本列表恒为空 | glob 改 `php*fpm.sock`，正则连字符可选 |

---

# 四、第一轮：渲染正确性修复

| 问题 | 修复 |
|------|------|
| SSL 就绪时只监听 443 丢掉 80 | 同时监听 80 与 443。修复前 `http://域名` 落到默认站返回别人内容、`force_https` 是死代码、ACME HTTP-01 无法验证导致续签失败 |
| HTTPS 跳转写在 server 级 | 移入 `location /` 内；新增 `location ^~ /.well-known/acme-challenge/` 放行。修复前 server 级 `if` 先于 location 匹配执行，把 ACME 挑战也 301 掉 |
| 反代/黑名单 location 被正则 location 抢走 | 反代 location 改用 `^~`。修复前 `/api/x.php` 会被 `location ~ \.phps` 抢走到 FPM，deny 正则也会把反代路径整个 403 |
| 静态缓存 location 抹掉安全响应头 | 抽出 `securityHeaders(indent)` 在缓存 location 内重复输出。修复前同时开缓存与安全选项时，静态资源的 `X-Frame-Options`/`X-Powered-By`/HSTS 静默丢失 |
| 黑名单/重定向路径被补尾斜杠 | 新增 `normalizeExactPath`（只规范前导斜杠）。修复前 `/secret.html` 渲染成 `^/secret\.html/`，精确文件路径整条失效 |
| 路径为 `/` 时 location 重复 | 子目录绑定跳过 `/`；渲染测试断言无重复 location。修复前 nginx -t 报 duplicate location 导致全量回滚 |
| WAF 漏掉 URL 编码 | 新增 `wafPattern` 扩展编码变体，`\s+` 放宽为 `(?:\s|%20\|\+)+`。修复前 `?q=%3Cscript%3E` 直接放过 |
| nginx 双引号反转义导致正则炸 | 规避：不再在正则里用字面反斜杠；新增"模拟 nginx 反转义后再编译"的守卫测试 |
| 子目录 map 死代码 | 整段删除（key 是字面量不插值，且无消费方） |
| HSTS 默认 `includeSubDomains` | 去掉。该指令影响域名下所有子域，用户没要求时不该替他决定；且改为只在 TLS 就绪时输出 |

---

# 五、第一轮：健壮性提升

| 项 | 内容 |
|----|------|
| 超长日志行不再让整次读取失败 | `bufio.ErrTooLong` 时跳过该行继续。修复前一条 >1MB 的畸形行会让 5xx 告警与流量统计同时静默停摆 |
| HTTP Server 超时配齐 | 补 `ReadTimeout: 30s`、`IdleTimeout: 120s`，防 Slowloris |
| 日志分页封顶 | `page ≤ 500`、`limit ≤ 1000`、`limit*page ≤ 100000`，防 OOM |
| 删除站点级联清理 | 同事务清 `integrity` 与 `tamper_events`，不再留孤儿数据 |
| 域名存储规范化 | 校验时 trim + 小写，避免带空格域名绕过冲突检查 |
| 配置下发失败不再吞错 | 续签后 `ApplyAllConfig` 失败会记日志 |
| 版本号进入 settings | 启动时 `db.SetSetting("version", Version)`，前端顶栏与设置页能显示版本 |

---

# 六、第一轮：前端修复

| 问题 | 修复 |
|------|------|
| 实时跟踪 100% 失败 | tail 路由从 `/sites/{id}/logs/tail` 改为正确的 `/system/logs/tail?site_id=` |
| 历史日志永远"暂无记录" | 响应键从猜的 `logs`/`lines`/`items` 改为实际的 `records` |
| 错误日志显示成 JSON 转储 | 改读 `{line}` 包装并用 `<pre>` 渲染 |
| 通知渠道永久保存失败 | 渠道列表 JSON 序列化后存 `alert_channels_json` 键，读取处 parse 并兼容旧格式 |
| 数字设置编辑后保存必 400 | 提交前统一 `String(v)` |
| PHP 版本列表走写死兜底 | `Array.isArray(status.php)` 正确取数组 |
| 面板版本号永远不显示 | 后端启动时写入 settings 的 `version` 键 |
| WebSocket 离开视图不关闭 | `render()` 开头 `stopTail()` + 清空 `lineBuf` |
| 切换站点 tail 缓冲不清空 | 同上 |
| 创建向导域名输入失焦 | `syncName` 去掉 `drawList()`，只更新名称输入框 |
| 吊销证书后列表不刷新 | 成功后重渲视图 |
| 站点保存的 202 warning 被吞 | `site-detail` 保存后按 warning 类型 toast |
| 站点列表跳日志预选丢失 | 先写 `sessionStorage` 再改 `location.hash` |
| 仪表盘单点失败打穿整页 | 非关键接口（certs/status）加 `.catch` 降级 |
| 登录后白屏 | `doLogin` 补 `buildShell()` |

---

# 七、第一轮：工程质量

| 项 | 内容 |
|----|------|
| **gofmt 全量格式化** | 17 个文件从不合规到 `gofmt -l` 零输出 |
| **go vet 零告警** | `go vet ./...` 通过 |
| **死代码清理** | 删除 `listenPort`、`quoteRE`、`sanitizePath`、子目录死 map、`h()` 的 `html` prop |
| **依赖升级** | lego v4.21.0→v4.25.2（第二轮→v4.35.2）、sqlite v1.34.5→v1.60.1、x/net→v0.60.0、x/crypto→v0.58.0、go-jose→v4.1.4、Go 工具链→1.26.10；`govulncheck` 14 漏洞清零，第二轮复查 affected=0 |
| **回归测试** | 第一轮 56 个用例；第二轮扩充到 17 个包 / 38 个测试文件 / 209 个测试函数；第三轮再增 `method_gate_test.go`（15 个端点 × 6 种方法全量断言 + 2 个闸门自测）→ **39 个测试文件 / 212 个测试函数** |

---

# 八、已知未闭环项

以下为有意保留或留待下版本的内容，均已在《安全审查报告》第五节详述：

## 8.1 第二轮审计后仍保留的

1. **通知渠道 SSRF 防护**（`access_url` 与通知渠道 target 需自定义 DialContext 拒绝内网网段）
2. **敏感渠道凭据加密存储**（SMTP 密码、通知渠道密钥）
3. **`raw_conf` / `rewrite` 的管理员 raw 能力** —— 设计内的特权入口，改为二次确认即可缓解
4. **RBAC 角色强制** —— 当前单用户，`role` 为预留字段，读出但未用于授权
5. **审计只记写操作** —— 读操作量大价值低；等保场景若要求读审计需另开
6. **会话 token 在库中明文存储**
7. **站点 MySQL 密码在库中明文存储**（与面板既有凭据模型一致；改存储格式会破坏重置密码流程）
8. **shell 危险命令黑名单只是护栏，不是安全边界**（面板以 root 运行，shell 计划任务 = 交出 root shell）
9. **调度器回写整行数据**，会覆盖用户对任务的并发修改
10. **ACME 账户私钥文件损坏时静默换新密钥**，把账户绑定弄断
11. **crontab 表达式拒绝 `7`（星期日）**，与标准 crontab 不兼容
12. **防篡改事件表无限增长**（每 5 分钟重复写同一批文件）
13. **日志保留期在生产里从未生效**
14. **"立即执行"在任务运行中被静默丢弃**，接口却返回 ok
15. **告警投递失败无反馈**（四种渠道都真的发送，但失败被完全吞掉，用户看不到任何提示）
16. **`WriteFileAtomic` 临时文件用固定名**（并发写理论上有撕裂风险）
17. **DB schema 版本号与自动迁移机制**
18. **站点配置导入**（把现有手写 conf.d 反向解析为面板站点）
19. **前端无「建立防篡改基线」入口**（后端 `POST /api/sites/{id}/baseline` 可用，
    但界面没按钮 → 不建基线时 `tamper` 告警会持续误报）
