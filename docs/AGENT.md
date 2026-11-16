# ZhiweiPanel · AI Agent / 自动化脚本操作说明

> 本文面向**机器调用方**（AI agent、CI 脚本、运维自动化），不是给人类用户的教程。
> 人类用户请读 [docs/USAGE.md](USAGE.md)。借鉴自 goinfinite/os 的 SKILL.md 范式：
> 把「这个面板能做什么、哪些操作有副作用、有哪些坑」写成一份权威说明，
> 让 agent 不必把整套运维知识塞进提示词就能安全驱动面板。

## 1. 面板是什么（一句话心智模型）

ZhiweiPanel 是**单二进制自托管建站面板**：管理 nginx 站点配置、SSL 证书、计划任务、备份与告警。

调用方必须记住的三条运行时事实：

1. **面板以 root 运行，且直接操纵宿主机 nginx**。任何写操作最终都会变成磁盘上的
   nginx 配置文件 + 一次 `nginx -t` + 一次 reload。配置写坏会被 `nginx -t` 拦下并回滚，
   但**语义写对、效果不符预期**的变更（例如把错误的反代上游标成在线）不会被拦。
2. **写操作一律 POST/PUT/DELETE**。GET 永远不改状态——这是面板刻意的安全设计，
   不是巧合。不要试图用 GET「顺便触发」任何动作。
3. **`shell` scope 等于 root 执行能力**。「Shell 脚本」类型的计划任务由面板以 root 执行。
   `write` ≠ `shell`：泄漏一个 `write` Token 拿到的是站点配置权限，泄漏一个 `shell`
   Token 拿到的是整机。

## 2. 认证

两种凭据，别混用：

| 凭据 | 用法 | 适用 |
|------|------|------|
| **API Token**（推荐给机器） | `Authorization: Bearer zw_<base64url>` 或 `X-API-Token: zw_<base64url>` | CI / agent / 脚本 |
| 浏览器会话 Cookie | `POST /api/auth/login` 后持 Cookie | 交互式前端 |

- Token 明文**只在创建响应里出现一次**（`show_token: true`），服务端只存 SHA-256 摘要。
  丢了就吊销重建，不要问面板要明文。
- **scope 语义**：`scopes` 为空数组 = 全权限（等价管理员会话，慎用）；`read` = 只能 GET；
  `write` = 可写；`shell` = 额外授权位，**不会**从 `write` 推导，创建时必须显式勾选。
- scope 不足时写接口返回 **403**，未认证返回 **401**——不要重试 403，那是授权问题不是抖动。

创建 Token：

```bash
curl -X POST https://<panel-host>:8888/api/tokens \
  -H "Authorization: Bearer <admin-token-or-session>" \
  -H "Content-Type: application/json" \
  -d '{"name":"ci-deploy","scopes":["read","write"],"expires_in_days":30}'
```

## 3. 有副作用的端点速查（全部要求 write，标注 ★ 的另需 shell）

| 端点 | 方法 | 说明 |
|------|------|------|
| `/api/sites` | POST | 建站；响应 201 = 配置已下发，**202 = 已落库但下发失败**（看 `warning` 字段，不要当成功） |
| `/api/sites/{id}` | PUT | 改站配置 |
| `/api/sites/{id}` | DELETE | 删站（联动删 nginx 配置） |
| `/api/sites/batch` | POST | 批量启动/停止 |
| `/api/sites/{id}/baseline` | POST | **改写防篡改基线**。会把受保护文件当前哈希记为「正常」——在已被篡改的机器上调用等于把篡改洗白。调用前先确认文件干净 |
| `/api/sites/{id}/tamper` | POST | 立即检测篡改（写事件表、触发告警） |
| `/api/certs/issue` | POST | ACME 签发（HTTP-01 需要 80 端口可达；DNS-01 需要先配 DNS 提供商凭据） |
| `/api/cron/jobs/run` | POST | 立即执行计划任务；**Shell 类型任务另需 shell scope**（★） |
| `/api/backups/{id}/restore` | POST | 只返回恢复预览，不会直接覆盖线上配置 |
| `/api/system/config-apply` | POST | 手动触发全量配置下发 |
| `/api/system/config-history/{id}/restore` | POST | 回滚 nginx 配置（不回滚数据库，见第 5 节） |
| `/api/tokens` | POST / DELETE | 创建 / 吊销 Token |

## 4. 只读端点（read scope 即可）

`GET /api/sites`、`GET /api/system/status`、`GET /api/system/metrics`、
`GET /api/system/config-history`、`GET /api/audit-logs`、
`GET /api/audit-logs/export?format=csv|json`（下载文件；**导出本身会留痕**
`audit.export`——把审计日志带走的动作与改配置同等敏感，agent 不要在无人
确认时批量导出）、`GET /api/alerts`、`GET /api/backups`、`GET /api/cron/jobs`、
`GET /api/auth/me`（恒 200，用 `authenticated` 字段区分登录态——**4xx 才是错误**）。

## 5. 必须知道的运行时约定（踩过坑的）

1. **配置下发是串行流水线**：渲染 → 写 `conf.d` → `nginx -t` → reload，任一步失败自动回滚
   全部已写文件。所以「下发失败」不会留下半套配置，但站点会停在旧版本。
2. **单个坏站点不拖垮全站**：渲染失败的站点配置被整段注释掉，其余站点照常下发。
   面板会在状态里记录被隔离的站点——建站后先查它是否被隔离。
3. **回滚只恢复 nginx 配置，不回滚数据库**：站点定义仍是当前那份。要连站点定义一起回退，
   走站点备份。
4. **建基线的顺序**：改受保护文件列表 → 保存 → 建基线 → 之后检测才有意义。
   基线前没保存的修改不会生效。
5. **同一监听端口只能给一个站点**，冲突会被面板拦截（400，不是 nginx 报错）。
6. **告警类型是固定枚举**：`status_5xx` / `traffic_quota` / `cert_expiry` / `site_down` /
   `tamper` / `nginx_config` / `config_drift`，以 `GET /api/alert-types` 的运行时返回为准，不要自己编。
7. **登录有限流**：IP+用户名 5 次/分钟、锁定 15 分钟，纯账号维度阈值更高。
   自动化脚本不要用密码登录循环试错，用 Token。
8. **容器部署形态**：面板与 nginx 同容器（`-runtime=host`），nginx 由镜像入口脚本拉起。
   容器里没有 systemd，`systemctl` 类探测必然失败属正常现象。

## 6. Agent 行为红线（建议直接写进 agent 系统提示）

- **不要**在没有人工确认的情况下调用：删站、改写基线、恢复配置、创建 shell Token、
  执行 Shell 计划任务。这五类操作要么不可逆、要么等于交出 root。
- **不要**把 Token 明文写进日志、提示词或对话历史；用完即弃的 Token 用短有效期。
- **不要**用「多试几次」应对 4xx：401/403/400 分别是未认证/无权限/参数错，重试无意义。
- **先读后写**：改站点前先 `GET /api/sites/{id}` 拿当前定义，做最小字段修改后 PUT 回去；
  面板对未知字段的处理是忽略，但整个 PUT 是全量替换语义。
- 版本兼容性：先 `GET /api/auth/me`（恒 200）确认连通，再看错误响应里的 `code` 字段判断。

## 7. 本文档的维护

- 本文与代码同仓维护；端点行为以源码为准（`internal/api/server.go` 的路由表是唯一权威）。
- 新增有副作用的端点时，必须同步更新第 3 节速查表——这是 release 流程的一部分。
