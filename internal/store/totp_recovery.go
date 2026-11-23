package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 两步验证恢复码（v0.8.0）
//
// 恢复码存在的理由：用户开了两步验证后手机丢失/损坏，除了走「忘记密码」
// 邮箱流程外没有任何自救通道——而邮箱也可能一起丢。恢复码是认证器之外
// 的第二条登录凭证，一次性、可重新生成。
//
// 存储设计：
//   - 只存 SHA-256 摘要（恢复码是 12 字符 base32 高熵随机串，约 60 bit，
//     无需 bcrypt 慢哈希；慢哈希是给 6 位低熵验证码准备的）；
//   - 一行一枚，used_at 非空即已消费——「一次性」由行状态天然保证；
//   - ReplaceRecoveryCodes 整体替换：重新生成即使旧码全部作废（防泄漏残留）。
// ---------------------------------------------------------------------------

// RecoveryCodeCount 一次生成的恢复码数量。10 枚是行业通行值
// （GitHub/Google 同量级）：够覆盖多次丢失又不至于多到用户懒得保存。
const RecoveryCodeCount = 10

// HashRecoveryCode 计算恢复码的存储摘要。
//
// 内部先归一化（转大写、去空白与分组符）——用户从纸上抄回来的码带空格/
// 小写/短横是常态，展示形态 XXXX-XXXX-XXXX 与裸码必须映射到同一摘要。
// 归一化放在哈希边界而不是指望每个调用方记得做。
func HashRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// ReplaceRecoveryCodes 用一组新码整体替换用户的旧码（旧码立即全部作废）。
// hashes 为 HashRecoveryCode 后的摘要，调用方负责生成随机明文。
func (d *DB) ReplaceRecoveryCodes(username string, hashes []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM totp_recovery_codes WHERE username=?`, username); err != nil {
		return err
	}
	now := time.Now()
	for _, h := range hashes {
		if _, err := tx.Exec(
			`INSERT INTO totp_recovery_codes (id, username, code_hash, used_at, created_at) VALUES (?,?,?,?,?)`,
			NewID(), username, h, nil, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UseRecoveryCode 消费一枚未使用的恢复码。命中即标记 used_at 并返回 true；
// 未命中（不存在/已用过）返回 false。调用方负责限流与尝试次数控制。
// code 原样传入即可——HashRecoveryCode 内部会归一化。
func (d *DB) UseRecoveryCode(username, code string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	hash := HashRecoveryCode(code)
	res, err := d.db.Exec(
		`UPDATE totp_recovery_codes SET used_at=? WHERE username=? AND code_hash=? AND used_at IS NULL`,
		time.Now(), username, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// CountUnusedRecoveryCodes 统计剩余未消费的恢复码（设置页展示「还剩 N 枚」）。
func (d *DB) CountUnusedRecoveryCodes(username string) (int, error) {
	var n int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM totp_recovery_codes WHERE username=? AND used_at IS NULL`,
		username).Scan(&n)
	return n, err
}

// DeleteRecoveryCodes 清空用户的全部恢复码（关闭两步验证时调用——
// 密钥都没了，留着旧恢复码没有任何意义，还多一分泄露面）。
func (d *DB) DeleteRecoveryCodes(username string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM totp_recovery_codes WHERE username=?`, username)
	return err
}
