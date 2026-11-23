package twofa

import (
	"crypto/rand"
	"fmt"
)

// ---------------------------------------------------------------------------
// 两步验证恢复码（v0.8.0）
//
// 恢复码是认证器之外的第二条登录凭证：手机丢失/损坏时用户唯一的自救通道。
// 与 TOTP 密钥不同，恢复码**一次性**、可整体重新生成。
// ---------------------------------------------------------------------------

// recoveryAlphabet Crockford base32：剔除 I/L/O/U 四个易混淆字符，
// 人工抄录/口述时不会把 1 和 I、0 和 O 搞混。
const recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// recoveryCodeLen 单枚恢复码长度（不含分组符）。12 字符 × 5 bit ≈ 60 bit
// 熵，配合登录限流（5 次锁定）与 pending 尝试上限，暴力枚举不可行。
const recoveryCodeLen = 12

// GenerateRecoveryCode 生成一枚恢复码（展示形态：XXXX-XXXX-XXXX）。
// 存储方（store.HashRecoveryCode）内部会归一化去分组符，展示形态与
// 裸码映射到同一摘要。
func GenerateRecoveryCode() (string, error) {
	b := make([]byte, recoveryCodeLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成恢复码失败: %w", err)
	}
	out := make([]byte, recoveryCodeLen)
	for i, v := range b {
		out[i] = recoveryAlphabet[int(v)%len(recoveryAlphabet)]
	}
	return groupRecoveryCode(string(out)), nil
}

// groupRecoveryCode 把 12 位裸码格式化成 XXXX-XXXX-XXXX。
func groupRecoveryCode(plain string) string {
	if len(plain) != recoveryCodeLen {
		return plain
	}
	return plain[0:4] + "-" + plain[4:8] + "-" + plain[8:12]
}
