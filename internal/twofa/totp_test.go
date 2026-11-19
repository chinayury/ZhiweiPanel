package twofa

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// 参考 RFC 6238 附录 B 的测试向量：密钥 = ASCII("12345678901234567890")，
// SHA1，8 位数字。本项目用 6 位数字 = 同一截断值 mod 10^6，由此换算出
// 6 位期望值（保留前导零）。Validate 吃 base32 形态的密钥，测试里先把
// ASCII 字节编码成 base32。
var rfcVectors = []struct {
	t    int64
	hotp string
	code string
}{
	{59, "94287082", "287082"},
	{1111111109, "07081804", "081804"},
	{1111111111, "14050471", "050471"},
	{1234567890, "89005924", "005924"},
	{2000000000, "69279037", "279037"},
	{20000000000, "65353130", "353130"},
}

// rfcSecretRaw 是 RFC 6238 附录 B 的密钥（这 20 个 ASCII 字符本身就是密钥字节）。
const rfcSecretRaw = "12345678901234567890"

// rfcSecretB32 是 rfcSecretRaw 的 base32 形态（= GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ）。
var rfcSecretB32 = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(rfcSecretRaw))

func TestValidateRFCVectors(t *testing.T) {
	for _, v := range rfcVectors {
		// hotpAt 输出 6 位（mod 10^6），应与 RFC 8 位向量 mod 10^6 一致
		if got := hotpAt([]byte(rfcSecretRaw), v.t/StepSec); got != v.code {
			t.Errorf("t=%d: 6 位 hotp = %q, want %q", v.t, got, v.code)
		}
		if !Validate(rfcSecretB32, v.code, time.Unix(v.t, 0)) {
			t.Errorf("t=%d: Validate(%q) = false, want true", v.t, v.code)
		}
	}
}

func TestValidateWindow(t *testing.T) {
	// ±1 窗口内（±30 秒）仍通过；±2 步之外拒绝
	v := rfcVectors[1] // t=1111111109 → code 081804（选大 t，避开负数时间步退化）
	if !Validate(rfcSecretB32, v.code, time.Unix(v.t-StepSec, 0)) {
		t.Error("前 30 秒应在容差窗口内通过")
	}
	if !Validate(rfcSecretB32, v.code, time.Unix(v.t+StepSec, 0)) {
		t.Error("后 30 秒应在容差窗口内通过")
	}
	if Validate(rfcSecretB32, v.code, time.Unix(v.t-StepSec*2, 0)) {
		t.Error("前 60 秒（跨 2 步）应拒绝")
	}
	if Validate(rfcSecretB32, v.code, time.Unix(v.t+StepSec*2, 0)) {
		t.Error("后 60 秒（跨 2 步）应拒绝")
	}
}

func TestValidateRejects(t *testing.T) {
	now := time.Unix(rfcVectors[0].t, 0)
	cases := []string{
		"",        // 空
		"12345",   // 5 位
		"1234567", // 7 位
		"abcdef",  // 非数字
		"28708 ",  // 少一位
		"000000",  // 错误口令
	}
	for _, c := range cases {
		if Validate(rfcSecretB32, c, now) {
			t.Errorf("非法/错误口令 %q 不应通过", c)
		}
	}
	// 带空格分隔仍应通过（认证器复制粘贴常见形态）
	if !Validate(rfcSecretB32, "287 082", now) {
		t.Error("「287 082」（带空格）应规整后通过")
	}
	// 坏密钥
	if Validate("!!not-base32!!", "287082", now) {
		t.Error("非法 base32 密钥应返回 false")
	}
}

func TestValidateLeadingZero(t *testing.T) {
	// 081804 带前导零：字符串比较必须保留前导零（数字比较会丢）
	v := rfcVectors[1]
	if !Validate(rfcSecretB32, v.code, time.Unix(v.t, 0)) {
		t.Errorf("前导零口令 %q 应通过", v.code)
	}
}

func TestGenerateSecretRoundTrip(t *testing.T) {
	s, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if len(s) != 32 { // 20 字节 base32 无填充 = 32 字符
		t.Fatalf("密钥长度应 32 字符，实际 %d", len(s))
	}
	if strings.ContainsAny(s, "=/+") {
		t.Errorf("密钥不应包含 padding/+/= 符号: %q", s)
	}
	now := time.Now()
	code := hotpAt(mustDecode(t, s), now.Unix()/StepSec)
	if !Validate(s, code, now) {
		t.Errorf("GenerateSecret 的密钥应能通过自身口令校验: %q", code)
	}
	// 两次生成的密钥不同（随机源在动）
	s2, _ := GenerateSecret()
	if s == s2 {
		t.Error("两次生成的密钥不应相同")
	}
}

func TestOTPAuthURI(t *testing.T) {
	uri := OTPAuthURI("智维面板", "admin", "MFRGGZDFMZTWQ2LK")
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Errorf("URI 应以 otpauth://totp/ 开头: %q", uri)
	}
	for _, want := range []string{"secret=MFRGGZDFMZTWQ2LK", "issuer=", "digits=6", "period=30", "algorithm=SHA1"} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI 缺少 %q: %q", want, uri)
		}
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	k, err := decodeSecret(s)
	if err != nil {
		t.Fatalf("decodeSecret: %v", err)
	}
	return k
}
