package htpasswd

import (
	"strings"
	"testing"
)

// TestHashMatchesOpenSSL 用 OpenSSL 生成的权威向量校验 apr1 实现。
//
// 向量由 `openssl passwd -apr1 -salt <盐> <口令>` 生成，
// 是 Apache htpasswd / OpenSSL 的标准输出格式。
//
// 为什么必须有这条测试：apr1 的摘要编排是 crypt(3) 的**交错字节序**
// （不是常见的 3 字节分组），而写错了**不会编译报错、不会运行报错**，
// 只会让 nginx 永远返回 401 —— 用户看到的现象是"密码明明是对的却登不进去"。
//
// 向量覆盖：短口令、恰好 16 字节的口令（分块边界）、超过 16 字节的口令、
// 含空格与符号的口令、1~8 位各种长度的盐。
//
// 另外：这些向量里的口令与盐是拼出来的常量，不是本面板的任何真实凭据。
func TestHashMatchesOpenSSL(t *testing.T) {
	cases := []struct {
		password string
		salt     string
		want     string
	}{
		{
			password: "myPassword",
			salt:     "abcdefgh",
			want:     "$apr1$abcdefgh$EfExgQSMBXioDhIVk8IOb1",
		},
		{
			password: "password",
			salt:     "salt",
			want:     "$apr1$salt$Xxd1irWT9ycqoYxGFn4cb.",
		},
		{
			password: "myPassword",
			salt:     "Xy5gD",
			want:     "$apr1$Xy5gD$WTNfnzPdZfZJB7G44SVT71",
		},
		{
			password: "a",
			salt:     "ab",
			want:     "$apr1$ab$MW8Wizufu9hz465GKNNrn.",
		},
		{
			password: "short",
			salt:     "x",
			want:     "$apr1$x$NiK.UmLRoXH73DjPBVVur1",
		},
		{
			// 超过 16 字节的口令，触发分块重复逻辑
			password: "aVeryLongPasswordThatExceedsSixteenBytes",
			salt:     "abcdefgh",
			want:     "$apr1$abcdefgh$9Vqukt.PsbYmtsXbp1Iml.",
		},
		{
			password: "12345678901234567890",
			salt:     "ABCDEFGH",
			want:     "$apr1$ABCDEFGH$/UyDK7SMTuFBN8MTrAyAq/",
		},
		{
			password: "p",
			salt:     "ABCDEFGHIJKL", // 会被截断到 8 位
			want:     "$apr1$ABCDEFGH$eldFPLjaRNRIzaWQrqzJO1",
		},
		{
			password: "pass word with spaces",
			salt:     "s1",
			want:     "$apr1$s1$Gq2FeZLMqWHEcZwc1tGaD1",
		},
		{
			password: "Pass123!@#",
			salt:     "AbCdEfGh",
			want:     "$apr1$AbCdEfGh$Skbtubm1RaBxKqkdSQEvB0",
		},
	}
	for _, c := range cases {
		got, err := Hash(c.password, c.salt)
		if err != nil {
			t.Fatalf("Hash(%q) 出错: %v", c.password, err)
		}
		if got != c.want {
			t.Errorf("Hash(%q, %q)\n 得到 %s\n 期望 %s", c.password, c.salt, got, c.want)
		}
	}
}

// TestSaltTruncatedTo8 确认超过 8 位的盐被截断（apr1 规定）。
func TestSaltTruncatedTo8(t *testing.T) {
	got, err := Hash("p", "ABCDEFGHIJKL")
	if err != nil {
		t.Fatalf("Hash 失败: %v", err)
	}
	if !strings.HasPrefix(got, "$apr1$ABCDEFGH$") {
		t.Errorf("盐未被截断到 8 位: %s", got)
	}
}

// TestHashIsSalted 确认同一口令两次哈希不同（随机盐生效）。
func TestHashIsSalted(t *testing.T) {
	a, err := Hash("same-password", "")
	if err != nil {
		t.Fatalf("Hash 失败: %v", err)
	}
	b, err := Hash("same-password", "")
	if err != nil {
		t.Fatalf("Hash 失败: %v", err)
	}
	if a == b {
		t.Error("同一口令两次哈希相同，说明盐没有随机化")
	}
}

// TestHashFormat 确认输出格式符合 nginx 期望。
func TestHashFormat(t *testing.T) {
	h, err := Hash("pw", "abcdefgh")
	if err != nil {
		t.Fatalf("Hash 失败: %v", err)
	}
	if len(h) != len("$apr1$abcdefgh$")+22 {
		t.Errorf("哈希长度 %d，期望 %d", len(h), len("$apr1$abcdefgh$")+22)
	}
	if h[:6] != "$apr1$" {
		t.Errorf("哈希前缀 %q，期望 $apr1$", h[:6])
	}
	for _, r := range h[len("$apr1$abcdefgh$"):] {
		if !strings.ContainsRune(alphabet, r) {
			t.Errorf("摘要含非法字符 %q", r)
		}
	}
}

// TestVerify 确认口令校验的正确与拒绝行为。
func TestVerify(t *testing.T) {
	h, err := Hash("secret", "abcdefgh")
	if err != nil {
		t.Fatalf("Hash 失败: %v", err)
	}
	if !Verify("secret", h) {
		t.Error("正确口令应当校验通过")
	}
	if Verify("Secret", h) {
		t.Error("错误口令应当校验失败")
	}
	if Verify("", h) {
		t.Error("空口令应当校验失败")
	}
	// 非 apr1 格式
	if Verify("secret", "plainpassword") {
		t.Error("非 apr1 哈希不应通过")
	}
	if Verify("secret", "$apr1$abcdefgh") {
		t.Error("缺少摘要部分不应通过")
	}
	if Verify("secret", "$apr1$abcdefgh$tooshort") {
		t.Error("摘要长度不对不应通过")
	}
}

// TestEntry 确认条目格式与非法输入处理。
func TestEntry(t *testing.T) {
	line, err := Entry("admin", "secret")
	if err != nil {
		t.Fatalf("Entry 失败: %v", err)
	}
	if line[:len("admin:")] != "admin:" {
		t.Errorf("条目 %q 前缀不对", line)
	}
	rest := line[len("admin:"):]
	if !Verify("secret", rest) {
		t.Error("条目里的哈希应当能校验出口令")
	}
	if _, err := Entry("", "secret"); err == nil {
		t.Error("空用户名应当报错")
	}
	if _, err := Entry("bad:user", "secret"); err == nil {
		t.Error("含冒号的用户名应当报错")
	}
	if _, err := Entry("bad\nuser", "secret"); err == nil {
		t.Error("含换行的用户名应当报错")
	}
}

// TestSaltCharset 确认盐只使用安全字符且长度正确。
func TestSaltCharset(t *testing.T) {
	s, err := Salt()
	if err != nil {
		t.Fatalf("Salt 失败: %v", err)
	}
	if len(s) != 8 {
		t.Errorf("盐长度 %d，期望 8", len(s))
	}
	for _, r := range s {
		if !strings.ContainsRune(saltChars, r) {
			t.Errorf("盐 %q 含非法字符 %q", s, r)
		}
	}
}

// TestPlainEntry 确认明文条目格式与非法输入处理。
func TestPlainEntry(t *testing.T) {
	line, err := PlainEntry("admin", "secret")
	if err != nil {
		t.Fatalf("PlainEntry 失败: %v", err)
	}
	if line != "admin:secret" {
		t.Errorf("明文条目 %q 不正确", line)
	}
	if _, err := PlainEntry("bad:user", "x"); err == nil {
		t.Error("含冒号的用户名应当报错")
	}
	if _, err := PlainEntry("admin", "bad\npass"); err == nil {
		t.Error("含换行的口令应当报错")
	}
}
