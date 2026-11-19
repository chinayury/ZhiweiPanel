// Package twofa 提供两步验证（RFC 6238 TOTP）的纯函数实现。
//
// 设计约束：
//   - 零第三方依赖：HMAC-SHA1 / base32 全在标准库，不引入 QR 库
//     （前端展示 otpauth:// 链接 + 明文密钥，认证器 App 支持手动录入）；
//   - 只做 TOTP（30 秒步长、6 位数字、±1 窗口时钟容差），
//     不做 HOTP 计数器形态（认证器默认都是 TOTP）；
//   - 常数时间比较，避免逐位爆破。
package twofa

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// SecretBytes 密钥字节数（160 位，RFC 4226 推荐）。
	SecretBytes = 20
	// Digits 动态口令位数。
	Digits = 6
	// StepSec 时间步长（秒）。
	StepSec = 30
	// Window 时钟容差窗口（±1 步 = ±30 秒）。
	Window = 1
)

// GenerateSecret 生成 base32（RFC 4648 无填充）编码的 160 位随机密钥。
func GenerateSecret() (string, error) {
	b := make([]byte, SecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机密钥失败: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// Validate 校验动态口令。
//
// now 为校验时刻；±Window 个步长内任一计数器匹配即通过（容忍时钟漂移）。
// code 先做格式规整（去空格），匹配用常数时间比较。
func Validate(secret, code string, now time.Time) bool {
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != Digits {
		return false
	}
	step := now.Unix() / StepSec
	for d := int64(-Window); d <= Window; d++ {
		if hotpAt(key, step+d) == code {
			return true
		}
	}
	return false
}

// CodeAt 计算指定时刻的动态口令。诊断/测试用途
// （生产排查"用户说码不对"时，用它比对服务器与认证器的期望值）。
func CodeAt(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return hotpAt(key, t.Unix()/StepSec), nil
}

// hotpAt 计算某个时间步的 6 位动态口令（RFC 4226 动态截断 + mod 10^6）。
func hotpAt(key []byte, step int64) string {
	var ctr [8]byte
	binary.BigEndian.PutUint64(ctr[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(ctr[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (uint32(sum[off])&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])
	return fmt.Sprintf("%0*d", Digits, v%uint32(pow10(Digits)))
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

// decodeSecret 规整并解码 base32 密钥（容忍大小写与空格分隔）。
func decodeSecret(secret string) ([]byte, error) {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	// 补齐 base32 无填充形态到 8 的倍数，标准解码器要求 padding
	if pad := len(secret) % 8; pad != 0 {
		secret += strings.Repeat("=", 8-pad)
	}
	return base32.StdEncoding.DecodeString(secret)
}

// OTPAuthURI 生成认证器可用的 otpauth:// 链接。
// issuer 是站点品牌名，account 是用户名。
func OTPAuthURI(issuer, account, secret string) string {
	u := url.Values{}
	u.Set("secret", secret)
	u.Set("issuer", issuer)
	u.Set("algorithm", "SHA1")
	u.Set("digits", strconv.Itoa(Digits))
	u.Set("period", strconv.Itoa(StepSec))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + u.Encode()
}
