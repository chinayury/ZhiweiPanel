// 管理员账户 + SMTP + 异常登录 + 验证码集中在这里。
//
// 业务边界：
//  1. 邮箱绑定：API 写值 → service 落库 → 发邮件验证
//  2. SMTP 配置：放 settings 表（key-value），统一前缀 "smtp."
//  3. 异常登录检测：纯函数，API 层调
//  4. 验证码：service.CreateChallenge 发邮件，service.VerifyChallenge 校验
package service

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zhiwei/panel/internal/mailer"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
)

// ---------- 管理员账户 / 邮箱 ----------

// SetEmail 设置当前用户的邮箱，置 email_verified=0（需新邮件验证）。
// 改邮箱后旧邮箱失效，必须走 verifyEmailCode 验证后才能被异常登录检测用。
func (s *Service) SetEmail(username, email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return errors.New("邮箱不能为空")
	}
	// 简单合法性校验：mail.ParseAddress 比 regex 严，拒掉明显畸形。
	if _, err := mail.ParseAddress(email); err != nil {
		return fmt.Errorf("邮箱格式不合法: %w", err)
	}
	return s.db.UpdateUserEmail(username, email)
}

// GetUserProfile 取扩展资料（含邮箱 + 最近登录）。
func (s *Service) GetUserProfile(username string) (*store.UserProfile, error) {
	return s.db.GetUserProfile(username)
}

// MailerReady 报告 SMTP 是否已配置齐。
//
// 用于登录流程的"异常检测跳过"判断：未配置通知通道时，challenge 邮件发不出去，
// 硬性要求用户先配 SMTP + 绑邮箱 = 把首次配置的管理员锁在外面，明显不友好。
// 直接放行 + 写日志 + 让用户进系统后补配，比锁死更合理。
func (s *Service) MailerReady() bool {
	return s.mailer != nil && s.mailer.IsConfigured()
}

// MarkEmailVerified 邮箱验证通过：把 email_verified 置 1。
// 流程：发验证码 → 用户输入 → API 调本方法。
func (s *Service) MarkEmailVerified(username string) error {
	return s.db.MarkEmailVerified(username)
}

// ResetPassword 用 challenge 验证 + 直接重置密码。
//
// 流程：
//  1. 拿 challenge（必须 purpose=password_reset 且未过期/未超 attempts）
//  2. 校验 username 与 challenge.Username 一致（防拿别人的 challenge 改密）
//  3. bcrypt 比对 code；错则 IncChallengeAttempts，对则改密 + ConsumeChallenge
//  4. 改密后踢掉该用户所有 session（除空 token）—— 强制所有已登录端下线
//
// 不复用 VerifyChallenge 因为它会 delete challenge，而改密端点需要先读 username 再 delete。
func (s *Service) ResetPassword(challengeID, username, code, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("新密码至少 8 位")
	}
	c, err := s.db.GetChallenge(challengeID)
	if err != nil {
		return err
	}
	if c.Purpose != "password_reset" {
		return errors.New("challenge 用途不匹配")
	}
	if time.Now().After(c.ExpiresAt) {
		_ = s.db.ConsumeChallenge(challengeID)
		return errors.New("验证码已过期，请重新获取")
	}
	if c.Attempts >= challengeMaxAttempts {
		_ = s.db.ConsumeChallenge(challengeID)
		return errors.New("尝试次数过多，请重新获取验证码")
	}
	if c.Username != username {
		// 拿别人的 challenge 改密：算一次失败，attempts++，但用同样的文案模糊化
		_, _ = s.db.IncChallengeAttempts(challengeID)
		return errors.New("验证码错误")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(c.CodeHash), []byte(code)); err != nil {
		_, _ = s.db.IncChallengeAttempts(challengeID)
		return errors.New("验证码错误")
	}
	// 改密
	hash, role, err := s.db.GetUser(username)
	if err != nil {
		return errors.New("用户不存在")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.db.SaveUser(username, string(newHash), role); err != nil {
		return err
	}
	// consume + 踢掉所有 session（空 keepToken）
	_ = s.db.ConsumeChallenge(challengeID)
	if _, err := s.db.DeleteUserSessionsExcept(username, ""); err != nil {
		log.Printf("[reset] delete sessions: %v", err)
	}
	_ = hash
	log.Printf("[reset] password reset for %s", username)
	return nil
}

// ---------- 验证码（Challenge）----------

// challengeTTL 验证码有效期。10 分钟够用户从邮箱复制回来，太长则窗口期长。
const challengeTTL = 10 * time.Minute

// challengeMaxAttempts 单条 challenge 校验失败上限。
// 超过直接 consume 掉，必须重新发。10s 一次 6 位纯数字 → 5 次够用。
const challengeMaxAttempts = 5

// CreateChallenge 生成 6 位数字验证码，bcrypt 入库，发邮件，返回 challenge id。
//
// 收件人：用户绑定的邮箱（必须已存在）。SMTP 未配置时仍返回 id，
// 但邮件发不出去 → 前端能看到 challenge id 但用户收不到 → 报错。
// 这是显式比"默默成功"好：管理员得知道 SMTP 没配。
func (s *Service) CreateChallenge(username, purpose string) (string, error) {
	prof, err := s.db.GetUserProfile(username)
	if err != nil {
		return "", err
	}
	if prof.Email == "" {
		return "", errors.New("未绑定邮箱，无法发送验证码")
	}
	if s.mailer == nil || !s.mailer.IsConfigured() {
		return "", errors.New("SMTP 未配置，无法发送验证码")
	}
	code, err := genCode6()
	if err != nil {
		return "", err
	}
	codeHash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.MinCost)
	if err != nil {
		return "", err
	}
	id := store.NewID()
	if err := s.db.SaveChallenge(id, username, string(codeHash), purpose, challengeTTL); err != nil {
		return "", err
	}
	subject, body := renderChallengeMail(purpose, code)
	s.mailer.SendSensitiveSafe(prof.Email, subject, body)
	return id, nil
}

// VerifyChallenge 校验验证码。
//
// 返回 (true, nil) 表示通过；通过的同时 consume 掉（一次性）。
// 失败要区分两种：attempts 超限（直接 consume，不再允许尝试）vs 单纯密码错。
//
// 通过时按 purpose 触发对应副作用：
//   - email_verify：MarkEmailVerified —— 把该用户的 email_verified 置 1
//   - 其他（login_verify / password_reset）：不动
//
// 不放 api 层做这个判断，是因为 verify-challenge 是共用端点，api 层
// 不知道调用方是哪个流程。challenge.purpose 是事实上的"作用域标签"，
// service 层拿到就能决策。
func (s *Service) VerifyChallenge(id, code string) (bool, error) {
	c, err := s.db.GetChallenge(id)
	if err != nil {
		return false, err
	}
	if time.Now().After(c.ExpiresAt) {
		_ = s.db.ConsumeChallenge(id)
		return false, errors.New("验证码已过期，请重新获取")
	}
	if c.Attempts >= challengeMaxAttempts {
		_ = s.db.ConsumeChallenge(id)
		return false, errors.New("尝试次数过多，请重新获取验证码")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(c.CodeHash), []byte(code)); err != nil {
		// 失败时 attempts++，下次查询自然反映新值
		_, _ = s.db.IncChallengeAttempts(id)
		return false, errors.New("验证码错误")
	}
	_ = s.db.ConsumeChallenge(id)
	// 副作用：邮箱验证
	if c.Purpose == "email_verify" {
		if err := s.MarkEmailVerified(c.Username); err != nil {
			log.Printf("[challenge] mark email verified failed for %s: %v", c.Username, err)
		} else {
			log.Printf("[challenge] email verified for %s", c.Username)
		}
	}
	return true, nil
}

// renderChallengeMail 给不同 purpose 渲染不同主题/正文。
// 当前只有 3 个 purpose，switch 列出避免字符串拼错。
func renderChallengeMail(purpose, code string) (subject, body string) {
	min := codeExpiryMinutes()
	switch purpose {
	case "login_verify":
		subject = fmt.Sprintf("[智维面板] 登录验证码：%s", code)
		body = fmt.Sprintf("您正在从新位置尝试登录智维面板管理控制台。\n\n验证码：%s\n\n%d 分钟内有效。如非本人操作，请立即修改管理员密码。", code, min)
	case "password_reset":
		subject = fmt.Sprintf("[智维面板] 密码重置验证码：%s", code)
		body = fmt.Sprintf("您正在重置智维面板管理员密码。\n\n验证码：%s\n\n%d 分钟内有效。如非本人操作，请忽略本邮件。", code, min)
	case "email_verify":
		subject = fmt.Sprintf("[智维面板] 邮箱验证：%s", code)
		body = fmt.Sprintf("您正在绑定/更换智维面板联系邮箱。\n\n验证码：%s\n\n%d 分钟内有效。验证后该邮箱将用于异常登录通知。", code, min)
	default:
		subject = fmt.Sprintf("[智维面板] 验证码：%s", code)
		body = fmt.Sprintf("您的验证码：%s\n\n%d 分钟内有效。", code, min)
	}
	return
}

func codeExpiryMinutes() int { return int(challengeTTL / time.Minute) }

// ---------- 恢复码登录提醒（v0.8.1） ----------

// NotifyRecoveryLogin 恢复码登录成功后给账户邮箱发提醒。
//
// 语义：恢复码登录 = 用户大概率丢了手机（或恢复码纸被翻出来）。
// 这是一个**安全相关事件**，本人必须第一时间知道——否则捡到手机的人
// 可以拿着恢复码静默登录。提醒内容带时间/IP/UA，并提示如非本人操作
// 立即去面板重新生成恢复码/修改密码。
//
// 设计约束（best-effort，绝不反过来伤害主流程）：
//   - 邮箱未绑定 / SMTP 未配置 → 只记日志，静默跳过（登录已成功，
//     不能因为通知发不出去把用户踢回去）；
//   - 由调用方放 goroutine 调用——SMTP 发送可能耗时数秒，
//     不能拖慢登录响应；
//   - SendSensitiveSafe 内部已容错（失败只记日志不 panic）。
func (s *Service) NotifyRecoveryLogin(username, ip, ua string) {
	prof, err := s.db.GetUserProfile(username)
	if err != nil || prof.Email == "" {
		return
	}
	if s.mailer == nil || !s.mailer.IsConfigured() {
		log.Printf("[2fa] 恢复码登录提醒未发送（%s 未绑邮箱或 SMTP 未配置）", username)
		return
	}
	subject, body := renderRecoveryLoginMail(ip, ua)
	s.mailer.SendSensitiveSafe(prof.Email, subject, body)
}

// renderRecoveryLoginMail 渲染恢复码登录提醒邮件。
func renderRecoveryLoginMail(ip, ua string) (subject, body string) {
	subject = "[智维面板] 安全提醒：恢复码被用于登录"
	body = fmt.Sprintf("您的智维面板管理员账户刚刚通过「恢复码」完成了登录。\n\n"+
		"时间：%s\n来源 IP：%s\n浏览器/客户端：%s\n\n"+
		"恢复码是手机丢失或损坏时的备用登录凭证。如果这是您本人操作，忽略本邮件即可；"+
		"如果不是，请立即登录面板「重新生成恢复码」并修改管理员密码。",
		time.Now().Format("2006-01-02 15:04:05"), ip, ua)
	return subject, body
}

// genCode6 生成 6 位数字验证码（000000-999999）。
// 用 crypto/rand 而不是 math/rand：避免被时间侧信道预测。
func genCode6() (string, error) {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// ---------- 异常登录检测 ----------

// AnomalousLogin 标记需要挑战验证的登录信号。
type AnomalousLogin struct {
	Reason  string // 给前端展示的简短原因
	IP      string
	UA      string
	IPClass string // "same_subnet" | "diff_subnet"
}

// IsAnomalousLogin 比对上次成功登录与本次，判断是否要触发验证码。
//
// 算法：
//   - 首次登录（无历史）不算异常
//   - 同子网（IPv4 /24，IPv6 /48） + 相似 UA → 正常
//   - 子网不同 或 UA 完全不同 → 异常
//   - 距上次 >7 天（不活跃）→ 即使同子网也异常
//
// 为什么用子网粒度而不是城市：内网没有可信的 IP 地理库，自建/家用宽带
// 拨号 IP 也经常变；/24 子网粒度在公网上对应同一城市同一运营商段，
// 误判比"严格 IP 字符串相等"低得多。
func IsAnomalousLogin(prevIP, prevUA string, prevTime time.Time, newIP, newUA string, now time.Time) *AnomalousLogin {
	if prevIP == "" || prevUA == "" || prevTime.IsZero() {
		return nil
	}
	if now.Sub(prevTime) > 7*24*time.Hour {
		return &AnomalousLogin{
			Reason: "距上次登录已超过 7 天",
			IP:     newIP, UA: newUA, IPClass: subnetClass(newIP, prevIP),
		}
	}
	if !sameSubnet(prevIP, newIP) {
		// ⚠️ 原因文案里**不能回显上一次的登录 IP**。
		//
		// 这条文案会原样返回给未完全认证的客户端（密码已对，但验证码还没过）。
		// 早期版本写成 "IP 段变化：203.0.113.7 → 1.2.3.4"，
		// 于是攻击者只要先用正确密码登录一次，就拿到了管理员上次的登录 IP，
		// 随后带上 `X-Forwarded-For: 203.0.113.7` 重放：
		// 同子网判定成立 → 判定为正常登录 → 不再需要邮箱验证码。
		// "密码 + 邮箱验证码"的双因素被降级成单因素。
		//
		// 现在只说"发生了变化"，不给出上一次的具体地址。
		return &AnomalousLogin{
			Reason: "IP 段变化（与上次登录不同）",
			IP:     newIP, UA: newUA, IPClass: "diff_subnet",
		}
	}
	if !similarUA(prevUA, newUA) {
		return &AnomalousLogin{
			Reason: "浏览器/设备指纹变化",
			IP:     newIP, UA: newUA, IPClass: subnetClass(newIP, prevIP),
		}
	}
	return nil
}

// sameSubnet IPv4 /24 / IPv6 /48。
func sameSubnet(a, b string) bool {
	ipA := net.ParseIP(a)
	ipB := net.ParseIP(b)
	if ipA == nil || ipB == nil {
		return false
	}
	// IPv4
	if v4A := ipA.To4(); v4A != nil {
		if v4B := ipB.To4(); v4B != nil {
			return v4A[0] == v4B[0] && v4A[1] == v4B[1] && v4A[2] == v4B[2]
		}
		return false
	}
	// IPv6 /48
	if v6A := ipA.To16(); v6A != nil {
		if v6B := ipB.To16(); v6B != nil {
			for i := 0; i < 6; i++ {
				if v6A[i] != v6B[i] {
					return false
				}
			}
			return true
		}
	}
	return false
}

// subnetClass 给前端展示"同/跨子网"标签。
func subnetClass(newIP, prevIP string) string {
	if sameSubnet(newIP, prevIP) {
		return "same_subnet"
	}
	return "diff_subnet"
}

// similarUA 简化 UA 比对：只比对 "浏览器 + OS" 两段（UA 里取 Mozilla 之前 / 之后信息）。
//
// 真实场景：用户换浏览器、升级 OS、UA 大小写不一致都会变化。
// 折中：取前 64 字符做"包含"判断（多数浏览器/UA 前缀稳定）。
func similarUA(prev, new string) bool {
	if prev == new {
		return true
	}
	// 截前 64 字符比较：覆盖 Chrome/Edge/Safari/Firefox 在常见 OS 上的稳定前缀
	short := func(s string) string {
		if len(s) > 64 {
			return s[:64]
		}
		return s
	}
	return short(prev) == short(new)
}

// ---------- SMTP 配置 ----------

// smtpSettingKeys 是 settings 表里 smtp 相关 key 的集中声明。
// 改这里就改全局读写路径，避免散落的字符串硬编码。
var smtpSettingKeys = []string{
	"smtp.host",
	"smtp.port",
	"smtp.user",
	"smtp.password",
	"smtp.from",
	"smtp.from_name",
	"smtp.encryption",
}

// SMTPConfig 业务层 SMTP 配置（去掉了 mailer.Config 的零值兼容字段）。
type SMTPConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"` // 明文读写（settings 表就是明文）
	From       string `json:"from"`
	FromName   string `json:"from_name"`
	Encryption string `json:"encryption"` // "starttls" / "ssl" / "none"
}

// LoadSMTPConfig 读 SMTP 配置。空值字段保留空字符串。
// 密码是密文/明文？目前 settings 表就是明文（项目里 DB 没有加密层），
// 风险见 GetSMTPConfig 的注释。
func (s *Service) LoadSMTPConfig() (*SMTPConfig, error) {
	all, err := s.db.AllSettings()
	if err != nil {
		return nil, err
	}
	c := &SMTPConfig{}
	c.Host = all["smtp.host"]
	c.User = all["smtp.user"]
	c.Password = all["smtp.password"]
	c.From = all["smtp.from"]
	c.FromName = all["smtp.from_name"]
	c.Encryption = all["smtp.encryption"]
	if p := all["smtp.port"]; p != "" {
		var port int
		fmt.Sscanf(p, "%d", &port)
		c.Port = port
	}
	return c, nil
}

// SaveSMTPConfig 写 SMTP 配置。空值字段会写空串。
func (s *Service) SaveSMTPConfig(c *SMTPConfig) error {
	if c == nil {
		return model.ErrBadRequest
	}
	if c.Port < 0 || c.Port > 65535 {
		return errors.New("port 非法")
	}
	pairs := map[string]string{
		"smtp.host":       c.Host,
		"smtp.port":       fmt.Sprintf("%d", c.Port),
		"smtp.user":       c.User,
		"smtp.password":   c.Password,
		"smtp.from":       c.From,
		"smtp.from_name":  c.FromName,
		"smtp.encryption": c.Encryption,
	}
	for k, v := range pairs {
		if err := s.db.SetSetting(k, v); err != nil {
			return err
		}
	}
	// 同步到运行时 mailer 实例
	if s.mailer != nil {
		*s.mailer = *mailer.New(smtpConfigToMailer(c))
	}
	return nil
}

// smtpConfigToMailer 把业务层 SMTPConfig 转 mailer.Config。
func smtpConfigToMailer(c *SMTPConfig) mailer.Config {
	return mailer.Config{
		Host:       c.Host,
		Port:       c.Port,
		User:       c.User,
		Password:   c.Password,
		From:       c.From,
		FromName:   c.FromName,
		Encryption: c.Encryption,
	}
}

// TestSMTP 给当前 SMTP 配置发一封测试邮件到 toEmail。
// 返回 mailer 错误（可能：未配置 / 认证失败 / 网络问题）。
// 用单独 toEmail 而非绑定的 admin 邮箱：方便运维/测试人员即时验证。
func (s *Service) TestSMTP(toEmail string) error {
	if s.mailer == nil || !s.mailer.IsConfigured() {
		return errors.New("SMTP 未配置")
	}
	return s.mailer.Send(toEmail, "[智维面板] SMTP 测试", "这是一封智维面板控制台发出的 SMTP 测试邮件。\n\n如果你收到，说明邮件发送链路正常。")
}
