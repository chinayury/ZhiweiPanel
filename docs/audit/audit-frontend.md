# 审计报告：前端与 API 契约

> 审计范围（已逐文件通读）：
> 前端（18 个 JS/HTML）：`web/index.html`、`web/js/api.js`、`web/js/app.js`、`web/js/components.js`、
> `web/js/views/dashboard.js`、`web/js/views/sites.js`、`web/js/views/site-create.js`、`web/js/views/site-detail.js`、
> `web/js/views/site-files.js`、`web/js/views/certs.js`、`web/js/views/cron.js`、`web/js/views/backups.js`、
> `web/js/views/logs.js`、`web/js/views/alerts.js`、`web/js/views/ops.js`、`web/js/views/settings.js`
> （另有 `web/css/app.css`，仅作资源存在性核对，未审 CSS 规则）
>
> 后端对照（只读）：`internal/api/server.go`、`auth.go`、`account.go`、`certs.go`、`cron.go`、`backup.go`、
> `files.go`、`misc.go`、`ops.go`、`tokens.go`、`audit.go`、`batch.go`、`helpers.go`、
> `internal/model/model.go`、`internal/model/ops.go`、`internal/model/sanitize.go`、`internal/driver/driver.go`、
> `internal/acme/dns.go`、`internal/acme/acme.go`、`internal/cron/handlers.go`、`internal/cron/scheduler.go`、
> `internal/logmgr/logmgr.go`、`internal/mysql/mysql.go`、`internal/confhistory/confhistory.go`、
> `internal/alert/engine.go`、`internal/alert/checker.go`、`internal/service/account.go`、`main.go`
>
> 审计时间：2026-10-29
> 审计性质：只读。未修改任何 Go / JS / CSS 源码；本次唯一写入的文件是本报告。
> 未执行 `go build` / `go test` / `go vet` / `npx esbuild`。
> `tmp/out_*.js` 为构建产物，不在 `web/` 内，按任务范围排除。
> 注：审计期间后端被并行任务并发修改（见文末「未决项」），已复核本报告引用的 API 契约仍然成立。

---

## 一、BUG 与缺陷

### BUG-01 `unwrap()` 丢弃站点汇总指标，仪表盘与列表的流量/请求/告警恒为 0

- **严重级**：P1
- **位置**：`web/js/api.js:76-90`（`unwrap`）、`web/js/api.js:143-147`（`api.sites.list`）；
  受影响消费点 `web/js/views/dashboard.js:31-34`、`web/js/views/sites.js:139`、`:145`、`:152`
- **现象**：后端 `GET /api/sites` 返回的元素是 `SiteSummary`：`{site:{...}, today_reqs, today_bytes, month_bytes, cert_days, tamper_count, open_alerts}`（`internal/model/model.go:648-656`，`internal/api/server.go:114`）。
  `unwrap()` 对 `data.sites` 的每个元素做 `item.site ? item.site : item`，**只保留 `site` 字段，同层的汇总指标被整个丢弃**。
  随后 `api.sites.list()` 里的 `list.map(flattenSiteSummary)` 已经拿不到 `.site`（`flattenSiteSummary` 在 `x.site` 为空时原样返回），于是**该函数实际是死代码**，api.js:103-105 注释描述的 `{site, today_reqs} -> {...site, today_reqs}` 语义从未发生。
  结果：`dashboard.js` 的"今日请求 / 今日流量 / 本月流量 / 待处理告警"四个指标永远是 `0`；`sites.js` 的"今日请求 / 今日流量 / 告警"三列永远显示 `0` / `0 B` / `-`。
- **触发条件**：任何一次打开仪表盘或网站列表页（无需任何特殊操作）。
- **根因**：`unwrap()` 的语义是"把 `item.site` 整体替换掉该项"，而 `flattenSiteSummary()` 的语义是"把 `item.site` 展开并保留同层字段"；两者意图相反且串联执行，后执行的那个先被前一个消解。
- **影响面**：数据正确性（不是崩溃）。面板最核心的"今天跑了多少流量 / 有多少待处理告警"长期显示 0，管理员会误判站点健康度；`open_alerts` 恒为 0 导致 `sites.js:151-158` 的告警角标列永远是死列。涉及 8 个调用 `api.sites.list()` 的视图全部受影响（dashboard / sites / site-create / certs / cron / backups / logs）。
- **修复方案**：删除 `unwrap()` 对 `sites` 的处理（保留 `certs` / `accounts` 分支或一并删除），让 `api.sites.list()` 里的 `flattenSiteSummary` 真正生效；若保留 `unwrap`，则应改为 `item.site ? {...item.site, ...omit(item,'site')} : item`，即合并而非替换。
- **证据**：
```js
// web/js/api.js:80-88
for (const k of ['sites', 'certs', 'accounts']) {
  if (Array.isArray(data[k]) && data[k].length) {
    data[k] = data[k].map((item) =>
      (item && typeof item === 'object' && item.site && typeof item.site === 'object')
        ? item.site            // ← 只剩 site，today_reqs / open_alerts 等同层字段全丢
        : item
    );
  }
}
```
```js
// web/js/api.js:143-147  ← flattenSiteSummary 此处已是死代码
const res = await http.get('/sites');
const list = (res && res.sites) || [];
return { ...res, sites: list.map(flattenSiteSummary) };
```

---

### BUG-02 "忘记密码"流程 100% 失败：`http` 未被 import

- **严重级**：P1
- **位置**：`web/js/app.js:456`、`web/js/app.js:501`；缺失的 import 在 `web/js/app.js:4`
- **现象**：`doResetRequest()` 与 `doResetConfirm()` 调用 `http.post(...)`，但 `app.js:4` 只 import 了 `{ api, setUnauthorizedHandler }`，`http` 在该模块作用域**未定义**。运行到这两行会抛 `ReferenceError: http is not defined`。
  该异常被同函数内的 `catch (err)` 捕获，于是 `errBox` 里显示的是字面量 **"http is not defined"** —— 用户看到的是一个 JS 报错文案，而不是任何中文提示。整个"忘记密码"（申请验证码 → 提交验证码+新密码）流程**完全不可用**。
- **触发条件**：点击登录页"忘记密码？"→ 输入用户名 → 点"发 送 验 证 码"（第 1 步即失败）；或在第 2 步提交（第 2 步也失败）。
- **根因**：新增密码重置功能时直接写了 `http.post(...)`，但 `http` 是 `api.js` 的具名导出而 `app.js` 未 import（`api.js:92-97` 导出 `http`，但 `api.js` 内部没有把重置接口包成 `api.auth.*`）。
- **影响面**：管理员忘记密码时无法自助重置，只能走服务器端运维（改库/重置 admin 密码）。`app.js:501` 的成功分支（重置成功后回登录页）同样不可达。
- **修复方案**：在 `app.js:4` 补 `http` 的 import；更规范的做法是在 `api.js` 的 `api.auth` 下补 `passwordResetRequest` / `passwordResetConfirm` 两个方法，把 `http` 保持为模块私有。
- **证据**：
```js
// web/js/app.js:4
import { api, setUnauthorizedHandler } from './api.js';   // ← 没有 http
...
// web/js/app.js:456
const r = await http.post('/auth/password-reset/request', { username });
// → ReferenceError: http is not defined
```

---

### BUG-03 `document.getElementById('view')` 恒为 null，操作后刷新即崩溃

- **严重级**：P1
- **位置**：`web/js/views/sites.js:296`、`web/js/views/backups.js:99`、`:156`、`:189`、`web/js/views/cron.js:102`、`:129`、`:285`
- **现象**：视图容器在 `app.js:124` 被创建为 `h('main', { class: 'content', id: 'outlet' })`，DOM 里**根本不存在 id 为 `view` 的元素**（已全库 grep 确认）。这 7 处 `render(document.getElementById('view'))` 传入 `null`：
  - `render()` 第一行 `clear(container)` → `clear(null)` 只 `console.warn` 后返回 null（`components.js:45-52`）；
  - 紧接着 `container.appendChild(loading(...))` → **`TypeError: Cannot read properties of null (reading 'appendChild')`**。
  该异常发生在 `await api.xxx()` 之后的同步语句里，位于 `try` 内且没有对应的 `catch`（如 `sites.js:290-300` 的 `batchDo` 只有 `try/catch` 包住 await 部分）→ 抛出到事件处理器 → **未捕获的 Promise 拒绝，页面上没有任何错误提示**。
- **触发条件**：站点列表"批量启动/批量停止"、备份列表"删除备份"、"备份站点"、"备份数据库"、计划任务"启停开关"、"立即执行后的重绘"、"删除任务"、"保存任务"。
- **根因**：视图容器 id 从 `view` 改名为 `outlet` 时，这 7 处调用点没有同步更新。
- **影响面**：批量启停站点、删除备份、计划任务增删改——这几类操作**执行成功但界面不刷新**（后端已生效，列表仍是旧状态），且控制台出现未捕获异常。计划任务的"启用"开关尤其误导：开关视觉上翻转了，但列表状态与后端可能不一致。
- **修复方案**：把 `render(container)` 改为 `render(container)`（各视图的 `render` 都已持有 `container` 形参），或统一改为 `ctx.navigate(location.hash)` 触发路由重绘。`cron.js` 的 `drawList/showEditor` 是模块级函数、没有 `container` 形参，需要把 `container` 提升为模块级变量。
- **证据**：
```js
// web/js/views/sites.js:290-300
async function batchDo(action, ids) {
  try {
    const r = await api.sitesBatch(action, ids);
    toast(`${action} 完成：成功 ${r.ok_count}/${ids.length}`, 'success');
    state.selected.clear();
    render(document.getElementById('view'));   // ← null
  } catch (e) { toast(e.message || '批量操作失败', 'error'); }
}
```
```js
// web/js/app.js:124
outlet = h('main', { class: 'content', id: 'outlet' });   // 真实 id 是 outlet，不是 view
```

---

### BUG-04 读文件时内容若恰好是合法 JSON 会被当成 JSON 解析 → 编辑器空白 → 保存即清空文件

- **严重级**：P1
- **位置**：`web/js/api.js:48-58`（`request()` 的解析分支）、`web/js/views/site-files.js:370-388`（`onOpen`）
- **现象**：`api.sites.files.read()` 走 `GET /api/sites/{id}/files/raw?path=`，后端对文本文件返回 `Content-Type: text/plain` 的**裸文本**（`internal/api/files.go:102-105`），对二进制返回 `{content, encoding:'base64', mime, size}` JSON（`files.go:107-113`）。
  `api.js:48-58` 的做法是：`res.text()` → 尝试 `JSON.parse(text)` → 成功则当 JSON，失败且 `res.ok` 才回退成裸字符串。
  于是当被打开的文件内容**本身是合法 JSON**（`package.json`、`config.json`、`{"a":1}`、纯数字 `123`、字符串 `"foo"` 等——`json`/`map` 扩展名在 `TEXT_EXT` 里，`site-files.js:16-21`）时：
  - `JSON.parse` 成功 → 返回的是对象/数字/字符串，**不是**后端约定的 `{encoding:'base64'}` 形状；
  - `site-files.js:377-387` 的分支既不匹配 `typeof raw === 'string'`，也不匹配 `raw.encoding === 'base64'` → `initial` 保持 `''`、`isBinary` 保持 false；
  - 编辑器**打开的是一个空 textarea**，底部还显示"二进制按字节读取"以外的一切正常；
  - 用户以为文件是空的或随手改一下点"保存" → `api.sites.files.write()` 把空内容写回去 → **原文件内容被清空**。
  数字型内容（`123`）会走 `typeof raw === 'string'` 为 false 的分支，同样清空；字符串型（`"foo"`）会把带引号的 `"foo"` 当成内容写回。
- **触发条件**：在文件管理器里打开一个内容恰好是合法 JSON 的文本文件，然后点"保存"。
- **根因**：`api.js` 用"能否 JSON.parse"来猜测响应类型，而同一个接口存在两种完全不同的响应形态（裸文本 vs JSON），没有依据 `Content-Type` 判定。
- **影响面**：**静默数据丢失**。站点源码里 `package.json`、`composer.json`、`.eslintrc.json` 等极其常见。
- **修复方案**：`request()` 按 `Content-Type` 分流——`res.headers.get('content-type')` 含 `application/json` 才 `JSON.parse`，否则返回原始文本；或给"读文件"单独走一条不经 `request()` 的原始文本通道。`site-files.js:377-387` 再补一个"未知形状"兜底分支，解析不出内容时直接报错而不是显示空白编辑器。
- **证据**：
```js
// web/js/api.js:48-58
const text = await res.text();
let data = null;
if (text) {
  try { data = JSON.parse(text); }          // ← '{"a":1}' 会被解析成对象
  catch (e) { if (res.ok) return text; throw {...}; }
}
```
```js
// web/js/views/site-files.js:377-387
if (typeof raw === 'string') { initial = raw; }
else if (raw && typeof raw === 'object' && raw.encoding === 'base64') { ... }
// ↑ raw 是 {"a":1} 时两个分支都不进 → initial = '' → 保存后文件为空
```

---

### BUG-05 后端不可达时启动流程无兜底：永久 loading，用户看不到任何错误

- **严重级**：P1
- **位置**：`web/js/app.js:557-571`（`boot()`）、`web/js/app.js:580`（`boot()` 调用处）
- **现象**：`boot()` 里 `const me = await api.auth.me();` **没有 try/catch**。当后端未启动 / 网络中断 / 反代 502 时，`api.js:41-44` 会抛出 `{status:0, message:'无法连接面板服务，请确认后端已启动'}`，该异常穿透 `boot()`；而 `app.js:580` 是裸调用 `boot();`（无 `.catch()`）。
  结果：`index.html:26-29` 的 `.boot-loading` 转圈动画**永久停留**，页面上没有任何错误文案——"无法连接面板服务"这条已经生成好的 message 被丢进了浏览器的 unhandledrejection 里。
- **触发条件**：后端进程没起、端口不通、反向代理配置错误、或后端在启动过程中崩溃。
- **根因**：`boot()` 缺少失败分支；全项目**没有任何全局 `window.addEventListener('unhandledrejection', ...)` 或 `window.onerror`**（已全库 grep 确认，只有 `app.js:231` 与 `dashboard.js:216` 两处 `console.error`，且这两处都同时有可见的 `errorState`，不构成"只写控制台"的问题）。
- **影响面**：面板最常见的运维故障场景（服务没起来）给出的是"永远转圈"这种最难排查的表现。
- **修复方案**：`boot()` 整体包 try/catch，失败时 `clear(app).appendChild(errorState('无法连接面板服务：' + err.message, () => location.reload()))`；同时补一个全局 `unhandledrejection` 兜底监听器，把未捕获异常以 toast 形式暴露（当前所有未捕获异常在 UI 上是完全静默的，见 BUG-03、BUG-12）。
- **证据**：
```js
// web/js/app.js:557-571
async function boot() {
  const app = document.getElementById('app');
  const me = await api.auth.me();     // ← 无 try/catch，失败即穿透
  if (!me || !me.authenticated) { renderLogin(); return; }
  ...
}
...
// web/js/app.js:580
boot();                               // ← 无 .catch()
```

---

### BUG-06 删除数据库的确认框没有正文，也不是危险样式

- **严重级**：P2
- **位置**：`web/js/views/site-detail.js:531`
- **现象**：`confirmDialog` 的签名是**单一对象解构**：`confirmDialog({ title='确认操作', message='', okText='确定', cancelText='取消', danger=false })`（`components.js:390`）。但此处传的是**两个位置参数**：`confirmDialog('确认删除数据库', '真的要删 xxx 吗？...')`。
  对字符串做对象解构时，字符串上不存在 `title` / `message` 属性 → 两者都取默认值：标题变成"确认操作"、正文是**空字符串**、`danger` 为 false（图标是 info 不是 warning）。
  于是这个"drop database + drop user、不可恢复"的高危操作，弹出的确认框**没有任何正文说明要删哪个库**。
- **触发条件**：站点详情 → 数据库标签页 → "删除该数据库（不可恢复）"。
- **根因**：`confirmDialog` 从位置参数形式改成对象参数形式时，这一个调用点漏改。全库 14 个 `confirmDialog` 调用点中，只有这一处是位置参数。
- **影响面**：破坏性操作的确认信息缺失，用户无法判断自己在删什么；且样式不是 danger 态，视觉上弱化了风险。
- **修复方案**：改为 `confirmDialog({ title:'确认删除数据库', message:`真的要删 ${m.name} 吗？…`, okText:'删除', danger:true })`。
- **证据**：
```js
// web/js/views/site-detail.js:530-532
button('删除该数据库（不可恢复）', async () => {
  const yes = await confirmDialog('确认删除数据库', `真的要删 ${m.name} 吗？…`);
  if (!yes) return;
```
```js
// web/js/components.js:390 —— 只认对象参数
export function confirmDialog({ title = '确认操作', message = '', okText = '确定', cancelText = '取消', danger = false }) {
```

---

### BUG-07 站点详情的 PHP 版本下拉恒为写死列表，与服务器实际版本不符

- **严重级**：P2
- **位置**：`web/js/views/site-detail.js:108-109`
- **现象**：后端 `SystemStatus.PHP` 是 `[]PHPVersion` **数组**（`internal/driver/driver.go:54`，元素形如 `{version, socket, running}`）。
  `site-create.js:39-41` 与 `settings.js:38-40` 都已按数组正确处理（`Array.isArray(st.php)`），但 `site-detail.js:108` 仍写 `statusRes.php.versions` → 数组没有 `versions` 属性 → 恒为 `undefined` → `phpVersions` 永远保持写死的 `['8.2','8.1','8.0','7.4','5.6']`。
  结果：站点详情页选 PHP 版本时，会出现服务器上**根本没装**的版本（如 5.6/7.4），而服务器实际装了但不在写死列表里的版本（如 8.3）**选不到**。选中不存在的版本后，后端按 socket 匹配失败 → 站点 502/404。
- **触发条件**：打开任一站点的详情页 → PHP 标签页 → 看"PHP 版本"下拉。
- **根因**：修 `st.php.versions` → `st.php` 数组的那个 bug 时，`site-detail.js` 被漏掉。
- **影响面**：可导致新建/修改站点时写入不可用的 PHP 版本，属于"前端放行、后端不报错但站点起不来"的隐性故障。
- **修复方案**：改为 `const v = statusRes && Array.isArray(statusRes.php) ? statusRes.php.map((p)=>String(p.version)) : [];`（与 `site-create.js:39-41` 保持一致）。
- **证据**：
```js
// web/js/views/site-detail.js:108-109
const v = statusRes && statusRes.php && statusRes.php.versions;   // ← statusRes.php 是数组
if (Array.isArray(v) && v.length) phpVersions = v.map((x) => String(x));
```
```go
// internal/driver/driver.go:52-58
type SystemStatus struct {
    Nginx   NginxStatus  `json:"nginx"`
    PHP     []PHPVersion `json:"php"`     // ← 数组，不是对象
    ...
}
```

---

### BUG-08 历史日志分页恒为 1 页，第 100 条之后永远看不到

- **严重级**：P2
- **位置**：`web/js/views/logs.js:241-267`
- **现象**：`GET /api/sites/{id}/logs?type&limit&page` 后端返回 `total = len(records)`，而 `records` 是 `ReadAccessLog(logPath, limit*page)` 的结果 —— 即**读到的行数**（`internal/api/misc.go:121`、`:138-142`），不是日志文件的真实总行数。`ReadAccessLog` 只读文件**末尾** `limit` 行且倒序返回（`internal/logmgr/logmgr.go:94-100`）。
  于是第 1 页（limit=100, page=1）：读到末尾 100 行 → `total=100` → `pages = ceil(100/100) = 1` → `pager()` 的"下一页"按钮 `disabled = page >= pages` → **恒禁用**。
  前端永远只能看到最近 100 条日志，且分页器显示"第 1 / 1 页"，用户不会意识到还有更多。
- **触发条件**：任意站点日志超过 100 行。
- **根因**：后端把"本次读取到的行数"当作"总行数"返回，前端据此算总页数；`total` 语义在错误日志分支（`misc.go:149-155`，无 `total` 字段）还不一致。
- **影响面**：无法排查较早的访问/错误记录（5xx 突增往往在更早的时间段），且 UI 给出"共 100 条"的错误信息。
- **修复方案**：后端用文件总行数（`wc -l` 语义的计数）作为 `total` 返回；或前端改为"始终允许点下一页，由后端返回空数组时提示'没有更多'"。最小改法：前端在 `total === PAGE_SIZE`（即可能还有更多）时不把 `pages` 卡死，改为 `pages = Math.max(pages, page + 1)`，并在后端返回空 `records` 时展示"没有更多日志了"（`logs.js:254` 已有该分支）。
- **证据**：
```js
// web/js/views/logs.js:244-246
const total = Number(res.total) || logs.length;
const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));   // total 恒等于本次读到的行数
```
```go
// internal/api/misc.go:138-142
records, err := logmgr.ReadAccessLog(logPath, limit*page)   // 只读末尾 limit*page 行
...
writeJSON(w, http.StatusOK, map[string]any{
    "records": records[start:end], "page": page, "total": len(records),  // ← 不是文件总行数
})
```

---

### BUG-09 "站点到期时间"占位符引导的格式后端不接受，保存必然 400

- **严重级**：P2
- **位置**：`web/js/views/site-detail.js:238-241`；后端 `internal/model/model.go:33`
- **现象**：后端 `Edate *time.Time`（`json:"edate,omitempty"`）用的是 Go 标准 `time.Time` 的 JSON 解码，要求 **RFC3339**（`2026-12-31T00:00:00Z`）。前端输入框的 placeholder 写的是 `'YYYY-MM-DD（留空 = 无到期）'`（`site-detail.js:240`），用户按提示填 `2026-12-31` → 后端 `json.Decode` 报 `parsing time "2026-12-31" as "2006-01-02T15:04:05Z07:00"` → **整个站点 PUT 返回 400**，站点其它所有改动也一并保存失败。
  另一个方向的坑：后端返回的是完整 RFC3339 串，前端 `textInput(site.edate || '', ...)` 原样塞进输入框 → 用户看到 `2026-12-31T00:00:00Z` 这样的怪串，不改它直接保存又是合法的（能被解析），但与 placeholder 严重不符。
- **触发条件**：按 placeholder 提示填写日期并保存站点。
- **根因**：前端未对 `*time.Time` 字段做日期格式转换（`site.edate` 是字符串直传），placeholder 也与后端格式要求不符。
- **影响面**：站点保存整体失败（一次改一个字段却让整站保存失败），且错误文案是英文的 Go 时间解析错误，用户难以理解。
- **修复方案**：前端保存前把 `site.edate` 规范化为 RFC3339（`new Date(v).toISOString()`），并在加载时反向格式化为 `YYYY-MM-DD` 显示；或后端为 `Edate` 定义自定义 `UnmarshalJSON` 接受日期-only。
- **证据**：
```js
// web/js/views/site-detail.js:238-241
field('站点到期时间',
  textInput(site.edate || '', (v) => { site.edate = v || null; markDirty(); },
    { placeholder: 'YYYY-MM-DD（留空 = 无到期）' }),
  '列表里未来 7 天内标黄，过期标红'),
```
```go
// internal/model/model.go:33
Edate *time.Time `json:"edate,omitempty"`   // time.Time 的 JSON 解码只接受 RFC3339
```

---

### BUG-10 202 响应的 `warning` 被丢弃：配置下发失败仍提示"保存成功"

- **严重级**：P2
- **位置**：`web/js/views/site-detail.js:725-728`、`web/js/views/site-create.js:372-379`；后端 `internal/api/server.go:143-147`、`:191-196`
- **现象**：后端在"站点已落库但 nginx 配置下发失败"时返回 **202** + `{site, warning}`（`server.go:140-148`、`:191-197`），注释里明写"用 202 明确区分，避免前端把它当成失败而丢弃用户的编辑"。
  前端 `doSave()` 只看 Promise 是否 reject（202 是 2xx → `res.ok` → 不 reject），随后无条件 `toast('站点配置已保存', 'success')`；**`warning` 字段从未被读取**。`site-create.js:375-379` 同样只检查 `show_password`，忽略 `warning`。
  结果：站点对象写库成功、但 nginx 配置没下发（例如 nginx -t 失败、磁盘写失败）时，界面显示绿色"已保存"，用户以为站点已上线，实际 502。
- **触发条件**：保存站点时 nginx 配置渲染或下发失败。
- **根因**：202 的 `warning` 载荷没有前端消费点；`api.js` 的 `unwrap()` 也不会把它并入站点对象（`unwrap` 只处理 `sites`/`certs`/`accounts` 三个键）。
- **影响面**：故障静默化——最难排查的一类问题（"站点显示运行中但打不开"）。后端专门设计的 202 语义在白侧被浪费。
- **修复方案**：`doSave()` 检查 `res.warning`，有则 `toast(res.warning, 'error', 8000)` 并在页头加一条告警条；`site-create.js` 同理。
- **证据**：
```go
// internal/api/server.go:143-147
writeJSON(w, http.StatusAccepted, map[string]any{
    "site":    created,
    "warning": applyErr.Error(),
})
```
```js
// web/js/views/site-detail.js:725-728
await api.sites.update(site.id, site);
snapshot = JSON.stringify(site);
store.markDirty(false);
toast('站点配置已保存', 'success');      // ← res.warning 被丢弃
```

---

### BUG-11 取消离开后 `store.dirty` 被清空，二次导航不再提示未保存修改

- **严重级**：P2
- **位置**：`web/js/app.js:207-221`
- **现象**：`handleRoute` 在弹确认框**之前**就执行了 `store.markDirty(false)`（`app.js:208`），该调用会同时移除 `beforeunload` 监听（`app.js:19-23`）。若用户在确认框点"取消"（`app.js:215-220` 直接 return），`store.dirty` 停留在 `false`，而视图内的实际编辑内容并没有丢（`site-detail.js:120` 的 `isDirty()` 仍为 true）。
  于是**用户第二次点导航时不再弹确认框**，直接离开，未保存的编辑被静默丢弃；若用户此时直接关标签页，`beforeunload` 提示也已失效。
- **触发条件**：站点详情页改了配置 → 点别的导航 → 在确认框选"取消" → 再点一次导航（或直接关页面）。
- **根因**：`markDirty(false)` 被当成"清理状态"用在了确认之前，而不是在确认通过之后。
- **影响面**：站点配置编辑丢失（17 组配置项，编辑成本高）。
- **修复方案**：把 `store.markDirty(false)` 移到确认通过之后（`if (!ok) { ...; return; }` 之后再执行）；取消分支里恢复 `store.markDirty(true)`。
- **证据**：
```js
// web/js/app.js:207-221
if (store.dirty) {
  store.markDirty(false);          // ← 先清标志
  const ok = await confirmDialog({...});
  if (!ok) {
    routing = false;
    if (location.hash !== `#${path}`) history.replaceState(null, '', `#${path}`);
    return;                        // ← store.dirty 仍是 false，编辑仍在但不再提示
  }
}
```

---

### BUG-12 多个破坏性按钮的 await 没有 try/catch，失败时无任何提示

- **严重级**：P2
- **位置**：`web/js/views/cron.js:113-116`、`:127-129`；`web/js/views/backups.js:97-99`
- **现象**：
  - `cron.js:113-116` "立即执行"：`await api.cron.run(j.id); toast('已加入执行队列…')` —— **没有 try/catch，也没有禁用按钮**。请求失败（如任务 id 失效、后端 500）时抛出的异常无人处理，**UI 上没有任何反馈**，用户以为"点了没反应"，会反复点。
  - `cron.js:127-129` "删除任务"：`await api.cron.remove(j.id)` 同样没有 try/catch，删除失败时列表不变且无提示。
  - `backups.js:97-99` "删除备份"：`await api.backups.remove(b.id)` 同样没有 try/catch。
  这些异常最终变成 unhandledrejection——项目里**没有全局 unhandledrejection 监听**（已 grep 确认），因此浏览器控制台里能看到，界面上完全看不到。
- **触发条件**：后端返回非 2xx（网络抖动、任务不存在、权限不足、磁盘错误）。
- **根因**：这三处按钮的处理函数只写了 happy path，与同文件其它按钮（`cron.js:103-105`、`backups.js:157-160` 有 try/catch）风格不统一。
- **影响面**：错误不可见，用户无法判断操作是否生效。
- **修复方案**：补 `try/catch` + `toast(e.message || '…失败', 'error')`；同时给这三个按钮加 `disabled` 防抖（"立即执行"尤其需要，后端是"标记 next_run_at=now"，重复点击虽幂等但会让用户误以为执行了多次）。
- **证据**：
```js
// web/js/views/cron.js:113-116
button('立即执行', async () => {
  await api.cron.run(j.id);                 // ← 无 try/catch
  toast('已加入执行队列，约 30s 内触发', 'success');
}, 'ghost'),
```

---

### BUG-13 实时日志的 WebSocket 在离开视图后不关闭，连接与内存持续泄漏

- **严重级**：P2
- **位置**：`web/js/views/logs.js:11`、`:19`、`:212-222`
- **现象**：`state` 与 `state.ws` 是**模块级**变量。`render()` 入口处调用 `stopTail()`（`logs.js:19`）确实能在"再次进入日志页"时关掉旧连接，但**离开日志页**（切到别的路由）时 `render()` 不会再执行 → WebSocket 保持 OPEN：
  - 服务端 `logmgr.Tail` 继续为该连接读文件、推送日志（`internal/api/misc.go:215-222`）；
  - 前端 `onmessage` 继续往 `lineBuf` 推（上限 1000 行，`logs.js:13`），并对已经脱离文档的 `tailBox` 反复 `clear()+appendChild()`（`logs.js:122-137`）→ 每次推送都重建最多 1000 个 DOM 节点。
  结果：只要开过一次实时跟踪再切走，后台就一直有一个长连接 + 高频 DOM 重建。
- **触发条件**：点击"开始实时跟踪" → 切到其它页面。
- **根因**：路由系统没有视图卸载钩子（`app.js` 的 `handleRoute` 只做 `clear(outlet)`），视图无法感知自己被卸载。`logs.js:16-18` 的注释已经意识到这一点，但只处理了"重新进入"这一半。
- **影响面**：连接数与 CPU 占用；反复进出日志页会叠加（每次进入 `stopTail()` 会关掉上一个，但离开后新建的那个仍然留着）。
- **修复方案**：在 `app.js` 的路由切换里给视图模块一个可选的 `unmount()` 导出，切换前调用；或在 `logs.js` 里监听 `hashchange`，在离开时 `stopTail()`。
- **证据**：
```js
// web/js/views/logs.js:11
const state = { siteId: '', type: 'access', page: 1, ws: null, tailing: false };
// web/js/views/logs.js:16-19  —— 注释承认没有卸载钩子
// 视图无卸载钩子，模块级 state 跨渲染存活。若不在这里收尾，…
stopTail();
```

---

### BUG-14 证书来源标签取值与后端枚举不符，显示原始英文

- **严重级**：P3
- **位置**：`web/js/views/certs.js:143-147`、`web/js/views/site-detail.js:91-95`
- **现象**：后端证书的 `source` 取值是 **`"letsencrypt"`**（ACME 签发，`internal/acme/acme.go:310`）和 **`"imported"`**（手动导入，`internal/acme/acme.go:373`）；`model.Cert.Source` 的注释也写着 `letsencrypt / manual / imported`（`internal/model/model.go:537`）。
  前端判断的是 `src === 'acme'` → "ACME 签发"、`src === 'import'` → "手动导入"（`certs.js:143-147`、`site-detail.js:91-95`）→ **两个分支都匹配不上**，界面直接显示英文原值 `letsencrypt` / `imported`。
- **触发条件**：证书列表页、站点详情的 SSL 标签页。
- **根因**：前后端对同一枚举用了两套字面量。
- **影响面**：界面显示不一致（中文界面里出现英文枚举），不影响功能。
- **修复方案**：前端改为 `if (src === 'letsencrypt') return 'ACME 签发'; if (src === 'imported' || src === 'manual' || src === 'import') return '手动导入';`；更稳妥的是后端统一枚举常量并由 `/api/dns-providers` 之类的方式下发。
- **证据**：
```js
// web/js/views/certs.js:143-147
function sourceLabel(src) {
  if (src === 'acme') return 'ACME 签发';      // ← 后端给的是 'letsencrypt'
  if (src === 'import') return '手动导入';     // ← 后端给的是 'imported'
  return src || '未知来源';
}
```
```go
// internal/acme/acme.go:310 / :373
Source: "letsencrypt",
Source: "imported",
```

---

### BUG-15 `request()` 的 `options` 参数从未被读取，`onProgress` 是死代码

- **严重级**：P3
- **位置**：`web/js/api.js:21`、`:182-187`；`web/js/views/site-files.js:255`、`:259`
- **现象**：`request(method, path, body, options = {})` 声明了 `options` 却**在整个函数体内一次都没有引用**。`api.sites.files.upload()` 把 `{ onProgress }` 传给 `http.post`，而 `http.post` 只是透传到 `request` —— 回调永远不会被调用。
  后果：文件上传过程中进度条永远停在"正在上传 N 个文件（xx MB）…"这一行静态文案，直到请求结束才跳到"已上传"；50MB 的文件上传期间界面看起来像卡死。
- **触发条件**：上传任何文件（尤其大文件）。
- **根因**：`fetch` 本身没有上传进度事件，需要改用 `XMLHttpRequest.upload.onprogress` 才能实现；`options.onProgress` 只是个未接线的占位。
- **影响面**：可用性（大文件上传无进度反馈）；同时是一处会误导后续开发者的"看起来已实现"的 API。
- **修复方案**：要么在 `request()` 里对带 `onProgress` 的请求改走 `XMLHttpRequest`；要么删掉 `onProgress` 参数并在 UI 上明确说明"上传中，请勿关闭页面"。
- **证据**：
```js
// web/js/api.js:21
async function request(method, path, body, options = {}) {   // ← options 之后再也没被引用
```
```js
// web/js/api.js:182-187
upload: (id, dir, files, onProgress) => {
  ...
  return http.post(`/sites/${encodeURIComponent(id)}/files/upload`, fd, { onProgress });
},
```

---

### BUG-16 `textInput` 不支持 `maxlength`，验证码输入框长度不受限

- **严重级**：P3
- **位置**：`web/js/components.js:163-172`；调用点 `web/js/app.js:256`、`web/js/views/settings.js:358`
- **现象**：`textInput(value, onInput, opts)` 只识别 `type` / `value` / `placeholder` / `disabled` 四个 opt，**没有 `maxlength`**，也没有把未知 opt 透传成属性（`h()` 里 `opts` 的其它键根本没进 props）。于是：
  - `app.js:256` 的验证码输入框 `{ maxlength: 6 }` 无效；
  - `settings.js:358` 的邮箱验证码输入框 `{ maxlength: 6 }` 同样无效。
  两处都另有 `/^\d{6}$/` 正则兜底，所以功能不受影响，只是用户可以粘贴 20 位字符再被前端拒绝。
- **触发条件**：在验证码框里输入超过 6 位。
- **根因**：`textInput` 的 opts 白名单缺 `maxlength`（以及 `min`/`max`/`pattern`/`autocomplete` 等）。
- **影响面**：轻微可用性；也是"组件能力与调用方预期不一致"的样板问题。
- **修复方案**：`textInput` 增加 `maxlength`（以及 `min`/`max`/`step`/`autocomplete`/`aria-label` 等常用属性）的透传。
- **证据**：
```js
// web/js/components.js:163-172
export function textInput(value, onInput, opts = {}) {
  return h('input', {
    class: 'input', type: opts.type || 'text',
    value: ..., placeholder: opts.placeholder || '',
    disabled: opts.disabled,
    oninput: (e) => onInput && onInput(e.target.value, e),
  });          // ← opts.maxlength 从未被使用
}
```

---

### BUG-17 使用了不存在的图标名，静默退化成 `info` 图标

- **严重级**：P3
- **位置**：`web/js/views/site-files.js:118`（`folder`）、`web/js/views/site-create.js:400`（`copy`）、`web/js/views/site-detail.js:384`（`unlink`）
- **现象**：`icon(name)` 在 `ICONS` 表里找不到名字时**静默回退**到 `ICONS.info`（`components.js:96-97`：`const d = ICONS[name] || ICONS.info;`）。
  `ICONS` 表（`components.js:63-94`）里没有 `folder` / `copy` / `unlink`：
  - "新建目录"按钮显示为一个 ℹ️ 图标（`site-files.js:118`）；
  - "复制到剪贴板"按钮显示 ℹ️（`site-create.js:400`）；
  - "解除绑定"按钮显示 ℹ️（`site-detail.js:384`）。
- **触发条件**：打开文件管理 / 新建站点的密码弹窗 / 站点详情 SSL 标签页。
- **根因**：图标名未与 `ICONS` 表对齐，且回退是静默的。
- **影响面**：界面观感与可理解性；「复制到剪贴板」这类关键动作失去视觉标识。
- **修复方案**：补 3 个 SVG path 到 `ICONS`；或让 `icon()` 在名字不存在时 `console.warn`（便于发现此类漂移）。
- **证据**：
```js
// web/js/components.js:96-97
export function icon(name, size = 16) {
  const d = ICONS[name] || ICONS.info;   // ← 静默回退
```

---

### BUG-18 `ops.js` 的 `reload()` 在 hash 变化时会触发两次渲染

- **严重级**：P3
- **位置**：`web/js/views/ops.js:244-248`
- **现象**：`reload()` 先 `location.hash = '#/ops/' + currentTab()`，再无条件 `window.dispatchEvent(new HashChangeEvent('hashchange'))`。
  当 hash 确实发生变化时（例如从 `#/ops/tokens` 切到 `#/ops/audit`），浏览器**本来就会**异步派发一次 `hashchange` → `handleRoute` 跑两遍 → 同一标签页的接口被请求两次、视图渲染两次。
  当前 `reload()` 的两个调用点（创建 Token `ops.js:198`、吊销 Token `ops.js:234`）都发生在 hash 未变的场景，所以当前没有实际的双请求；但这是埋着的坑，任何在 `reload()` 前切 tab 的改动都会立刻踩到。
- **触发条件**：在 `reload()` 之前让 hash 发生变化。
- **根因**：用"手动派发事件"来补偿 hash 未变的情况，但没判断 hash 是否真的变了。
- **影响面**：潜在的重复请求 / 重复渲染（当前未触发）。
- **修复方案**：`if (location.hash === target) { window.dispatchEvent(new HashChangeEvent('hashchange')); } else { location.hash = target; }`。
- **证据**：
```js
// web/js/views/ops.js:244-248
function reload() {
  location.hash = `#/ops/${currentTab()}`;
  window.dispatchEvent(new HashChangeEvent('hashchange'));   // ← hash 变了就会双触发
}
```

---

### BUG-19 `state.dbs` 永远为空（后端 `/api/system/mysql` 无 `databases` 字段）

- **严重级**：P3
- **位置**：`web/js/views/backups.js:19`、`:23`
- **现象**：`backups.js:19` 用**裸 `fetch`**（绕开了 `api.js` 的统一封装）请求 `/api/system/mysql`，然后取 `dbres?.databases`。
  但后端 `mysql.Status` 只有 `installed / running / version / server / socket` 五个字段（`internal/mysql/mysql.go:41-47`），**没有 `databases`** → `state.dbs` 恒为 `[]`。
  代码里有一行自认的注释"v0.2.0 暂未实现 /api/dbs 列表"，但 `state.dbs` 之后**从未被使用**——"备份数据库"弹窗用的是让用户手填库名的 `textInput`（`backups.js:167`）。所以这是一段彻底的死代码 + 一次多余的网络请求。
- **触发条件**：每次打开备份页。
- **根因**：前端臆造了一个后端不存在的字段。
- **影响面**：死代码 + 每次进备份页多一次请求；掩盖了"备份数据库无法选择库名"这一真实的功能缺口（见第四节）。
- **修复方案**：删除 `state.dbs` 与那段裸 `fetch`；若要提供库名选择，需后端先提供 `GET /api/dbs`（或扩展 `/api/system/mysql` 返回库列表）。
- **证据**：
```js
// web/js/views/backups.js:19, 23
fetch('/api/system/mysql', { credentials: 'same-origin' }).then((r) => r.json()).catch(() => null),
state.dbs = (dbres?.databases) || [];   // ← 后端没有 databases 字段
```

---

### BUG-20 站点列表的跨页勾选状态残留

- **严重级**：P3
- **位置**：`web/js/views/sites.js:11`、`:171-178`
- **现象**：`state.selected` 是**模块级** `Set`，不随视图销毁重置。用户在 A 站勾选了 3 个 → 切到别的页面 → 再回到站点列表 → `drawBatchBar()`（`sites.js:303`）发现 `state.selected.size > 0` → **批量操作条带着 3 个旧选择重新出现**，对应复选框也是勾上的。
  站点在别处被删除后，残留的 id 会让 `/api/sites/batch` 返回逐条 error（前端只显示 `ok_count`，逐条错误被吞掉，见第四节）。
- **触发条件**：勾选站点后离开页面再回来。
- **根因**：视图状态放在模块作用域且无生命周期管理。
- **影响面**：误操作风险（用户可能对"以为没选"的站点执行批量停止）。
- **修复方案**：在 `render()` 开头 `state.selected.clear()`（与 `logs.js:19` 对 `state.ws` 的处理方式一致），或在批量操作前剔除已不存在的 id。

---

### BUG-21 站点告警规则类型与后端检查器对不上：多数规则永远不触发，能触发的两种又选不到

- **严重级**：P1
- **位置**：`web/js/views/site-detail.js:42-50`（`ALERT_TYPES`）、`web/js/views/site-detail.js:662`（下拉渲染）、`:672`（阈值说明文案）
- **现象**：三方的"告警规则类型"枚举互不相同：

  | 来源 | 支持的 type 取值 |
  |---|---|
  | 前端下拉 `site-detail.js:42-50` | `status_5xx`、`status_4xx`、`traffic_quota`、`cert_expire`、`file_tamper`、`site_down`、`response_time` |
  | 后端**真正执行规则**的检查器 `internal/alert/checker.go` | `status_5xx`(:88)、`traffic_quota`(:127)、`cert_expiry`(:168)、`site_down`(:189)、`tamper`(:254)、`nginx_config`(:53) |
  | 后端 `model.AlertRule.Type` 注释 `internal/model/model.go:487` | `status_5xx / traffic_quota / cert_expiry / site_down / tamper / disk` |

  前后端交集只有 `status_5xx`、`traffic_quota`、`site_down` **三种**。因此：
  - 用户在下拉里选的 `status_4xx`、`cert_expire`、`file_tamper`、`response_time` **共 4 种规则，保存时不报错，但检查器根本不认识这些 type，规则永远不会触发**；
  - 其中 `cert_expire` 与后端的 `cert_expiry`、`file_tamper` 与后端的 `tamper` 只差一个词，属于纯粹的字面量不一致（拼写错 → 静默失效，最难发现的一类）；
  - 反过来，后端支持的 `cert_expiry`（证书临期）、`tamper`（文件篡改）、`nginx_config`（配置检测失败）**前端下拉里压根没有**，用户无法配置这两类真正有用的告警；
  - `site-detail.js:672` 的说明文案还写着"5xx/4xx 为窗口内错误数上限，…响应超时为秒"，为两个根本不会生效的类型承诺了语义。
- **触发条件**：用户配置站点级告警规则并等待触发。
- **根因**：前端把告警类型写死成一份本地常量，没有从后端获取（后端也没有提供 `/api/alert-types` 这类枚举接口）。
- **影响面**：**告警功能形同虚设**——用户以为配了"证书临期告警"或"文件篡改告警"，实际永远不会收到通知，且没有任何界面提示"该规则类型不被支持"。这是典型的"前端放行、后端静默忽略"，正是本类审计要抓的那类缺陷。
- **修复方案**：① 把前端 `ALERT_TYPES` 改为后端实际支持的集合（`status_5xx` / `traffic_quota` / `cert_expiry` / `site_down` / `tamper` / `nginx_config`），并把 `cert_expire`→`cert_expiry`、`file_tamper`→`tamper`；② 更彻底的做法是后端新增 `GET /api/alert-types` 返回 `{value,label,description,params:[...]}`，前端动态渲染（与 `/api/dns-providers` 的既有模式一致，见 `api.js:210`）；③ 后端在 `SaveSite` 时校验规则 type，非法 type 直接 400，而不是静默保存。
- **证据**：
```js
// web/js/views/site-detail.js:42-50
const ALERT_TYPES = [
  { value: 'status_5xx', label: '5xx 错误数' },
  { value: 'status_4xx', label: '4xx 错误数' },      // ← 检查器没有这个 type
  { value: 'traffic_quota', label: '流量超额' },
  { value: 'cert_expire', label: '证书临期' },      // ← 后端是 cert_expiry
  { value: 'file_tamper', label: '文件被篡改' },    // ← 后端是 tamper
  { value: 'site_down', label: '站点停止' },
  { value: 'response_time', label: '响应超时' },    // ← 检查器没有这个 type
];
```
```go
// internal/alert/checker.go:168 —— 证书临期检查器认的是 cert_expiry
if rule.Type != "cert_expiry" || !rule.Enabled { continue }
// internal/alert/checker.go:254 —— 篡改检查器认的是 tamper
if rule.Type == "tamper" && rule.Enabled { ... }
```

---

## 二、XSS 与注入

### 2.1 汇总结论

**在 `web/` 全目录内未发现任何可利用的 XSS / DOM 注入 sink。**
用正则对整个 `web/` 目录扫描 `innerHTML` / `outerHTML` / `insertAdjacentHTML` / `document.write` / `eval` / `new Function` / `srcdoc` / `javascript:` / 字符串形式的 `setTimeout` / `setInterval`，**命中数为 0**（唯二命中是 `app.js:46` 与 `components.js:10` 的中文注释）。

根因是架构性的：`components.js:13-31` 的 `h()` **刻意不提供 `html` 属性**，全部字符串子节点经 `appendAll` → `document.createTextNode(String(c))`（`components.js:37`）插入；字符串属性只经 `setAttribute`（`components.js:25`）。因此**服务端可控数据（站点名、域名、文件名、日志行、证书名、告警内容、Token 名、上传文件名等）即使含 `<script>` / `onerror=`，在 DOM 里也只是纯文本**。

### 2.2 逐条 sink 清单

| 位置 | sink（写入方式） | 数据来源 | 是否可控 | 结论 |
|---|---|---|---|---|
| `web/js/components.js:37` | `document.createTextNode(String(c))` | 任意字符串子节点（服务端数据、用户输入、日志行、nginx 配置、PEM） | **可控**（如站点名可含 `<img onerror>`） | **安全**。文本节点不解析标签，`<`/`&` 被转义 |
| `web/js/components.js:19` | `el.textContent = v`（props.text） | 视图内的静态文案 | 低 | 安全 |
| `web/js/components.js:596` | `h('code', {}, text)` → 文本节点 | nginx 配置预览 / 主配置 | 可控（管理员自己写） | 安全，无 HTML 解析 |
| `web/js/components.js:641`、`:690` | SVG `el.textContent = text`（图表轴标签、tooltip） | 统计日期 / 数值 | 低 | 安全 |
| `web/js/components.js:25` | `el.setAttribute(k, v)`（通用属性兜底） | 属性名与值均来自前端源码常量 | 不可控 | 安全（无动态属性名/动态 `href`） |
| `web/js/components.js:20` | `Object.assign(el.style, v)` | 前端常量（`maxHeight`/`display`/`width`…） | 不可控 | 安全 |
| `web/js/components.js:141`、`:282` 等 | `tag(text, type)` → 文本节点 + `class="tag tag-${type}"` | `type` 来自前端枚举；`text` 来自服务端 | text 可控 / type 不可控 | 安全（class 注入不构成 XSS） |
| `web/js/app.js:163` | `href: item.hash` | 前端常量 `'#/dashboard'`… | 不可控 | 安全 |
| `web/js/app.js:287`、`:298`；`site-create.js:51`；`site-detail.js:133`；`site-files.js:149`/`:156`/`:174` | `href: '#'` / `'#/sites'` | 前端常量 | 不可控 | 安全（`site-files.js` 的 3 处还额外 `preventDefault()`） |
| `web/js/views/alerts.js:97` | `href: \`#/sites/${encodeURIComponent(a.site_id)}\`` | 服务端 `site_id`（自增 ID） | 不可控（且已 encodeURIComponent） | 安全 |
| `web/js/views/backups.js:108` | `window.open(\`/api/backups/${encodeURIComponent(b.id)}/download\`)` | 服务端生成的备份 id | 不可控（且已 encode） | 安全 |
| `web/js/app.js:368`、`:528`、`site-create.js:381`、`sites.js:194`、`ops.js:72`/`:245` | `location.hash = …` | 常量或 encode 后的 id | 不可控 | 安全（hash 变更不会执行脚本） |
| 全目录 | `innerHTML` / `outerHTML` / `insertAdjacentHTML` / `document.write` / `eval` / `new Function` / `srcdoc` / `javascript:` URL | — | — | **未发现**（grep 命中 0） |

### 2.3 需要说明的"接近 sink"的点

| 位置 | 说明 |
|---|---|
| `web/js/components.js:22`、`:29` | `h()` 的 `value` 属性被**延迟**到子节点挂载后赋值（`el._value`）。这是正确做法（否则 `<option>`/`<select>` 会丢值），不构成注入 |
| `web/js/components.js:198` | `selectInput` 用 `h('option', {value, selected})`，`selected` 走 `el[k] = !!v` 分支，不会被当成 HTML 属性 |
| `web/js/components.js:432-435` | `toast()` 的 message 是文本节点，服务端错误文案（含 nginx stderr、路径）不会变成 HTML |
| `web/js/api.js:56` | 非 JSON 错误响应会把响应体前 400 字符塞进 `message`，最终经 `toast`/`errBox` 的**文本节点**展示，不会执行 |
| `web/js/views/site-files.js:180` | 文件名 `e.name`（服务端 `ListFiles` 结果，可能含 `<`/`>`）作为文本节点插入 → 安全 |

### 2.4 防御纵深建议（非缺陷）

1. **无 Content-Security-Policy**：`main.go:589-599` 的 `securityHeaders` 只设了 `X-Content-Type-Options` / `X-Frame-Options` / `Referrer-Policy` / `HSTS`。虽然前端构造上无 XSS sink，但补一条 `Content-Security-Policy: default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'` 可把"未来有人加了个 `innerHTML`"变成可被审计发现的失败，而不是静默上线。（P3）
2. `main.go:569-578` 的 **SPA 回退**会对任何未匹配路径返回 `index.html`（`Content-Type: text/html`）。`/api/*` 由 `mux.Handle("/api/", protected)` 拦截（`server.go:94`），不会落到回退；但任何拼错的、非 `/api/` 前缀的接口调用会拿到一整页 HTML —— `api.js:56` 会把这段 HTML 当错误文案塞进 toast。建议 `staticHandler` 对 `/api/` 前缀直接 404。（P3）

---

## 三、前后端 API 契约对照

后端路由来源：`internal/api/server.go:32-94`（注册表）、`internal/api/misc.go`、`files.go`、`certs.go`、`cron.go`、`backup.go`、`tokens.go`、`audit.go`、`batch.go`、`account.go`、`ops.go`。

### 3.1 前端调用 → 后端路由（全量）

| # | 前端调用（method + path） | 前端位置 | 后端路由 | 一致？ | 不一致的影响 |
|---|---|---|---|---|---|
| 1 | POST `/api/auth/login` `{username,password}` | api.js:114 | server.go:32 → `handleLogin` | ✅ | body 与 `loginRequest`(auth.go:116) 一致 |
| 2 | POST `/api/auth/logout` | api.js:115 | server.go:33 | ✅ | 后端忽略 body |
| 3 | GET `/api/auth/me` | api.js:116 | server.go:34 → `handleMe` | ✅ | 恒 200 + `{authenticated}`，前端 `app.js:563` 按此判定 ✅ |
| 4 | POST `/api/auth/password` `{old_password,new_password}` | api.js:117-118 | server.go:35 | ✅ | 与 `passwordRequest`(auth.go:122) 一致 |
| 5 | POST `/api/auth/verify-challenge` `{challenge_id,code}` | api.js:121-122 | server.go:36 | ✅ | 复用于邮箱验证码；`service.VerifyChallenge` 按 purpose 触发 `MarkEmailVerified`（service/account.go:197）✅ |
| 6 | POST `/api/auth/password-reset/request` `{username}` | app.js:456 | server.go:37 | ⚠️ | **路由与 body 都对，但 `http` 未 import → 运行时 ReferenceError**（BUG-02） |
| 7 | POST `/api/auth/password-reset/confirm` `{challenge_id,username,code,new_password}` | app.js:501-506 | server.go:38（auth.go:344） | ⚠️ | 同上 BUG-02。body 字段名与 `handlePasswordResetConfirm`(auth.go:349) 完全一致 |
| 8 | GET `/api/admin/account` | api.js:127 | server.go:84 → `handleGetAccount` | ✅ | 返回 `{username,role,email,email_masked,email_verified,last_login_ip,last_login_ua,last_login_at,created_at}`，前端 `settings.js:321-335` 逐字段用到且名称一致 ✅ |
| 9 | PUT `/api/admin/account/email` `{email}` | api.js:128 | server.go:90 → `handleSetEmail` | ✅ | body 一致；未登录时后端还接受 `password`，前端不传（已登录场景）✅ |
| 10 | POST `/api/admin/account/email/verify` | api.js:129 | server.go:85 | ✅ | 返回 `{ok,challenge_id}`，前端 `settings.js:341-344` 用 `r.challenge_id` ✅ |
| 11 | GET `/api/admin/smtp` | api.js:132 | server.go:91 → `handleGetOrSetSMTP` | ✅ | 返回**顶层**配置对象，前端 `settings.js:414-425` 按顶层读 ✅ |
| 12 | PUT `/api/admin/smtp` `{host,port,user,password,from,from_name,encryption}` | api.js:133 | server.go:91 (PUT 分支) | ✅ | 与 `service.SMTPConfig`(service/account.go:356-364) json tag 完全一致；空密码=保留原值（account.go:152）✅ |
| 13 | POST `/api/admin/smtp/test` `{to}` | api.js:134 | server.go:86 → `handleTestSMTP` | ✅ | body 一致 ✅ |
| 14 | GET `/api/sites` | api.js:144 | server.go:46 → `handleSites` | ⚠️ | 路由一致，但 **`unwrap()` 丢弃 `today_reqs/today_bytes/month_bytes/open_alerts/cert_days`** → 前端恒为 0（BUG-01） |
| 15 | GET `/api/system/info` | api.js:149 | server.go:58 → `handleSystemInfo` | ⚠️ | 路由存在，但**全前端无任何调用点**（`api.sites.systemInfo` 是死代码）→ `public_port` / `disable_ipv6` 前端拿不到 |
| 16 | GET `/api/sites/{id}` | api.js:150 | server.go:47 → `handleSiteByID` action="" | ✅ | 返回 `model.Site`；前端 `site-detail.js:107` 整体接收 ✅ |
| 17 | POST `/api/sites` `{site…}` | api.js:151 | server.go:46 (POST) | ⚠️ | 三种响应：201 `{site,show_password}` / 202 `{site,warning}` / 201 `Site`。前端 `site-create.js:375-381` 兼容了前两种的 id 取法，但**丢弃 `warning`**（BUG-10） |
| 18 | PUT `/api/sites/{id}` `{site…}` | api.js:152 | server.go:47 (PUT) | ⚠️ | 200 返回 `Site`，202 返回 `{site,warning}`；前端 `site-detail.js:725` 丢弃 `warning`（BUG-10） |
| 19 | DELETE `/api/sites/{id}` | api.js:153 | server.go:47 (DELETE) | ✅ | 返回 `{ok:true}` ✅ |
| 20 | POST `/api/sites/{id}/toggle` `{status}` | api.js:154 | server.go:209 | ✅ | `status` 取值 `running`/`stopped` 与 `model.SiteStatus`(model.go:17-18) 一致 ✅ |
| 21 | POST `/api/sites/{id}/reload` | api.js:155 | server.go:223 | ✅ | ✅ |
| 22 | GET `/api/sites/{id}/config` | api.js:156 | server.go:230 | ✅ | 返回 `{site_conf,http_conf,nginx_test:{ok,message}}`，前端 `site-detail.js:781-791` 字段名一致 ✅ |
| 23 | GET `/api/sites/{id}/logs?type&limit&page` | api.js:157-158 | server.go:242 → `handleSiteLogs` | ⚠️ | 参数名一致；**`total` 语义错误**导致分页只有 1 页（BUG-08）。错误日志分支不返回 `total`（misc.go:152-155） |
| 24 | GET `/api/sites/{id}/stats?days=` | api.js:159 | server.go:245 | ✅ | 返回 `{stats:[TrafficStat]}`，`date/requests/bytes/status_2xx…` 与 `model.TrafficStat`(model.go:635-645) 一致，`dashboard.js:114-121` 对得上 ✅ |
| 25 | POST `/api/system/logs/tail?site_id=` | api.js:163 | server.go:63 → `handleTail` | ✅ | 返回 `{ws_url}`，前端 `logs.js:166` 用 `res.ws_url` ✅ |
| 26 | GET `/api/sites/{id}/files?path=` | api.js:168-170 | files.go:85 → `fileList` | ✅ | 返回 `{entries,path}`；前端 `site-files.js:290` 用 `res.entries` ✅ |
| 27 | GET `/api/sites/{id}/files/raw?path=` | api.js:172 | files.go:95 → `fileRaw` | ⚠️ | **双形态响应**（text/plain 裸文本 vs JSON base64），`api.js` 用 JSON.parse 猜测 → 合法 JSON 内容被误解析 → 编辑器空白 → 保存清空文件（BUG-04） |
| 28 | PUT `/api/sites/{id}/files?path=` `{content,encoding:'base64'}` | api.js:175-177 | files.go:116 → `fileWrite` | ✅ | body 与 `files.go:124-127` 一致 ✅ |
| 29 | DELETE `/api/sites/{id}/files?path=` | api.js:178 | files.go:161 | ✅ | ✅ |
| 30 | POST `/api/sites/{id}/files/rename` `{from,to}` | api.js:179 | files.go:174 | ✅ | 字段名一致 ✅ |
| 31 | POST `/api/sites/{id}/files/mkdir` `{path}` | api.js:180 | files.go:194 | ✅ | ✅ |
| 32 | POST `/api/sites/{id}/files/upload` multipart `path`+`file` | api.js:182-187 | files.go:221 | ✅ | 字段名一致；`onProgress` 不被消费（BUG-15） |
| 33 | POST `/api/sites/{id}/files/unzip` `{zip_path,dest}` | api.js:191-192 | files.go:332 | ✅ | 字段名一致；返回 `{ok,extracted,dest}`，前端 `site-files.js:268` 用 `r.extracted` ✅ |
| 34 | GET `/api/certs` | api.js:198 | server.go:48 → `handleCerts` | ✅ | 返回 `{certs}`；`unwrap` 对 certs 不生效（Cert 无 `site` 字段）✅ |
| 35 | POST `/api/certs/issue` `{domains,site_id,account_id,method,dns_provider,dns_config}` | certs.js:424-431 | server.go:49 → `handleCertIssue` | ⚠️ | 字段名一致；但前端**从不发送 `auto_renew`**（后端 `issueRequestBody.AutoRenew`，certs.go:33）→ ACME 证书永远不自动续签，且 UI 无开关 |
| 36 | POST `/api/certs/{id}/revoke` | api.js:200 | server.go:51 → action `revoke` | ✅ | ✅ |
| 37 | POST `/api/certs/import` `{name,domains,cert_pem,key_pem}` | certs.js:491-496 | server.go:50 → `handleCertImport` | ✅ | 与 `importRequestBody`(certs.go:264-269) 一致 ✅ |
| 38 | GET `/api/certs/{id}/download` | api.js:202 | server.go:51 → action `download` | ✅ | 返回 PEM 文本（`Content-Type: application/x-pem-file`），`api.js` 回退成字符串，前端 `certs.js:598-603` 按字符串处理 ✅ |
| 39 | GET `/api/acme/accounts` | api.js:203 | server.go:52 | ✅ | 返回 `{accounts}`，字段 `id/email/ca/key_type/created_at` 与 `model.ACMEAccount`(model.go:593-601) 一致 ✅ |
| 40 | POST `/api/acme/accounts` `{email,ca,key_type}` | certs.js:268 | server.go:52 (POST) | ✅ | ✅ |
| 41 | POST `/api/certs/{id}/bind` `{site_id}` | api.js:206 | server.go:51 → action `bind` | ✅ | ✅ |
| 42 | GET `/api/dns-providers` | api.js:210 | server.go:92 → `handleDNSProviders` | ✅ | 返回 `{providers:[{key,name,fields:[{key,label}]}]}`，与 `acme.DNSProviderInfos`(acme/dns.go:114-128) 一致，前端 `certs.js:329-349` 对得上 ✅ |
| 43 | GET `/api/alerts?limit=` | api.js:214 | server.go:53 → `handleAlerts` | ✅ | 返回 `{alerts}`，`model.Alert`(model.go:604-614) 的 `site_id/site_name/type/level/message/created_at/resolved` 与前端一致 ✅ |
| 44 | POST `/api/alerts/{id}/resolve` | api.js:215 | server.go:55 → `handleAlertByID` | ✅ | ✅ |
| 45 | POST `/api/alerts/test` `{channel:{type,target}}` | api.js:216 / alerts.js:246 | server.go:54 → `handleAlertTest` | ✅ | 与 `model.AlertChannel`(model.go:496-500) 一致；`Enabled` 缺省 false 不影响（`engine.go:124` 只看 Type）✅ |
| 46 | GET `/api/cron/jobs` | api.js:221 | server.go:66 | ✅ | 返回 `{jobs}` ✅ |
| 47 | GET `/api/cron/jobs/{id}` | api.js:222 | server.go:67 → `handleCronJobByID` | ✅ | 返回 `{job,runs}`，前端 `cron.js:308-309` 一致 ✅ |
| 48 | POST `/api/cron/jobs` `{name,type,schedule,target,payload,enabled}` | api.js:223 / cron.js:270-277 | server.go:66 (POST) | ✅ | 与 `cron.go:35-42` 一致 ✅ |
| 49 | PUT `/api/cron/jobs/{id}` | api.js:224 | server.go:67 (PUT) | ✅ | **后端是部分更新**（指针字段，`cron.go:104-141`），前端 `cron.js:99` 只传 `{enabled}` 不会清空其它字段 ✅ |
| 50 | DELETE `/api/cron/jobs/{id}` | api.js:225 | server.go:67 (DELETE) | ✅ | ✅（前端无 try/catch，见 BUG-12） |
| 51 | POST `/api/cron/jobs/run` `{job_id}` | api.js:226 | server.go:68 → `handleCronJobRun` | ✅ | ✅（前端无 try/catch，见 BUG-12） |
| 52 | GET `/api/backups?kind&target&limit` | api.js:231-237 | server.go:69 → `handleBackups` | ✅ | 参数名与 `backup.go:29-31` 一致 ✅ |
| 53 | POST `/api/backups` `{kind,target,note}` | api.js:239-240 | server.go:69 (POST) | ✅ | 与 `backup.go:42-46` 一致 ✅ |
| 54 | DELETE `/api/backups/{id}` | api.js:241 | server.go:70 → `handleBackupByID` | ✅ | ✅（前端无 try/catch） |
| 55 | POST `/api/backups/{id}/restore` | api.js:242 | server.go:70 → action `restore` | ✅ | 返回 `{ok,preview,preview_bytes}`，前端 `backups.js:119-120` 一致 ✅ |
| 56 | GET `/api/backups/{id}/download`（`window.open`） | backups.js:108 | server.go:70 → action `download` | ✅ | ✅ |
| 57 | POST `/api/sites/batch` `{action,ids}` | api.js:246 | server.go:71 → `handleSitesBatch` | ✅ | 返回 `{results,ok_count}`，前端 `sites.js:293` 用 `r.ok_count` ✅；**逐条 `results[i].error` 被前端丢弃**（部分失败只有计数可见） |
| 58 | GET `/api/settings` | api.js:250 | server.go:56 → `handleSettings` | ✅ | 返回 `{settings}` ✅ |
| 59 | PUT `/api/settings` `{settings:{k:string}}` | api.js:251 | server.go:56 (PUT) | ✅ | api.js 自动包一层 `{settings}` ✅；值必须为 string（后端 `map[string]string`），`settings.js:160` 统一 `String()` ✅；`alerts.js:229` 存 JSON 字符串 ✅ |
| 60 | GET `/api/system/status` | api.js:254 | server.go:57 → `handleSystemStatus` | ✅ | `model.SystemStatus`(driver.go:52-58)：`nginx{installed,running,version,config_ok,error}` / `php[]` / `runtime` / `docker{available,containers[]}` / `disk{total,used,free}` ✅ |
| 61 | GET `/api/system/mysql` | api.js:257 / backups.js:19 | server.go:59 → `handleMySQLStatus` | ⚠️ | 返回 `mysql.Status{installed,running,version,server,socket}`；**无 `databases` 字段** → `backups.js:23` 恒空（BUG-19）。`site-create.js:279-296` 用 `st.installed/st.running/st.server/st.version` ✅ |
| 62 | GET `/api/system/nginx-config` | api.js:258 | server.go:60 → `handleNginxConfig` | ✅ | 返回 `{config}`，`settings.js:122` 读 `conf.config` ✅ |
| 63 | POST `/api/system/nginx-test` | api.js:259 | server.go:61 → `handleNginxTest` | ✅ | 返回 `{ok,message}`，`settings.js:138-142` 一致 ✅ |
| 64 | GET `/api/system/metrics` | api.js:262 | server.go:77 → `handleMetrics` | ✅ | 返回 `{current,history}`，`dashboard.js:305-368` 用的 `cpu_percent/mem_percent/disk_percent/cpu_count/load1/net_rx_bps/...` 与 `svc.Metrics()` 一致 ✅ |
| 65 | GET `/api/system/config-history` | api.js:265 | server.go:78 | ✅ | 返回 `{history}`，`ops.js:88` 一致 ✅ |
| 66 | GET `/api/system/config-history/{id}` | api.js:266 | server.go:79 → `showConfigSnapshot` | ✅ | 返回 `{snapshot,contents}`，`ops.js:347` 用 `r.contents` ✅ |
| 67 | GET `/api/system/config-history/{id}/diff` | api.js:267 | server.go:79 → `diffConfigSnapshot` | ✅ | 返回 `{diffs}`，`model.FileDiff`(confhistory.go:288-299) 的 `name/lines[].op/lines[].text/added/removed/identical/old_missing/new_missing` 与 `ops.js:376-391` 逐字段一致 ✅ |
| 68 | POST `/api/system/config-history/{id}/restore` | api.js:268 | server.go:79 → `restoreConfigSnapshot` | ✅ | 返回 `{restored,message}`，`ops.js:414` 用 `r.restored` ✅ |
| 69 | GET `/api/tokens` | api.js:275 | server.go:74 → `handleTokens` | ✅ | 返回 `{tokens}`，`model.APIToken`(model/ops.go:22-38) 字段 `id/name/prefix/scopes/created_by/created_at/last_used_at/expires_at` 与 `ops.js:118-145` 一致 ✅ |
| 70 | POST `/api/tokens` `{name,scopes,expires_in_days}` | api.js:276 / ops.js:191-195 | server.go:74 (POST) | ✅ | 与 `createTokenRequest`(tokens.go:53-59) 一致；响应 `{token:{...},show_token}`，前端 `showTokenSecret(r.token)` 读 `r.token.token` = 明文 ✅ |
| 71 | DELETE `/api/tokens/{id}` | api.js:277 | server.go:75 → `handleTokenByID` | ✅ | 后端返回 **204**，`api.js:46` 专门处理了 `res.status === 204` ✅ |
| 72 | GET `/api/audit-logs?limit&action` | api.js:282-287 | server.go:76 → `handleAuditLogs` | ✅ | 返回 `{logs,total,limit}`，`ops.js:83-85` 一致 ✅；`action` 过滤参数后端支持但**前端无调用点** |

### 3.2 后端有、前端未使用

| 后端路由 | 位置 | 缺失的前端能力 |
|---|---|---|
| POST `/api/system/config-apply` | server.go:62 → `handleConfigApply` | 无"一键下发全部配置"按钮；只能靠改单个站点/保存设置间接触发 |
| POST `/api/sites/{id}/baseline` | server.go:262 | "防复改"标签页只能配置哈希锁定，**无法建立基线**，也就无法开启篡改检测 |
| POST `/api/sites/{id}/tamper` | server.go:275 | **无法查看篡改检测结果**（`modified/added/deleted/total`）；"防复改"页只有开关没有结果 |
| GET `/api/certs/{id}/renew` | certs.go:179 | 无"手动续签"按钮 |
| DELETE `/api/certs/{id}/delete` | certs.go:228 | 证书只能"吊销"，没有"仅删除记录"的动作 |
| GET `/api/certs/{id}` | certs.go:171 | 前端只用列表，不取单条 |
| GET `/api/system/info` | server.go:58 | **前端已定义 `api.sites.systemInfo()` 却无任何调用点**（死代码）；`public_port` / `disable_ipv6`（全局 `-public-port` 模式、IPv6 开关）在界面上完全不可见 |

### 3.3 契约层面的其它不一致（散点汇总）

| 位置 | 不一致 |
|---|---|
| `web/js/views/site-detail.js:108` | `status.php.versions` ≠ 后端 `status.php` 是数组（BUG-07） |
| `web/js/views/certs.js:143`、`site-detail.js:91` | `source` 取值 `acme`/`import` ≠ 后端 `letsencrypt`/`imported`（BUG-14） |
| `web/js/views/backups.js:23` | `databases` 字段后端不存在（BUG-19） |
| `web/js/views/site-detail.js:240` | `edate` 前端引导 `YYYY-MM-DD` ≠ 后端 `*time.Time` 要求 RFC3339（BUG-09） |
| `web/js/api.js:82-86` vs `api.js:105-109` | `unwrap()` 与 `flattenSiteSummary()` 语义冲突，汇总指标被吞（BUG-01） |
| `web/js/api.js:21` | `options`（含 `onProgress`）被声明但从未使用（BUG-15） |
| `web/js/views/backups.js:19` | 唯一一处绕过 `api.js` 的裸 `fetch`（不走统一错误/401 处理） |
| `web/js/views/site-detail.js:42-50` vs `internal/alert/checker.go` | **告警规则 type 枚举三方不一致**：前端 7 种 vs 检查器 6 种，仅 3 种交集（BUG-21） |
| `web/js/views/site-detail.js:47`（`cert_expire`）vs `checker.go:168`（`cert_expiry`） | 拼写不一致 → 证书临期规则静默失效 |
| `web/js/views/site-detail.js:48`（`file_tamper`）vs `checker.go:254`（`tamper`） | 命名不一致 → 篡改告警规则静默失效 |

---

## 四、功能残缺与简陋项

1. **全局告警永远不会外发通知**（P2）。`alerts.js:157-238` 的"通知渠道"把渠道存进面板设置键 `alert_channels_json`，但**后端全库不读这个键**（已 grep 确认，仅存在于前端与 `docs/`）。告警引擎只使用**站点级** `site.Alert.Channels`（`internal/alert/engine.go:83`）。同时 `Engine.NotifyGlobal`（`engine.go:99-109`）明确"只落库不通知"。结果：cron 失败、备份失败这类全局告警只出现在"告警"页列表里，永远不会发邮件/webhook；"发送测试"能用只是因为 `/alerts/test` 把渠道内联传了过去。
2. **站点级告警规则也基本失效**（P1，BUG-21）：前端下拉给的 7 种 type 里只有 3 种被后端检查器实现，另外 4 种保存后永不触发；后端支持的 `cert_expiry` / `tamper` / `nginx_config` 又选不到。
2. **证书自动续签完全没接上**（P2）。后端 `model.Cert.AutoRenew` 存在（`model.go:568`），`POST /api/certs/issue` 也接受 `auto_renew`（`certs.go:33`），但前端申请时不发送该字段（`certs.js:424-431`），UI 也没有开关 → 列表里"自动续签"列恒为"未开启"，通配符证书到期后无人续签。
3. **"防复改"功能只有壳**（P2）。站点详情的防篡改标签页只能配置受保护文件列表与策略，但后端的 `POST /api/sites/{id}/baseline`（建立基线）与 `POST /api/sites/{id}/tamper`（查看篡改结果）**前端都没有调用** → 用户无法看到任何篡改检测结论。
4. **站点/数据库备份不能选对象**（P2）。站点备份可以下拉选站点，但数据库备份只能手填库名（`backups.js:167`）；后端也没有"列出数据库"的接口（`state.dbs` 恒空，BUG-19）。
5. **上传没有进度**（P2）。`onProgress` 是死代码（BUG-15），50MB 文件上传期间界面完全静止。
6. **历史日志只能看最后 100 条**（P2，BUG-08）。
7. **仪表盘与列表的流量/告警指标恒为 0**（P1，BUG-01）——这是本项目最影响日常使用的功能缺陷。
8. **无"重新下发全部配置"入口**（P3）：后端有 `/api/system/config-apply`，前端无按钮。
9. **审计日志无法按 action 过滤**（P3）：后端支持 `?action=`，前端 `api.audit.list(limit, action)` 的第二个参数无人使用（`ops.js:83` 只传了 limit），UI 也没有过滤控件。
10. **无"全部配置搜索"**（P3）：`site-detail.js` 有 20 个标签页，无站点内搜索/锚点跳转，配置项多时定位成本高。
11. **站点列表无后端搜索/排序**（P3）：过滤全在前端内存完成（`sites.js:55-71`），站点多时性能与准确性受限。
12. **文件管理器不支持新建/编辑二进制**（P3）：`site-files.js:24-29` 只按扩展名 + 大小判定，超过 2MB 一律不可预览。
13. **`app.js:438` 有一行无副作用的死代码**：`document.querySelector('.login-back-link:not([class*=forgot])') || document.querySelectorAll('.login-back-link')[1];` —— 结果未被使用。
14. **`app.js:502` 的 `resetToLogin` 调用参数错位**（P3）：`resetToLogin` 的形参顺序是 `(state, form, errBox, hintBox, cInput, pInput, uInput, npInput, submitBtn)`（`app.js:404`），但密码重置成功后的调用（`app.js:510`）把第 6 个形参 `pInput` 传成了 `npInput`：
    `resetToLogin(state, form, errBox, hintBox, cInput, npInput, document.querySelectorAll('input')[0], npInput, submitBtn);`
    结果 `resetToLogin` 内部（`app.js:417-418`）的 `pInput.value = ''` 清掉的是**新密码框**，而**旧密码框的明文密码没有被清除**，仍留在 DOM 里（该行此时虽被隐藏，但值还在）。第 7 个形参 `uInput` 传 `document.querySelectorAll('input')[0]` 恰好命中用户名框，属巧合正确。

---

## 五、代码质量

### 5.1 做得好的地方（值得保留的模式）

- **`h()` 的"无 HTML 通道"设计**（`components.js:10-12` 的注释明确写出了这个决策）是本项目最大的安全资产。审查全部 sink 后确认它被贯彻到底。
- **`api.js` 的统一错误规范化**：`request()` 把 `{status,message,data}` 统一成可 `catch (e) => e.message` 的形状（`api.js:60-63`），配合后端 `{error,code}` 的响应体（`helpers.go:19-24`）能正确取到中文错误文案 —— 这解决了"前端拿不到后端错误信息"的常见坑。
- **`api.js:55` 的非 JSON 响应回退**（`if (res.ok) return text;`）让"nginx 配置预览 / PEM 下载 / 纯文本日志"这类非 JSON 端点可以复用同一个 `request()`。
- **`certDaysLeft` 对 Go 零值时间的防御**（`api.js:350-359`）：拦住 `0001-01-01T00:00:00Z` 被解析成负时间戳，是一个真实的、已修好的坑。
- **视图模块的错误降级分级**：`dashboard.js:15-20` 对关键接口失败进错误态、对非关键接口 `.catch(() => …)` 降级，处理得当。
- **`logs.js:16-22` 的注释**清楚记录了"模块级 state 跨渲染存活"这个陷阱及 `stopTail()` 的必要性（虽然只解决了重新进入的一半，见 BUG-13）。

### 5.2 结构性问题

| 问题 | 位置 | 说明 |
|---|---|---|
| 视图状态放在模块作用域 | `sites.js:11`、`logs.js:11-13`、`cron.js:27`、`backups.js:9`、`alerts.js:29`、`ops.js:10-15` | 模块级 `state` 跨视图存活，无生命周期管理 → 产生 BUG-13、BUG-20 这类"上一屏的状态漏到下一屏"的问题 |
| 视图无卸载钩子 | `app.js:183-236` | `handleRoute` 只 `clear(outlet)`，无法通知旧视图清理定时器/WS/监听器 → 内存与连接泄漏（BUG-13）。建议 `mod.unmount?.()` 约定 |
| 容器 id 硬编码漂移 | `sites.js:296` 等 7 处 | `getElementById('view')` 与实际的 `id: 'outlet'` 不一致（BUG-03）。应改为传 `container` 形参或用 `ctx` 提供的上下文对象 |
| API 封装不完整 | `api.js:111-290` | `http` 被导出并被 `app.js` 误用（BUG-02）；`options` 参数是摆设（BUG-15）；`backups.js:19` 绕过封装裸调 `fetch`。建议：所有接口收进 `api.*`，`http` 保持模块私有 |
| 错误处理三段式不统一 | 各视图 | 有的按钮 `try/catch`（`site-detail.js:729`），有的没有（`cron.js:114`、`backups.js:97`）；有的把错误塞进 `toast`，有的塞进 `errBox`。建议统一"toast + 错误态 + 按钮复位"三件套 |
| 重复的"重绘当前视图"写法 | `sites.js:296`、`cron.js:102/129/285`、`backups.js:99/156/189`、`certs.js:271/499/584/629`、`site-detail.js:383/447`、`dashboard.js:59` | 11 处各自 `render(container)`，其中 6 处是坏的（BUG-03）。建议收敛成一个 `ctx.reload()` |
| 组件缺少可访问性属性 | `components.js:154-160`（`field`）、`:126-137`（`switchControl`）、`:560-578`（`pillMultiSelect`）、`:713-745`（`tabPanel`） | `field()` 生成的 `<label>` **没有 `for`、也没有包住控件**（`components.js:156-157`），点击标签不会聚焦输入框，输入框也没有可访问名称；`switchControl` 的 checkbox 没有任何文字标签；`pillMultiSelect` 的选中态只有 CSS class、没有 `aria-pressed`；`tabPanel` 的 tab 没有 `role="tab"`/`aria-selected` |
| 模态框无焦点管理 | `components.js:350-379` | `modal()` 不设 `role="dialog"`/`aria-modal`、不做焦点陷阱、不在打开时移动焦点、关闭后不还原焦点；背景内容在键盘 Tab 时仍可达。ESC 关闭（`components.js:382-387`）实现正确 ✅ |
| 路由切换无焦点管理 | `app.js:223-229` | 换页后焦点仍停在被移除的节点上，屏幕阅读器不会朗读新页标题 |
| 图标表与调用点漂移 | `components.js:63-94` vs 三处调用 | `folder`/`copy`/`unlink` 缺失且**静默回退**（BUG-17） |
| `textInput` 的 opts 白名单过窄 | `components.js:163-172` | 不支持 `maxlength`/`min`/`max`/`pattern`/`autocomplete`（BUG-16） |
| 无前端自动化测试 | 整个 `web/` | 项目有 `internal/api/ops_test.go` 等 Go 测试，但前端无任何测试；BUG-01/BUG-02/BUG-03/BUG-07/BUG-08 这类"契约/拼写级"缺陷本可由一轮契约快照测试挡住 |
| 注释与实现不一致 | `api.js:67-75` | 注释描述 `unwrap` 是"把 site 展平并保留汇总指标"，实现却是"只取 site" —— 注释反而是错的实现依据（BUG-01 的成因） |

### 5.3 未发现缺陷的模块（明确列出）

- `web/index.html`：主题预置脚本（`:9-18`）只读写 `localStorage` 的 `zhiweipanel_theme` 并做白名单校验（`t === 'dark' || t === 'light'`），异常被 `try/catch` 兜住；`noscript` 兜底存在；无外链 CDN。**未发现缺陷**。
- `web/js/components.js` 中除 5.2 列出的可访问性/图标/opts 问题外：表格、图表、Toast、分页、行编辑器等实现正确，**未发现功能缺陷或注入点**。
- `web/js/views/dashboard.js`：`Promise.all` + 逐项降级的策略合理，SVG 图表全部走 `textContent`/`setAttribute` 注入静态值。**未发现注入点**；指标恒 0 的问题源自 BUG-01（api.js），不计入本文件。

---

## 附：未决项 / 未能判定

> **后端在本审计期间被并行任务修改过**（`internal/model/sanitize.go` 新建于 20:16、`internal/model/validate.go` 20:17 扩容、`internal/nginx/render.go` 20:17、`internal/service/service.go` 20:20）。
> 已复核：本报告引用的**所有 API 响应结构**都定义在未被改动的 `internal/model/model.go`（最后修改 2026-10-24）、`internal/api/*` 与 `internal/driver/driver.go` 中，因此第三节的契约表仍然成立。
> 新增的 `model.SanitizeSite`（`internal/model/sanitize.go:37`）目前**只在 `internal/nginx/render.go:68` 被调用**，未接入 API 层，故不影响前端拿到的 JSON 形状；但它引入的告警 type 白名单（`sanitize.go:428-431`）与前端不一致，已记入 BUG-21。

1. `internal/model/validate.go` 的站点校验规则本次**未逐条通读**（该文件在审计期间刚被并行任务扩容 3KB），因此"前端放行、后端拒绝"的完整清单不完整。已确认的前后端校验不一致只有 BUG-09（`edate`）。
2. `internal/service/*` 的业务规则（如"端口冲突"是否在 `CreateSite` 时校验并返回可读错误）未逐行确认。
3. `web/css/app.css` 未做规则级审计（只确认文件存在、43KB）。若存在 `content: attr(...)` 之类把属性值写进 CSS 内容的写法，本次未覆盖。
4. 未运行任何构建/测试/静态检查工具（按任务约束），因此"运行时才会暴露"的问题（如某个 `undefined.map`）可能仍有一小部分未被发现；本报告的结论全部基于静态代码阅读。
