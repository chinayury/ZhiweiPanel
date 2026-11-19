# 智维面板 ZhiweiPanel 借鉴与实现记录（v0.6.0）

> 产出时间：2026-11-19　｜　minor 轮　｜　前几轮见 [RESEARCH-BORROW-v0.5.1.md](RESEARCH-BORROW-v0.5.1.md)
> 本轮主题：**两步验证（TOTP）**——P2 待排清单里安全价值最高的一项，落地为默认关闭的登录第二因子。

## 一、来源与设计

**来源**：所有主流面板（1Panel / 宝塔 / nginx-proxy-manager / Home Assistant）的
2FA 都是 TOTP（RFC 6238）形态；本项目登录页已有「密码 → 异常登录验证码」的
两阶段状态机，TOTP 可复用同一形态，增量主要在密钥管理与校验逻辑。

**关键设计决策**：

| 决策 | 理由 |
|------|------|
| 零第三方依赖（HMAC-SHA1 + base32 手写） | RFC 6238 核心只有 ~40 行；引入 pquerna/otp 等库只为省 40 行，违背「单二进制、少依赖」约束 |
| 密钥明文存 SQLite users 表 | 共享密钥验证时需要原文（哈希化则无法验码）；DB 只有 root 可读，与 bcrypt 哈希同库风险可控 |
| setup/enable 两步走 | 先出 pending 密钥、用户验证首码后才 enabled——避免「生成了密钥没扫成码，把自己锁在门外」 |
| pending 登录凭据：2 分钟 TTL + 一次性 + 5 次错误作废 | 动态口令 6 位、90 秒有效窗，5 次尝试成功率约百万分之六；超限逼攻击者重过 bcrypt 密码关 |
| 已启用 2FA → 跳过异常登录邮件检测 | 两个第二因子串联无收益：动态口令证明持有认证器，比 IP/UA 异常更强 |
| 2FA 管理端点会话专用（Token 403） | 泄漏 write Token 不能关闭登录第二因子；这与「shell scope 独立授权位」同一安全思路 |
| 关闭需出示当前口令 | 能出示动态口令的人才有资格降级防护（防「点一下就关」） |
| 不做 QR 图片生成 | 认证器都支持手动录 secret；otpauth:// 链接照常可扫。省一个 QR 库 |

**明确不做**：恢复码（lost-phone 恢复走「关闭 2FA 需口令 + 忘记密码邮件流程」组合，
单独的恢复码列表留待用户反馈）、HOTP、WebAuthn/Passkey（观察需求再排）。

## 二、落地映射

| 项 | 产物 |
|----|------|
| RFC 6238 + base32 + otpauth URI | `internal/twofa/totp.go`（新包，零依赖） |
| RFC 向量 + 窗口/拒绝/前导零/round-trip 测试 | `internal/twofa/totp_test.go` |
| users 表 totp_secret/totp_enabled 列（兼容旧库） | `internal/store/store.go` |
| 存取方法（Set/Enable/Disable/Get） | `internal/store/store.go` |
| 登录第二阶段（pending 凭据 + verify-totp） | `internal/api/auth.go` |
| 管理端点（会话专用守卫） | `internal/api/twofa.go`（新）+ 路由 |
| 登录页动态口令阶段（复用既有两阶段 UI） | `web/js/app.js` |
| 设置页两步验证卡片（开启/关闭） | `web/js/views/settings.js`、`web/js/api.js` |
| 测试（api 6 用例：流程/一次性/尝试耗尽/401/403） | `internal/api/twofa_test.go` |
| 文档 | USAGE.md 1.4（新）/1.5~1.7 顺延、AGENT.md 4/5 节 |

## 三、一句话结论

> 2FA 的难点从来不是算法（RFC 6238 核心一行 HMAC 就有），而是
> 「锁门外 vs 锁自己」的边界：setup/enable 两步走防自锁、
> pending 5 次作废防爆破、关闭要口令防降级、管理端点会话专用防 Token 越权。
> 默认关闭 + 旧库自动迁移，升级零风险。
