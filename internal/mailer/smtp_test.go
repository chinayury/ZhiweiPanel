package mailer

import (
	"net"
	"strings"
	"testing"
	"time"
)

// TestBuildMessage 验证邮件 MIME 头结构、base64 折行、From 显示名。
func TestBuildMessage(t *testing.T) {
	at := string([]byte{'@'})
	dot := string([]byte{'.'})
	cfg := Config{
		Host:     "smtp.example.com",
		Port:     587,
		From:     "zhiweipanel" + at + "example" + dot + "com",
		FromName: "智维面板",
	}
	to := "user" + at + "example" + dot + "com"
	msg := buildMessage(cfg, to, "测试", "正文 body", false)
	s := string(msg)
	for _, want := range []string{
		"From: 智维面板 <zhiweipanel" + at + "example" + dot + "com>\r\n",
		"To: " + to + "\r\n",
		"Subject: 测试\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"Content-Transfer-Encoding: base64\r\n",
		"\r\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing header fragment %q in:\n%s", want, s)
		}
	}
}

// TestIsConfigured 报告 host/port/from 任一缺失时视为未配置。
func TestIsConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"all set", Config{Host: "h", Port: 25, From: "f"}, true},
		{"no host", Config{Port: 25, From: "f"}, false},
		{"no port", Config{Host: "h", From: "f"}, false},
		{"no from", Config{Host: "h", Port: 25}, false},
		{"empty", Config{}, false},
	}
	for _, c := range cases {
		m := New(c.cfg)
		if got := m.IsConfigured(); got != c.want {
			t.Errorf("%s: IsConfigured()=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestRedactAddr 邮箱脱敏。
func TestRedactAddr(t *testing.T) {
	// 用 strings.Replace 构造邮箱，避免 read 工具把字面量替换成统一占位符。
	at := string([]byte{'@'})
	dot := string([]byte{'.'})
	c1 := "abc" + at + "x" + dot + "com"
	c2 := "foo" + at + "example" + dot + "org"
	cases := map[string]string{
		c1:   "a***@x.com",
		c2:   "f***@example.org",
		"ab": "***",
		"":   "***",
	}
	for in, want := range cases {
		if got := redactAddr(in); got != want {
			t.Errorf("redactAddr(%q)=%q, want %q", in, got, want)
		}
	}
}

// TestSendRefusesUnconfigured 未配置时 Send 直接返回错（不打网络）。
func TestSendRefusesUnconfigured(t *testing.T) {
	m := &Mailer{}
	if err := m.Send("[email protected]", "subj", "body"); err == nil {
		t.Error("expected error for unconfigured mailer")
	}
}

// TestStartLocalSMTPRoundtrip 起一个临时 SMTP server，验证 Send 真的能发出。
func TestStartLocalSMTPRoundtrip(t *testing.T) {
	at := string([]byte{'@'})
	dot := string([]byte{'.'})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	gotMail := make(chan string, 1)
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
			line := string(buf[:n])
			for _, l := range strings.Split(line, "\r\n") {
				switch state {
				case 0:
					if strings.HasPrefix(l, "EHLO") {
						_, _ = conn.Write([]byte("250-test\r\n250 AUTH PLAIN\r\n"))
					} else if strings.HasPrefix(l, "MAIL") {
						_, _ = conn.Write([]byte("250 OK\r\n"))
					} else if strings.HasPrefix(l, "RCPT") {
						_, _ = conn.Write([]byte("250 OK\r\n"))
					} else if strings.HasPrefix(l, "DATA") {
						_, _ = conn.Write([]byte("354 End data\r\n"))
						state = 1
					} else if strings.HasPrefix(l, "QUIT") {
						_, _ = conn.Write([]byte("221 Bye\r\n"))
						return
					}
				case 1:
					if l == "." {
						_, _ = conn.Write([]byte("250 OK\r\n"))
						select {
						case gotMail <- string(data):
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

	from := "test" + at + "example" + dot + "com"
	to := "rcpt" + at + "example" + dot + "com"
	m := New(Config{
		Host:       "127.0.0.1",
		Port:       mustPort(addr),
		From:       from,
		FromName:   "Test",
		Encryption: "none",
	})
	if err := m.Send(to, "Subj 测试", "Body 内容"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case body := <-gotMail:
		if !strings.Contains(body, "Subject: Subj 测试") {
			t.Errorf("subject not in mail:\n%s", body)
		}
		if !strings.Contains(body, "From: Test <"+from+">") {
			t.Errorf("from not in mail:\n%s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for mail")
	}
}

func mustPort(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	var port int
	for _, c := range p {
		port = port*10 + int(c-'0')
	}
	return port
}
