# 智维面板 ZhiweiPanel 同类项目借鉴要点清单（v0.5.0）

> 产出时间：2026-11-21　｜　紧凑轮次　｜　前几轮见 [RESEARCH-BORROW-v0.3.0.md](RESEARCH-BORROW-v0.3.0.md) /
> [RESEARCH-BORROW-v0.4.0.md](RESEARCH-BORROW-v0.4.0.md)
> 本轮主题：**为生产已验收的功能集正式打版**（v0.5.0），并把这批功能的借鉴来源与取舍归档。

## 一、本轮实施项的来源与依据

这批功能由并行工作会话开发，2026-11-17 已部署生产实机（0.1.3 → 0.4.0+ 升级，8 条验收），
2026-11-18 合入仓库（commit `1118be2`，CI 全绿），本轮补版本号、CHANGELOG 与 Tag。
实施前照例先读了实现，三个关键事实决定了文档与打版方式：

1. `internal/apps/catalog.go` 只放**静态目录**，探测逻辑全在 service 层（复用 NginxStatus /
   PHPVersions / mysql.Status + 白名单 exec）——目录与运行时解耦，文档无需写探测细节。
2. `prevalidateSite` 改变了建站/改站的**错误语义**（400 + nginx -t 原文回显）——
   USAGE 与 AGENT 都必须写明，否则调用方会把它当偶发失败重试。
3. `80/443 拆块`重构动了渲染器骨架（三个既有测试文件同步改）——回归面在渲染层。

## 二、可借鉴要点（本轮归档）

### A. 软件商店：从「不做」到「只读目录」的定位演化

**来源**：宝塔「软件商店」同型（代码注释明示对齐其分类：web / runtime / database / cache / dev）。

项目定位原是「刻意不做应用商店」。实际需求（运维想知道"这台机器缺什么"）催生了收敛版：

| 宝塔软件商店 | ZhiweiPanel 收敛版 |
|------|------|
| 在线安装 / 卸载 / 升级 | ❌ 只给 `InstallHint` 命令提示，动作由运维执行 |
| 商店服务端 / 版本源 | ❌ 目录硬编码在 `internal/apps`（7 条），离线可用 |
| 一键配置 | ❌ 探测复用现有子系统 + 固定命令白名单（3 秒超时、无用户输入拼接） |

**取舍备忘**：这是定位的**软化**而非反转——面板仍然不管理软件生命周期，
只回答「装没装、装了哪个版本、缺的话命令是什么」。理由：自动安装等于把
root 级包管理暴露到 Web，与「Shell 任务是独立授权位」的同一条安全边界冲突。

### B. 落库前预校验：把「失败」挡在 DB 之前（P0）

**来源**：站点级自定义配置的事故形态——早期「先 SaveSite 再 ApplyAllConfig」，
`nginx -t` 失败时 DB 已落坏配置、磁盘回滚成旧配置，之后**每一次**全量下发都卡死。

| 要点 | 落地 |
|------|------|
| 写盘（原子替换）→ `nginx -t` → 失败恢复旧文件 | ✅ `prevalidateSite`，建站与改站共用；顺序不能错 |
| 错误原文直达用户 | ✅ 新增 `model.ErrNginxConfig`，API 层识别后原样回显（不包装成通用文案），前端弹窗完整展示多行报错 |
| 预校验只测「本站点」，http 级/全量同步仍由 ApplyAllConfig 收口 | ✅ 职责不重叠 |

### C. extra_conf 双注入位置

**来源**：`upstream` / `limit_req_zone` / `map` 只能出现在 http 上下文，
`server 块内固定末尾` 的旧 raw_conf 表达不了这些需求。

- `inside`（默认）：server 块内，ssl_session_timeout 之后、`/.well-known` 之前。
- `outside`：server 块后（文件末尾，http 上下文）。
- `raw_conf` 老库自动迁移（extra_conf 为空时搬入并清空 raw_conf）。

### D. 80/443 拆块与 HTTP-01 挑战直达

**来源**：certbot/webroot 类 ACME 客户端要求 `/.well-known/acme-challenge/` 返回 **200**；
早期 TLS 站点 80 块里 301 强制跳转会拦截挑战。

- TLS 站点拆成独立 80 块（ACME + 跳转）与 443 TLS 块；渲染器拆为
  `buildSiteServerBlock` / `buildHTTPRedirectServer` / `effectiveListenPorts`。

### E. 默认安全头三件套（通用安全实践）

`X-Frame-Options SAMEORIGIN` / `X-Content-Type-Options nosniff` /
`Referrer-Policy strict-origin-when-cross-origin` 无条件输出；已处理 nginx
add_header 的子 location 继承陷阱（静态缓存 location 里按需重复声明）。

### F. 建站向导根目录自动填写（P1）

主域名 → `/www/wwwroot/<主域名>`，通配符 `*.foo.com` → `_foo.com`，与后端
CreateSite 兜底一致；只覆盖「空值或仍是上次自动值」，手动值永不回填。

## 三、与历轮的去重说明

| 事项 | 状态 |
|------|------|
| 发布工程（v0.1.4）、AGENT.md / CI 断言 / 限制清单勘误（v0.1.5）、审计导出（v0.3.0）、漂移告警（v0.4.0） | 已落地，本轮不涉及 |
| 软件商店 | 定位从「不做」软化为「只读目录」→ **本轮打版** |
| extra_conf / 预校验 / 80-443 拆块 / 安全头 / 向导自动填写 | **本轮打版**（并行会话开发） |

## 四、v0.5.0 落地映射

| 项 | 产物 |
|----|------|
| 软件商店 | `internal/apps/catalog.go`、`internal/model/app.go`、`internal/api/server.go`（GET /api/apps）、`web/js/views/apps.js`、`service.ListApps` |
| extra_conf + 预校验 | `internal/nginx/render.go`、`internal/service/service.go`（`prevalidateSite`）、`internal/model`（ErrNginxConfig、extra_conf 字段）、`web/js/views/site-detail.js` |
| 80/443 拆块 + 安全头 | `internal/nginx/render.go` + 3 个渲染测试文件同步 |
| 向导自动填写 + body_size 默认值 | `web/js/views/site-create.js`、`site-detail.js`（`defaultBodySize`） |
| 打版（版本号/CHANGELOG/Tag） | 本轮：`internal/version/version.go`（0.5.0）、`docs/CHANGELOG.md`、5 个锚点 |

## 五、下一轮排期建议

| 优先级 | 事项 | 版本类型 |
|--------|------|---------|
| P0 | 测试机 SSH 恢复后：跑 `phase-h4-run.ps1` 补 0.4.0 部署验证，再部署 0.5.0（含漂移 e2e） | — |
| P1 | 生产升级到 v0.5.0 官方 Tag 构建（消除「生产构建无 Tag」错位，由用户/运维执行） | — |
| P2 | 2FA/TOTP、监控历史落盘、`-backup-dir` 生效（README #14） | minor/patch |

## 六、一句话结论

> 本轮没有写新功能——它把「生产在跑、仓库缺失」的 898 行验收过的代码变成了
> 有版本号、有 CHANGELOG、有 Tag 的正式版本。定位上项目第一次有了软件商店，
> 但收敛成「只看不装」：能力越界的边界仍然画在 root 包管理那条线上。
