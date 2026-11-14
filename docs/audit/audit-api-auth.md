# 审计报告：API 层与鉴权

> 审计范围（逐个读过的文件）：
> `main.go`、
> `internal/api/server.go`、`internal/api/auth.go`、`internal/api/helpers.go`、`internal/api/principal.go`、`internal/api/tokens.go`、`internal/api/misc.go`、`internal/api/files.go`、`internal/api/certs.go`、`internal/api/account.go`、`internal/api/audit.go`、`internal/api/ops.go`、`internal/api/cron.go`、`internal/api/backup.go`、`internal/api/batch.go`、`internal/api/ops_test.go`、
> `internal/model/model.go`、`internal/model/errors.go`、`internal/model/validate.go`、`internal/model/ops.go`、
> `internal/service/service.go`、`internal/service/files.go`、`internal/service/account.go`、`internal/service/ops.go`、
> `internal/store/store.go`、`internal/store/ops_store.go`、
> `internal/mysql/mysql.go`、`internal/backup/backup.go`、`internal/confhistory/confhistory.go`、`internal/logmgr/integrity.go`、`internal/acme/acme.go`、`internal/acme/webroot.go`、`internal/driver/hostdriver/host.go`、`internal/driver/dockerdriver/docker.go`、`internal/cron/handlers.go`、`internal/nginx/validator.go`
>
> 审计时间：2026-10-29
> 方法：纯静态阅读（未运行 `go build` / `go test` / `go vet`，未修改任何源码）

**总体结论**：未发现 P0（无未认证即可利用的接管级漏洞）。共 25 条编号缺陷：BUG 类 12 条（P1×1 / P2×3 / P3×8）、SEC 类 13 条（P1×3 / P2×6 / P3×4），合计 **P1×4 / P2×9 / P3×12**；另有功能残缺 14 条（F-01…F-14）、代码质量 12 条（Q-01…Q-12）。
整体安全基线明显高于同类自托管面板：SQLite 全部参数化、站点文件路径做了 `EvalSymlinks` + 前缀断言 + 受保护子目录三重防护、密码用 bcrypt、错误响应做了最小化、WebSocket 确实有真实鉴权。问题集中在**「写操作的语义判定」与「HTTP 方法判定」脱节**、**站点写入路径缺少服务端字段校验**、**信任客户端提供的 `X-Forwarded-For`** 三处。

---

## 一、BUG 与缺陷

### BUG-01 只读 API Token 可以用 GET 请求执行写操作（scope 判定与写语义脱节）
- **严重级**：P1
- **位置**：`internal/api/auth.go:535`、`internal/api/principal.go:121-127`、`internal/api/certs.go:170-237`、`internal/api/server.go:209-228`、`internal/api/server.go:358-364`、`internal/api/certs.go:349-364`
- **现象**：`authMiddleware` 只对「非 GET/HEAD/OPTIONS」的方法要求 `write` scope（`isReadMethod`）。但多个有副作用的子动作**没有做 HTTP 方法校验**，GET 请求即可触发。
- **触发条件**：持有一个 `scopes:["read"]` 的 API Token（`internal/api/tokens.go:83-94` 创建时可指定只读），发送 GET 请求即可，无需任何会话 cookie。
- **根因**：授权模型把「是否可写」绑定在 HTTP 方法上，把「是否写」绑定在 URL 子路径上，两者不一致；凡是没有写 `if r.Method != http.MethodPost` 的子动作，就自动退化成「只读操作」。
- **影响面**（全部可由只读 Token 触发）：
  | 端点 | 副作用 | 位置 |
  |---|---|---|
  | `GET /api/certs/{id}/delete` | **删除证书记录** | `internal/api/certs.go:228-233` |
  | `GET /api/certs/{id}/revoke` | **吊销证书**（对外不可用） | `internal/api/certs.go:194-199` |
  | `GET /api/certs/{id}/renew` | 触发 ACME 续签 | `internal/api/certs.go:179-192` |
  | `GET /api/certs/{id}/bind` | 改写站点 SSL 绑定 | `internal/api/certs.go:212-226` |
  | `GET /api/sites/{id}/toggle` | **启停站点** | `internal/api/server.go:209-221` |
  | `GET /api/sites/{id}/reload` | 全量下发 nginx 配置 | `internal/api/server.go:223-228` |
  | `GET /api/system/config-apply` | 全量下发 nginx 配置 | `internal/api/server.go:358-364` |
  | `GET /api/alerts/{id}/resolve` | 关闭告警 | `internal/api/certs.go:349-364` |
- **修复方案**：把 scope 判定从「HTTP 方法」改为「路由语义」——在 `server.go:42-45` 的 `reg()` 上增加一个 `needsWrite bool` 参数，列表只读端点用 `readOnly`，其余一律要求 `write`；同时给上表每个子动作补上显式方法校验（参考同文件 `handleBackupByID` `internal/api/backup.go:119-169` 的正确写法）。
- **证据**：
```go
// internal/api/auth.go:534-538
if !isReadMethod(r.Method) && !p.HasScope("write") {
    writeError(w, http.StatusForbidden, errTokenScopeDenied)
    return
}
```
```go
// internal/api/certs.go:228-233 —— case "delete" 内没有任何 r.Method 判断
case "delete":
    if err := s.svc.DB().DeleteCert(id); err != nil {
        writeModelError(w, err)
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"ok": true})
```

---

### BUG-02 变更管理员邮箱 / SMTP 凭据不落审计日志
- **严重级**：P2
- **位置**：`internal/api/server.go:90-91`（注册在主 mux，未经过 `auditMiddleware`）、`internal/api/audit.go:65-76`
- **现象**：审计中间件只包在 `reg()` 注册的 protected 路由上（`internal/api/server.go:44`）。`/api/admin/account/email`（改管理员邮箱）与 `/api/admin/smtp`（写 SMTP 账号密码）直接 `mux.HandleFunc` 挂主 mux，绕过了鉴权中间件与审计中间件。
- **触发条件**：任意已认证调用方执行 `PUT /api/admin/account/email` 或 `PUT /api/admin/smtp`。
- **根因**：这两条路由为了支持「被锁管理员」自助配置（`setupAuthOrLogin`）而挂在主 mux，但挂载时把审计一起丢了。
- **影响面**：攻击者若拿到面板会话/Token，**可以把管理员邮箱改到自己名下、把 SMTP 指向自己的服务器**（接管告警邮件与验证码通道），而 `GET /api/audit-logs` 里查不到任何痕迹；同理这两个端点也**不受 token scope 约束**（`setupAuthOrLogin` 只认 cookie 与 setup 口令，不认 API Token）。
- **修复方案**：保留主 mux 挂载，但把这两个 handler 用 `auditMiddleware` 包一层；写操作再单独走一次 `write` scope 判定（对 API Token 而言）。
- **证据**：
```go
// internal/api/server.go:88-91
// 邮箱写 + SMTP 读写：挂到主 mux 自管鉴权，PUT 允许未登录 + setup 鉴权
// （被异常登录锁住的管理员仍能配）。GET SMTP 仍需登录。
mux.HandleFunc("/api/admin/account/email", s.handleSetEmail)
mux.HandleFunc("/api/admin/smtp", s.handleGetOrSetSMTP)
```

---

### BUG-03 密码重置流程完全不落审计
- **严重级**：P2
- **位置**：`internal/api/auth.go:303-336`（request）、`internal/api/auth.go:344-375`（confirm）、`internal/api/server.go:37-38`
- **现象**：登录/登录失败有 `handleAuditLogin` 留痕（`internal/api/auth.go:236`、`internal/api/auth.go:179`），但「申请重置 → 提交验证码 → 改密」这条最敏感的链路**没有任何审计记录**，只在服务端 `log.Printf` 里留了一行 `[reset] password reset for %s`（`internal/service/account.go:119`）。
- **触发条件**：任何人调用 `POST /api/auth/password-reset/request` 与 `/confirm`。
- **根因**：这两个路由挂在主 mux 且未接 `auditMiddleware`，handler 内也没调 `handleAuditLogin`。
- **影响面**：发生「管理员账号被改密接管」时，审计表里查不到任何痕迹，无法回答「谁在什么时候重置了密码」。
- **修复方案**：在 `handlePasswordResetRequest` / `handlePasswordResetConfirm` 内显式调用 `s.handleAuditLogin(...)`（或统一把审计中间件也套到主 mux 的这几条路由上）。
- **证据**：
```go
// internal/api/auth.go:332-335 —— 只回 JSON，没有任何审计写入
writeJSON(w, http.StatusOK, map[string]any{
    "ok":           true,
    "challenge_id": challengeID,
})
```

---

### BUG-04 `PUT /api/sites/{id}` 是全量替换，未回传的字段被静默抹掉（MySQL 绑定/密码丢失）
- **严重级**：P2
- **位置**：`internal/api/server.go:181-198`、`internal/service/service.go:533-596`、`internal/store/store.go:294-305`
- **现象**：`UpdateSite` 只保留了 `ID` / `CreatedAt` / `Root.Path` / `Cert`（`internal/service/service.go:573-582`），**没有保留 `Mysql`**；而 `SaveSite` 是把整个 `model.Site` 序列化成一个 JSON blob 覆盖写入（`internal/store/store.go:305`）。因此：
  1. 前端若把 `GET /api/sites/{id}` 的结果原样回传，`mysql.password` 已被 `SanitizeForRead` 置空（`internal/model/model.go:189-198`）→ 回传后落库密码变成空串 → 站点数据库账号密码失效。
  2. 前端若不带 `mysql` 字段 → 站点的数据库绑定整体消失 → MySQL 侧留下无人管的孤儿库（删除站点时也不会再走 `deprovisionMySQL`）。
- **触发条件**：`GET` → 改 → `PUT` 的常规编辑流程（前端只需漏传一个字段即触发）。
- **根因**：API 层未定义 PUT 的部分更新语义，service 层又只补了几个特定字段。
- **影响面**：站点数据库凭据静默损坏；DB 与面板两侧状态不一致；用户无任何报错提示。
- **修复方案**：明确 PUT 为全量替换并在 `UpdateSite` 中补齐「`site.Mysql == nil` 时沿用 `old.Mysql`」的保留逻辑（与现有 `site.Cert` 的处理一致，`internal/service/service.go:580-582`）；`mysql.password` 为空时保留旧密码而非写空。
- **证据**：
```go
// internal/service/service.go:572-582
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
```
（注意：`Mysql` 不在保留列表里。）

---

### BUG-05 `/api/sites/batch` 文档承诺的 `delete` 动作未实现，且逐条回显原始错误
- **严重级**：P3
- **位置**：`internal/api/batch.go:14-18`（注释）、`internal/api/batch.go:58-75`（实现）、`internal/api/batch.go:55/61/68/74`（错误回显）
- **现象**：注释写 `action: "start"|"stop"|"delete"`，但 `switch body.Action` 只有 `start` / `stop`，传 `delete` 每条都返回「不支持的 action」。同时每条 item 的 `Error` 直接放 `err.Error()`。
- **触发条件**：前端批量删除；或批量启停时后端报错。
- **根因**：功能未完成 + 绕过了 `writeModelError` 的错误最小化约定。
- **影响面**：功能缺失（批量删站点不可用）；错误详情可能包含绝对路径、nginx stderr 等内部信息。
- **修复方案**：补 `delete` 分支（复用 `svc.DeleteSite`）；错误统一走 `writeModelError` 的分类文案，或至少过滤掉路径前缀。
- **证据**：
```go
// internal/api/batch.go:53-57
site, err := s.svc.DB().GetSite(id)
if err != nil {
    out = append(out, item{ID: id, Error: err.Error()})
    continue
}
```

---

### BUG-06 setup 兜底鉴权硬编码用户名 `admin`，与 `-admin` 启动参数不一致时永久失效
- **严重级**：P3
- **位置**：`internal/api/account.go:180-197`（`GetUser("admin")`）、`main.go:65-66`（`-admin` 可配）
- **现象**：`setupAuthOrLogin` 固定取 `admin` 用户的密码哈希做比对。若运维用 `-admin root` 启动（`main.go:103-107` 会创建 `root` 用户），则 setup 鉴权恒定 401。
- **触发条件**：管理员用户名不是 `admin`。
- **根因**：用户名常量写死，没有从服务层取当前管理员。
- **影响面**：被异常登录锁住时，「改邮箱 / 配 SMTP」的兜底通道全部失效——恰好是这个功能设计要解决的场景。
- **修复方案**：把管理员用户名作为 `service.Service` 的字段（或从 `EnsureAdminUser` 落库的记录里取），兜底鉴权与 `EnsureAdminUser` 用同一个来源。
- **证据**：
```go
// internal/api/account.go:185-190
hash, _, err := s.svc.DB().GetUser("admin")
if err != nil {
    limiter.fail(key)
    writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
    return "", false
}
```

---

### BUG-07 `handleGetAccount` 注释声明「邮箱默认不返回明文」，实际返回明文
- **严重级**：P3
- **位置**：`internal/api/account.go:18-19`（注释）vs `internal/api/account.go:50`（实现）
- **现象**：注释写「邮箱默认不返回明文（前端展示时用 email_masked 即可）」，但响应体同时填了 `Email: prof.Email`（明文）与 `EmailMasked`。
- **触发条件**：已认证调用 `GET /api/admin/account`（或带 `X-Setup-Password`）。
- **根因**：实现与注释脱节。
- **影响面**：该端点需要鉴权，实际暴露面有限；但按注释写的前端/文档会误以为明文邮箱不外泄，后续若有人放宽鉴权就会造成泄露。
- **修复方案**：要么删掉 `Email` 字段只留 `EmailMasked`，要么修正注释说明「仅已鉴权调用可见明文」。
- **证据**：
```go
// internal/api/account.go:47-52
resp := accountProfileResponse{
    Username:      prof.Username,
    Role:          role,
    Email:         prof.Email,          // ← 明文
    EmailMasked:   maskEmail(prof.Email),
```

---

### BUG-08 `GET /api/audit-logs` 的 `limit` 没有上限（与其它分页参数不一致）
- **严重级**：P3
- **位置**：`internal/api/audit.go:247-251`
- **现象**：`limit` 只校验 `n > 0`，没有上界；对照 `internal/api/misc.go:92-97`（日志分页 `n <= 1000`）、`internal/api/misc.go:334-338`（告警 `n <= 1000`）都有上界。
- **触发条件**：`GET /api/audit-logs?limit=999999999`。
- **根因**：参数校验漏写。
- **影响面**：实际数据量受 `auditLimit = 5000`（`internal/api/audit.go:29`）封顶，影响有限；但 SQL `LIMIT` 直接吃用户输入，属于不该出现的形态。
- **修复方案**：加上界（如 1000），与其它分页参数保持一致。
- **证据**：
```go
// internal/api/audit.go:247-251
limit := 200
if v := r.URL.Query().Get("limit"); v != "" {
    if n, err := strconv.Atoi(v); err == nil && n > 0 {
        limit = n
    }
}
```

---

### BUG-09 `GET /api/audit-logs` 等读接口返回的 `limit`/`limit` 语义与审计表实际行数脱节
- **严重级**：P3
- **位置**：`internal/api/audit.go:259-264`
- **现象**：响应里 `"limit": auditLimit`（=5000）指的是「保留条数上限」，但同名的入参 `limit` 指的是「本次返回条数」，两个含义不同的量共用一个字段名，前端容易误用。
- **影响面**：接口语义混淆，非安全缺陷。
- **修复方案**：把返回字段改名为 `max_retained`。
- **证据**：
```go
// internal/api/audit.go:260-264
writeJSON(w, http.StatusOK, map[string]any{
    "logs":  logs,
    "total": total,
    "limit": auditLimit,   // ← 与入参 limit 同名不同义
})
```

---

### BUG-10 改密后 API Token 未吊销
- **严重级**：P3
- **位置**：`internal/api/auth.go:443-449`（改密踢会话）、`internal/service/account.go:113-117`（重置踢会话）、`internal/api/tokens.go:130-148`（吊销只删单条）
- **现象**：改密与密码重置都调用了 `DeleteUserSessionsExcept` 踢掉其它会话，但**没有任何逻辑吊销该用户名创建的 API Token**。
- **触发条件**：管理员密码泄露后执行改密。
- **根因**：会话与 Token 是两套独立的凭据体系，失效策略只覆盖了前者。
- **影响面**：已泄露的密码即使被改掉，攻击者手里的 API Token 仍然可用（并且 Token 权限通常等于管理员）。
- **修复方案**：改密/重置时一并吊销该用户名创建的 API Token（或提供「改密后同时吊销全部 Token」的可选开关）。
- **证据**：
```go
// internal/api/auth.go:443-449
if c, err := r.Cookie(sessionCookie); err == nil {
    if n, derr := s.svc.DB().DeleteUserSessionsExcept(user, c.Value); derr == nil && n > 0 {
        log.Printf("[auth] password changed for %s, invalidated %d other session(s)", user, n)
    }
}
```

---

### BUG-11 `POST /api/auth/password-reset/request` 无任何限流，可邮件轰炸并作废管理员正在用的重置码
- **严重级**：P3
- **位置**：`internal/api/auth.go:303-336`、`internal/store/store.go:801-811`
- **现象**：该端点既不走 `limiter`，也不检查频率；每次成功调用都会 `SaveChallenge`，而 `SaveChallenge` 会先 `DELETE FROM login_challenges WHERE username=? AND purpose=?`（`internal/store/store.go:805`）——即**每次申请都会作废该用户上一个未用的重置码**。
- **触发条件**：任意匿名请求。
- **根因**：重置申请端点没有独立的限流与「频繁申请冷却」。
- **影响面**：① 匿名邮件轰炸（每次一封验证码邮件）；② 攻击者持续申请可让管理员手里的重置码始终失效，管理员改密被无限拖慢。
- **修复方案**：给该端点加按 IP/用户名的独立限流（例如同一用户名 N 分钟内只允许 1 次申请），并对「用户不存在」与「已申请过」两种情况都返回同一响应。
- **证据**：
```go
// internal/store/store.go:804-810
// 同 purpose 只保留最新一条：避免旧 challenge 留作后门。
_, _ = d.db.Exec(`DELETE FROM login_challenges WHERE username=? AND purpose=?`, username, purpose)
```

---

### BUG-12 panic 无统一兜底中间件（**不会**打挂进程，但错误响应不统一）
- **严重级**：P3
- **位置**：`main.go:296`（中间件链只有 `securityHeaders`）、`main.go:301-307`（`http.Server` 未挂 recover）
- **现象**：全仓 grep `recover(` **无任何匹配**，没有自定义的 panic 兜底中间件。
  > 澄清（避免误判）：Go 标准库 `net/http` 在 `conn.serve` 里自带 per-connection 的 `recover`，所以**单个 handler panic 不会打挂整个面板进程**，只会终止该次请求、打印栈到 stderr、关闭该连接。其余请求不受影响。
- **影响面**：真实损失是——① 客户端拿到的是「连接被重置」而不是统一的 JSON 500，前端只能报网络错误；② 栈信息只进 stderr，不进审计/错误日志通道；③ 若 panic 发生在响应头已写出之后，客户端会收到截断的响应体（前端 JSON 解析报错，错误信息具有误导性）。
- **修复方案**：加一个最外层的 recover 中间件，panic 时统一返回 `{"error":"内部错误","code":500}` 并写审计日志。
- **证据**：
```go
// main.go:296 —— 中间件链只有安全响应头，没有 recover
handler := securityHeaders(mux, *tlsEnable)
```

---

## 二、安全缺陷

### SEC-01 `site.Mysql.User` / `Password` / `Charset` 由客户端指定并被原样拼进 SQL —— 注入面
- **严重级**：P1
- **位置**：`internal/api/helpers.go:87-93`（`bindSite` 零校验）、`internal/service/service.go:631-680`（`provisionMySQL`）、`internal/mysql/mysql.go:265-267`、`internal/mysql/mysql.go:214-215`、`internal/mysql/mysql.go:289`
- **现象**：`POST /api/sites` 的请求体里的 `mysql` 对象被 `bindSite` 原样解出（`internal/api/helpers.go:87-93` 只做 `json.Decode`，无任何字段校验）。`provisionMySQL` 只在字段为空时才填默认值：
  - `site.Mysql.User` 为空才填（`internal/service/service.go:642-644`）→ **客户端可指定任意用户名**；
  - `site.Mysql.Password` 为空才生成（`internal/service/service.go:659-661`）→ **客户端可指定任意密码字符串**；
  - `site.Mysql.Charset` 为空才填（`internal/service/service.go:655-657`）→ **客户端可指定任意字符集串**。
  这些值最终进入 `fmt.Sprintf` 拼出的 DDL：
  ```go
  // internal/mysql/mysql.go:266
  return fmt.Sprintf("CREATE USER IF NOT EXISTS `%s`@`%s` IDENTIFIED BY '%s'", user, host, password)
  // internal/mysql/mysql.go:214-215
  stmt := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET %s COLLATE %s", name, charset, collation)
  // internal/mysql/mysql.go:289
  stmt := fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO `%s`@`%s`", db, user, host)
  ```
  项目里本来有现成的校验函数 `model.ValidateMysql`（`internal/model/validate.go:812-829`，对 `Name`/`User`/`Charset` 走 `NginxSafeIdentifier` 白名单），但**全仓 grep 显示它从未被调用**（只在定义处出现），`model.ValidateSite`（`internal/model/validate.go:318`）同样从未被调用——`CreateSite`/`UpdateSite` 都没调用它们（`internal/service/service.go:425-499`、`internal/service/service.go:533-596`）。
- **触发条件**：任意能调 `POST /api/sites` 的调用方（会话管理员，或 `write` scope 的 API Token）。
- **根因**：① `bindSite` 零校验（任务提示中已知）；② `model.ValidateSite` / `model.ValidateMysql` 是死代码，写入路径上根本没接；③ MySQL DDL 无法用占位符，代码注释里说明了这一点（`internal/mysql/mysql.go:241-247`），但**只考虑了密码由面板生成的场景，没有考虑客户端可传的场景**，因此没有做标识符/字面量转义。
- **攻击者视角的利用链**：
  1. 拿到一个 `write` scope 的 API Token（或管理员会话）。
  2. `POST /api/sites`，body 中 `"mysql":{"user":"x","password":"p' WITH SUPER GRANT OPTION -- ","host":"localhost","charset":"utf8mb4"}`。
  3. 面板用**自己的 MySQL root 凭据**（`internal/backup/backup.go:203` 的 `mysql-root.cnf` 同级 root）执行：
     `CREATE USER IF NOT EXISTS \`x\`@\`localhost\` IDENTIFIED BY 'p' WITH SUPER GRANT OPTION -- '`
     → 生成一个带 `SUPER` + `GRANT OPTION` 的 MySQL 账号，密码已知。
  4. 直接用该账号连 MySQL：读/改服务器上**所有**数据库、`LOAD DATA LOCAL INFILE` 读本地文件、与面板自身凭据同级。
  5. 即使不用注入手法，只把 `user` 设成 `root` 也能直接命中 `DROP USER IF EXISTS \`root\`@\`localhost\``（`internal/mysql/mysql.go:319`，删站点时经 `deprovisionMySQL` → `internal/service/service.go:698` 触发）→ **删掉面板自己的 MySQL root 账号**。
  6. 注入 `user` 标识符（反引号闭合）同样可以造出 `WITH SUPER` 账号：`user = "u`@`localhost` IDENTIFIED BY 'p' WITH SUPER -- "`。
- **影响面**：面板 MySQL root 权限旁落；站点用户「不能 GRANT 给别人」的产品约束（`internal/mysql/mysql.go:287-288` 注释明写）被绕过；可导致面板自身 MySQL 账号被删导致后续所有站点 MySQL 操作失败。
- **修复方案**：
  1. 在 `CreateSite` / `UpdateSite` 入口调用 `model.ValidateSite(site)` 与 `model.ValidateMysql(site.Mysql)`（两个函数已写好，只是没接）；
  2. `provisionMySQL` 里**不要接受客户端的 `User` / `Password` / `Charset`**：`User` 一律用 `mysql.NameForSite(site.ID)` 推导（与 `Name` 一致），`Charset` 用白名单 `utf8mb4/utf8/latin1`，`Password` 一律面板生成（客户端要指定就单独走「重置数据库密码」端点）；
  3. 兜底：在 `buildCreateUserStmt` / `buildAlterUserStmt` / `CreateDatabase` / `GrantPrivileges` / `DropUser` 里对标识符做反引号转义（`` ` `` → ``` `` ```），对密码字面量做单引号转义。
- **证据**：
```go
// internal/service/service.go:641-661
if site.Mysql.User == "" {
    site.Mysql.User = name
}
...
if site.Mysql.Charset == "" {
    site.Mysql.Charset = "utf8mb4"
}
if site.Mysql.Password == "" {
    site.Mysql.Password = mysql.GeneratePassword()
}
site.Mysql.Name = name
```

---

### SEC-02 客户端提供的 `X-Forwarded-For` 被无条件信任 —— 登录限流与「异常登录二次验证」双双可绕过
- **严重级**：P1
- **位置**：`internal/api/auth.go:107-113`（`clientKey`）、`internal/api/auth.go:476-492`（`clientIP`）、`internal/api/auth.go:163-170`（限流）、`internal/api/auth.go:203-223`（异常登录）、`internal/service/account.go:262-285`（`IsAnomalousLogin`）、`internal/service/account.go:272-277`
- **现象**：`clientKey` 与 `clientIP` 都**优先**取 `X-Forwarded-For` 的第一段，只有该头缺失时才用 `RemoteAddr`。面板默认直接监听 `0.0.0.0:8888`（`main.go:302`）且没有可信代理列表，客户端可以随意伪造该头。
- **触发条件**：任意匿名请求自带 `X-Forwarded-For`。
- **根因**：把「客户端可写」的请求头当作可信身份来源。
- **攻击者视角的利用链**：
  **A. 暴力破解无限化**：限流键是 `XFF|username`（`internal/api/auth.go:112`）。攻击者每次请求换一个 `X-Forwarded-For`（`1.1.1.1`、`2.2.2.2` …），`loginLimiter.allow` 永远命中新条目 → **5 次锁定 / 15 分钟封禁形同虚设**，可对管理员口令做无限次在线猜测（bcrypt 成本 10 每次约 50–100ms，属于可接受的速率）。
  **B. 二次验证（异常登录检测）被完全绕过**：这是本条最关键的部分。异常判定是「同子网 + 相似 UA 即放行」（`internal/service/account.go:272-283`）。攻击链：
  1. 攻击者用**正确密码**从真实 IP 发起登录 → 服务端判定为异常，返回 `need_verify=true`，并且 **`Reason` 字段把管理员上一次登录的 IP 原样回显**：
     `internal/service/account.go:274` → `Reason: fmt.Sprintf("IP 段变化：%s → %s", prevIP, newIP)`；
     `internal/api/auth.go:216-221` 把 `Reason` 写进响应体。
  2. 攻击者由此得知管理员的上次登录 IP（例如 `203.0.113.7`）。
  3. 攻击者重放登录并带上 `X-Forwarded-For: 203.0.113.7` → `sameSubnet` 成立 → `IsAnomalousLogin` 返回 `nil` → **不再需要邮箱验证码**，`issueSession` 直接下发完整会话 cookie（`internal/api/auth.go:235`）。
  4. 结果：**「密码 + 邮箱验证码」的双因素被降级成单因素**，且攻击者连邮箱都不用碰。
- **影响面**：① 口令爆破无速率限制；② 异常登录检测这道纵深防御可被单步绕过；③ 审计日志里的 IP（`internal/api/audit.go:104`、`internal/api/audit.go:283`）与 `users.last_login_ip` 全部可伪造，事后溯源失真（攻击者可把审计记录里的 IP 指向任意地址，栽赃第三方）。
- **修复方案**：
  1. 增加「可信代理」配置（`-trusted-proxy` 或反向代理 IP 段），**只有在 RemoteAddr 命中可信代理时才读 `X-Forwarded-For`**，否则一律用 `RemoteAddr`；
  2. 限流键改为「可信 IP + 用户名」，并额外加一个「按用户名的全局失败计数」（防止换 IP 绕开）；
  3. `IsAnomalousLogin` 的回显文案里**不要包含上一次登录的具体 IP**（只说「IP 段变化」），避免把管理员 IP 送到未认证的客户端面前；
  4. UA 相似度判定目前是「前 64 字符相等」（`internal/service/account.go:327-339`），可被轻易伪造，建议把 UA 相似度降权、不要作为放行条件。
- **证据**：
```go
// internal/api/auth.go:107-113
func clientKey(r *http.Request, username string) string {
    ip := r.RemoteAddr
    if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
        ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
    }
    return ip + "|" + strings.ToLower(strings.TrimSpace(username))
}
```
```go
// internal/service/account.go:272-277
if !sameSubnet(prevIP, newIP) {
    return &AnomalousLogin{
        Reason: fmt.Sprintf("IP 段变化：%s → %s", prevIP, newIP),  // ← 回显管理员上次 IP
```

---

### SEC-03 只读 API Token 的写能力绕过（与 BUG-01 同源，安全视角）
- **严重级**：P1
- **位置**：`internal/api/principal.go:36-39`、`internal/api/auth.go:535`
- **现象**：`HasScope` 的语义是「`scopes` 为空 = 全权限」（`internal/api/principal.go:36-39`），而「只读」的定义是「只允许 GET/HEAD/OPTIONS」。结合 BUG-01（写语义挂在 URL 上），**只读 Token 的实际能力远超「只读」**。
- **触发条件**：创建一个 `scopes:["read"]` 的 Token（`internal/api/tokens.go:83-94` 允许），然后用 GET 打上表中的写端点。
- **根因**：scope 是「方法级」的，而实际权限需求是「资源+动作级」的。
- **攻击者视角的利用链**：CI/监控用的只读 Token（典型用途是拉 `/api/system/metrics` 观察面板存活）一旦泄露，攻击者无需升级权限，直接 `GET /api/certs/{id}/delete` 即可删掉生产站点的证书，或 `GET /api/sites/{id}/toggle` 把站点停掉 → **只读凭据变成破坏性凭据**。且这些操作**不产生任何审计记录**（见 SEC-04）。
- **影响面**：凭据泄露后的爆炸半径远超标签承诺；「只读」这一安全标签失去意义。
- **修复方案**：同 BUG-01 的修复（把 scope 判定改成路由语义），并为「有副作用的 GET」补审计。
- **证据**：
```go
// internal/api/principal.go:36-45
if len(p.Scopes) == 0 {
    // 没写 scope 的 token 视为全权限（创建时的默认行为）。
    return true
}
for _, s := range p.Scopes {
    if s == scope || s == "*" {
        return true
    }
}
```

---

### SEC-04 有副作用的 GET 操作完全不产生审计记录
- **严重级**：P2
- **位置**：`internal/api/audit.go:79-85`（`isAuditedMethod`）、`internal/api/audit.go:65-76`
- **现象**：审计只覆盖 POST/PUT/PATCH/DELETE。由于 BUG-01 里的写操作都挂在 GET 上，**删证书、吊销证书、启停站点、下发配置、关闭告警这些高危动作一条审计都不会写**。
- **触发条件**：任意上述 GET 调用。
- **根因**：审计的「写」判定同样绑定在 HTTP 方法上。
- **影响面**：面板自称「以 root 运行、能改任意配置删任意文件，审计是等保硬要求」（`internal/api/audit.go:16-18` 注释），但**恰恰是最危险的那批操作没有审计**；事故后无法回答「谁删了证书」。
- **修复方案**：审计判定与 scope 判定统一改为「路由语义」；至少给上表 8 个端点显式登记审计动作。
- **证据**：
```go
// internal/api/audit.go:79-85
func isAuditedMethod(method string) bool {
    switch method {
    case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
        return true
    }
    return false
}
```

---

### SEC-05 `GET /api/settings` 返回 SMTP 密码明文（与 `/api/admin/smtp` 的脱敏口径矛盾）
- **严重级**：P2
- **位置**：`internal/api/misc.go:39-45`、`internal/api/account.go:126-135`
- **现象**：`handleSettings` 的 GET 直接返回 `AllSettings()` 的全量 key-value，而 SMTP 密码就以明文存在 `smtp.password` 键下（`main.go:151` 读取 `all["smtp.password"]`）。而同一个密码在 `GET /api/admin/smtp` 里是被 `maskPassword` 脱敏的（`internal/api/account.go:130`）——**同一个密码，两个接口两种口径**。
- **触发条件**：任意已认证调用方（含只读 API Token）`GET /api/settings`。
- **根因**：settings 表是通用 KV，读取时没有按 key 做敏感值过滤。
- **影响面**：SMTP 凭据（等于一个可发信账号）泄露给任何持有只读凭据的一方；只读 Token 因此可读到明文密码。
- **修复方案**：`AllSettings` 的 API 出口统一过滤 `smtp.password`（以及未来任何 `*password*`/`*secret*`/`*token*` 键），统一走 `maskPassword`；最好在 store 层提供 `AllSettingsMasked()`。
- **证据**：
```go
// internal/api/misc.go:39-45
case http.MethodGet:
    all, err := s.svc.DB().AllSettings()
    if err != nil {
        writeModelError(w, err)
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"settings": all})   // ← 含 smtp.password 明文
```
```go
// internal/api/account.go:129-131 —— 同一密码这里却是脱敏的
"password":   maskPassword(c.Password),
```

---

### SEC-06 站点根目录可指向面板自身数据目录 → 通过 nginx 公开泄露面板数据库与 MySQL root 凭据
- **严重级**：P2
- **位置**：`internal/model/model.go:88-126`（`ValidatePath`）、`internal/service/service.go:508-530`（`validateSitePaths`）、`internal/service/service.go:464-466`
- **现象**：`ValidatePath` 只黑名单了 `/etc /root /boot /dev /proc /sys /run /bin /sbin /lib /lib64 /usr /var/log`，**没有排除面板自己的数据目录**。默认数据目录是 `/opt/zhiweipanel/data`（`main.go:56`），里面放着：
  - `zhiweipanel.db`——SQLite 主库，含 `users.password_hash`、**明文会话 token**（`internal/store/store.go:128-133`，sessions 表 token 是主键明文）、API Token 的 SHA-256 摘要；
  - `mysql-root.cnf`——**MySQL root 凭据**（`internal/mysql/mysql.go:370-372`）。
  只要把 `site.Root.Path` 设为 `/opt/zhiweipanel/data`，nginx 就会把该目录当站点根对外伺服。
- **触发条件**：`POST/PUT /api/sites` 指定 `root.path = /opt/zhiweipanel/data`（该路径能通过 `ValidatePath`）。
- **根因**：路径黑名单是「通用系统目录」而非「面板自身敏感路径」；且站点根路径可由客户端任意指定（只被 `ValidatePath` 拦）。
- **攻击者视角的利用链**：
  1. 用只读/写 Token 或管理员会话创建站点，`root.path = /opt/zhiweipanel/data`；
  2. 面板下发 nginx 配置，该目录成为对外站点根；
  3. 匿名访问 `http://<站点域名>/zhiweipanel.db` 下载 SQLite 主库 → 拿到管理员密码哈希（可离线爆破）、**所有现存会话 cookie 的明文 token**（拿到即可直接冒充已登录管理员）、API Token 摘要；
  4. `http://<站点域名>/mysql-root.cnf` 直接拿到 MySQL root 密码。
- **影响面**：面板自身凭据全量泄露（会话劫持免登录、MySQL root 沦陷）。即使把该路径写进 `ValidatePath` 黑名单，更根本的问题是**站点根路径不该允许指向面板数据目录所在的父目录树**。
- **修复方案**：① `ValidatePath` 增加面板数据目录（`/opt/zhiweipanel` 及 `-data` 指定值）黑名单；② 更稳妥的做法是限制站点根必须位于 `/www/wwwroot`（或一个可配置的「站点根父目录」）之下；③ 站点根创建时校验目标目录里不存在 `*.db` / `*.cnf` / `*.key` 之类的敏感文件。
- **证据**：
```go
// internal/model/model.go:112-120
for _, bad := range []string{
    "/etc", "/root", "/boot", "/dev", "/proc", "/sys", "/run",
    "/bin", "/sbin", "/lib", "/lib64", "/usr", "/var/log",
} {
    if clean == bad || strings.HasPrefix(clean, bad+"/") {
```
```go
// internal/service/service.go:464-466 —— 客户端可任意指定站点根
if site.Root.Path == "" && len(site.Domains) > 0 {
    site.Root.Path = "/www/wwwroot/" + strings.ReplaceAll(site.Domains[0], "*.", "_")
}
```

---

### SEC-07 备份接口 `target` 传给 `mysqldump` 时未做 `--` 分隔 → 参数注入，可导出全库
- **严重级**：P2
- **位置**：`internal/api/backup.go:70`、`internal/backup/backup.go:193-212`、`internal/backup/backup.go:228`
- **现象**：`POST /api/backups` 的 `body.Target` 原样传给 `backup.BackupDatabase`，最终作为 `mysqldump` 的最后一个 argv 元素（`internal/backup/backup.go:204-212`）。没有做 `--` 结束选项解析。
- **触发条件**：`body.kind = "database"`，`body.target = "--all-databases"`（或其它 mysqldump 选项）。
- **根因**：把「数据库名」参数当成普通字符串直接交给命令行，未考虑 argv 语义。
- **攻击者视角的利用链**：
  1. `POST /api/backups`，`{"kind":"database","target":"--all-databases"}`；
  2. 面板以 MySQL root 身份执行 `mysqldump --defaults-extra-file=… --all-databases` → 导出**服务器上所有数据库**（不止面板自己建的 `site_*` 库）；
  3. 产物落在 `${dataDir}/backup/database/<id>.sql.gz`，并被记入 `backups` 表；
  4. `GET /api/backups/{id}/download`（`internal/api/backup.go:135-146`）把文件原样吐给调用方 → 全库数据到手。
- **影响面**：跨库数据泄露（面板本只应能操作自己建的 `site_*` 库）；`--defaults-extra-file=` 类选项还可能被用来改写 mysqldump 读取的凭据文件。
- **修复方案**：`args = append(args, "--", dbName)`；并对 `dbName` 做白名单校验（`^site_[0-9a-f]{12}$`，与 `mysql.NameForSite` 的推导规则一致）。
- **证据**：
```go
// internal/backup/backup.go:204-212
args := []string{
    "--defaults-extra-file=" + cnfPath,
    "--single-transaction",
    ...
    dbName,          // ← 客户端可控，且未用 "--" 终止选项解析
}
```

---

### SEC-08 `POST /api/cron/jobs` 可创建任意 bash 任务 —— 「write」scope 实际等价于 root RCE
- **严重级**：P2
- **位置**：`internal/api/cron.go:34-72`、`internal/cron/handlers.go:84-103`
- **现象**：`write` scope 的 API Token 可以 `POST /api/cron/jobs` 提交任意 `payload.script`，调度器用 `bash -c` 以面板的运行身份（root）执行（`internal/cron/handlers.go:98`）。唯一的约束是 `checkForbiddenCmd` 黑名单（`internal/cron/handlers.go:110-119`），而黑名单只拦 `shutdown`/`mkfs`/`passwd` 这几类，「一键作死」类命令之外一律放行——`curl | bash`、`rm -rf /`、`dd`、反弹 shell 都不在名单里。
- **触发条件**：任意 `write` scope 的 Token。
- **根因**：Token 的权限模型只有 read/write 两档，而 write 档的能力边界是「以 root 执行任意命令」，远超「管理站点」这一标签的暗示。
- **影响面**：**「write」这个 scope 名称严重低估了它的权限**——拿到一个 CI 用的 write token 等于拿到 root shell。权限标签与实际能力严重不匹配，运营方容易把 CI token 当成「低危凭据」管理。
- **修复方案**（三选一或组合）：
  1. 把 cron shell 任务拆出 API Token 体系（只允许会话认证创建），或给 Token 增加第三档 scope（如 `shell`）；
  2. 给 `checkForbiddenCmd` 换成「白名单 + 审计」策略，或至少把 `curl|wget|bash`、递归 `rm`、`dd`、`chmod`、写 `/etc` 等加进黑名单；
  3. 在 Token 列表页与创建响应里**明确标注该 Token 具备 root 命令执行能力**。
- **证据**：
```go
// internal/cron/handlers.go:94-103
workdir := strings.TrimSpace(p.Workdir)
if workdir == "" {
    workdir = os.TempDir()
}
cmd := exec.CommandContext(cctx, "bash", "-c", p.Script)
cmd.Dir = workdir
```

---

### SEC-09 验证码的 `purpose` 未在验证端点校验（与数据库 schema 注释声明的设计相反）
- **严重级**：P2
- **位置**：`internal/api/auth.go:271`、`internal/service/account.go:177-205`、`internal/store/store.go:138`
- **现象**：`/api/auth/verify-challenge` 调 `VerifyChallenge(id, code)`，而 `VerifyChallenge` **只对 `purpose == "email_verify"` 做副作用判断，其它 purpose 一律放行**（`internal/service/account.go:196-203`）；通过后 API 直接给 `c.Username` 下发完整会话（`internal/api/auth.go:289-291`）。因此 `purpose == "password_reset"` 的验证码同样能换到完整会话。
- **触发条件**：拿到一次密码重置邮件里的 6 位码。
- **根因**：purpose 只是标签，验证路径不按它分流；而 `internal/store/store.go:138` 的 schema 注释明确写着「purpose 区分用途，**避免『改密验证码』被拿去登录**」——**实现与 schema 声明的设计意图直接冲突**。
- **影响面**：两条凭据链路（改密 / 登录）被混用：改密邮件的验证码可以直接当登录凭证使用，绕开了「改密验证码不该能登录」的边界；一旦邮箱被其它系统共享、或邮件转发规则误配，边界就彻底消失。
- **修复方案**：`VerifyChallenge` 增加 `wantPurpose` 参数（`login_verify` / `password_reset` / `email_verify`），端点按自身用途传入；`password_reset` 的验证码只能用于 `/api/auth/password-reset/confirm`。
- **证据**：
```go
// internal/service/account.go:195-203
_ = s.db.ConsumeChallenge(id)
// 副作用：邮箱验证
if c.Purpose == "email_verify" {
    if err := s.MarkEmailVerified(c.Username); err != nil {
```
```go
// internal/store/store.go:138 —— schema 注释声明的意图
purpose     TEXT NOT NULL,      -- purpose 区分用途，避免"改密验证码"被拿去登录。
```

---

### SEC-10 会话 token 在数据库中明文存储
- **严重级**：P3
- **位置**：`internal/store/store.go:128-133`、`internal/store/store.go:877-903`
- **现象**：`sessions` 表的 `token` 是明文主键，`GetSession` 直接按明文比对。对比同库的 `api_tokens` 只存 SHA-256 摘要（`internal/store/ops_store.go:44-52`），**两套凭据的存储策略不一致**。
- **触发条件**：数据库文件泄露（备份文件被打包带走、`zhiweipanel.db` 被下载——见 SEC-06、备份目录被读等）。
- **根因**：会话 token 本身是不可逆派生的（随机 32 字节），但落库时没有做单向摘要。
- **影响面**：拿到 DB 即可直接接管所有在线会话（无需破解哈希），等价于完整接管面板。
- **修复方案**：会话 token 落库只存 SHA-256 摘要，比对时摘要后查；登出/改密按摘要删除（兼容性需要一次迁移）。
- **证据**：
```go
// internal/store/store.go:128-133
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,     -- ← 明文
    username   TEXT NOT NULL,
```

---

### SEC-11 会话 cookie 的 `Secure` 标志通过字符串拼接追加，且登出 cookie 不带 `Secure`
- **严重级**：P3
- **位置**：`internal/api/auth.go:460-470`、`internal/api/auth.go:382-384`
- **现象**：`issueSession` 先 `http.SetCookie` 再把 `"; Secure"` 拼到 `Set-Cookie` 头尾（`internal/api/auth.go:468-470`）；`handleLogout` 清理 cookie 时没有加 `Secure`。
- **触发条件**：启用 `-tls`。
- **根因**：没有直接用 `http.Cookie.Secure` 字段。
- **影响面**：① 靠字符串拼接追加属性，格式脆弱（若将来再追加 `SameSite` 之类容易写错顺序）；② 登出时下发不带 `Secure` 的同名 cookie，与浏览器的 secure-cookie 完整性规则存在边缘不一致；③ 未启用 TLS 时面板明文传密码（`main.go:372` 的启动日志已经提示了这点，属设计取舍）。
- **修复方案**：直接设置 `Secure: s.secureCookie` 字段，删除字符串拼接。
- **证据**：
```go
// internal/api/auth.go:460-470
http.SetCookie(w, &http.Cookie{
    Name:     sessionCookie,
    Value:    token,
    Path:     "/",
    HttpOnly: true,
    SameSite: http.SameSiteStrictMode,
    MaxAge:   int(sessionTTL.Seconds()),
})
if s.secureCookie {
    w.Header().Set("Set-Cookie", w.Header().Get("Set-Cookie")+"; Secure")
}
```

---

### SEC-12 `role` 字段被读出并回显，但全链路没有任何一处按角色授权
- **严重级**：P3
- **位置**：`internal/api/auth.go:237`、`internal/api/auth.go:405`、`internal/api/account.go:49`、`internal/model/ops.go`（无 role 授权逻辑）、`internal/store/store.go:124`
- **现象**：`users` 表有 `role` 列，登录响应、`/api/auth/me`、账户资料都会把 `role` 回显给前端，但**没有任何 handler 检查调用者的 role**；`principal.HasScope` 对会话认证一律返回 true（`internal/api/principal.go:31-35`）。
- **触发条件**：当前只有 `admin` 一个用户，所以现在不可利用。
- **根因**：role 是「预留字段」，授权模型实际只有「登录了 / 没登录」。
- **影响面**：一旦将来加了非管理员账号（`users` 表已支持多用户），**任何账号都自动获得完整管理员权限**，包括删站点、建库、读全部站点文件。当前 `admin` 账号被攻破的影响面也因此没有第二道防线。
- **修复方案**：要么删掉 role 字段明说「单管理员模型」，要么把 role 纳入 `principal.HasScope` 的判定（至少区分 read/admin 两档），并给管理端点加 `requireAdmin` 检查。
- **证据**：
```go
// internal/api/principal.go:31-35
func (p principal) HasScope(scope string) bool {
    if !p.ViaToken {
        // 会话认证 = 管理员本人，全权限。
        return true
    }
```

---

### SEC-13 `handleBackupByID` 与 `handleTestSMTP` 把原始错误回显给客户端
- **严重级**：P3
- **位置**：`internal/api/backup.go:75-78`、`internal/api/backup.go:157-161`、`internal/api/account.go:218-221`、`internal/api/batch.go:55/61/68/74`
- **现象**：这几处直接 `writeError(w, ..., err)` 回显 `err.Error()`，绕开了 `writeModelError` 的错误最小化约定（`internal/api/helpers.go:45-78` 的注释明确写了「早期 default 分支直接回传 err.Error()，于是 open /www/...: no such file、nginx -t 的完整 stderr、SQLite 报错都会出现在响应体里」）。
- **触发条件**：让后端出错即可（不存在的备份文件、mysqldump 失败、SMTP 连不上）。
- **根因**：新写的 handler 没有统一走 `writeModelError`。
- **影响面**：泄露绝对路径、SMTP 主机/端口、mysqldump 的 stderr（含库表名）、内部错误结构。
- **修复方案**：统一改用 `writeModelError`；确需回显细节的场景只回显固定白名单文案。
- **证据**：
```go
// internal/api/backup.go:75-78
if err != nil {
    writeError(w, http.StatusInternalServerError, err)   // ← 原始错误
    return
}
```

---

## 三、功能残缺与简陋项

| 编号 | 位置 | 描述 |
|---|---|---|
| F-01 | `internal/api/server.go:23-25`、`internal/api/auth.go:544-555` | `NewServer(svc, secureCookie)` 只有两个参数，**无法注入时钟、随机源、限流器**，导致限流/验证码/过期逻辑只能靠真实时间与全局单例（`internal/api/auth.go:104` 的包级 `var limiter`），无法做单测、无法多实例共享。 |
| F-02 | `internal/api/auth.go:104` | 登录限流器是**包级全局变量 + 内存 map**，不随实例走；面板重启即清零（暴力破解窗口重置），多副本部署时限流互相不可见；map 无淘汰策略，长期运行会缓慢增长（每个 IP+用户名组合一条，永不删除直到进程退出）。 |
| F-03 | `internal/api/helpers.go:81-84` | `decodeJSON` 用 `http.MaxBytesReader(nil, ...)`：**第一参数传 nil**，超限时无法让 net/http 关闭连接（`MaxBytesReader` 需要 ResponseWriter 才能触发 `requestTooLarge`）；且超限返回的是解码错误 → 一律 400，而不是 413。 |
| F-04 | `internal/api/helpers.go:87-93` | `bindSite` 只做 `json.Decode`，不拒绝未知字段（`DisallowUnknownFields`）、不限制嵌套深度。注：`model.ValidateSite` 已经是完整的校验器（`internal/model/validate.go:318-368`），只是**没有接上**——见 SEC-01。 |
| F-05 | `internal/api/certs.go:157-237` | `handleCertByID` 的子动作**没有任何 HTTP 方法区分**，连「读」和「写」都混在一个 switch 里；对照 `internal/api/backup.go:119-169` 的正确写法，属于同一代码库内的风格分裂。 |
| F-06 | `internal/api/misc.go:159-176` | `handleTail` 只返回一个拼接出来的 ws URL，实际连不连得上由前端负责；URL 用字符串拼接而非 `url.Values` 编码（当前 `site_id` 必须是已存在的站点 ID，故暂无注入风险，但模式不佳）。 |
| F-07 | `internal/api/account.go:8-15` | `handleSetEmail` / `handleGetOrSetSMTP` 完全不校验 HTTP 方法（`internal/api/server.go:90-91` 直接挂主 mux），GET 会因为解不出 body 而返回 400，语义不干净。 |
| F-08 | `internal/api/cron.go:197-200` | `strconvItoaSafe` 是空壳兼容函数 + `var _ = strconvItoaSafe`，典型的死代码。 |
| F-09 | `internal/api/backup.go:206-215` | `strconvParseInt` 的 `def` 参数在 `s == ""` 与解析失败时返回，但**没有上界**，`limit` 可被传成任意大整数（当前影响有限，表有上限）。 |
| F-10 | `internal/api/tokens.go:164-171` | `parseExpiresInDays` 注释写「保留给内部测试用」，但全仓无调用方，属死代码。 |
| F-11 | `internal/api/certs.go:212-226` | `bind` 动作没有校验 `body.SiteID` 是否为空/存在：`bindCertToSite("")` 会取不到站点而**静默返回**（`internal/api/certs.go:132-135` 直接 `return`），但接口仍返回 `{"ok":true}`——调用方以为绑定成功了。 |
| F-12 | `internal/api/server.go:84-85` | `/api/admin/account` 与 `/api/admin/account/email/verify` 自管 setup 鉴权（`internal/api/account.go:36-40`、`internal/api/account.go:94-98`），与其它端点的鉴权模型不一致；`handleGetAccount` 在 setup 鉴权下也返回完整资料，攻击面见 SEC-05/SEC-13 同类问题。 |
| F-13 | `internal/api/principal.go:109` | `go s.svc.DB().TouchAPIToken(t.ID)` **每次 API 调用都起一个 goroutine** 做一次 UPDATE；高 QPS 下会产生大量 goroutine 与 SQLite 写竞争（`internal/store/ops_store.go:114-118` 内部还持 `d.mu` 锁）。 |
| F-14 | `internal/api/backup.go:156-166` | `restore` 动作只回显 tar 里的 `nginx.conf` 内容（注释自认「v0.2.0 简化」），**并没有真正恢复**，但接口名与返回的 `{"ok":true}` 会让人误以为恢复成功。 |

## 四、代码质量

| 编号 | 位置 | 描述 |
|---|---|---|
| Q-01 | `internal/api/auth.go:24-32` | 限流参数（5 次 / 1 分钟 / 锁 15 分钟）写死为常量，无法按部署场景调整；`loginWindow` 与 `loginLockFor` 的关系没有文档化（窗口 1 分钟但锁定 15 分钟，实际是「失败 5 次 → 锁 15 分钟」，与常量名的字面语义不一致）。 |
| Q-02 | `internal/api/auth.go:51-102` | `loginLimiter` 只有「失败计数 + 锁定」两态，没有成功后的半开探测、没有按 IP 的总量限流、没有 map 淘汰 —— 长期运行内存单调增长。 |
| Q-03 | `internal/api/helpers.go:35-37` | `writeError` 把 `err.Error()` 直接作为响应体，`writeModelError` 的最小化约定只在「走 writeModelError 的那条路」生效，缺少编译期/测试期的约束（`ops_test.go` 也没覆盖错误文案）。 |
| Q-04 | `internal/api/helpers.go:45-78` | `writeModelError` 的错误分类是 `errors.Is` 白名单 + 兜底 500，**新增业务错误时容易漏加分支**，从而把「本该 400 的用户输入错误」变成 500（虽然不会泄露细节，但会误导前端与监控）。 |
| Q-05 | `internal/api/server.go:28-96` | 路由注册把「公开 / 需鉴权 / 自管鉴权」三类混在同一个 `Routes()` 里，公开路由（`server.go:32-38`、`:84-91`）与 protected 路由（`:46-79`）的边界只靠注释维护；新增端点时最容易漏挂鉴权（本次审计发现的 BUG-02/SEC-05/BUG-03 都源于此）。建议用一张显式的路由表（pattern + 是否需鉴权 + 是否需 write scope）驱动注册。 |
| Q-06 | `internal/api/audit.go:140-223` | `deriveAuditAction` 靠字符串切分 + 两张映射表推导动作名，可读性好但脆弱：新增子动作必须同步改 `subActions`（`internal/api/audit.go:196-202`），漏改就会退化成 `cert.action` 这种无意义动作名。 |
| Q-07 | `internal/api/audit.go:39-59` | `statusRecorder` 未透传 `http.Flusher` / `http.Hijacker` / `io.ReaderFrom`，注释里承认了这点（`internal/api/audit.go:36-38`）；当前没有 SSE 端点，但一旦加就会静默失效。 |
| Q-08 | `internal/api/files.go:128`、`internal/api/files.go:148` | 文件写入用 `http.MaxBytesReader(nil, ...)`，第一参数为 nil（同 F-03）；另外 `fileWrite` 用 `ct[:16] == "application/json"` 判断（`internal/api/files.go:123`），字符串切片比较应写成 `strings.HasPrefix(ct, "application/json")`。 |
| Q-09 | `internal/api/misc.go:25-32` | `readFileString` 出错时返回空字符串，调用方（`internal/api/certs.go:207`）会把「文件不存在 / 读失败」与「证书是空文件」混为一谈，下载接口会返回 200 + 空 body。 |
| Q-10 | `internal/api/principal.go:36-39` + `internal/model/ops.go:44-54` | `HasScope` 在 `internal/api/principal.go` 与 `internal/model/ops.go` **重复实现了两份**，语义一致但没有共用；将来改一处忘另一处会造成 API 层与 model 层判定不一致。 |
| Q-11 | `internal/api/auth.go:468-470` | 用字符串拼接操作 `Set-Cookie` 头（见 SEC-11），是典型的「能用但不该这么写」。 |
| Q-12 | 全仓 | 静态文件服务（`main.go:554-582`）与 API 错误响应都依赖 `http.FileServer` / `http.ServeFile` 的默认行为，未统一封装；`internal/api/misc.go:238-243` 的 `handleStatic` 是个只做转发的空壳函数，没有任何调用方。 |

## 五、已核实「未发现缺陷」的部分（附文件清单）

| 检查项 | 结论 | 依据 |
|---|---|---|
| WebSocket 端点鉴权 | **已鉴权**，不是漏洞 | `main.go:293` `mux.Handle("/ws/logs", authWS(srv, http.HandlerFunc(srv.HandleWebSocket)))`；`main.go:533-545` 的 `authWS` 调 `srv.Authorize(r)` → `internal/api/principal.go:90-116` 的 `currentPrincipal` 做真实会话/Token 校验；早期「只看 cookie 是否存在」的问题已被修掉 |
| WebSocket 同源检查 | 正确（用相等而非 Contains） | `internal/api/misc.go:185-192` |
| 站点文件任意文件读取/穿越 | **未发现缺陷** | `internal/service/files.go:84-131` `resolveInRoot`：禁 `..` 段 + `EvalSymlinks` + `pathInRoot` 前缀断言；`internal/service/files.go:147-155` 额外保护 `logs/`、`.well-known/`；Windows 反斜杠穿越由 `filepath.Join` 的 Clean + 前缀断言兜住 |
| 上传文件名穿越 | 已防 | `internal/api/files.go:311-326` `safeFilename` |
| zip-slip | 已防 | `internal/service/files.go:417-453`（拒 symlink / `..` / 绝对路径 / 越界，并有三重体积上限） |
| 站点日志路径穿越 | 已防 | `internal/api/misc.go:81-90` `type` 白名单 + `internal/api/misc.go:111-118` 前缀断言 |
| 证书下载任意文件读取 | **未发现缺陷** | 证书落盘目录用 `store.NewID()`（`internal/acme/acme.go:268`、`:349`），`cert.CertFile` 不含用户可控片段 |
| 配置快照任意文件读取 | **未发现缺陷** | `internal/confhistory/confhistory.go:253-264` `validateID` 白名单 + `internal/confhistory/confhistory.go:187-196` 文件名校验 |
| SQLite SQL 注入 | **未发现** | `internal/store/store.go` 与 `internal/store/ops_store.go` 全部使用 `?` 占位符，无字符串拼接 |
| 密码比对 | 正确（bcrypt，恒定时间） | `internal/api/auth.go:183`、`internal/api/auth.go:430`、`internal/api/account.go:191` 均为 `bcrypt.CompareHashAndPassword` |
| 密码哈希存储 | 正确（bcrypt） | `internal/api/auth.go:434`、`internal/service/account.go:106` |
| API Token 存储 | 正确（只存 SHA-256 摘要，明文只返回一次） | `internal/store/ops_store.go:44-52`、`internal/api/tokens.go:101-127` |
| API Token 吊销 | 已实现（删行，立即生效） | `internal/api/tokens.go:130-148` → `internal/store/ops_store.go:121-126` |
| 登录限流 / 锁定 | 已实现（但键可伪造，见 SEC-02） | `internal/api/auth.go:28-102` |
| 用户名枚举（登录/改密） | 已防 | 用户不存在时也做一次 bcrypt 比对（`internal/api/auth.go:174-176`）；改密申请对不存在的用户也返回 200（`internal/api/auth.go:320-331`） |
| CSRF | 无 token，但由 `SameSite=Strict` 覆盖 | `internal/api/auth.go:465`；未发现任何 `Access-Control-Allow-*` 响应头（无 CORS 误配） |
| Cookie 属性 | HttpOnly + SameSite=Strict + MaxAge 已设；Secure 仅 `-tls` 时追加（见 SEC-11） | `internal/api/auth.go:460-470` |
| 中间件顺序 | 正确（鉴权在前、审计在后，身份与响应码都能拿到） | `internal/api/server.go:42-45` |
| 静态文件目录穿越 | 未发现（`http.FileServer` + `fs.Sub`，且 `ServeMux` 会清理路径） | `main.go:554-582` |
| 请求体大小限制 | 已设（8MB / 50MB 各端点分别限制），但超限返回 400 而非 413、且 `MaxBytesReader` 首参传 nil（见 F-03） | `internal/api/helpers.go:82` 等 |
| 错误信息最小化 | `writeModelError` 做得不错（未知错误统一 500 + 服务端日志），但个别 handler 绕过（见 SEC-13） | `internal/api/helpers.go:45-78` |
| 命令注入（driver 层） | 未发现 | `internal/driver/hostdriver/host.go:84-87`、`internal/driver/dockerdriver/docker.go:83-97` 全部固定参数 |
| 审计中间件 | 已实现，但判定基于 HTTP 方法（见 SEC-04） | `internal/api/audit.go:65-119` |
| HTTP 方法限制 | 部分端点缺失（见 BUG-01 表格），`handleBackups`/`handleTokens`/`handleCronJobs`/`handleBackupByID` 等写法正确 | `internal/api/backup.go:26-88` 等 |

---

## 六、修复优先级建议（按投入产出比）

1. **接上已有的校验器**（改动最小、收益最大）：`CreateSite`/`UpdateSite` 调用 `model.ValidateSite` + `model.ValidateMysql` → 直接消掉 SEC-01 的一半攻击面（客户端无法再指定 `User`/`Charset`）。
2. **把 `site.Mysql.User`/`Password`/`Charset` 改成服务端独占**（SEC-01 剩余部分）。
3. **加可信代理判定**（SEC-02）+ **去掉异常登录文案里的上一个 IP**（SEC-02 的第 3 步是靠这个回显拿到管理员 IP 的）。
4. **scope / 审计判定改成路由语义**（BUG-01 + SEC-03 + SEC-04 一并解决）。
5. **`GET /api/settings` 过滤 `smtp.password`**（SEC-05，改动最小）。
6. **站点根路径限制在可配置父目录之下**（SEC-06）。
7. **`mysqldump` 参数加 `--` + 库名白名单**（SEC-07）。
