# 智维面板 ZhiweiPanel 功能使用说明

适用版本：**v0.1.1**（版本号权威源：`internal/version/version.go`）。

本文讲**每个功能怎么用**：在哪配、字段含义、生效条件、注意事项。安装运维见 [DEPLOYMENT.md](DEPLOYMENT.md)，项目概览见 [README.md](../README.md)。

---

## 一、登录与账户

### 1.1 首次登录

| 项 | 说明 |
|----|------|
| 地址 | 默认 `http://<服务器IP>:8888`（开了 `-tls` 则是 `https://...:8888`） |
| 用户名 | `-admin` 指定，默认 `admin` |
| 密码 | `-admin-pass` 指定，**仅首次启动创建账户时生效** |

`-admin-pass` 只在账户不存在时创建账户。如果启动时没带这个参数、账户又不存在，面板日志会打印警告「未指定 -admin-pass 且管理员账户不存在，首次登录将不可用」，此时需要带 `-admin-pass` 重启一次把账户建出来。

### 1.2 异常登录二次验证

密码正确但**来源 IP 段或设备指纹相对上次登录发生变化**时，面板不下发会话，改为：

1. 返回 `need_verify: true` + `challenge_id`；
2. 登录页切到验证码输入，提示「检测到异常登录（…），验证码已发送到您绑定的邮箱」；
3. 输入邮箱里的 6 位码，提交 `POST /api/auth/verify-challenge` 换取会话。

判定规则：同子网（IPv4 /24、IPv6 /48）+ 相似 UA = 正常；子网不同或 UA 完全不同 = 异常；距上次登录超过 7 天 = 异常。

**生效条件**：管理员已绑定邮箱，且 SMTP 通知通道可用。两者任缺其一则跳过异常检测直接放行（服务端日志会记录「异常登录跳过检测（未配通知通道）」）。

> 异常提示文案**不会回显上次登录的 IP**，只说「IP 段变化（与上次登录不同）」。

### 1.3 登录失败限流

| 维度 | 阈值 | 窗口 | 锁定 |
|------|------|------|------|
| IP + 用户名 | 5 次 | 1 分钟 | 15 分钟 |
| 仅用户名（换 IP 也算） | 15 次 | 1 分钟 | 15 分钟 |

第二层是专门防「换一个来源 IP 继续猜同一个账号」的。触发后返回 HTTP 429 并带 `Retry-After`。

### 1.4 两步验证（TOTP）

设置 → **两步验证（TOTP）**。开启后，登录除密码外还需输入认证器 App 的 6 位动态口令。

**开启流程**（设置 → 两步验证 → 开启）：

1. 面板生成 base32 密钥，展示 `otpauth://totp/...` 链接与明文密钥；
2. 用认证器 App（Google Authenticator 等）扫描或手动录入；
3. 输入认证器显示的 6 位动态口令完成启用（密钥在验证首码前只是 pending，未启用）。

**关闭**：需要输入当前有效口令确认——能出示动态口令的人才有资格降级防护。

**恢复码**（启用后自动发放）：

- 开启成功时面板发放 **10 枚一次性恢复码**，弹窗展示（只此一次，需勾选"我已保存"才能关闭）——请立即抄录或复制到安全的地方；
- 登录第二步的输入框**同时接受动态口令和恢复码**（手机丢失/损坏时输入恢复码即可登录），每枚仅能用一次；
- 设置页显示"剩余 N 枚未使用"；`重新生成恢复码`会使旧码**全部立即作废**并发放新 10 枚（怀疑泄漏时的止损手段）；
- 关闭两步验证时恢复码一并清空；
- 恢复码为 12 位（形如 `XXXX-XXXX-XXXX`），抄录时大小写、短横、空格不敏感；接口：`POST /api/2fa/recovery-codes`（重新生成）、`GET /api/2fa/status`（含 `recovery_remaining` 余量）。

行为细节：

- 动态口令 30 秒一换，±30 秒时钟漂移内可通过（认证器与服务器时间差 1 分钟以内即可）；
- 登录第二阶段的动态口令：2 分钟有效期、一次性、累计 5 次错误即作废，需重新输入密码；恢复码登录同样受该上限与限流器约束；
- 已启用 2FA 时**跳过 1.2 的异常登录邮件验证**（动态口令是更强的第二因子）；
- 2FA 管理端点（setup / enable / disable / recovery-codes / status）**只允许浏览器会话**，API Token 一律 403；
- 接口：`POST /api/2fa/setup` / `enable` / `disable` / `recovery-codes`、`GET /api/2fa/status`；
- 密钥存于 SQLite（users 表 totp_secret / totp_enabled），无法从界面找回——启用时请妥善保存展示的密钥；恢复码只存 SHA-256 摘要（totp_recovery_codes 表）。

### 1.5 修改密码

**设置 → 修改密码**，需要当前密码 + 新密码 + 确认新密码，新密码至少 8 位。接口：`POST /api/auth/password`。

### 1.6 忘记密码

登录页的「忘记密码」走两步：

1. `POST /api/auth/password-reset/request`（用户名）→ 面板发验证码邮件，返回 `challenge_id`；
2. `POST /api/auth/password-reset/confirm`（`challenge_id` + 用户名 + 验证码 + 新密码）。

### 1.7 绑定邮箱与 SMTP

| 位置 | 作用 |
|------|------|
| 设置 → 账户邮箱 | 绑定管理员邮箱，保存后点「发送验证邮件」完成验证；未验证的邮箱标黄 |
| 设置 → SMTP 配置 | 配置 `host` / `port` / `user` / `password` / `from` / `from_name` / `encryption`，可「发送测试邮件」 |

SMTP 密码读取时以 `********` 脱敏显示；保存时传 `********` 视为「不修改」，沿用旧密码。

> 这两个入口在**未登录时也接受 `X-Setup-Password` 请求头**（值为管理员密码），用于「被异常登录锁在外面」的管理员还能把邮箱和 SMTP 配好。setup 鉴权同样消耗登录限流额度。

---

## 二、界面导航

左侧导航共 10 项：

| 导航项 | 路由 | 内容 |
|--------|------|------|
| 仪表盘 | `#/dashboard` | 资源卡片（实时采样曲线 + 近 24 小时趋势：分钟级持久化，默认保留 72 小时，重启不丢）、流量趋势、站点状态、证书临期、未处理告警 |
| 网站 | `#/sites` | 站点列表，支持多选批量启停 |
| 软件商店 | `#/apps` | 面板已知软件的目录与安装状态（已安装/可安装、版本、安装命令提示）；只做检测，不自动安装/卸载 |
| 证书 | `#/certs` | 证书库、申请、导入、绑定、吊销、ACME 账户 |
| 计划任务 | `#/cron` | 任务列表、执行历史、手动触发 |
| 备份 | `#/backups` | 站点/数据库备份列表与操作 |
| 日志 | `#/logs` | 按站点查访问/错误日志、实时跟踪 |
| 告警 | `#/alerts` | 全局告警列表、通知渠道管理 |
| 运维中心 | `#/ops` | 三个子页签：API Token / 操作审计 / 配置历史 |
| 设置 | `#/settings` | 面板设置、运行时信息、nginx 主配置、账户与 SMTP |

顶栏右侧有浅色/深色主题开关，选择写入 `localStorage`，刷新后保持。站点详情页顶栏有「保存修改」按钮，页面有未保存改动时离开会提示。

---

## 三、站点生命周期

### 3.1 新建站点（向导）

「网站 → 新建站点」，五步：

| 步骤 | 填什么 |
|------|--------|
| 1 域名 | 站点名称（留空自动取主域名）、绑定域名列表（第一个是主域名） |
| 2 根目录 | 网站根目录、运行用户/组、允许符号链接、对外监听端口、默认文档 |
| 3 PHP | 是否启用 PHP、PHP 版本、内存限制 |
| 4 SSL | 是否启用 SSL、绑定已有证书、强制 HTTPS 跳转、HTTP/2（可跳过） |
| 5 确认 | 汇总确认 + 「同时创建 MySQL 数据库」 |

**域名**：一个站点可绑多个域名，第一行为主域名。域名不能与已有站点的域名重复（大小写不敏感），重复会报「域名 X 已被站点 Y 占用」。

**根目录**：必须是绝对路径，不能含 `..`，不能指向 `/`、`/etc`、`/root`、`/usr`、`/var/log` 等系统目录。留空时面板按 `/www/wwwroot/<主域名>` 自动生成并创建（含 `logs/` 子目录；启用 PHP 时额外建 `public/`）。新建站点的根目录里会被写入一份默认欢迎页。

**站点 ID**：由服务端生成，客户端传入的 ID 一律忽略（ID 会参与构成配置文件名与备份路径，由客户端提供等于开放任意文件写）。

### 3.2 编辑与保存

站点详情页共 **19 个功能页签**，改完点右上角「保存修改」下发配置。

**保存 ≠ 一定成功**：如果配置已落库但 nginx 配置下发失败，接口返回 **202**（不是 200），响应体带 `warning` 字段，界面会提示「站点已更新，但配置下发失败」。数据不会丢，修好后在站点列表点「重载 nginx 配置」重新下发即可。

### 3.3 站点状态

| 面板状态 | 磁盘上的配置 | nginx 行为 |
|----------|-------------|-----------|
| `running` | 正常 `server {}` 块 | 正常服务 |
| `stopped` / `disabled` | 整段逐行注释 | 不再匹配该 server_name |
| 已删除 | 文件被删除 | 彻底不再服务 |

站点详情页「基本信息 → 运行状态」的开关改的是面板状态，**需要保存后生效**。

### 3.4 批量操作

网站列表勾选多个站点 → 底部出现批量操作条 → 「批量启动」/「批量停止」。接口：`POST /api/sites/batch`，body `{"action":"start|stop","ids":[...]}`。

### 3.5 删除站点

站点列表或详情页的删除按钮。确认弹窗会明确告知：

- 会移除 nginx 站点配置；
- 若站点绑定了数据库，会**同时 drop database + drop user**，不可恢复；
- **不会删除站点根目录里的文件**。

删除后同步清理：防篡改基线、备份目录、访问认证口令文件。

> 删库失败**不会**回滚站点删除（避免「因为库删不掉所以站点也删不掉」的死锁），错误会写进服务端日志，提示手动清理残留。

### 3.6 站点到期时间

基本信息 → 「站点到期时间」，填 `YYYY-MM-DD`。列表里未来 7 天内到期标黄，已过期标红。留空 = 无到期。

---

## 四、站点功能（19 个页签）

### 4.1 基本信息

| 字段 | 含义 | 生效条件 / 注意事项 |
|------|------|---------------------|
| 站点名称 | 面板内标识 | 不影响 nginx |
| 运行状态 | 运行中 / 已停止 | 需保存后生效 |
| 域名 | 一行一个，第一行为主域名 | 域名不可与其他站点重复 |
| 网站根目录 | nginx `root` | 绝对路径，禁止 `..` 与系统目录 |
| 运行用户 / 运行用户组 | nginx worker 以谁的身份伺服文件 | 目录权限要对得上，否则 403 |
| 运行目录 `run_path` | 相对根目录的子路径 | Laravel / ThinkPHP 填 `public`；`SCRIPT_FILENAME` 拼成 `$document_root/<run_path>/$script_name` |
| 允许符号链接 | 关闭后 nginx 拒绝跟随指向站外的软链接 | 面板以 root 运行，默认开着有风险 |
| 自定义 404 页面 | 相对根目录的路径，如 `404.html` / `errors/404.php` | 渲染为 server 级 `error_page 404` |
| 对外监听端口 | 逗号分隔，如 `80, 8080` | 默认 80；SSL 启用时自动追加 443；**同一端口只能给一个站点**；非标端口需云安全组 + 防火墙同时放行 |
| 站点到期时间 | `YYYY-MM-DD` | 见 3.6 |
| 站点备注 | 内部备注 | 不影响配置 |

### 4.2 文件管理

站点详情 → 「文件管理」页签（站点根目录的浏览器）。

| 操作 | 说明 |
|------|------|
| 浏览 | 列出当前目录内容，点击目录进入 |
| 上传 | 多文件上传，`multipart/form-data`，上传到当前目录 |
| 编辑 | 打开文本文件在线编辑后保存 |
| 新建文件 / 新建目录 | 填名称即可 |
| 重命名 | 同站点根内改名/移动 |
| 删除 | 需二次确认 |
| 解压 | 对站点内的 zip 解压，可指定目标目录 |

**约束**（服务端强制，返回 403/400）：

- 所有路径必须落在站点根目录内（`..` 逃逸、软链接指向站外一律拒绝）；
- **`logs/` 与 `.well-known/` 是受保护子目录**，不可浏览、不可读写、不可解压进去；
- 单文件读/写上限 50MB，超出报 413；
- 不能直接覆盖或删除根目录；
- 解压会拒绝 zip-slip、绝对路径与符号链接条目。

### 4.3 子目录绑定

把站点的某个 URL 前缀映射到独立的磁盘目录。

| 字段 | 含义 |
|------|------|
| 启用子目录绑定 | 总开关 |
| 访问路径 | 站点内路径，如 `/blog` |
| 磁盘目录 | 目标目录，如 `/data/blog` |

示例：访问 `https://example.com/blog` 实际读取 `/data/blog` 下的文件。

**生效条件**：目标目录同样要过路径校验（绝对路径、禁 `..`、禁系统目录）。渲染为 `location /blog/ { alias ... }`。

### 4.4 访问限制

#### 4.4.1 IP 黑白名单

| 字段 | 含义 |
|------|------|
| 启用访问限制 | 总开关 |
| 允许的 IP / CIDR | 白名单，一行一个，**留空表示不限制** |
| 禁止的 IP / CIDR | 黑名单，一行一个 |

**黑名单优先级高于白名单**：同时命中时拒绝。

#### 4.4.2 允许的请求方法

可选 `GET` / `POST` / `PUT` / `DELETE` / `HEAD` / `OPTIONS` / `PATCH`。**未勾选任何项表示不限制**。

> 务必保留 `GET` 和 `HEAD`，否则页面与静态资源都访问不了。

#### 4.4.3 站点访问认证（HTTP Basic Auth）

开启后，未认证访问会弹出浏览器的登录框。这是站点级的第二道门（第一道是 IP 名单）。

**在哪配**：站点详情 → 访问限制 → 「访问认证」区块。

| 字段 | 含义 |
|------|------|
| 启用访问认证（Basic Auth） | 总开关 |
| 认证用户名 | `auth_user`，开启时**必填** |
| 认证密码 | `auth_pass`，**只写字段** |

**工作原理**（三段闭环，缺一不可）：

1. **规范化** — 面板把明文口令换成 **apr1** 哈希（`$apr1$xxxxxxxx$xxxxxxxxxxxxxxxxxxxx`），明文即刻丢弃：不入库、不回显、不落任何文件；
2. **落盘** — 哈希写进 `<data>/htpasswd/<站点ID>`，内容是 `用户名:哈希`，权限 0644；
3. **渲染** — nginx 侧输出：

```nginx
    # 站点访问认证（HTTP Basic Auth）
    auth_basic "example.com";
    auth_basic_user_file "/opt/zhiweipanel/data/htpasswd/<站点ID>";
```

**密码只写不读**：

- 保存后接口不再回显密码（明文与哈希都不返回），界面上密码框永远是空的；
- **留空 = 不修改**已设置的密码；填了新值 = 改成新密码；
- 想确认当前密码只能删掉重设。

**生效条件**：三个必须同时满足才输出 `auth_basic` —— ① 开启访问认证；② 有认证用户名；③ 有口令哈希。少任何一条都不输出（此时不会因缺文件导致 `nginx -t` 失败拖垮全站）。

**注意事项**：

- 口令文件必须在**站点根目录之外**（默认在数据目录下的 `htpasswd/`），否则 nginx 会把它当静态文件伺服出去，等于把口令哈希公开下载；
- nginx 在**解析配置时**就会打开 `auth_basic_user_file`，文件不存在 → `nginx -t` 失败 → 整批配置回滚。面板因此在渲染前先写文件，并且每次下发前自愈补齐被外部删掉的文件；
- **ACME 挑战目录显式 `auth_basic off;`** —— 渲染器在 `location ^~ /.well-known/acme-challenge/` 里单独关闭认证，避免开了访问认证后 Let's Encrypt 验证请求拿不到挑战文件导致**续签失败**；
- 数据目录路径含空格/引号/分号会导致 nginx 配置结构被改变，面板会直接报错拒绝启用访问认证；
- Basic Auth 的凭据走 HTTP 头传输，**必须配 HTTPS 才安全**；
- 关闭访问认证或删除站点时，残留的口令文件会被删除。

### 4.5 流量限制

| 字段 | 含义 | 说明 |
|------|------|------|
| 带宽限速 | 单连接限速，如 `512k` / `2m` | nginx `limit_rate` 语法，留空不限制 |
| 每月流量配额（MB） | 月度流量上限 | `0` 表示不限；超出触发告警 |
| 单 IP 限速（次/秒） | 单 IP 请求频率 | `0` 表示不限，渲染为 `limit_req_zone` |
| 超额后强制执行 | 超额直接拒绝访问 | 关闭时只告警不拦截 |

**生效条件**：流量统计由后台采集器每 5 分钟聚合访问日志写入 `traffic_stats` 表。「每月流量配额」与「超额强制执行」都依赖这张表——刚建站还没跑满一个采集周期时，配额判定基于空数据。配额告警同理。

### 4.6 伪静态

| 字段 | 含义 |
|------|------|
| 启用伪静态规则 | 总开关 |
| 规则内容 | 等价的 nginx `location` / `rewrite` 片段原文 |
| 规则文件路径 | 指向服务器上的一个文件，如 `/www/server/panel/vhost/rewrite/example.com.conf` |

**生效条件**：填了「规则文件路径」时**优先从该文件读取规则**，文件不存在会报错。

**注意事项**：规则内容原样注入 nginx，语法错误会导致 `nginx -t` 失败。面板会在下发前做静态校验，但静态校验不能替代真实的 `nginx -t`。

### 4.7 默认文档

一行一个文件名，自上而下匹配。常见配置：`index.php index.html index.htm`。

渲染为 nginx 的 `index` 指令。**留空**时使用 nginx 默认（`index index.html`）。

### 4.8 配置文件（自定义 Nginx 配置）

站点级自定义 nginx 片段，**原样注入**。支持选择**注入位置**：

| 注入位置 | 含义 | 典型用途 |
|----------|------|---------|
| `server 块内`（默认） | 插在 ssl_session_timeout 之后、`/.well-known` 之前，可覆盖面板自带的 IP 白名单/限速 | 站点级 location / if / header 覆盖 |
| `server 块外` | 插在文件末尾（http 上下文） | `upstream` / `limit_req_zone` / `map` 等只能出现在 http 上下文的指令 |

- 早期字段 `raw_conf` 会自动迁移为 `extra_conf`（位置默认 inside），老站点无需手工处理。
- 保存前**先做 nginx -t 预校验**（写盘 → `nginx -t` → 失败自动恢复保存前内容）：本次修改**不落库**，
  并以弹窗完整回显 nginx 原始报错——不是被截断的通用「保存失败」。由此保证数据库与磁盘配置永远一致。
- 三个默认安全头（`X-Frame-Options` / `X-Content-Type-Options` / `Referrer-Policy`）无条件输出。
  在 extra_conf 里自行 add_header 时注意 nginx 的 add_header 继承陷阱：子 location 一旦出现
  add_header，server 级的全部 add_header 失效（静态缓存 location 里已按需重复声明）。

旁边的「查看生成的 Nginx 配置」按钮会调 `GET /api/sites/{id}/config`，返回后端已保存的配置 + `nginx -t` 检测结果（HTTP 片段与站点 server 片段分开显示）。

> 预览的是**后端已保存**的配置，保存后才会生效；有未保存改动时会提示「预览的是上次保存的配置」。

### 4.9 SSL

| 字段 | 含义 |
|------|------|
| 启用 SSL | 总开关 |
| 证书文件路径 / 私钥文件路径 | 指向服务器上的 PEM 文件 |
| 强制 HTTPS 跳转 | HTTP 访问自动 301 到 HTTPS |
| 启用 HSTS | 强制加密传输安全 |
| HSTS max-age（秒） | 默认 31536000（1 年） |
| 启用 OCSP 证书 stapling | 由 nginx 提供 OCSP 响应 |
| 启用 HTTP/2 | |
| 允许的 TLS 协议版本 | `TLSv1.2` / `TLSv1.3` |

**当前证书**区块显示证书名称、域名、签发者、来源、有效期、剩余天数、自动续签开关、验证方式、文件路径，并提供「解除绑定」。

**绑定证书的三条路径**：

1. 站点详情 → SSL → 「从证书库选择」；
2. 证书页 → 证书 → 「绑定」；
3. 证书页 → 「一键绑定」—— 按域名交集自动绑到所有匹配站点（未开启 SSL 的会自动开启）。

**生效条件与注意事项**：

- 启用 SSL 后站点会**同时监听 80 和 443**，HTTP 跳转写在 `location /` **内部**（不是 server 级）——server 级的 `if(...) return` 会把 ACME 挑战一起 301 掉，导致续签失败；
- 80 端口必须保留并对外可达；
- 证书由面板签发时，文件在 `<cert-dir>/<证书ID>/fullchain.pem` 与 `privkey.pem`，目录 0700、文件 0600；
- HSTS 会影响该域名下**所有**子域，面板不默认加 `includeSubDomains`，需要的话自己在自定义配置片段里加。

### 4.10 PHP

| 字段 | 含义 |
|------|------|
| 启用 PHP | 关闭则按纯静态站点处理 |
| PHP 版本 | 下拉来自「系统状态」里探测到的 PHP-FPM 版本 |
| PHP-FPM Socket | 留空则由面板按版本自动匹配 |
| 最大执行时间（秒） | |
| 内存限制 | 如 `256M` |
| 禁用函数 | 一行一个函数名，如 `exec` |
| 自定义 php.ini 配置 | 追加到 php.ini 的自定义内容 |

**注意事项**：

- Socket 路径填错是 502 的头号原因（见 [DEPLOYMENT.md](DEPLOYMENT.md) 8.4）；
- Laravel / ThinkPHP 需要同时配「网站根目录」= 项目根、「运行目录」= `public`；
- 面板以 root 运行，禁用函数列表建议至少包含 `exec`、`system`、`passthru`、`shell_exec`、`popen`。

### 4.11 数据库（MySQL / MariaDB）

站点详情 → 数据库页签。

**联动建库**（新建站点向导第 5 步勾选，或直接调 API）：

| 项 | 值 |
|----|----|
| 数据库名 | `site_<站点ID前 12 位 hex>`，唯一且不可猜 |
| 用户名 | 与数据库同名 |
| 主机 | 固定 `localhost`（`%` 会被拒绝） |
| 端口 | 默认 3306 |
| 字符集 | `utf8mb4`（白名单：utf8mb4 / utf8 / latin1） |
| 密码 | 24 位 hex，由面板用 `crypto/rand` 生成 |

**客户端不能指定用户名/密码/主机/字符集**——这些一律由服务端决定，指定会被拒绝（`数据库用户名由面板按站点 ID 自动生成，不接受自定义`）。

**权限**：`GRANT ALL PRIVILEGES ON site_xxx.* TO 'site_xxx'@'localhost'`，**不带 `GRANT OPTION`** —— 站点用户能建表改表，但不能再把权限 GRANT 给别人。

**密码只明文回显一次**：创建响应带 `show_password: true`，界面弹「请保存数据库密码」模态框。关闭后任何 GET 路径都拿不到明文（读取时被脱敏）。丢了只能重置。

**删站联动 drop**：`DELETE /api/sites/{id}` 会依次 `RevokePrivileges` → `DropUser` → `DropDatabase`，最后删站点记录。

**前置条件**：MySQL 子系统必须就绪。`GET /api/system/mysql` 返回 `{installed, running, version, server, socket}`；凭据文件 `<data>/mysql-root.cnf` 存在（`install.sh` 生成，权限 0600）时子系统才会初始化。未就绪时向导里的「同时创建 MySQL 数据库」复选框会被禁用并提示原因。

### 4.12 重定向

规则列表，每条：

| 字段 | 含义 |
|------|------|
| 来源 | 来源路径或域名，支持正则前缀 `~` |
| 目标 | 目标路径或 URL |
| 状态码 | 301 / 302 / 303 / 307 / 308 |
| 启用 | 单条开关 |

渲染为 nginx `return <status> <target>`。

### 4.13 反向代理

| 字段 | 含义 |
|------|------|
| 启用反向代理 | 总开关 |
| 路径 | 匹配路径前缀，如 `/api` |
| 目标地址 | 上游地址，如 `http://127.0.0.1:3000` |
| WS | 透传 `Upgrade` 头 |
| SNI | 向上游发送 SNI |
| 超时（秒） | `0` 用默认 60 |
| 缓存 / 缓存（秒） | 开启代理缓存并设时长 |

**注意事项**：

- 目标地址必须能解析。域名目标需要在「其他设置 → DNS 解析器」里配 `resolver`，否则 nginx 启动时无法解析、reload 失败；
- 反代站点通常不需要同时开 PHP；
- 推荐做法：80/443 挂主站，非标端口的「次要服务」用反代指向 `127.0.0.1:<port>`，避免多站点抢端口。

### 4.14 防盗链

| 字段 | 含义 |
|------|------|
| 启用防盗链 | **仅对静态资源生效** |
| 允许引用的域名 | 一行一个域名，留空表示不校验 |
| 防盗链文件后缀 | 一行一个后缀，**不带点**，如 `jpg` |
| 触发时返回状态码 | 403 / 302 / 404 |

渲染为 `valid_referers` + `$invalid_referer` 判定。

### 4.15 防复改（防篡改）

| 字段 | 含义 |
|------|------|
| 启用防篡改（文件锁定） | 总开关 |
| 受保护文件列表 | 一行一个文件路径，**相对站点根目录**，如 `index.php` |
| 检测到篡改后自动还原 | 从基线恢复 |
| 检测到篡改时触发告警 | 触发 `tamper` 告警 |

**工作方式**：建立基线时对每个受保护文件算 SHA-256 存进 `integrity` 表；检测时重新计算并比对，得出「修改了 / 新增了 / 删除了」三类差异。

**生效条件（重要）**：必须**先建立基线**，否则不成立。

- 建立基线：`POST /api/sites/{id}/baseline`，返回建立的文件数；
- 检测结果：`POST /api/sites/{id}/tamper`，返回 `{modified, added, deleted, total}`。

**界面操作**（v0.1.0 起「防复改」页签底部新增两个按钮）：

| 按钮 | 作用 | 前置条件 |
|------|------|---------|
| **建立防篡改基线** | 把当前受保护文件的哈希记为"正常状态" | 已启用防篡改 + 受保护文件列表非空 + **表单已保存**（列表要先生效） |
| **立即检测篡改** | 与基线比对，记录事件并按站点配置触发告警 | 已启用防篡改 |

推荐顺序：**填好受保护文件列表 → 保存修改 → 建立防篡改基线 → 之后才能检测出真实改动**。

没有基线时，检测会把受保护文件全部报成「新增」，`tamper` 告警因此会**持续误报**。先建基线，再开「检测到篡改时触发告警」。

> **这两个接口必须用 POST**（GET 会返回 405）。它们都不是纯读操作：
> 建基线会改写 `integrity` 基线表，检测会写 `tamper_events` 事件表**并触发告警通知**。
> 只读 API Token 调用它们会被权限中间件拦成 403。
>
> 需要脚本化时用：
>
> ```bash
> curl -X POST http://<面板地址>:8888/api/sites/<站点ID>/baseline \
>   -H "Authorization: Bearer <token>" -H "Content-Type: application/json"
> ```

**注意事项**：受保护文件列表里的路径同样过路径校验，禁止 `..` 穿越。`auto_restore`（自动还原）在检测到篡改时从基线恢复文件。

### 4.16 网站安全

| 分组 | 字段 | 含义 |
|------|------|------|
| 攻击防护 | 拦截 SQL 注入 | 命中 SQLi 特征直接拒绝 |
| | 拦截 XSS 攻击 | |
| | 拦截目录穿越 | 拦截 `../` 形式的路径穿越 |
| | 拦截 User-Agent | 总开关 |
| | User-Agent 黑名单关键词 | 一行一个关键词，命中即拦截 |
| 路径与后缀拦截 | 禁止访问的路径 | 一行一个，如 `/config` |
| | 禁止访问的后缀 | 一行一个后缀，如 `sql` |
| 响应头与信息隐藏 | 隐藏 nginx 版本号 | `server_tokens off` |
| | 禁止被 iframe 嵌套 | `X-Frame-Options` |

**注意事项**：WAF 类规则基于特征匹配，是**减少攻击面**的护栏，不是完备的入侵检测。规则太宽可能误伤正常请求，规则太窄挡不住变形攻击。调整后务必用真实请求验证。

### 4.17 网站日志

| 字段 | 含义 |
|------|------|
| 开启访问日志 | 写 `<站点根>/logs/access.log` |
| 开启错误日志 | 写 `<站点根>/logs/error.log` |
| 完全关闭日志 | 两者都不写 |
| 日志保留天数 | 超期日志由面板定时清理 |
| 自定义日志格式 | nginx `log_format` 内容，留空用默认组合格式 |

「前往日志查看」按钮会跳到日志页并自动选中该站点。

### 4.18 网站告警

| 字段 | 含义 |
|------|------|
| 启用站点告警 | 总开关 |
| 规则类型 | 下拉来自 `GET /api/alert-types`，共 6 种 |
| 等级 | 提示 / 警告 / 严重 |
| 阈值 | 含义随类型而变 |
| 窗口（分钟） | 统计窗口 |
| 启用 | 单条开关 |
| 通知渠道 | 见下 |

**7 种告警类型**（后端权威清单 `model.AlertRuleTypes`）：

| 类型 | 阈值含义 | 单位 |
|------|----------|------|
| `status_5xx` | 统计窗口内 5xx 响应数量超过阈值 | 次 |
| `traffic_quota` | 站点月流量配额用尽 | MB |
| `cert_expiry` | 证书距离到期不足阈值天数 | 天 |
| `site_down` | 站点探测失败（连不通或返回 5xx） | — |
| `tamper` | 受保护文件哈希与基线不一致 | 个 |
| `nginx_config` | nginx 配置检测失败 | — |
| `config_drift` | nginx 配置被面板以外的途径修改（与最近一次成功下发的快照不一致，如 certbot / 手工编辑），告警消息列出不一致的文件名 | — |

> 配置漂移的基线是**最近一次成功下发的快照**：面板内保存配置会自动刷新基线，不会误报；
> 只有绕过面板的改动才会触发。30 分钟站点级抑制窗口内不重复告警。
> 前端下拉由后端动态下发，不会出现「选了但永远不触发」的枚举漂移。

**通知渠道**（全局，站点级引用）：类型 `email` / `webhook` / `dingtalk` / `wecom`，填接收目标，可单独启用。

**生效条件**：

- `traffic_quota` 依赖流量统计表（后台每 5 分钟采集一次）；
- `site_down` 依赖面板对主域名发起的真实 HTTP 探测；
- `tamper` 依赖站点开启了防篡改且勾了「检测到篡改时触发告警」；
- 没有通知渠道时告警仍会记录（告警中心可见），只是不会外发。

### 4.19 其他设置

| 字段 | 含义 |
|------|------|
| 开启 gzip 压缩 / 压缩类型 / 最小压缩长度（字节） | 留空压缩类型用默认（text/html 等） |
| 请求体大小上限 | 如 `50m` |
| 浏览器缓存时长（秒） | `0` 表示不设置 |
| KeepAlive / send_timeout / read_timeout（秒） | `0` 用默认 |
| DNS 解析器 | 如 `223.5.5.5 119.29.29.29`，反代目标用域名时必填 |
| 缓存后缀 | 一行一个后缀（不带点），走静态资源缓存 |

> 面板自身（不是站点）也会下发 `X-Content-Type-Options`、`X-Frame-Options`、`Referrer-Policy`，开启 `-tls` 时额外下发 HSTS。

---

## 五、证书管理

### 5.1 证书库

「证书」页列出全部证书：名称、域名、签发者、来源（Let's Encrypt / 导入）、有效期、剩余天数、自动续签开关、验证方式、文件路径。

### 5.2 申请证书

「申请证书」弹窗：

1. 选验证方式：**HTTP-01**（需要 80 端口可达且域名已解析到本机）或 **DNS-01**（支持通配符）；
2. HTTP-01 时从已有站点里选承载挑战的域名（挑战文件会写进该站点的 `<根目录>/.well-known/acme-challenge/`）；
3. DNS-01 时选提供商并填凭据：
   - 阿里云 DNS：`API_KEY` / `SECRET_KEY`
   - DNSPod（腾讯云）：`API_KEY`，格式 `ID,Token`
   - 腾讯云 DNS：`SECRET_ID` / `SECRET_KEY`
   - Cloudflare：`DNS_API_TOKEN`
4. 也可「手动输入域名」。

### 5.3 自动续签

- 后台每 12 小时检查一次，**剩余 ≤ 15 天**的证书进入续签队列（启动后 1 分钟先跑一次）；
- 续签**复用原证书 ID**，文件路径不变、内容被覆盖，不会产生孤儿证书文件；
- 续签成功后自动**重绑站点**（刷新站点的证书元数据）并重新下发 nginx 配置。

> 早期版本续签后不重绑站点、不下发配置，接口返回 200、界面显示新到期时间，nginx 却继续 serve 旧证书——证书自然过期后站点 HTTPS 静默下线。现在这两步都做了。

### 5.4 手动续签 / 吊销 / 删除 / 下载

| 操作 | 方法 |
|------|------|
| 手动续签 | `POST /api/certs/{id}/renew`（面板上是续签按钮） |
| 吊销 | `POST /api/certs/{id}/revoke` |
| 删除记录 | `POST /api/certs/{id}/delete` |
| 下载证书 | `GET /api/certs/{id}/download`（返回 PEM 原文） |
| 绑定站点 | `POST /api/certs/{id}/bind` |

> **写操作全部强制 POST**。用 GET 触发这些动作会让只读 Token 直接获得写权限（早期版本确实存在这类端点）。

### 5.5 导入外部证书

「导入证书」弹窗填名称、域名、证书 PEM、私钥 PEM。导入的证书来源标记为 `imported`，不参与自动续签。

### 5.6 ACME 账户管理

「ACME 账户管理」弹窗：列出已有账户（邮箱、CA、密钥类型），可创建新账户。CA 可选生产环境或 staging（staging 签发的证书不受信任，仅用于测试）。

---

## 六、计划任务

### 6.1 任务类型

6 种，界面下拉可选：

| 类型 | 参数 | 说明 |
|------|------|------|
| Shell 脚本 | `script` / `timeout` / `workdir` | 自由 shell，默认超时 1 小时，工作目录默认 `/tmp` |
| 备份站点 | `retain` / `note` | 目标站点 ID |
| 备份数据库 | `retain` / `note` | 目标数据库名 |
| 切割访问日志 | `retain` | 目标站点 ID，保留份数，0 = 不删 |
| 释放内存 | — | 仅 Linux（`sync` + `drop_caches`） |
| 访问 URL | `url` / `method` / `timeout` | 定期打一个 URL（心跳、探活） |

### 6.2 调度表达式

标准 **5 段 crontab** 表达式（分 时 日 月 周），支持 `@daily` 这类描述符。界面提供预设：每天凌晨 2 点 / 3 点、每 30 分钟、每小时、每周日 0 点。

调度器每 **30 秒**扫一次数据库，挑出 `enabled=1 且 next_run_at <= now` 的任务执行。单时间点最大并发 4 个任务，同一任务不会重叠执行。每个任务有 **1 小时**默认超时。

执行历史保留每个任务最近 **50 条**。

### 6.3 Shell 脚本的危险命令检查

执行**前**（不只是创建时）会过一遍检查，因此历史遗留的任务同样会被拦。检查方式不是子串黑名单，而是：

1. **归一化** — 去掉引号、反斜杠、`$`、反引号，`sh''utdown` / `"shutdown"` / `sh\utdown` 归一化后都是 `shutdown`；
2. **词法切分** — 按命令分隔符切成段、段内切成词；
3. **命令词匹配** — 按 basename 匹配禁用命令词，而不是子串。

拦截范围：关机重启类（`shutdown` / `reboot` / `halt` / `poweroff` / `init`）、格式化磁盘类（`mkfs*` / `mke2fs` / `dd` / `wipefs` / `fdisk` / `parted` / `sgdisk` / `mkswap`）、改密码与改用户类（`passwd` / `chpasswd` / `useradd` / `userdel` / `usermod`）、内核模块类（`insmod` / `rmmod` / `modprobe` / `sysctl`）、`kill 1` / `kill -1`、`systemctl` 的危险子命令（`poweroff` / `reboot` / `halt` / `kexec` / `isolate` / `suspend` / `hibernate` / `emergency`）、`rm -rf /` 或 `rm -rf ~`、fork 炸弹模式、`sysrq-trigger`、`curl`/`wget` 管道给解释器（下载即执行）。

> **这不是沙箱。**面板以 root 运行，无法彻底隔离。它只是一道减少误操作的护栏，挡不住有意绕过。真正的授权边界在 API 层：执行 Shell 任务需要独立的 `shell` scope。

### 6.4 手动执行

任务行上的「执行」按钮调 `POST /api/cron/jobs/run`，body `{"job_id": "..."}`。手动执行不写 `last_run_at`（那由调度器写），但会写执行历史。

### 6.5 失败通知

任务失败时自动通过告警引擎通知（`cron_failed` 事件）。

---

## 七、备份

### 7.0 备份落盘位置（-backup-dir）

默认落在 `<data>/backup/{site,database}/`。启动参数 `-backup-dir=<目录>` 可把**新的备份**
改写到自定义根目录（`<目录>/{site,database}/`）；已有备份的记录与文件不受影响。
注意：切换目录后，旧目录里的历史文件不再被孤儿清理（PruneOrphanFiles）扫描，需要迁移的话手工挪。

### 7.1 站点备份

「备份 → 备份站点」，选站点 + 可选备注。产物是一个 `tar.gz`：

```
nginx.conf              渲染好的该站点 nginx 配置副本
site/...                站点根目录内容（logs/ 被显式排除）
```

**单文件上限 100MB**，超出直接报错并删除临时文件。

### 7.2 数据库备份

「备份 → 备份数据库」，选库名。产物是 `mysqldump --single-transaction` + gzip 的 `sql.gz`。

**前置条件**：MySQL 子系统必须启用，且 `mysqldump` 在 PATH 里。

### 7.3 备份操作

| 操作 | 说明 |
|------|------|
| 下载 | `GET /api/backups/{id}/download` |
| 查看 conf | 站点备份可预览包内的 `nginx.conf`（`POST /api/backups/{id}/restore`） |
| 删除 | `DELETE /api/backups/{id}`（同时删磁盘文件与数据库记录） |

> **恢复只提供预览。**`POST /api/backups/{id}/restore` 返回备份内 `nginx.conf` 的内容供你确认，**不会**自动覆盖站点文件。数据文件需要你自己 `tar xzf` 恢复。

### 7.4 保留份数与孤儿清理

计划任务的 `retain` 参数控制保留份数（0 = 永久保留）。裁剪顺序是**先删磁盘文件、再裁数据库记录**，反过来做磁盘上的文件永远删不掉。此外还有一轮孤儿文件清理：扫描备份目录，删掉已经没有数据库记录的文件——历史遗留的孤儿也能被收走。

---

## 八、日志

「日志」页：

| 功能 | 说明 |
|------|------|
| 选站点 | 顶部下拉切换 |
| 类型 | 访问日志 / 错误日志 |
| 分页 | 访问日志按页读，`limit` 默认 200、上限 1000；`limit × page` 有总量上限（防 OOM） |
| 实时跟踪 | 「开始实时跟踪」通过 WebSocket 推送；「停止」结束；「清空显示」只清界面 |
| 返回第一页 | 翻页后回到第一页 |

**实时跟踪的地址获取**：`POST /api/system/logs/tail?site_id=<id>` 返回 `{ws_url}`，前端连这个地址。WebSocket 端点 `/ws/logs` 会做**真实的会话校验**——早期版本只检查 cookie 是否存在（等于没鉴权），任意客户端带个垃圾 token 就能拖走任意站点的访问日志（含访客 IP、UA、请求路径）。

**错误日志**按纯文本返回。

---

## 九、告警

「告警」页：

| 功能 | 说明 |
|------|------|
| 告警列表 | 按时间倒序，显示站点、类型、级别、消息、时间 |
| 标记处理 | 单条标记已处理 |
| 全部标记已处理 | 批量处理所有未处理告警 |
| 通知渠道 | 弹窗管理全局渠道：添加渠道、填写接收目标、启用/禁用、删除 |
| 发送测试 | 对某个渠道发一条测试消息，验证连通性 |

通知渠道类型：`email` / `webhook` / `dingtalk` / `wecom`。

> 渠道地址由用户直接填写，面板**不校验目标是否指向内网**，也**不加密存储**。把面板暴露给不受信任的人时，这两点是已知风险面。

---

## 十、运维中心

三个子页签。

### 10.1 API Token

用于 CI、监控、备份脚本等自动化调用面板 API。

| 项 | 说明 |
|----|------|
| 创建 | 「创建 Token」弹窗：名称、权限、有效期 |
| 权限 | **三档**可选：只读（`read`）/ 可写（`write`）/ 可执行 Shell 计划任务（`shell`） |
| 有效期 | 永不过期 / 30 天 / 90 天 / 1 年 |
| 明文 | **只在创建响应里出现一次**（`show_token: true`），库里只存 SHA-256 摘要 |
| 吊销 | 「吊销 Token」= 直接删行，凭据泄漏时可立刻失效 |

**用法**：

```bash
curl -H "Authorization: Bearer <token>" http://<面板地址>:8888/api/sites
# 或
curl -H "X-API-Token: <token>" http://<面板地址>:8888/api/sites
```

**scope 语义**：

| scope | 能力 |
|-------|------|
| `read` | 只能调 GET / HEAD / OPTIONS |
| `write` | 可以创建/修改/删除 |
| `shell` | **独立授权位**：可以执行 Shell 脚本类型的计划任务 |
| 留空 | 全权限（等价于管理员会话） |

`shell` 是独立的，因为面板以 root 运行，「Shell 脚本」类型的计划任务等于交出 root shell。一个泄漏的 `write` Token（CI / 监控最常用）不应该等于整机 root。

**勾选规则**（v0.1.0 起创建弹窗提供三个独立勾选框）：

| 勾选 | 实际下发 | 说明 |
|------|---------|------|
| 只读 | `["read"]` | 默认 |
| 只读 + 可写 | `["read","write"]` | `write` 蕴含 `read` |
| 勾了 shell | `["read","write","shell"]` | `shell` 必须同时具备 `write`（要能创建/修改任务本身），因此会自动补上 `write` |

三个勾选框互相独立，勾选顺序不影响最终结果。

**Token 列表的权限标签**：只读（蓝）、可写（橙）、**Shell（红）**。`shell` 在界面上单独标红——它等于交出 root 执行能力，不应该和普通写权限长得一样。

**只读 Token 越权不了**：写方法一律要求 `write`；所有有副作用的端点强制 POST——GET 不再能改状态。

### 10.2 操作审计

记录**写操作**（POST / PUT / PATCH / DELETE）与登录事件。审计写在鉴权之后，所以拿得到调用者身份与响应码。

| 列 | 说明 |
|----|------|
| 时间 | |
| 操作者 | 用户名 |
| 动作 | 如 `site.create` / `site.update` / `site.delete` / `site.toggle` / `cert.issue` / `token.create` / `auth.login` / `auth.login_failed` |
| 目标 | 资源 ID（如站点 ID） |
| 来源 IP | 可信客户端 IP（受信代理外一律用直连地址） |
| 结果 | HTTP 状态码 |

**过滤**：`GET /api/audit-logs?limit=200&action=site.delete`。

**导出**：`GET /api/audit-logs/export?format=csv|json&action=<前缀>`（`format` 留空为 CSV）。
CSV 带 UTF-8 BOM（Excel 直接打开中文不乱码），文件名 `zhiweipanel-audit-<时间>.<格式>`；
导出不受列表接口 1000 行的钳制（最多导出表内全部 5000 条）。**导出本身会留痕**
（记为 `audit.export`，含操作者、格式与行数）——"谁把审计日志拿走了"与"谁动了配置"同等重要。

**保留**：表内最多 5000 条，每写入 200 条触发一次裁剪。

> 审计**只记写操作与登录事件**（外加审计导出留痕）——"谁看了站点配置"不留痕。审计写入失败只记服务端日志，绝不影响主流程。

### 10.3 配置历史

每次配置下发成功后自动留一份 `conf.d` 目录快照，保留最近 **30** 份（存盘不存库，避免主库膨胀）。

| 操作 | 说明 |
|------|------|
| 查看快照 | 展开看每个文件的原始内容 |
| 与当前对比 | 逐行 diff，显示新增 / 移除 |
| 回滚 | 把快照里的文件写回 `conf.d` 并重载 nginx |

> **回滚只恢复 nginx 配置文件，不回滚数据库。**面板里的站点定义仍是「当前」那一份。要连站点定义一起回退，请配合站点备份。

---

## 十一、设置

### 11.1 面板设置

| 设置项 | 说明 |
|--------|------|
| 面板端口 | 面板 HTTP 服务监听端口，**修改后需重启后端生效** |
| 面板路径 | 访问路径前缀，如 `/panel`（留空 = 根路径） |
| 默认 PHP 版本 | 新建站点时的默认 PHP 版本 |
| 默认站点根目录 | 新建站点根目录的默认父路径，如 `/www/wwwroot` |
| 日志保留天数 | 站点日志默认保留天数 |
| 时区 | 影响日志与告警的时间显示 |

### 11.2 运行时信息

面板自动探测并展示：nginx 是否安装/运行/版本、配置检测状态、可用 PHP 版本与 socket、运行时类型（host/docker）、磁盘用量、Docker 状态。

### 11.3 nginx 主配置

「查看配置」读取 nginx 主配置原文（只读）；「测试配置」执行真实的 `nginx -t` 并显示结果。

> 宿主机模式下主配置位置因发行版而异，读不到时会退回显示面板生成的 http 级配置。

---

## 十二、API 速查

面板 API 全部挂在 `/api` 前缀下。浏览器会话用 HttpOnly Cookie 认证；机器访问用 API Token（`Authorization: Bearer <token>` 或 `X-API-Token: <token>`）。

### 12.1 认证与账户

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/auth/login` | 登录；异常时返回 `need_verify` + `challenge_id` |
| POST | `/api/auth/logout` | 登出 |
| GET | `/api/auth/me` | 当前登录用户 |
| POST | `/api/auth/password` | 修改密码 |
| POST | `/api/auth/verify-challenge` | 提交异常登录验证码 |
| POST | `/api/auth/password-reset/request` | 申请改密验证码 |
| POST | `/api/auth/password-reset/confirm` | 提交验证码 + 新密码 |
| GET | `/api/admin/account` | 账户资料（含上次登录 IP/UA） |
| PUT | `/api/admin/account/email` | 设置邮箱（未登录时可用 `X-Setup-Password`） |
| POST | `/api/admin/account/email/verify` | 发送邮箱验证码 |
| GET | `/api/admin/smtp` | 读 SMTP 配置（密码脱敏） |
| PUT | `/api/admin/smtp` | 写 SMTP 配置 |
| POST | `/api/admin/smtp/test` | 发测试邮件 |

### 12.2 站点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/sites` | 站点列表（含今日请求/流量等汇总） |
| POST | `/api/sites` | 新建站点 |
| GET | `/api/sites/{id}` | 站点详情 |
| PUT | `/api/sites/{id}` | 更新站点 |
| DELETE | `/api/sites/{id}` | 删除站点（联动 drop 库） |
| POST | `/api/sites/{id}/toggle` | 启停站点（body `{"status":"running|stopped"}`） |
| POST | `/api/sites/{id}/reload` | 全量下发 nginx 配置 |
| GET | `/api/sites/{id}/config` | 站点配置 + http 片段 + `nginx -t` 结果 |
| GET | `/api/sites/{id}/logs?type=access\|error&limit=&page=` | 访问/错误日志 |
| GET | `/api/sites/{id}/stats?days=7` | 流量统计（1–365 天） |
| GET | `/api/sites/{id}/files?path=` | 列目录 |
| GET | `/api/sites/{id}/files/raw?path=` | 读文件（文本返回原文，二进制返回 base64 JSON） |
| PUT | `/api/sites/{id}/files?path=` | 写文件（body `{"content":"…","encoding":"base64"}`） |
| DELETE | `/api/sites/{id}/files?path=` | 删文件/目录 |
| POST | `/api/sites/{id}/files/rename` | 重命名（body `{"from":"…","to":"…"}`） |
| POST | `/api/sites/{id}/files/mkdir` | 新建目录 |
| POST | `/api/sites/{id}/files/upload` | 上传文件（multipart） |
| POST | `/api/sites/{id}/files/unzip` | 解压（body `{"zip_path":"…","dest":"…"}`） |
| POST | `/api/sites/{id}/baseline` | 建立防篡改基线 |
| GET | `/api/sites/{id}/tamper` | 篡改检测结果 |
| POST | `/api/sites/batch` | 批量启停（body `{"action":"…","ids":[…]}`） |

### 12.3 证书与 ACME

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/certs` | 证书列表 |
| POST | `/api/certs/issue` | 申请证书 |
| POST | `/api/certs/import` | 导入证书 |
| GET | `/api/certs/{id}` | 证书详情 |
| POST | `/api/certs/{id}/renew` | 续签（自动重绑站点） |
| POST | `/api/certs/{id}/revoke` | 吊销 |
| POST | `/api/certs/{id}/delete` | 删除记录 |
| POST | `/api/certs/{id}/bind` | 绑定站点（body `{"site_id":"…"}`） |
| GET | `/api/certs/{id}/download` | 下载 PEM |
| GET | `/api/acme/accounts` | ACME 账户列表 |
| POST | `/api/acme/accounts` | 创建 ACME 账户 |
| GET | `/api/dns-providers` | DNS-01 提供商与凭据字段说明 |

### 12.4 系统与运维

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/settings` | 面板设置 |
| PUT | `/api/settings` | 写面板设置 |
| GET | `/api/system/status` | 系统总览（nginx / PHP / 运行时 / 磁盘 / Docker） |
| GET | `/api/system/info` | 运行参数（`public_port` / `disable_ipv6`） |
| GET | `/api/system/mysql` | MySQL 子系统状态 |
| GET | `/api/system/nginx-config` | nginx 主配置 |
| POST | `/api/system/nginx-test` | 执行 `nginx -t` |
| POST | `/api/system/config-apply` | 全量下发配置 |
| POST | `/api/system/logs/tail?site_id=` | 取 WebSocket 地址 |
| GET | `/api/system/metrics` | 宿主机指标（`current` + `history`） |
| GET | `/api/system/config-history` | 配置快照列表 |
| GET | `/api/system/config-history/{id}` | 快照内容 |
| GET | `/api/system/config-history/{id}/diff` | 与当前配置对比 |
| POST | `/api/system/config-history/{id}/restore` | 回滚到该快照 |
| GET | `/api/tokens` | API Token 列表 |
| POST | `/api/tokens` | 创建 Token（`scopes` 支持 `read`/`write`/`shell`） |
| DELETE | `/api/tokens/{id}` | 吊销 Token |
| GET | `/api/audit-logs?limit=&action=` | 审计日志 |
| GET | `/api/audit-logs/export?format=csv\|json&action=` | 导出审计（本身留痕 `audit.export`） |
| GET | `/api/alert-types` | 告警规则类型清单 |
| GET | `/api/alerts?limit=` | 告警列表 |
| POST | `/api/alerts/{id}/resolve` | 标记告警已处理 |
| POST | `/api/alerts/test` | 测试通知渠道 |
| WS | `/ws/logs` | 实时日志推送（需有效会话） |

### 12.5 计划任务与备份

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/cron/jobs` | 任务列表 |
| POST | `/api/cron/jobs` | 新建任务 |
| GET | `/api/cron/jobs/{id}` | 任务详情 + 最近 50 条执行历史 |
| PUT | `/api/cron/jobs/{id}` | 更新任务 |
| DELETE | `/api/cron/jobs/{id}` | 删除任务 |
| POST | `/api/cron/jobs/run` | 手动执行（body `{"job_id":"…"}`） |
| GET | `/api/backups?kind=&target=&limit=` | 备份列表 |
| POST | `/api/backups` | 创建备份（body `{"kind":"site\|database","target":"…","note":"…"}`） |
| GET | `/api/backups/{id}` | 备份详情 |
| DELETE | `/api/backups/{id}` | 删除备份 |
| GET | `/api/backups/{id}/download` | 下载备份 |
| POST | `/api/backups/{id}/restore` | 恢复预览（仅站点备份） |

---

## 附：配置预览

站点详情 → 配置文件页签 → 「查看生成的 Nginx 配置」，返回三部分：

- **`nginx_test`** — `nginx -t` 的结果（`ok` + `message`）；
- **`http_conf`** — http 级片段（`log_format`、`limit_req_zone` 等）；
- **`site_conf`** — 该站点的完整 `server` 块。

这是排查「面板里配了但 nginx 里不是这么生效」的最直接手段。
