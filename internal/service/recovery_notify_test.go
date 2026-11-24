package service

import (
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/mailer"
)

// 恢复码登录提醒（v0.8.1）：best-effort 语义——任何缺件都不能 panic/报错，
// 因为登录已经成功，通知发不出去绝不能把用户踢回去。

func TestNotifyRecoveryLoginNoEmailNoPanic(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	// 未绑邮箱 + mailer nil → 静默跳过
	s.NotifyRecoveryLogin("admin", "203.0.113.9", "Mozilla/5.0")
}

func TestNotifyRecoveryLoginUnknownUserNoPanic(t *testing.T) {
	s := newAccountTestService(t)
	// 用户不存在 → GetUserProfile 报错 → 直接返回
	s.NotifyRecoveryLogin("ghost", "203.0.113.9", "curl/8")
}

func TestNotifyRecoveryLoginMailerUnconfiguredNoPanic(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	// 真实 mailer 但配置为空 → IsConfigured=false → 只记日志
	s.mailer = mailer.New(mailer.Config{})
	s.NotifyRecoveryLogin("admin", "203.0.113.9", "Mozilla/5.0")
}

// startFakeSMTP 起一个一次性假 SMTP server，返回地址与收件 channel。
// 照搬 internal/mailer/smtp_test.go 的 roundtrip 模式（本包不重复造轮子的前提是
// 看得懂那边怎么假造服务器）。
func startFakeSMTP(t *testing.T) (addr string, gotMail <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("220 test ESMTP\r\n"))
		buf := make([]byte, 4096)
		state := 0
		data := []byte{}
		for {
			n, _ := conn.Read(buf)
			if n == 0 {
				return
			}
			for _, l := range strings.Split(string(buf[:n]), "\r\n") {
				switch state {
				case 0:
					switch {
					case strings.HasPrefix(l, "EHLO"):
						_, _ = conn.Write([]byte("250-test\r\n250 AUTH PLAIN\r\n"))
					case strings.HasPrefix(l, "MAIL"), strings.HasPrefix(l, "RCPT"):
						_, _ = conn.Write([]byte("250 OK\r\n"))
					case strings.HasPrefix(l, "DATA"):
						_, _ = conn.Write([]byte("354 End data\r\n"))
						state = 1
					case strings.HasPrefix(l, "QUIT"):
						_, _ = conn.Write([]byte("221 Bye\r\n"))
						return
					}
				case 1:
					if l == "." {
						_, _ = conn.Write([]byte("250 OK\r\n"))
						select {
						case ch <- string(data):
						default:
						}
						state = 0
						data = data[:0]
					} else {
						data = append(data, []byte(l+"\r\n")...)
					}
				}
			}
		}
	}()
	return ln.Addr().String(), ch
}

// TestNotifyRecoveryLoginSendsMail 全链路：配好 SMTP + 绑定邮箱的账户 →
// 提醒邮件真的到达，且内容带时间/IP/UA 与行动指引。
func TestNotifyRecoveryLoginSendsMail(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	if err := s.DB().UpdateUserEmail("admin", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	addr, ch := startFakeSMTP(t)
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	s.mailer = mailer.New(mailer.Config{
		Host: "127.0.0.1", Port: port,
		From: "panel@example.com", FromName: "ZhiweiPanel",
		Encryption: "none",
	})

	s.NotifyRecoveryLogin("admin", "203.0.113.9", "Mozilla/5.0 (Rescue)")

	select {
	case raw := <-ch:
		if !strings.Contains(raw, "Subject: [智维面板] 安全提醒：恢复码被用于登录") {
			t.Errorf("主题不对:\n%s", raw)
		}
		if !strings.Contains(raw, "To: owner@example.com") {
			t.Errorf("收件人不对:\n%s", raw)
		}
		// 正文是 base64（buildMessage 统一编码），断言前先解码
		parts := strings.SplitN(raw, "\r\n\r\n", 2)
		if len(parts) != 2 {
			t.Fatalf("邮件格式异常:\n%s", raw)
		}
		decoded, derr := base64DecodeAll(parts[1])
		if derr != nil {
			t.Fatalf("正文 base64 解码失败: %v", derr)
		}
		for _, want := range []string{"203.0.113.9", "Mozilla/5.0 (Rescue)", "重新生成恢复码", "修改管理员密码"} {
			if !strings.Contains(decoded, want) {
				t.Errorf("邮件正文应包含 %q:\n%s", want, decoded)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: 提醒邮件未到达")
	}
}

// base64DecodeAll 解码（可能跨多行折叠的）base64 正文。
func base64DecodeAll(s string) (string, error) {
	compact := strings.ReplaceAll(s, "\r\n", "")
	compact = strings.ReplaceAll(compact, "\n", "")
	b, err := base64.StdEncoding.DecodeString(compact)
	return string(b), err
}

func TestRenderRecoveryLoginMail(t *testing.T) {
	subject, body := renderRecoveryLoginMail("203.0.113.9", "Mozilla/5.0 (Test)")
	if !strings.Contains(subject, "恢复码") {
		t.Errorf("主题应点明恢复码: %q", subject)
	}
	for _, want := range []string{"203.0.113.9", "Mozilla/5.0 (Test)", "重新生成恢复码", "修改管理员密码"} {
		if !strings.Contains(body, want) {
			t.Errorf("正文应包含 %q: %s", want, body)
		}
	}
}
