# 审计报告：驱动 / 服务 / 存储

> 审计范围：
> - 驱动层：`internal/driver/driver.go`、`internal/driver/hostdriver/host.go`、`internal/driver/hostdriver/disk_linux.go`、`internal/driver/hostdriver/disk_other.go`、`internal/driver/dockerdriver/docker.go`
> - 服务层：`internal/service/service.go`、`internal/service/files.go`、`internal/service/account.go`、`internal/service/ops.go`、`internal/service/site_default.go`
> - 存储层：`internal/store/store.go`、`internal/store/helpers.go`、`internal/store/ops_store.go`、`internal/store/cron_store.go`、`internal/store/backup_store.go`
> - 交叉验证（为判定可达性而读）：`internal/model/model.go`、`internal/model/validate.go`、`internal/model/ops.go`、`internal/nginx/render.go`、`internal/api/server.go`、`internal/api/helpers.go`、`internal/api/certs.go`、`internal/mailer/smtp.go`、`internal/service/apply_test.go`、`main.go`、`deploy/install.sh`、`deploy/zhiweipanel.service`、`deploy/docker-compose.yml`
> - 未读：`internal/service/*_test.go`（除 apply_test.go）、`internal/store/*_test.go`、`internal/nginx/*`、`internal/api/*` 其余文件、`internal/cron/*`、`internal/backup/*`、`internal/logmgr/*`、`internal/acme/*`、`internal/mysql/*`、`internal/alert/*`、`internal/sysinfo/*`、`internal/tlsgen/*`、`internal/confhistory/*`
>
> 审计时间：2026-10-29
>
> 说明：本次为**纯静态只读审计**，未执行 `go build` / `go test` / `go vet`，未修改任何 Go/JS 源码。所有结论均来自源码阅读，涉及运行期行为处已标注推断链与行号。

---

## 一、BUG 与缺陷

### BUG-01 UpdateSite 全量覆盖丢失 Status / Edate / Mysql —— 停用站点被自动恢复运行，MySQL 库变孤儿
- **严重级**：P1
- **位置**：`internal/service/service.go:533-596`（保留字段清单在 `572-582`）；后果落点在 `internal/service/service.go:142-145` 与 `internal/service/service.go:607-615`
- **现象**：
  `PUT /api/sites/{id}` 的请求体不带 `status` 字段时，`site.Status` 保持零值 `""`。`UpdateSite` 只保留了 `ID` / `CreatedAt` / `Root.Path` / `Cert` 四类字段，**没有保留 `Status`、`Edate`、`Mysql`**。
  随后 `SaveSite` 把 `status=""` 落库（`internal/store/store.go:317`），而 `ApplyAllConfig` 判断是否注释站点配置的条件是 `site.Status == stopped || site.Status == disabled`（`internal/service/service.go:142`）——空串两者都不满足，于是**被人工停用的站点配置按"运行中"重新写盘并 reload，站点自动恢复对外服务**。
  同一处还导致：`site.Mysql` 被清空 → 站点与 MySQL 库的绑定丢失 → 之后 `DeleteSite` 的 `if site.Mysql != nil` 判断不成立（`internal/service/service.go:607`），**MySQL 库与用户永久残留且面板再也无从清理**；`site.Edate` 被清空 → 到期时间静默丢失。
- **触发条件**：任何不带 `status` / `mysql` / `edate` 的 PUT。API 层 `bindSite` 是全量 JSON 反序列化（`internal/api/helpers.go:87-93`），不做字段级合并，因此前端只要不是"回传完整对象"就会命中。
- **根因**：PUT 全量替换语义下，跨请求需要保留的字段必须显式从 `old` 回填。代码作者**已经意识到这个坑并修了 `Cert`**（`internal/service/service.go:578-582` 的注释明确写了"全量替换的 PUT 语义下，请求体不带该字段会把它清空，导致站点 SSL 配置静默丢失"），但同一份保留清单漏掉了 `Status` / `Edate` / `Mysql`。
- **影响面**：站点可用性语义（停用形同虚设）、MySQL 资源泄漏、站点到期信息丢失。属于可被一次普通 API 调用触发的功能性缺陷。
- **修复方案**：在 `internal/service/service.go:572` 起的保留清单中补齐 `site.Status`（为空时取 `old.Status`）、`site.Edate`、`site.Mysql`；或把更新接口改为字段级 PATCH 语义。另建议 `Status` 为空时兜底为 `model.SiteStatusRunning`（与 `CreateSite` 的 `internal/service/service.go:470-472` 对齐）。
- **证据**：
```go
// internal/service/service.go:572-586
site.ID = id
site.CreatedAt = old.CreatedAt
if site.Root.Path == "" {
    site.Root.Path = old.Root.Path
}
// cert 是跨实体引用（由证书绑定写入），全量替换的 PUT 语义下
// 请求体不带该字段会把它清空，导致站点 SSL 配置静默丢失。
if site.Cert == nil {
    site.Cert = old.Cert
}
// ← 此处缺少 Status / Edate / Mysql 的回填
if err := validateSitePaths(site); err != nil {
    return nil, err
}
```

---

### BUG-02 CreateSite 在 SaveSite 之前就真实建了 MySQL 库与用户 —— SaveSite 失败即留永久孤儿库
- **严重级**：P1
- **位置**：`internal/service/service.go:483-491`
- **现象**：`provisionMySQL`（`internal/service/service.go:631-680`）会真的执行 `CREATE DATABASE` + `CREATE USER` + `GRANT`，并在 `internal/service/service.go:659-661` 生成一次性密码。该调用发生在 `SaveSite` **之前**。若 `SaveSite` 失败（磁盘满、SQLite 忙、序列化异常），返回 `nil, err`，而 **MySQL 服务器上已存在该库与该用户**，站点记录却不存在。
- **触发条件**：`site.Mysql != nil` 且 `SaveSite` 失败。SaveSite 失败在正常运维下不罕见（SQLite `busy_timeout=10000` 超时、磁盘满、data 目录只读）。
- **根因**：副作用（建库）与持久化（落库）之间没有补偿逻辑。`provisionMySQL` 内部**有**回滚（建用户失败删库、授权失败删库删用户，`internal/service/service.go:668-678`），但**只在 MySQL 侧失败时回滚**，落库失败时无人回滚 MySQL。
- **影响面**：孤儿数据库 + 孤儿用户（带 `ALL` 权限）残留在 MySQL 上；密码只存在于内存中的 `site.Mysql.Password`（`internal/service/service.go:660`），错误返回时随对象一起丢弃，用户**永远拿不到这个密码**，也就无法登录该库。资源泄漏 + 数据不可达。
- **修复方案**：把 `SaveSite` 提到 `provisionMySQL` 之前落库"意图"（或在 SaveSite 失败分支补 `deprovisionMySQL` 回滚）。注意 `internal/service/service.go:480-482` 的注释描述的正是"先落库再建库"的意图，**代码与自身文档相反**——以注释为准调整顺序即可。
- **证据**：
```go
// internal/service/service.go:480-491
// 注意：site.Mysql 落库后再做实际建库——即使建库失败也能保留"意图"，
// 让用户从面板再次触发（我们用 wrap error 把驱动错误透传……）。
if site.Mysql != nil {
    if err := s.provisionMySQL(ctx, site); err != nil {
        return nil, err
    }
}

if err := s.db.SaveSite(site); err != nil {
    return nil, err          // ← 此处返回时，MySQL 库/用户已经建好，没人回滚
}
```

---

### BUG-03 dockerdriver 的 PHP-FPM socket 路径拼接错误 —— Docker 模式下 PHP 站点全部 502
- **严重级**：P1
- **位置**：`internal/driver/dockerdriver/docker.go:235`
- **现象**：`Socket` 由 `fmt.Sprintf("/run/php/php-%s-fpm.sock", strings.TrimPrefix(ver, "7"))` 拼出。对探测列表中的容器名逐一展开：
  - `php74-fpm` → `ver="74"` → `TrimPrefix("74","7")="4"` → **`/run/php/php-4-fpm.sock`**（不存在的路径）
  - `php81-fpm` → `ver="81"` → `/run/php/php-81-fpm.sock`（格式也不对，见第二节）
  - `php-fpm` → `ver="default"` → **`/run/php/php-default-fpm.sock`**（不存在的路径）
- **触发条件**：Docker 运行时下，界面把驱动上报的 socket 写进 `site.PHP.Socket` → `internal/service/service.go:398-400` 填入 `opt.DefaultPHPSocket` → `internal/nginx/render.go:891-902` 输出 `fastcgi_pass unix:/run/php/php-4-fpm.sock;` → **该 socket 不存在，PHP 请求全部 502 Bad Gateway**。
- **根因**：`TrimPrefix(ver, "7")` 想把 "74" 变成 "4.0" 之类的语义，但没有实现该转换，反而把 "74" 的首位 7 直接削掉；"default" 分支更是完全没处理。
- **影响面**：Docker 运行时下所有使用 PHP 的站点不可用。hostdriver 无此问题（它 `os.Stat` 真实存在的 socket 并返回真实路径，`internal/driver/hostdriver/host.go:284-287`）。
- **修复方案**：删掉 `TrimPrefix`，socket 名应与容器内实际路径一致；更稳妥的做法是参照 hostdriver，在容器内 `ls` 出真实 socket（`internal/driver/dockerdriver/docker.go:239-250` 已有此兜底逻辑，只是它只在 `len(out)==0` 时才走，且 `sockRe` 正则不匹配 Debian 命名，见第二节）。
- **证据**：
```go
// internal/driver/dockerdriver/docker.go:222-237
for _, name := range []string{"php-fpm", "php74-fpm", "php80-fpm", "php81-fpm", "php82-fpm", "php83-fpm"} {
    ...
    ver := strings.TrimPrefix(name, "php")
    ver = strings.TrimSuffix(ver, "-fpm")
    if ver == "" {
        ver = "default"
    }
    out = append(out, driver.PHPVersion{
        Version: ver,
        Socket:  fmt.Sprintf("/run/php/php-%s-fpm.sock", strings.TrimPrefix(ver, "7")), // ← "74" → "4"
        Running: state == "running",
    })
}
```

---

### BUG-04 `model.ValidateSite` 整层校验从未被调用（死代码）—— nginx 配置文本注入面零防护
- **严重级**：P2
- **位置**：`internal/model/validate.go:318`（定义，全仓库零调用点）；调用缺口在 `internal/service/service.go:467`（CreateSite）与 `internal/service/service.go:583`（UpdateSite）
- **现象**：`model/validate.go` 里的 `ValidateSite` 及其下辖 15 个子校验（`validateSiteName` / `validateIndexFiles` / `validateAccess` / `validateTraffic` / `validateSecurity` / `validateHotlink` / `validateSSL` / `validateProxy` / `validateRedirects` / `validateSubDir` / `validatePHP` / `validateExtra` / `validateLogs` / `validateRoot` / `validateAlerts`）**一个都没有被调用**。全仓库 grep `ValidateSite` 只命中定义处与注释。
  service 层落库前只做了两件事：`site.Domains.Validate()` 与 `validateSitePaths(site)`（`internal/service/service.go:508-530`），而后者**只校验 3 个路径**：`Root.Path`、`SubDir.Rules[].Root`、`Lock.Files`。
  因此以下直接写进 nginx 配置文本的字段**均无校验**：
  - `SSL.CertFile` / `SSL.KeyFile` → 原样进入 `ssl_certificate` / `ssl_certificate_key`（`internal/nginx/render.go:608-609`）
  - `Redirect.Rules[].From/To` → 原样进入 `return`
  - `Proxy.Rules[].Target` → 原样进入 `proxy_pass`
  - `Security.DenyPaths` → 原样进入 location
  - `Logs` / `Extra` / `Hotlink` 等
  `internal/model/validate.go:282-305` 专门写了 `NginxSafeCertPath` 来防止证书路径穿越与语法注入——但它没有调用点。
- **触发条件**：任意 `POST /api/sites` 或 `PUT /api/sites/{id}`，请求体由 `bindSite`（`internal/api/helpers.go:87-93`）全量反序列化，无任何字段级过滤。
- **根因**：`internal/model/validate.go:33-39` 的设计注释写明"写入路径（建站/改站）：ValidateSite 返回精确到字段的错误"，但**服务层从未接入**；渲染层的"净化降级"也只覆盖了 `sanitizeResolver`（`internal/nginx/render.go:247`）与 UA 关键词的 `regexp.QuoteMeta`（`internal/nginx/render.go:423`）等少数几处，不是系统性兜底。
- **影响面**：
  1. 注入类字段（含 `;` `{}` 换行）会让 `nginx -t` 失败 → `ApplyAllConfig` **整体回滚**（`internal/service/service.go:248-252`）→ **所有站点**的本次变更都不生效，而用户看不出是哪一格填错（`internal/model/validate.go:28-31` 的注释恰好描述了这个后果）。
  2. `SSL.CertFile` 指向任意绝对路径 → nginx 以 root 读取任意文件；配合 `ssl_reject_handshake` 兜底（`internal/service/service.go:184-200`）行为更不可预期。
  3. 面板以 root 运行（`internal/service/service.go:503` 注释自述），落库前缺校验的代价被放大。
  **说明**：`Rewrite.Rules` 与 `RawConf` 原样注入是**产品设计意图**（`internal/nginx/render.go:960-985`），管理员场景下可接受；本条的缺陷是"专门为此写的防御层完全没有生效"，而不是"允许自定义配置"本身有问题。
- **修复方案**：在 `CreateSite`（`internal/service/service.go:467` 之前）与 `UpdateSite`（`internal/service/service.go:583` 之前）调用 `model.ValidateSite(site)`；或让 `validateSitePaths` 复用 `model/validate.go` 的谓词集合，避免两套规则漂移。
- **证据**：
```go
// internal/service/service.go:467-469（CreateSite）
if err := validateSitePaths(site); err != nil {
    return nil, err
}
// internal/service/service.go:583-585（UpdateSite）
if err := validateSitePaths(site); err != nil {
    return nil, err
}
// internal/service/service.go:508-530：validateSitePaths 只覆盖 3 个路径字段
//   Root.Path / SubDir.Rules[].Root / Lock.Files
// internal/model/validate.go:318：func ValidateSite(s *Site) error { … }  ← 全仓库无调用点
```

---

### BUG-05 孤儿站点配置的备份读取错目录 —— 回滚时孤儿配置被永久删除
- **严重级**：P2
- **位置**：`internal/service/service.go:235`（读取）、`internal/service/service.go:236-242`（备份入快照）
- **现象**：清理孤儿配置时，代码用 `readConfIfExists(s.drv, ctx, driver.SiteConfName(id))` 读取旧内容以便回滚。但 `readConfIfExists` → `drv.ReadConf` 读的是 **ConfDir**（`internal/driver/hostdriver/host.go:230-236`、`internal/driver/dockerdriver/docker.go:191-197`），而站点 vhost 配置在 **SiteConfDir**（写入见 `internal/driver/hostdriver/host.go:215-220`）。两个目录不同时，读取必然 `ErrNotExist` → `orphanExisted=false`。
  而 `RemoveSiteConf` 已经在 `&&` 的左操作数被求值并**执行了删除**（`internal/service/service.go:236`），只是因为 `orphanExisted=false` 导致**备份没有入快照**。随后若 `nginx -t` 失败触发 `rollback`（`internal/service/service.go:250`），该文件**不会被写回**。
- **触发条件**：`-site-conf` 与 `-conf` 指向不同目录时。当前 `deploy/install.sh:297-298`、`deploy/zhiweipanel.service:17-18`、`deploy/docker-compose.yml:43-44` 都把两者设成同一目录，**故线上未暴露**；但 `-site-conf` 这个 flag 的存在意义正是分离二者。
- **根因**：驱动接口缺少 `ReadSiteConf` 方法（`internal/driver/driver.go:126-127` 只有 `ReadConf`），service 层为读站点配置而在 `internal/service/service.go:147-148` 直接用 `os.ReadFile(filepath.Join(s.drv.Paths().SiteConfDir, …))` 绕过了驱动；到了孤儿分支却改用 `readConfIfExists`（走 `ReadConf`），两条路径指向不同目录。
- **影响面**：孤儿配置无法回滚（该站点已从 DB 删除，影响有限）；但它同时说明"读站点配置"这件事在代码里有两个互不一致的入口，后续改动极易再踩。测试未能发现的原因见 `internal/service/apply_test.go:36-38`——fakeDriver 把 `ConfDir` 与 `SiteConfDir` 设成同一个目录，**结构上掩盖了这个 bug**。
- **修复方案**：给 `driver.Driver` 增加 `ReadSiteConf(ctx, siteID)`，两处统一走驱动；或让 `readConfIfExists` 接受目录参数。
- **证据**：
```go
// internal/service/service.go:235-242
orphanOld, orphanExisted, _ := readConfIfExists(s.drv, ctx, driver.SiteConfName(id))
// ↑ ReadConf 读 ConfDir，站点配置却在 SiteConfDir → orphanExisted 恒为 false
if rmErr := s.drv.RemoveSiteConf(ctx, id); rmErr == nil && orphanExisted {
    backups = append(backups, configBackup{
        siteID:  id,
        content: orphanOld,
        existed: true,
    })
}
// ← 文件已被删除，但因为 existed=false 没有进快照，回滚时无法写回
```

---

### BUG-06 ApplyAllConfig 吞掉 ListSiteConfs / RemoveSiteConf 错误 —— 已删站点继续对外服务
- **严重级**：P2
- **位置**：`internal/service/service.go:224`（`lerr`）、`internal/service/service.go:236`（`rmErr`）
- **现象**：孤儿清理整段被 `if existing, lerr := s.drv.ListSiteConfs(ctx); lerr == nil {` 包裹——`lerr != nil` 时**整段被跳过且错误被完全丢弃**，调用方收到的是"成功"。`RemoveSiteConf` 的错误也被 `rmErr == nil` 条件吞掉。
- **触发条件**：`ListSiteConfs` 返回错误（目录权限异常、IO 错误、路径不是目录）；或 `RemoveSiteConf` 失败（文件被占用、权限不足）。
- **根因**：清理失败被当成"没有要清理的东西"，而不是"清理失败需要上报"。
- **影响面**：**安全边界问题**。`internal/service/service.go:212-216` 的注释明确说明了这个后果："删站后 10-site-<id>.conf 留在 conf.d 里继续被 nginx 加载，已删除的站点照常对外服务"。面板认为站点已删除、DB 里已无记录，但站点仍在对外提供内容，且**用户无法再从面板删除它**（记录已没了）。这是本次审计中最贴近"数据/资源不可控"的一条。
- **修复方案**：`ListSiteConfs` 失败时返回错误并中止本次下发（宁可不下发也不能留下未知的对外服务）；`RemoveSiteConf` 失败时记录日志并把该 siteID 计入"待清理"列表，在下次 `ApplyAllConfig` 重试。
- **证据**：
```go
// internal/service/service.go:224-236
if existing, lerr := s.drv.ListSiteConfs(ctx); lerr == nil {
    for _, name := range existing {
        ...
        if !known[id] {
            orphanOld, orphanExisted, _ := readConfIfExists(s.drv, ctx, driver.SiteConfName(id))
            if rmErr := s.drv.RemoveSiteConf(ctx, id); rmErr == nil && orphanExisted {
```
> 两处错误变量（`lerr` / `rmErr`）在失败分支里都没有被记录或上报。

---

### BUG-07 DeleteSite 无事务、无补偿 —— DB 已删而配置可能残留，站点从此不可管理
- **严重级**：P2
- **位置**：`internal/service/service.go:599-622`
- **现象**：删除流程是"先删 DB 记录 → 删备份目录 → 再下发配置"：
  1. `internal/service/service.go:616` `s.db.DeleteSite(id)` —— **站点记录立即消失**
  2. `internal/service/service.go:620` `_ = os.RemoveAll(filepath.Join(s.backupRoot, id))` —— 错误被 `_ =` 丢弃
  3. `internal/service/service.go:621` `return s.ApplyAllConfig(ctx)` —— 这一步才真正删掉 `10-site-<id>.conf`
  若第 3 步失败（写盘失败、`nginx -t` 不过导致回滚、reload 失败），**站点记录已经没了**，而配置文件可能因回滚（`internal/service/service.go:350-374`）被恢复 → **站点继续对外服务，但面板里已经不存在它，用户无法再删一次**。
- **触发条件**：`ApplyAllConfig` 任一环节失败。与 BUG-06 叠加时后果相同，但成因不同。
- **根因**：跨"DB 行"与"磁盘文件"两套存储的删除没有定义顺序与补偿策略。`internal/service/service.go:492-497`（CreateSite）已经采用了"数据先落、配置失败不回滚数据"的策略（并写明理由），但删除侧没有对称的说明与补偿。
- **影响面**：站点可管理性（"删不掉"）、对外服务残留。备份目录删除失败还会留下孤儿备份（`internal/store/backup_store.go` 里 `backups` 表的记录也会成为孤儿）。
- **修复方案**：调整为"先删配置并确认 reload 成功 → 再删 DB 记录"；或保留一条 `pending_delete` 状态，由后台重试清理。
- **证据**：
```go
// internal/service/service.go:616-622
if err := s.db.DeleteSite(id); err != nil {
    return err
}
// 同步清理防篡改基线与备份
_ = os.RemoveAll(filepath.Join(s.backupRoot, id))   // ← 错误被丢弃
return s.ApplyAllConfig(ctx)                        // ← 失败时记录已没了，配置可能还在
```

---

### BUG-08 RestoreConfigSnapshot 不持 reloadMu，且 WriteFileAtomic 用固定临时文件名 —— 并发写可撕裂配置
- **严重级**：P2
- **位置**：`internal/service/ops.go:174-208`（未加锁）、`internal/service/ops.go:243-264`（restoreConfFiles）、`internal/driver/driver.go:139-150`（WriteFileAtomic）
- **现象**：
  1. `ApplyAllConfig` 全程持 `s.reloadMu`（`internal/service/service.go:120-121`），但 `RestoreConfigSnapshot`（`internal/service/ops.go:174`）**完全不持这把锁**，可以与正在进行的配置下发并行操作同一批 `.conf` 文件。
  2. `restoreConfFiles` 会**删除"备份时不存在、当前存在"的 .conf 文件**（`internal/service/ops.go:255-258`）。若此刻 `ApplyAllConfig` 刚写入了新站点配置，该文件会被误删 → **新站点配置丢失，nginx 里没有它**。
  3. `driver.WriteFileAtomic` 的临时文件名是固定的 `path + ".tmp"`（`internal/driver/driver.go:141`），**无唯一性、无锁**。两个 goroutine 写同一路径时，一个的 rename 会把另一个的半截内容顶上去 → 配置内容被撕裂。
- **触发条件**：配置下发进行中同时点击"回滚到历史快照"。面板是 HTTP 服务，两者可并发。
- **根因**：锁只保护了一条路径（`ApplyAllConfig`），另一条同样写 conf 目录的路径（快照回滚）没纳入同一把锁。
- **影响面**：配置丢失 / 配置内容错乱；nginx -t 可能失败进而触发 `ApplyAllConfig` 的全局回滚（`internal/service/service.go:248-252`），影响面放大到所有站点。
- **修复方案**：`RestoreConfigSnapshot` 与 `DiffConfigSnapshot`/`SnapshotConfig` 一并纳入 `reloadMu`；`WriteFileAtomic` 改用 `os.CreateTemp` 生成唯一临时名并加目录级互斥。
- **证据**：
```go
// internal/driver/driver.go:139-149
func WriteFileAtomic(path, content string, perm os.FileMode) error {
    tmp := path + ".tmp"          // ← 固定名，并发写互相覆盖
    if err := os.WriteFile(tmp, []byte(content), perm); err != nil {
        return err
    }
    if err := os.Rename(tmp, path); err != nil {
        os.Remove(tmp)
        return err
    }
    return nil
}
// internal/service/ops.go:255-258（无锁执行，可能误删并发写入的新配置）
for name := range cur {
    if _, ok := prev[name]; !ok {
        _ = os.Remove(filepath.Join(confDir, name))
    }
}
```

---

### BUG-09 traffic_stats 缺 site_id 索引 —— 每站点一次全表扫描，ApplyAllConfig 触发 N 次
- **严重级**：P2
- **位置**：`internal/store/store.go:103-114`（建表，主键 `(date, site_id)`）、`internal/store/store.go:620-639`（GetTrafficStats）、`internal/store/store.go:642-657`（GetTrafficSummary）
- **现象**：`traffic_stats` 的主键是 `(date, site_id)`，**date 是前导列**，因此 `WHERE site_id=?`（`internal/store/store.go:623` 与 `650`）用不上主键索引 → 每次都全表扫描。表上没有任何其它索引。
- **触发条件 / 放大路径**：
  - `internal/service/service.go:403` — `renderOptions` 对**每个**站点调一次 `GetTrafficSummary` → `ApplyAllConfig` 每次下发触发 N 次全表扫描。
  - `internal/service/service.go:735` + `internal/service/service.go:744` — `SiteSummaries` 对每个站点各调一次 `GetTrafficSummary` 与 `CountTamperEvents` → 站点列表接口是 **2N+1** 次查询（N+1）。
- **根因**：建表时只声明了复合主键，没有为高频过滤列 `site_id` 补索引。
- **影响面**：站点数与流量表规模增长后，每次配置下发与每次列表刷新都会退化为 O(表大小 × 站点数)。叠加 `internal/store/store.go:33` 的 `SetMaxOpenConns(1)`（全库单连接），所有查询串行，慢查询会直接顶高接口延迟。
- **修复方案**：`CREATE INDEX IF NOT EXISTS idx_traffic_site ON traffic_stats(site_id)`；`GetTrafficSummary` 的月度聚合可改为范围条件 `date >= ? AND date < ?` 以便走索引；`SiteSummaries` 改为一次性 `GROUP BY site_id` 聚合查询，消除 N+1。
- **证据**：
```sql
-- internal/store/store.go:103-114
CREATE TABLE IF NOT EXISTS traffic_stats (
    date       TEXT NOT NULL,
    site_id    TEXT NOT NULL,
    ...
    PRIMARY KEY (date, site_id)     -- ← site_id 不是前导列
);
-- 无 CREATE INDEX idx_traffic_site
-- internal/store/store.go:650
FROM traffic_stats WHERE site_id=?    -- ← 全表扫描
```

---

### BUG-10 hostdriver.Reload 失败时降级为 systemctl restart —— 可能让 nginx 整体下线
- **严重级**：P2
- **位置**：`internal/driver/hostdriver/host.go:174-194`
- **现象**：`Reload` 先试 `systemctl reload nginx`；失败则**直接试 `systemctl restart nginx`**（`internal/driver/hostdriver/host.go:182`）。restart 与 reload 的失败语义完全不同：reload 失败 nginx 仍以旧配置继续服务；restart 失败则 **nginx 进程不存在，全部站点同时不可用**。
  另外，`nginx -s reload` 的兜底分支（`internal/driver/hostdriver/host.go:189`）只在 `exec.LookPath("systemctl")` **失败**时才可达——只要 systemctl 存在但 unit 名不匹配（例如 nginx 由其他 supervisor 托管、`UnitName` 配错），就**永远不会**退到信号重载，直接返回错误。
- **触发条件**：`systemctl reload` 因故失败（nginx -t 与 reload 之间配置被改、unit 名不对、systemd 繁忙）。
- **根因**：把"重载失败"和"重启失败"当成同一类失败做降级，没有区分"失败是否会导致服务中断"。
- **影响面**：宿主机模式下单次配置下发的失败可能升级为**全站中断**。这一条与 Docker 驱动的行为差异见第二节（Docker 模式下 reload 失败只保留旧配置，无中断风险）。
- **修复方案**：reload 失败时**先做 `nginx -t`** 确认配置可用，再决定是否 restart；restart 前记录明确的错误日志；把信号重载兜底提到 systemctl 分支之外，作为最后一级降级。
- **证据**：
```go
// internal/driver/hostdriver/host.go:174-193
func (d *Driver) Reload(ctx context.Context) error {
    if _, err := exec.LookPath("systemctl"); err == nil {
        _, out, err := d.run(ctx, "systemctl", "reload", d.unitName)
        if err == nil {
            return nil
        }
        // reload 不支持则退化为 restart
        _, _, rerr := d.run(ctx, "systemctl", "restart", d.unitName)   // ← 失败即全站中断
        if rerr == nil {
            return nil
        }
        return fmt.Errorf("systemctl reload 失败: %s / %s", out, err)
    }
    // 退化为信号重载（仅当 systemctl 不存在时可达）
    _, errOut, err := d.run(ctx, d.nginxBin, append(d.nginxPrefixArgs(), "-s", "reload")...)
```

---

### BUG-11 篡改事件写入错误被吞 —— 安全事件可静默丢失
- **严重级**：P2
- **位置**：`internal/service/service.go:821`、`internal/service/service.go:827`、`internal/service/service.go:832`
- **现象**：`CheckTamper` 检测到文件被篡改后写事件表，三处写库错误全部用 `_ =` 丢弃。写库失败时**面板既不报错也不记录日志**，检测结果直接返回给调用方，看起来像"检测成功"。
- **触发条件**：DB 写失败（磁盘满、SQLite busy 超时）。
- **根因**：安全审计写入被当作"尽力而为"。
- **影响面**：防篡改是面板的安全功能（`internal/service/service.go:839-848` 还会据此触发告警）。事件丢失 = **审计链断裂且无痕迹**，事后无法回答"文件是否被改过"。
- **修复方案**：写失败时至少 `log.Printf` 记录，并返回聚合错误（让调用方知道审计不完整）；批量写可放进一个事务。
- **证据**：
```go
// internal/service/service.go:820-836
for _, p := range diff.Modified {
    _ = s.db.SaveTamperEvent(&store.TamperEvent{   // ← 错误被丢弃
        ID: store.NewID(), SiteID: site.ID, Path: p,
        OldHash: baseline[p].Hash, Kind: "modified", CreatedAt: now,
    })
}
```

---

### BUG-12 SaveSMTPConfig 用结构体整体赋值替换共享 mailer —— 数据竞争
- **严重级**：P2
- **位置**：`internal/service/account.go:412-414`
- **现象**：`s.mailer` 是**共享指针**（`internal/service/service.go:32` 字段、构造函数 `internal/service/service.go:68`），`SaveSMTPConfig` 用 `*s.mailer = *mailer.New(...)` 对这个共享对象做**多字段结构体赋值**。而同一实例正被其它 goroutine 读取：`s.mailer.SendSensitiveSafe(...)`（`internal/service/account.go:161`）、`s.mailer.Send(...)`（`internal/service/account.go:438`）、`m.cfg.Host` / `m.cfg.Port`（`internal/mailer/smtp.go:93`）。
- **触发条件**：保存 SMTP 配置的同时有验证码邮件或测试邮件在发送。
- **根因**：`main.go:161` 的注释写的是"mailer.Config 不可变，重新构造"——**意图是换一个新对象**，但代码却是**原地改写共享对象**（`main.go:148-165` 走的是"新建变量再赋值"路径，是对的；service 层这里写反了）。
- **影响面**：Go 内存模型下的数据竞争，`-race` 下必报；线上表现为偶发的 SMTP 配置撕裂（Host 是新的、Port 是旧的），表现为"配好了但发不出信"，且**极难复现**。
- **修复方案**：改为原子换指针——把 `mailer` 字段换成 `atomic.Pointer[mailer.Mailer]`，或给 `Mailer` 内部加 `atomic.Value` 存 config。最小改动是给 `Service.mailer` 加一层 `sync.RWMutex` 保护整体赋值与读取。
- **证据**：
```go
// internal/service/account.go:411-414
// 同步到运行时 mailer 实例
if s.mailer != nil {
    *s.mailer = *mailer.New(smtpConfigToMailer(c))   // ← 原地改写共享对象，非原子
}
```

---

### BUG-13 CreateSite 中 site.ID 被赋值两次，457-459 为死代码
- **严重级**：P3
- **位置**：`internal/service/service.go:457-463`
- **现象**：`if site.ID == "" { site.ID = store.NewID() }` 之后第 463 行无条件 `site.ID = store.NewID()`，前者的结果立刻被覆盖。
- **根因**：463 行是后加的"客户端 ID 一律不采用"加固（注释见 460-462），但没有删掉前面的分支。
- **影响面**：无功能影响，纯冗余；但会让读者误以为客户端 ID 在某些路径下会被采用。
- **修复方案**：删除 457-459 两行。
- **证据**：
```go
// internal/service/service.go:457-463
if site.ID == "" {
    site.ID = store.NewID()
}
// 客户端传来的 ID 一律不采用：ID 参与构成配置文件名与备份路径，
// 任由请求体提供等于开放任意文件写（见 model.ValidateID 注释）。
// 此处恒重新生成，确保落库的 ID 一定是服务端产生的十六进制串。
site.ID = store.NewID()
```

---

### BUG-14 回滚过程中的错误被完全忽略 —— 回滚失败会留下坏配置
- **严重级**：P3
- **位置**：`internal/service/service.go:350-374`
- **现象**：`rollback` 里全部写回/删除操作都是 `_ = s.drv.WriteConf(...)` / `_ = s.drv.RemoveSiteConf(...)`，**一个错误都没检查，也没有任何日志**。
- **触发条件**：回滚期间磁盘满、权限变更、目录被删。
- **根因**：回滚被当作"尽力而为的补偿"，但它是保证"不留下 nginx 读不了的配置"的最后一道防线——`internal/service/service.go:170-172` 的注释正是这么描述它的。
- **影响面**：回滚失败 → 磁盘上残留新旧混合的坏配置 → 之后**任何**外部触发的 nginx reload 都会失败（直到下次面板成功下发）。
- **修复方案**：收集回滚错误并 `log.Printf` 至少记录；严重时把面板置为"配置降级"状态并拒绝后续下发。
- **证据**：
```go
// internal/service/service.go:350-374（节选）
for _, b := range backups {
    if b.siteID == httpConfKey {
        if b.existed {
            _ = s.drv.WriteConf(ctx, HTTPConfFile, b.content)   // ← 错误被丢弃
        } else {
            _ = s.drv.RemoveConf(ctx, HTTPConfFile)
        }
        continue
    }
    ...
}
```

---

### BUG-15 ApplyAllConfig 在 Reload 失败时不回滚（与函数头注释矛盾）
- **严重级**：P3
- **位置**：`internal/service/service.go:117-118`（注释）、`internal/service/service.go:254-256`（实现）
- **现象**：函数注释写"写配置 → nginx -t → reload，失败则回滚"，但 `Reload` 失败时直接 `return fmt.Errorf(...)`，**没有调用 `s.rollback`**。
- **根因**：reload 失败与 nginx -t 失败被区别对待了（前者配置已验证有效，留在盘上是"下次 reload 会生效"，后者必须回滚）。
- **影响面**：严格说当前行为是可辩护的（配置已通过 `nginx -t`，留盘无害）。但注释与实现不一致，且宿主机驱动下 `Reload` 内部会退化为 `systemctl restart`（见 BUG-10），"配置有效"这个前提并不总是成立。
- **修复方案**：要么补齐注释说明"reload 失败不回滚"是有意为之，要么在 reload 失败时也回滚。
- **证据**：
```go
// internal/service/service.go:117-118（注释）
// reloadMu 保证串行执行：写配置 → nginx -t → reload，失败则回滚。
// internal/service/service.go:254-256（实现）
if err := s.drv.Reload(ctx); err != nil {
    return fmt.Errorf("重载 nginx 失败: %w", err)   // ← 没有 s.rollback(...)
}
```

---

### BUG-16 store.migrate 靠匹配错误字符串判断 "duplicate column"
- **严重级**：P3
- **位置**：`internal/store/store.go:263`、`internal/store/store.go:276`、`internal/store/store.go:284`
- **现象**：三段 `ALTER TABLE … ADD COLUMN` 兼容逻辑用 `strings.Contains(err.Error(), "duplicate column")` 判断"列已存在"，其它错误则直接让 `migrate()` 失败 → `store.Open` 失败 → **面板启动即退出**。
- **根因**：SQLite 的错误文案是实现细节，随版本/构建可能变化（modernc.org/sqlite 是纯 Go 实现，与 C 版 SQLite 的文案不保证一致）。
- **影响面**：一次 SQLite 依赖升级就可能让所有存量库无法启动。当前 `internal/store/store.go:50-59` 的 `sites` 建表语句**不含 `edate`**，而 `SaveSite` 的 INSERT 用到了 `edate`（`internal/store/store.go:311`）——`edate` 完全依赖那段 ALTER 补齐，**这条 ALTER 一旦失效，整个站点表不可用**。
- **修复方案**：改用"查询 `PRAGMA table_info(sites)` 判断列是否存在"来决定是否 ALTER；或把 `edate` 直接补进 CREATE TABLE（`IF NOT EXISTS` 对新库生效，存量库再走 ALTER 兜底）。
- **证据**：
```go
// internal/store/store.go:280-287
for _, stmt := range []string{
    `ALTER TABLE sites ADD COLUMN edate TIMESTAMP`,
} {
    if _, err := d.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
        return err
    }
}
// 注意：internal/store/store.go:50-59 的 CREATE TABLE sites 不含 edate 列，
//       而 internal/store/store.go:311 的 INSERT 使用了 edate。
```

---

### BUG-17 sessions 表无 username 索引 —— 改密踢会话走全表扫描
- **严重级**：P3
- **位置**：`internal/store/store.go:128-133`（建表）、`internal/store/store.go:915-924`（DeleteUserSessionsExcept）
- **现象**：`sessions` 只有 `token` 主键，`username` 无索引。`DELETE FROM sessions WHERE username=? AND token<>?` 只能全表扫描。`CleanSessions`（`internal/store/store.go:927-932`）同理走 `expires_at` 全表扫描。
- **影响面**：会话表随登录次数单调增长（只有过期清理会收缩），长期运行后面板重启时清理会变慢。单次影响有限，故列 P3。
- **修复方案**：`CREATE INDEX idx_sessions_username ON sessions(username)` 与 `idx_sessions_expires ON sessions(expires_at)`。
- **证据**：
```sql
-- internal/store/store.go:128-133
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    username   TEXT NOT NULL,
    expires_at TIMESTAMP,
    created_at TIMESTAMP
);
-- 无 username / expires_at 索引
```

---

### BUG-18 ListAuditLogs 的 `action LIKE '前缀%'` 用不上已有索引
- **严重级**：P3
- **位置**：`internal/store/ops_store.go:160-161`、`internal/store/store.go:250`
- **现象**：`WHERE action LIKE ?`（参数为 `action+"%"`）。SQLite 的 LIKE 前缀优化要求列索引声明为 `COLLATE NOCASE`，而 `idx_audit_action ON audit_logs(action)` 是默认 BINARY 排序规则 → 索引不被使用，**退化为全表扫描**。
- **根因**：索引列的排序规则与查询谓词不匹配。
- **影响面**：审计表会持续增长（`internal/store/ops_store.go:193-215` 有 trim 但保留数较大），按动作筛选时扫描全表。
- **修复方案**：把索引改为 `ON audit_logs(action COLLATE NOCASE)`；或把谓词改成 `action >= ? AND action < ?` 范围形式以走 BINARY 索引。
- **证据**：
```go
// internal/store/ops_store.go:159-162
if action != "" {
    q += ` WHERE action LIKE ?`
    args = append(args, action+"%")   // ← LIKE + BINARY 索引 → 不走索引
}
```

---

### BUG-19 scanBackup 中 createdAt 字符串分支为死代码
- **严重级**：P3
- **位置**：`internal/store/backup_store.go:106`、`internal/store/backup_store.go:121-125`
- **现象**：`var createdAt string` 声明后从未被赋值，`else if createdAt != ""` 恒为 false。该分支的意图是兼容 `created_at` 以字符串形式存储的旧数据，但 Scan 的目标是 `sql.NullTime`，根本拿不到字符串形式。
- **影响面**：无功能影响（该分支不可达），但说明"兼容旧格式"的意图没有真正实现。
- **修复方案**：删除死分支；若确需兼容字符串时间，用 `sql.NullString` 单独接收后 `time.Parse`。
- **证据**：
```go
// internal/store/backup_store.go:106, 118-125
var createdAt string                     // ← 从未赋值
...
if created.Valid {
    b.CreatedAt = created.Time
} else if createdAt != "" {              // ← 恒 false
    if t, perr := time.Parse(time.RFC3339, createdAt); perr == nil {
        b.CreatedAt = t
    }
}
```

---

### BUG-20 dockerdriver.SystemStatus 吞掉 PHPVersions 错误，且从不填充 Disk
- **严重级**：P3
- **位置**：`internal/driver/dockerdriver/docker.go:269`（吞错）、`internal/driver/dockerdriver/docker.go:262-273`（不填 Disk）
- **现象**：`php, _ := d.PHPVersions(ctx)` 丢弃错误；`st.Disk` 在整个 `SystemStatus` 中从未被赋值。
- **根因**：与 hostdriver 的实现不对称——hostdriver 在同样位置是 `return st, err`（`internal/driver/hostdriver/host.go:299-302`）并设置 `st.Disk = diskUsage(d.dataDir)`（`internal/driver/hostdriver/host.go:304`）。
- **影响面**：Docker 模式下系统总览的磁盘卡片恒为空；PHP 探测失败时静默显示"无 PHP 版本"，用户无从判断是未安装还是探测出错。
- **修复方案**：与 hostdriver 对齐：返回错误、补 `st.Disk`。
- **证据**：
```go
// internal/driver/dockerdriver/docker.go:269-271
php, _ := d.PHPVersions(ctx)     // ← 错误被丢弃
st.PHP = php
st.Docker = d.dockerInfo(ctx)
// 全函数无 st.Disk = … 赋值（对比 internal/driver/hostdriver/host.go:304）
```

---

### BUG-21 GetCert / GetACMEAccount 全表加载后线性查找
- **严重级**：P3
- **位置**：`internal/store/store.go:453-464`、`internal/store/store.go:512-523`
- **现象**：`GetCert(id)` 调 `ListCerts()` 拉取**全部**证书后在 Go 里循环比对 ID；`GetACMEAccount(id)` 同理。两条路径都有现成的按主键查询能力（`certs.id` / `acme_accounts.id` 都是 PRIMARY KEY）。
- **影响面**：证书数量大时每次取单个证书都要搬运全表（含 `dns_config` JSON 反序列化）。`internal/store/store.go:444-446` 每次 ListCerts 还要解析一遍 DNS 凭据 JSON。
- **修复方案**：改成 `SELECT … WHERE id=?` 单行查询。
- **证据**：
```go
// internal/store/store.go:453-464
func (d *DB) GetCert(id string) (*model.Cert, error) {
    certs, err := d.ListCerts()      // ← 全表
    if err != nil {
        return nil, err
    }
    for _, c := range certs {
        if c.ID == id {
            return c, nil
        }
    }
    return nil, model.ErrNotFound
}
```

---

## 二、两套驱动的行为差异

> 前提：两套驱动实现同一个 `driver.Driver` 接口（`internal/driver/driver.go:96-137`），业务层（`internal/service/service.go`）**完全不知道**当前跑的是哪套。下表逐项列出同一站点配置在两套驱动下的行为差异。

| # | 维度 | hostdriver | dockerdriver | 差异后果 |
|---|---|---|---|---|
| 1 | **PHP-FPM socket 路径** | 返回磁盘上**真实存在**的 socket：`/run/php/php8.1-fpm.sock`（`host.go:284-287`，`os.Stat` 校验过） | 用字符串拼装：`/run/php/php-%s-fpm.sock`（`docker.go:235`），且 `TrimPrefix(ver,"7")` 把 `"74"` 削成 `"4"` → `/run/php/php-4-fpm.sock` | **同一个 PHP 版本在两套驱动下生成的 `fastcgi_pass` 指向不同路径**；Docker 模式下 PHP 站全 502（见 BUG-03）。这是本次审计中最严重的一致性缺陷 |
| 2 | **PHP 版本号格式** | `"8.1"`（正则 `([0-9]+\.[0-9]+)`，`host.go:258`） | `"81"` / `"74"` / `"default"`（`docker.go:228-232`） | 界面下拉展示不同；更严重的是 `model.isVersionLike`（`model/validate.go:660-664`）要求至少两段点分版本 → `"81"` **不合法**。该校验当前因 BUG-04 未被调用而未暴露，一旦接入就会大面积报错 |
| 3 | **socket 文件名正则** | `php-?([0-9]+\.[0-9]+)-?fpm\.sock$`（`host.go:258`）——同时接受 `php8.1-fpm.sock`（Debian）与 `php-8.1-fpm.sock` | `php-([0-9]+\.[0-9]+)-fpm\.sock$`（`docker.go:217`）——**只**接受带连字符版本号的形式 | Docker 兜底探测（`docker.go:239-250`）在 Debian/Ubuntu 常见命名下**一个都匹配不到**，PHP 版本列表恒为空 |
| 4 | **Reload 失败语义** | `systemctl reload` → 失败退化为 `systemctl restart`（`host.go:174-187`） | `nginx -s reload`，失败仅返回错误（`docker.go:153-158`） | 宿主机模式下 reload 失败可能升级为 **nginx 进程消失、全站中断**；Docker 模式只是保留旧配置。同一份站点配置在两种部署下的故障爆炸半径完全不同（见 BUG-10） |
| 5 | **重载信号兜底可达性** | `nginx -s reload` 兜底仅在 `systemctl` **不存在**时可达（`host.go:176` 的 if 守卫） | 不适用（容器内恒用 `nginx -s reload`） | 宿主机上若 nginx 非 systemd 托管（unit 名不匹配）→ 重载**永远失败**且没有兜底 |
| 6 | **系统总览的磁盘信息** | `st.Disk = diskUsage(d.dataDir)`（`host.go:304`，Linux 下 `statfs`） | 从不赋值（`docker.go:262-273`） | Docker 模式下磁盘卡片恒为空（见 BUG-20） |
| 7 | **PHP 探测错误处理** | `SystemStatus` 里 `PHPVersions` 出错则 `return st, err`（`host.go:299-302`） | `php, _ := d.PHPVersions(ctx)` 吞错（`docker.go:269`） | Docker 模式下探测失败与"没有 PHP"不可区分 |
| 8 | **上下文取消** | 所有命令都用传入的 ctx（`host.go:84-99`） | `containerExists()` 用 `context.Background()`（`docker.go:101`），**丢弃调用方 ctx** | Docker 模式下状态探测无法被上层取消，超时只能等内部 30s |
| 9 | **配置写入的可见性校验** | 写进 `confDir` 后由 nginx 自行 include（`host.go:200-220`） | 写进宿主机挂载目录 `HostConfDir`/`HostSiteConfDir`（`docker.go:161-181`），**不校验该目录确实挂载进了容器** | 若容器未挂载该卷（或用 `-v` 挂错路径），面板**静默成功**但 nginx 从未见过这些配置——站点建好却不生效，且无任何错误提示 |
| 10 | **容器存在性判断** | 无对应概念（nginx 在宿主机上） | `docker ps -a --filter name=^nginx$`（`docker.go:100-107`），匹配不到即报"容器不存在" | Docker 模式下容器名配错（如 nginx 实例名为 `nginx-1`）时，所有配置操作全部失败，但 `main.go:63` 的默认值是 `nginx` |
| 11 | **`ContainerConfDir` 配置项** | 无对应项 | 字段存在（`docker.go:28`）并被赋值（`docker.go:54`）、有默认值（`docker.go:61-63`），但**全文件从未被读取** | 死配置项：`main.go:124` 传入的 `/etc/nginx/conf.d` 毫无作用，容易误导运维以为改它能影响容器内路径 |
| 12 | **配置文件名/目录布局** | `ConfDir` 与 `SiteConfDir` 可分离（`host.go:76-77`） | `HostConfDir` 与 `HostSiteConfDir` 同样可分离（`docker.go:74-75`） | 此项**一致**。但两套驱动共用的 `ReadConf` 语义（读 ConfDir）导致 service 层读站点配置时必须绕过驱动（`service.go:147-148`），从而产生 BUG-05 |
| 13 | **命令参数化** | 一致：均用 `exec.CommandContext` 直接传 argv（`host.go:87`、`docker.go:86`），**没有** `sh -c` 拼接 | 同左；仅兜底 PHP 探测用了一次 `sh -c`（`docker.go:241`，参数为常量，无注入面） | 无差异。**未发现 shell 注入面** |
| 14 | **用户身份** | 一致：均以面板进程身份（本项目以 root 运行）执行；无 `SysProcAttr.Credential` 设置 | 同左 | 无差异。`docker exec` 默认以容器内 root 执行 |
| 15 | **环境变量透传** | 一致：均不设置 `cmd.Env`，继承父进程环境 | 同左 | 无差异。**未发现环境变量泄露或跨站串扰** |
| 16 | **容器/进程清理** | 无长驻子进程（nginx 由 systemd 托管），无孤儿进程风险 | 每次 `docker exec` 都是一次性进程，**无孤儿容器风险** | 无差异。两套驱动在"失败/超时不留残留"这一点上都是干净的（`exec.CommandContext` 会 kill 超时子进程） |

**驱动层总体判断**：命令执行与资源清理是干净的（参数化、无 `sh -c` 拼接、无孤儿进程/容器、超时有 context kill）；**不一致集中在"状态探测"侧**（PHP 版本/socket 的发现与上报），其中第 1、3 条是功能性缺陷，第 2 条会在 BUG-04 修复后立刻变成阻断性缺陷。

---

## 三、功能残缺与简陋项

1. **driver.Driver 接口缺少 `ReadSiteConf`（`internal/driver/driver.go:126-127`）** —— 只有读 ConfDir 的 `ReadConf`。service 层因此被迫用 `os.ReadFile(filepath.Join(s.drv.Paths().SiteConfDir, …))` 绕过抽象直接读盘（`internal/service/service.go:147-148`），并由此产生 BUG-05 的双入口不一致。建议补齐接口方法。
2. **`driver.WriteFileAtomic` 的临时文件名固定（`internal/driver/driver.go:141`）** —— 应改用 `os.CreateTemp` 生成唯一名，否则并发写同一路径会互相覆盖（见 BUG-08）。当前靠 `reloadMu` 侥幸不出事，但该锁并未覆盖全部写入路径。
3. **配置快照回滚绕过驱动（`internal/service/ops.go:185`、`internal/service/ops.go:243-264`）** —— `confhistory.Restore` 与 `restoreConfFiles` 直接对目录做文件 IO，不走 `driver.WriteSiteConf`，也**不是原子写**（`os.WriteFile` 直写目标文件）。进程在写入中途被杀会留下半截的 nginx 配置。
4. **配置历史快照只存站点配置目录（`internal/service/ops.go:65`、`internal/service/ops.go:176`）** —— `Paths().SiteConfDir` 之外还有 `Paths().ConfDir`（http 级配置 `00-zhiweipanel-http.conf`、catch-all `01-zhiweipanel-default-server.conf`）**没有被快照**。回滚到旧快照不会恢复这两份文件，"回到已知可用的一版"这个目标并不完整。
5. **`service.ZipDir` 是空实现（`internal/service/files.go:352-355`）** —— 直接 `return errors.New("暂未实现")`，而 `DeleteFile` 的注释（`internal/service/files.go:273-274`）却写"递归删除请用 Zip-and-remove 工具，当前不暴露"——文档描述了一个并不存在的能力。
6. **`store.DB` 单连接（`internal/store/store.go:33-34` `SetMaxOpenConns(1)` + `MaxIdleConns(1)`）** —— 读操作（`ListSites` / `ListCerts` 等全表遍历 + JSON 反序列化）会长时间占住唯一的连接，写操作必须等读结束。叠加 BUG-09 的 N+1 后，列表接口延迟随站点数线性上升。
7. **`internal/store/store.go` 中 `d.mu` 只保护写、不保护读** —— 读走 `d.db.Query` 无锁。SQLite 单连接下不会损坏数据，但"读-改-写"序列（如 `GetSite` → 改 → `SaveSite`）在 service 层并非原子，整体靠 service 层串行化。
8. **域名单独存放为逗号拼接字符串（`internal/store/store.go:55` 的 `domains TEXT`，写入见 `internal/store/store.go:317`）** —— 无法建唯一索引，所以域名冲突检查只能在 Go 里遍历全部站点做（`internal/service/service.go:443-455`、`internal/service/service.go:555-570`），且**该检查与 `SaveSite` 之间没有原子性**（TOCTOU，见第四节第 3 条）。
9. **`ListSites` 每次全量反序列化 config blob（`internal/store/store.go:346-368`）** —— `ApplyAllConfig` 每跑一次就要把全部站点的完整 JSON 解一遍。站点多时是可观的开销。
10. **`internal/driver/hostdriver/host.go:143-145` 的 `pidFile()` 硬编码 `/run/nginx.pid`** —— 与发行版无关，但 nginx 编译时可用 `-pid` 改路径；自编译 nginx 的机器上这个回退判断会失效。
11. **`GetTrafficSummary` 的月度聚合用 `date LIKE 'YYYY-MM%'`（`internal/store/store.go:649-650`）** —— 参数化安全，但 LIKE 无法走索引，且 `date` 列存的是 `YYYY-MM-DD`，语义上应当是范围查询。

---

## 四、代码质量

1. **注释与实现严重脱节的地方（至少 5 处）**，且都是"注释描述了一个更安全的行为、实现并没有"：
   - `internal/model/validate.go:33-39` 声称写入路径调用 `ValidateSite` —— 实际零调用（BUG-04）。
   - `internal/service/service.go:480-482` 声称"site.Mysql 落库后再做实际建库" —— 实际顺序相反（BUG-02）。
   - `internal/service/service.go:117-118` 声称 reload 失败也回滚 —— 实现不回滚（BUG-15）。
   - `internal/service/service.go:578-582` 正确识别了 PUT 覆盖问题的类别 —— 但保留清单漏了 3 个同类字段（BUG-01）。
   - `internal/service/service.go:619-620` 注释说"同步清理防篡改基线与备份"，实际只删了 `backupRoot/id`，**防篡改基线（`integrity` 表，`internal/store/store.go:150-160`）没有任何清理动作** —— 删站后该站点的完整性基线永久残留在库里（`internal/store/store.go:946` 的 `DELETE FROM integrity WHERE site_id=?` 只在 `ReplaceIntegrity` 时发生）。**这是本节唯一的实质数据残留问题**，建议一并清理。
2. **错误处理风格不统一** —— 同一层里既有 `_ =` 吞错（`internal/service/service.go:620`、`internal/service/service.go:821/827/832`）、又有 `lerr == nil` 条件吞错（`internal/service/service.go:224/236`）、又有"记日志后继续"（`internal/service/service.go:612-614`、`internal/service/ops.go:68`）。**没有任何一处区分"可忽略"与"不可忽略"**，读者无法判断哪处吞错是设计、哪处是遗漏。
3. **service 层与 store 层的职责边界有渗透** —— `internal/service/service.go:820-836` 直接构造 `store.TamperEvent` 并逐条写入；`internal/service/service.go:789-792` 直接构造 `store.FileHash`。store 已经提供了 `ReplaceIntegrity` 这样的批量接口，却没有一个"批量写篡改事件"的接口，导致 service 层只能循环单写、且每次的错误只能逐条吞。
4. **测试替身掩盖了真实 bug** —— `internal/service/apply_test.go:36-38` 的 fakeDriver 把 `ConfDir` 与 `SiteConfDir` 设成同一个目录，使得 BUG-05 在测试中**不可能被发现**。这类"为了让测试好写而让替身与真实实现同形"的做法，会让测试失去发现目录布局类缺陷的能力。
5. **命名与文档一致性好，值得肯定** —— `SiteConfPrefix`（`internal/driver/driver.go:89`）、`httpConfKey`/`defaultServerKey` 占位 ID（`internal/service/service.go:325/329`）、`commentOut`（`internal/service/service.go:312`）等都能看出设计上的用心；`internal/store/store.go:339-344` 关于"坏 config 不能静默跳过"的注释及实现是本次审计中质量最高的一段。
6. **未发现缺陷的模块**：
   - **`internal/service/files.go`（410 行）** —— 路径逃逸防护完整：拒绝 `..` 段（`files.go:95-99`）、`EvalSymlinks` 后校验前缀（`files.go:105-131`）、`pathInRoot` 用分隔符拼接避免前缀误判（`files.go:134-140`）、`protectedRel` 用 `path` 而非 `filepath` 避免 Windows 分隔符绕过（`files.go:147-155`）、zip-slip 与 symlink entry 双重检查（`files.go:423-453`）、解压三重上限（`files.go:358-362`）。**未发现缺陷**。
   - **`internal/service/account.go` 的验证码与异常登录逻辑** —— bcrypt 最小成本用于验证码、crypto/rand 生成码（`account.go:232-239`）、challenge 一次性消费与尝试次数上限（`account.go:88-95`）、改密后踢全部会话。**未发现缺陷**（仅 BUG-12 涉及其中对 mailer 的并发使用）。
   - **`internal/store` 的 SQL 注入面** —— 全部查询均使用 `?` 占位参数化，**未发现任何字符串拼接 SQL**；`LIKE` 的通配符也是走参数（`internal/store/ops_store.go:161`）。**未发现 SQL 注入**。
   - **`ListAuditLogs` 的 Scan 完整性** —— 历史遗留的"漏取字段"缺陷**已修复**：`SELECT` 的 12 列与 `Scan` 的 12 个目标一一对应（`internal/store/ops_store.go:157` / `internal/store/ops_store.go:176-178`），`model.AuditLog`（`internal/model/ops.go:67-86`）的 12 个字段全部落库且全部读回，`ip` / `ua` / `path` 均未遗漏。**逐条核对了 store 中全部 11 处带 Scan 的查询，SELECT 列与 Scan 目标数量、顺序全部一致，未发现类似遗漏**。
   - **`internal/store/store.go` 的 `migrate()` 幂等性** —— `CREATE TABLE/INDEX IF NOT EXISTS` + 逐条忽略 "duplicate column"，重复执行不产生副作用（除 BUG-16 的文案匹配脆弱性外）。
   - **`internal/driver` 的命令执行安全性** —— 见第二节第 13-16 行。

---

## 附：未能判定的点

- **`internal/service/ops.go` 的 `confhistory.Snapshot/Restore/Prune` 实现**（在 `internal/confhistory/`）不在本次指定范围内，未读，因此**"快照是否包含 http 级配置"**（第三节第 4 条）只能从 `internal/service/ops.go:65` 传入的 `Paths().SiteConfDir` 推断，**未直接验证** `confhistory.Snapshot` 是否额外扫描了 `ConfDir`。
- **并发行为的实际表现**（BUG-01 的 TOCTOU、BUG-08 的撕裂写、BUG-12 的数据竞争）均为源码层面的静态推断，未通过 `-race` 运行验证（按任务约束未执行 `go test`）。
- **BUG-04 的实际可利用性**取决于是否存在非管理员调用站点写接口的路径 —— `internal/api/auth.go` 的鉴权逻辑不在本次范围内，未读，因此**未能判定**该接口是否严格限制为管理员。
