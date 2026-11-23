# 智维面板 ZhiweiPanel 借鉴与实现记录（v0.8.0）

> 产出时间：2026-11-25　｜　minor 轮　｜　前几轮见 [RESEARCH-BORROW-v0.7.3.md](RESEARCH-BORROW-v0.7.0.md)（v0.7.0 起）
> 本轮主题：**两步验证恢复码**——v0.6.0 借鉴文档里明确「留待用户反馈」的项，本轮兑现：手机丢失时用户唯一的自救通道。

## 一、来源与设计

**来源**：GitHub / Google / 1Panel 的 2FA 恢复码机制——所有实现同构：启用时发一批一次性码、
登录时可替代第二因子、可重新生成（旧码作废）。差异只在码的格式与存储。

**关键设计决策**：

| 决策 | 理由 |
|------|------|
| 启用即发码（而不是"以后想要再生成"） | 用户开启 2FA 的时刻是唯一确定"手机还在手上"的时刻；错过这个窗口再让用户去生成，可能已经进不来了 |
| 12 位 Crockford base32（剔除 I/L/O/U） | 人工抄录/口述不混淆 1/I、0/O；60 bit 熵 + 一次性 + 限流，暴力枚举不可行 |
| SHA-256 存摘要而非 bcrypt | 高熵随机串不怕字典攻击，慢哈希纯属浪费 CPU（bcrypt 是给 6 位低熵验证码准备的——同库 `login_challenges` 正是如此） |
| 一行一枚 + `used_at` 标记消费 | 「一次性」由行状态天然保证；条件 UPDATE（`used_at IS NULL`）命中才生效，并发/重放不可能双花 |
| 归一化放在哈希边界 | 用户从纸上抄回来带小写/空格/短横是常态；放在 `HashRecoveryCode` 内部而不是指望每个调用方记得做 |
| 重新生成 = 整体替换（旧码全作废） | 泄漏止损必须立刻全量失效；"作废单枚"会让用户在 10 枚里猜哪枚泄漏了 |
| 恢复码登录走同一个 verify-totp 端点 | 前端零改动（同一输入框），后端动态口令失败后试恢复码；错误提示统一"动态口令或恢复码错误"——不向攻击者泄露"这个账号有没有恢复码" |
| 关闭 2FA 时清空恢复码 | 密钥没了恢复码就失去存在意义，留着只多一分泄露面 |

**明确不做**：恢复码使用通知邮件（需要邮箱已配置，作为后续增强观察）、按枚作废
（整体替换已覆盖止损场景）、WebAuthn/Passkey（仍是观察项）。

## 二、落地映射

| 项 | 产物 |
|----|------|
| 生成（Crockford base32、分组展示） | `internal/twofa/recovery.go` |
| 表 + 存取/消费/清理 | `internal/store/store.go`（schema）+ `internal/store/totp_recovery.go` |
| 登录兜底（verify-totp 内试恢复码） | `internal/api/auth.go` |
| 管理端点（启用即发码 / 重新生成 / 余量） | `internal/api/twofa.go` + `server.go` 路由 |
| 设置页恢复码区（保存弹窗/余量/重新生成） | `web/js/views/settings.js`、`web/js/api.js`、`web/js/app.js` |
| 测试 | `internal/twofa/recovery_test.go`、`internal/store/totp_recovery_test.go`、`internal/api/recovery_test.go` |
| 文档 | README 功能表、USAGE 1.4、AGENT.md 端点、CHANGELOG v0.8.0 |

## 三、一句话结论

> 恢复码的价值不在算法（12 位随机串而已），而在**时机**：启用 2FA 的那一刻
> 是唯一确定"手机还在手上"的时刻，码必须在那一刻交到用户手里；
> 之后的一切设计（一次性、整体替换、关闭即清、登录同端点）都是为了让这条
> 自救通道既可用又不成为新的攻击面。
