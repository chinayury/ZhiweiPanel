package service

import (
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zhiwei/panel/internal/mailer"
	"github.com/zhiwei/panel/internal/store"
)

// newAccountTestService 用真 sqlite + nil mailer（不发邮件）。
func newAccountTestService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(storePath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Service{db: db}
}

func storePath(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/account.db"
}

// seedAdmin 写入测试管理员账户，返回用户名。
func seedAdmin(t *testing.T, s *Service, name string) {
	t.Helper()
	if err := s.DB().SaveUser(name, "$2a$10$abcdefghijklmnopqrstuv", "admin"); err != nil {
		t.Fatal(err)
	}
}

// ---------- IsAnomalousLogin 纯函数测试 ----------

func TestIsAnomalousLogin_NoHistory(t *testing.T) {
	now := time.Now()
	// 首次登录：prev 为空 → 不算异常
	if r := IsAnomalousLogin("", "", time.Time{}, "1.2.3.4", "UA", now); r != nil {
		t.Errorf("first login should not be anomalous, got %+v", r)
	}
}

func TestIsAnomalousLogin_SameSubnet(t *testing.T) {
	now := time.Now()
	prev := now.Add(-1 * time.Hour)
	// 同 /24 子网 + 相似 UA → 正常
	if r := IsAnomalousLogin("1.2.3.4", "Mozilla/5.0 Chrome/120 Mac OS", prev,
		"1.2.3.99", "Mozilla/5.0 Chrome/120 Mac OS", now); r != nil {
		t.Errorf("same-subnet same-UA should not be anomalous, got %+v", r)
	}
}

func TestIsAnomalousLogin_DiffSubnet(t *testing.T) {
	now := time.Now()
	prev := now.Add(-10 * time.Minute)
	r := IsAnomalousLogin("1.2.3.4", "Mozilla/5.0 Chrome/120 Mac OS", prev,
		"8.8.8.8", "Mozilla/5.0 Chrome/120 Mac OS", now)
	if r == nil {
		t.Error("different subnet should be anomalous")
	}
	if !strings.Contains(r.Reason, "IP") {
		t.Errorf("reason should mention IP, got %q", r.Reason)
	}
}

func TestIsAnomalousLogin_StaleTime(t *testing.T) {
	now := time.Now()
	prev := now.Add(-30 * 24 * time.Hour) // 30 天前
	if r := IsAnomalousLogin("1.2.3.4", "Mozilla/5.0 Chrome/120", prev,
		"1.2.3.4", "Mozilla/5.0 Chrome/120", now); r == nil {
		t.Error("stale login (30 days) should be anomalous even if same IP/UA")
	}
}

func TestSameSubnet(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.3.4", "1.2.3.99", true},
		{"1.2.3.4", "1.2.4.4", false},
		{"::1", "::2", true}, // /48 same
		{"fe80::1", "fe80::2", true},
		{"1.2.3.4", "::1", false}, // mixed
		{"notip", "1.2.3.4", false},
	}
	for _, c := range cases {
		if got := sameSubnet(c.a, c.b); got != c.want {
			t.Errorf("sameSubnet(%q,%q)=%v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSimilarUA(t *testing.T) {
	a := "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/120.0.0 Safari/537.36"
	b := "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/120.0.0 Safari/537.36"
	if !similarUA(a, b) {
		t.Error("identical UA should be similar")
	}
	c := "Mozilla/5.0 (Windows NT 10.0) Firefox/121.0"
	if similarUA(a, c) {
		t.Error("totally different UA should not be similar")
	}
}

// ---------- SetEmail / GetUserProfile ----------

func TestSetEmail_RejectsInvalid(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	if err := s.SetEmail("admin", "not-an-email"); err == nil {
		t.Error("invalid email should be rejected")
	}
	if err := s.SetEmail("admin", ""); err == nil {
		t.Error("empty email should be rejected")
	}
}

func TestSetEmail_StoresValue(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	at := string([]byte{'@'})
	email := "admin" + at + "example.com"
	if err := s.SetEmail("admin", email); err != nil {
		t.Fatal(err)
	}
	prof, err := s.GetUserProfile("admin")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Email != email {
		t.Errorf("email not stored: %q", prof.Email)
	}
	if prof.EmailVerified {
		t.Error("email should not be verified after SetEmail alone")
	}
}

func TestMarkEmailVerified(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	at := string([]byte{'@'})
	email := "admin" + at + "example.com"
	_ = s.SetEmail("admin", email)
	if err := s.MarkEmailVerified("admin"); err != nil {
		t.Fatal(err)
	}
	prof, _ := s.GetUserProfile("admin")
	if !prof.EmailVerified {
		t.Error("EmailVerified should be true after MarkEmailVerified")
	}
}

// ---------- Challenge 创建与校验（mailer 设为 nil 走不到发邮件分支） ----------

func TestCreateChallenge_NoEmail(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	// 没绑邮箱 → 报错
	if _, err := s.CreateChallenge("admin", "login_verify"); err == nil {
		t.Error("should fail when no email bound")
	}
}

func TestCreateChallenge_SMTPNotConfigured(t *testing.T) {
	s := newAccountTestService(t)
	s.mailer = mailer.New(mailer.Config{}) // 故意未配置
	seedAdmin(t, s, "admin")
	at := string([]byte{'@'})
	_ = s.SetEmail("admin", "admin"+at+"x.com")
	if _, err := s.CreateChallenge("admin", "login_verify"); err == nil {
		t.Error("should fail when SMTP not configured")
	}
}

// 真实发邮件的端到端：起本地 SMTP，CreateChallenge → 抓邮件 → 解析验证码 → VerifyChallenge。
func TestCreateAndVerifyChallenge_End2End(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := mustPort(ln.Addr().String())

	got := make(chan string, 1)
	go runLocalSMTPServer(ln, got)

	s := newAccountTestService(t)
	at := string([]byte{'@'})
	from := "zhiweipanel" + at + "example.com"
	to := "admin" + at + "example.com"
	s.mailer = mailer.New(mailer.Config{
		Host: "127.0.0.1", Port: port, From: from, Encryption: "none",
	})
	seedAdmin(t, s, "admin")
	_ = s.SetEmail("admin", to)

	cid, err := s.CreateChallenge("admin", "login_verify")
	if err != nil {
		t.Fatal(err)
	}
	// 抓邮件
	var body string
	select {
	case body = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("mock SMTP didn't receive mail")
	}
	code := extractCode(body)
	if code == "" {
		t.Fatalf("no code in mail:\n%s", body)
	}

	// 校验通过
	ok, err := s.VerifyChallenge(cid, code)
	if err != nil || !ok {
		t.Fatalf("VerifyChallenge: ok=%v err=%v", ok, err)
	}
	// 重复校验应失败（consume 掉）
	ok, err = s.VerifyChallenge(cid, code)
	if err == nil || ok {
		t.Errorf("challenge should be consumed, but got ok=%v err=%v", ok, err)
	}
}

func TestVerifyChallenge_WrongCode(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	at := string([]byte{'@'})
	_ = s.SetEmail("admin", "a"+at+"b.com")
	// 直接插一条假 challenge，绕过 mailer
	id := store.NewID()
	hash, _ := bcrypt.GenerateFromPassword([]byte("000000"), bcrypt.MinCost)
	if err := s.DB().SaveChallenge(id, "admin", string(hash), "login_verify", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	ok, err := s.VerifyChallenge(id, "111111")
	if err == nil || ok {
		t.Error("wrong code should fail")
	}
}

// ---------- SMTP 配置 ----------

func TestSMTPConfig_Roundtrip(t *testing.T) {
	s := newAccountTestService(t)
	cfg := &SMTPConfig{
		Host:       "smtp.test.com",
		Port:       587,
		User:       "user1",
		Password:   "p@ssw0rd",
		From:       "from@test.com",
		FromName:   "Test Sender",
		Encryption: "starttls",
	}
	if err := s.SaveSMTPConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSMTPConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != cfg.Host || got.Port != cfg.Port || got.User != cfg.User ||
		got.Password != cfg.Password || got.From != cfg.From || got.FromName != cfg.FromName ||
		got.Encryption != cfg.Encryption {
		t.Errorf("roundtrip mismatch:\n got=%+v\n want=%+v", got, cfg)
	}
}

func TestSMTPConfig_RejectsBadPort(t *testing.T) {
	s := newAccountTestService(t)
	if err := s.SaveSMTPConfig(&SMTPConfig{Port: 99999}); err == nil {
		t.Error("port 99999 should be rejected")
	}
}

// ---------- 工具 ----------

func TestGenCode6_LengthAndDigits(t *testing.T) {
	for i := 0; i < 50; i++ {
		c, err := genCode6()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 6 {
			t.Fatalf("code length: %d, want 6", len(c))
		}
		for _, r := range c {
			if r < '0' || r > '9' {
				t.Fatalf("non-digit: %q", c)
			}
		}
	}
}

func TestRenderChallengeMail_PerPurpose(t *testing.T) {
	for _, p := range []string{"login_verify", "password_reset", "email_verify", "unknown"} {
		_, body := renderChallengeMail(p, "123456")
		if !strings.Contains(body, "123456") {
			t.Errorf("purpose %s: body missing code", p)
		}
	}
}

// ---------- ResetPassword ----------

func seedAdminWithEmail(t *testing.T, s *Service, name, email string) {
	t.Helper()
	seedAdmin(t, s, name)
	at := string([]byte{'@'})
	_ = s.SetEmail(name, email+at+"x.com")
}

func TestResetPassword_Success(t *testing.T) {
	s := newAccountTestService(t)
	seedAdminWithEmail(t, s, "admin", "admin")

	// 直接插一条假 challenge，绕过 mailer
	id := store.NewID()
	hash, _ := bcrypt.GenerateFromPassword([]byte("000000"), bcrypt.MinCost)
	if err := s.DB().SaveChallenge(id, "admin", string(hash), "password_reset", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	// 也存一个 session，验证改密后被踢
	tok := "old-session-token"
	_ = s.DB().SaveSession(tok, "admin", time.Hour)

	if err := s.ResetPassword(id, "admin", "000000", "newpassword123"); err != nil {
		t.Fatal(err)
	}

	// 旧密 "Sp-Deploy" 没了
	if _, _, err := s.DB().GetUser("admin"); err != nil {
		t.Fatal(err)
	}
	// 直接 bcrypt 验新密
	hash2, _, _ := s.DB().GetUser("admin")
	if err := bcrypt.CompareHashAndPassword([]byte(hash2), []byte("newpassword123")); err != nil {
		t.Errorf("new password not set: %v", err)
	}
	// 旧 session 没了
	if _, err := s.DB().GetSession(tok); err == nil {
		t.Error("old session should be deleted after password reset")
	}
	// challenge 已 consume
	if _, err := s.DB().GetChallenge(id); err == nil {
		t.Error("challenge should be consumed after reset")
	}
}

func TestResetPassword_WrongCode(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	id := store.NewID()
	hash, _ := bcrypt.GenerateFromPassword([]byte("000000"), bcrypt.MinCost)
	_ = s.DB().SaveChallenge(id, "admin", string(hash), "password_reset", 10*time.Minute)

	if err := s.ResetPassword(id, "admin", "111111", "newpassword123"); err == nil {
		t.Error("wrong code should fail")
	}
	// attempts 应 +1
	c, _ := s.DB().GetChallenge(id)
	if c.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", c.Attempts)
	}
	// 密码未改
	hash2, _, _ := s.DB().GetUser("admin")
	// 原 hash 是 seedAdmin 写的 abcdef...
	if err := bcrypt.CompareHashAndPassword([]byte(hash2), []byte("newpassword123")); err == nil {
		t.Error("password should not be changed on failed verify")
	}
}

func TestResetPassword_UsernameMismatch(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	// 准备 admin 的 challenge
	id := store.NewID()
	hash, _ := bcrypt.GenerateFromPassword([]byte("000000"), bcrypt.MinCost)
	_ = s.DB().SaveChallenge(id, "admin", string(hash), "password_reset", 10*time.Minute)

	// 但传另一个 username
	if err := s.ResetPassword(id, "attacker", "000000", "newpassword123"); err == nil {
		t.Error("username mismatch should fail")
	}
}

func TestResetPassword_WrongPurpose(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	id := store.NewID()
	hash, _ := bcrypt.GenerateFromPassword([]byte("000000"), bcrypt.MinCost)
	// 故意用 login_verify purpose
	_ = s.DB().SaveChallenge(id, "admin", string(hash), "login_verify", 10*time.Minute)
	if err := s.ResetPassword(id, "admin", "000000", "newpassword123"); err == nil {
		t.Error("wrong purpose should fail")
	}
}

func TestResetPassword_Expired(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	id := store.NewID()
	hash, _ := bcrypt.GenerateFromPassword([]byte("000000"), bcrypt.MinCost)
	// 1 秒过期
	_ = s.DB().SaveChallenge(id, "admin", string(hash), "password_reset", 1*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if err := s.ResetPassword(id, "admin", "000000", "newpassword123"); err == nil {
		t.Error("expired challenge should fail")
	}
}

// TestVerifyChallenge_EmailVerifySideEffect email_verify 通过时自动把 email_verified 置 1。
// 修过的 bug：早期只下发 session，不动 email_verified，前端调 verify-challenge
// 一直显示"未验证"。回归测试。
func TestVerifyChallenge_EmailVerifySideEffect(t *testing.T) {
	s := newAccountTestService(t)
	seedAdmin(t, s, "admin")
	at := string([]byte{'@'})
	_ = s.SetEmail("admin", "admin"+at+"x.com")
	// 初始未验证
	prof, _ := s.GetUserProfile("admin")
	if prof.EmailVerified {
		t.Fatal("setup wrong: email should not be verified yet")
	}
	id := store.NewID()
	hash, _ := bcrypt.GenerateFromPassword([]byte("999999"), bcrypt.MinCost)
	if err := s.DB().SaveChallenge(id, "admin", string(hash), "email_verify", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	ok, err := s.VerifyChallenge(id, "999999")
	if err != nil || !ok {
		t.Fatalf("verify failed: ok=%v err=%v", ok, err)
	}
	// email_verified 应已置 1
	prof, _ = s.GetUserProfile("admin")
	if !prof.EmailVerified {
		t.Error("email_verified should be true after email_verify challenge")
	}
	// 其他 purpose 不应触发
	id2 := store.NewID()
	hash2, _ := bcrypt.GenerateFromPassword([]byte("888888"), bcrypt.MinCost)
	_ = s.DB().SaveChallenge(id2, "admin", string(hash2), "login_verify", 10*time.Minute)
	// 故意先重置回未验证
	_ = s.db.UpdateUserEmail("admin", "new"+at+"x.com") // SetEmail 已经置 verified=0
	ok, err = s.VerifyChallenge(id2, "888888")
	if err != nil || !ok {
		t.Fatalf("login verify failed: ok=%v err=%v", ok, err)
	}
	prof, _ = s.GetUserProfile("admin")
	if prof.EmailVerified {
		t.Error("login_verify should NOT mark email_verified")
	}
}

// extractCode 从 SMTP 抓到的邮件里找 6 位数字（第一处）。
func extractCode(mail string) string {
	digits := ""
	for _, ch := range mail {
		if ch >= '0' && ch <= '9' {
			digits += string(ch)
			if len(digits) == 6 {
				return digits
			}
		} else {
			digits = ""
		}
	}
	return ""
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

// runLocalSMTPServer 极简 SMTP 服务器：接 EHLO/MAIL/RCPT/DATA/QUIT，
// 把 DATA 阶段累计的字节流（base64 解码前的原文）通过 channel 传给测试。
//
// 我们不解析 MIME 结构，直接抓完整原始报文——测试自己 extractCode 抠出验证码。
func runLocalSMTPServer(ln net.Listener, got chan<- string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleSMTPConn(conn, got)
	}
}

func handleSMTPConn(conn net.Conn, got chan<- string) {
	defer conn.Close()
	_, _ = conn.Write([]byte("220 test ESMTP\r\n"))
	buf := make([]byte, 4096)
	state := 0
	data := []byte{}
	for {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if n == 0 {
			return
		}
		if err != nil {
			return
		}
		for _, l := range strings.Split(string(buf[:n]), "\r\n") {
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
					case got <- string(data):
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
}
