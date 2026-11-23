package store

import (
	"testing"

	"github.com/zhiwei/panel/internal/twofa"
)

// 恢复码存储测试：整体替换、一次性消费、归一化、清理。

// genCodes 生成 n 枚恢复码及其存储摘要（与 API 层 generateRecoveryCodes 同构）。
func genCodes(t *testing.T, n int) ([]string, []string) {
	t.Helper()
	codes := make([]string, 0, n)
	hashes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		c, err := twofa.GenerateRecoveryCode()
		if err != nil {
			t.Fatalf("生成恢复码失败: %v", err)
		}
		codes = append(codes, c)
		hashes = append(hashes, HashRecoveryCode(c))
	}
	return codes, hashes
}

func TestRecoveryCodesLifecycle(t *testing.T) {
	db := newTestDB(t)
	const user = "admin"

	codes, hashes := genCodes(t, RecoveryCodeCount)
	if err := db.ReplaceRecoveryCodes(user, hashes); err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}

	n, err := db.CountUnusedRecoveryCodes(user)
	if err != nil || n != RecoveryCodeCount {
		t.Fatalf("初始应有 %d 枚未使用，实际 %d (err=%v)", RecoveryCodeCount, n, err)
	}

	// 消费第一枚（带分组符的展示形态 + 小写也应命中——归一化在哈希边界）
	used, err := db.UseRecoveryCode(user, codes[0])
	if err != nil || !used {
		t.Fatalf("第一枚应消费成功: used=%v err=%v", used, err)
	}
	n, _ = db.CountUnusedRecoveryCodes(user)
	if n != RecoveryCodeCount-1 {
		t.Errorf("消费一枚后应剩 %d，实际 %d", RecoveryCodeCount-1, n)
	}

	// 同一枚再试 → 一次性，必须失败
	used, err = db.UseRecoveryCode(user, codes[0])
	if err != nil || used {
		t.Errorf("已消费的恢复码不应再次生效: used=%v err=%v", used, err)
	}

	// 归一化：小写、无短横的抄写变体应命中（用户从纸上抄回来不带分组符）
	variant := ""
	for _, ch := range codes[1] {
		if ch == '-' {
			continue
		}
		if ch >= 'A' && ch <= 'Z' {
			variant += string(ch + 32) // 转小写
			continue
		}
		variant += string(ch)
	}
	used, err = db.UseRecoveryCode(user, variant)
	if err != nil || !used {
		t.Errorf("小写无短横的抄写变体应命中: used=%v err=%v", used, err)
	}

	// 不存在的码
	used, _ = db.UseRecoveryCode(user, "ZZZZ-ZZZZ-ZZZZ")
	if used {
		t.Error("不存在的恢复码不应命中")
	}

	// 重新生成 → 旧码全部作废
	newCodes, newHashes := genCodes(t, RecoveryCodeCount)
	if err := db.ReplaceRecoveryCodes(user, newHashes); err != nil {
		t.Fatalf("ReplaceRecoveryCodes(2): %v", err)
	}
	n, _ = db.CountUnusedRecoveryCodes(user)
	if n != RecoveryCodeCount {
		t.Errorf("重新生成后应有 %d 枚未使用，实际 %d", RecoveryCodeCount, n)
	}
	// 旧码里还没用过的那枚现在必须失效
	used, _ = db.UseRecoveryCode(user, codes[2])
	if used {
		t.Error("重新生成后旧恢复码应全部作废")
	}
	// 新码可用
	used, _ = db.UseRecoveryCode(user, newCodes[0])
	if !used {
		t.Error("新恢复码应可用")
	}

	// 全部清空
	if err := db.DeleteRecoveryCodes(user); err != nil {
		t.Fatalf("DeleteRecoveryCodes: %v", err)
	}
	n, _ = db.CountUnusedRecoveryCodes(user)
	if n != 0 {
		t.Errorf("清空后应剩 0，实际 %d", n)
	}
}

func TestRecoveryCodesUserIsolation(t *testing.T) {
	db := newTestDB(t)
	codesA, hashesA := genCodes(t, 3)
	_, hashesB := genCodes(t, 2)
	if err := db.ReplaceRecoveryCodes("alice", hashesA); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceRecoveryCodes("bob", hashesB); err != nil {
		t.Fatal(err)
	}
	nA, _ := db.CountUnusedRecoveryCodes("alice")
	nB, _ := db.CountUnusedRecoveryCodes("bob")
	if nA != 3 || nB != 2 {
		t.Errorf("多用户应隔离: alice=%d bob=%d", nA, nB)
	}
	// alice 的码不能被 bob 消费
	used, _ := db.UseRecoveryCode("bob", codesA[0])
	if used {
		t.Error("alice 的恢复码不应被 bob 消费")
	}
	// alice 自己仍可用
	used, _ = db.UseRecoveryCode("alice", codesA[0])
	if !used {
		t.Error("alice 应能消费自己的码")
	}
	// bob 清空不影响 alice
	if err := db.DeleteRecoveryCodes("bob"); err != nil {
		t.Fatal(err)
	}
	nA, _ = db.CountUnusedRecoveryCodes("alice")
	if nA != 2 {
		t.Errorf("bob 清空不应影响 alice: %d", nA)
	}
}
