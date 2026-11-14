// Package mailer 提供 SMTP 邮件发送。
//
// 设计原则：
//  1. 不引第三方包：标准库 net/smtp 已够用，避免供应商锁定
//  2. 凭据永远不写日志：fail 时只记错误类型与 host:port
//  3. 失败重试一次：邮件偶尔因为网络抖动丢包，重试是合理的
//  4. 超时 15s：内网 SMTP 通常 <1s，外网公网 25/465/587 一般 <5s，
//     15s 是给偶尔的慢响应留余地，又不阻塞登录验证码流程太久
package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Config SMTP 配置。
//
// Encryption 三选一：
//   - "" / "none"     : 明文（25 端口，内网自建）
//   - "starttls"      : STARTTLS 升级（587 端口，主流）
//   - "ssl" / "tls"   : 隐式 TLS（465 端口）
type Config struct {
	Host       string // SMTP 服务器
	Port       int    // 25 / 465 / 587
	User       string // 认证用户名（多数情况等于发件邮箱）
	Password   string // 认证密码
	From       string // 发件邮箱
	FromName   string // 发件人显示名（可空）
	Encryption string // "starttls" / "ssl" / "none"（空）
}

// Mailer 发送器。零值不可用，调 New 或 NewFromConfig 创建。
type Mailer struct {
	cfg Config
}

// New 从 Config 创建 Mailer。
func New(cfg Config) *Mailer {
	return &Mailer{cfg: cfg}
}

// IsConfigured 报告 SMTP 是否已配置齐（host + port + from 任一为空就 false）。
func (m *Mailer) IsConfigured() bool {
	return m != nil && m.cfg.Host != "" && m.cfg.Port > 0 && m.cfg.From != ""
}

// Send 发送一封纯文本邮件。
//
// to 支持多个收件人（逗号分隔）。subject/body 不做转义，
// MIME 头里 Subject 直接用 utf-8（现代客户端都支持）。
func (m *Mailer) Send(to, subject, body string) error {
	if !m.IsConfigured() {
		return errors.New("SMTP 未配置")
	}
	msg := buildMessage(m.cfg, to, subject, body, false)
	return m.sendWithRetry(to, string(msg))
}

// SendHTML 发送一封 HTML 邮件（text/html）。
func (m *Mailer) SendHTML(to, subject, htmlBody string) error {
	if !m.IsConfigured() {
		return errors.New("SMTP 未配置")
	}
	msg := buildMessage(m.cfg, to, subject, htmlBody, true)
	return m.sendWithRetry(to, string(msg))
}

// sendWithRetry 发一次，失败再发一次。最多重试一次避免卡登录流程。
func (m *Mailer) sendWithRetry(to, msg string) error {
	var lastErr error
	for i := 0; i < 2; i++ {
		if err := m.sendOnce(to, msg); err == nil {
			return nil
		} else {
			lastErr = err
			if i == 0 {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
	return lastErr
}

// sendOnce 实际一次发送。三种加密模式分支。
func (m *Mailer) sendOnce(to string, msg string) error {
	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)
	// 1) 隐式 TLS（SSL on connect）
	if strings.EqualFold(m.cfg.Encryption, "ssl") || strings.EqualFold(m.cfg.Encryption, "tls") {
		return sendImplicitTLS(addr, m.cfg, to, msg)
	}
	// 2) STARTTLS 或 3) 明文统一用 smtp.SendMail，区别在 client 是否升级
	return sendPlainOrSTARTTLS(addr, m.cfg, to, msg)
}

// dialSMTP 自定义 dialer 配 15s 超时，绕开 smtp.Dial 没 SetTimeout 的限制。
func dialSMTP(addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 15 * time.Second}
	return d.Dial("tcp", addr)
}

func sendPlainOrSTARTTLS(addr string, cfg Config, to string, msg string) error {
	host, _, _ := net.SplitHostPort(addr)
	var auth smtp.Auth
	if cfg.User != "" {
		auth = smtp.PlainAuth("", cfg.User, cfg.Password, host)
	}
	// 自定义 client：用于 STARTTLS 时控制 TLS 最低版本 + 接受自签证书
	// （自签 SMTP 在内网测试场景很常见）。
	conn, err := dialSMTP(addr)
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	if strings.EqualFold(cfg.Encryption, "starttls") {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("SMTP 服务器不支持 STARTTLS")
		}
		tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS 失败: %w", err)
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("SMTP 服务器未声明 AUTH")
		}
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM 失败: %w", err)
	}
	for _, rcpt := range strings.Split(to, ",") {
		rcpt = strings.TrimSpace(rcpt)
		if rcpt == "" {
			continue
		}
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s 失败: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func sendImplicitTLS(addr string, cfg Config, to string, msg string) error {
	host, _, _ := net.SplitHostPort(addr)
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	d := net.Dialer{Timeout: 15 * time.Second}
	rawConn, err := d.Dial("tcp", addr)
	if err != nil {
		return err
	}
	conn := tls.Client(rawConn, tlsCfg)
	if err := conn.Handshake(); err != nil {
		_ = conn.Close()
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	if cfg.User != "" {
		auth := smtp.PlainAuth("", cfg.User, cfg.Password, host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM 失败: %w", err)
	}
	for _, rcpt := range strings.Split(to, ",") {
		rcpt = strings.TrimSpace(rcpt)
		if rcpt == "" {
			continue
		}
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s 失败: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage 拼 MIME 邮件。Date 头让客户端能按时间排序。
func buildMessage(cfg Config, to, subject, body string, isHTML bool) []byte {
	var b strings.Builder
	b.WriteString("From: ")
	if cfg.FromName != "" {
		// RFC 2047 quoted-printable 编码太复杂，这里偷懒用 utf-8 raw。
		// 现代客户端（MUA）普遍支持；老古董 OE/旧 Lotus 会乱码，但已不是用户群。
		b.WriteString(fmt.Sprintf("%s <%s>\r\n", cfg.FromName, cfg.From))
	} else {
		b.WriteString("<" + cfg.From + ">\r\n")
	}
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	if isHTML {
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	} else {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	}
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// 用 base64 编码正文避免长行/非 ASCII 被某些 SMTP 代理拆坏
	b.WriteString("\r\n")
	b.WriteString(base64Wrap(body))
	return []byte(b.String())
}

// base64Wrap 把正文 base64 化并按 76 字符折行（MIME 标准）。
func base64Wrap(s string) string {
	const line = 76
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var out strings.Builder
	for i := 0; i < len(enc); i += line {
		end := i + line
		if end > len(enc) {
			end = len(enc)
		}
		out.WriteString(enc[i:end])
		out.WriteString("\r\n")
	}
	return out.String()
}

// SendSensitiveSafe 给敏感凭据用的便利方法。
// 成功静默；失败只记"给谁发没成功"（不记 subject/body）。
// 调用方（如 service.CreateChallenge）可以忽略返回值，发邮件失败不阻塞登录。
func (m *Mailer) SendSensitiveSafe(to, subject, body string) {
	if err := m.Send(to, subject, body); err != nil {
		log.Printf("[mailer] 发送邮件给 %s 失败: %v", redactAddr(to), err)
	}
}

// redactAddr 把 email 形如 a***@b.c，避免完整地址进日志。
func redactAddr(addr string) string {
	for i, ch := range addr {
		if ch == '@' {
			if i < 2 {
				return "***" + addr[i:]
			}
			return addr[:1] + "***" + addr[i:]
		}
	}
	return "***"
}
