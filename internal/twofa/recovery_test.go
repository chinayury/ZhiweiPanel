package twofa

import (
	"regexp"
	"testing"
)

// 恢复码生成测试：形态、字母表、随机性。

func TestGenerateRecoveryCodeFormat(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := GenerateRecoveryCode()
		if err != nil {
			t.Fatalf("GenerateRecoveryCode: %v", err)
		}
		if !re.MatchString(c) {
			t.Fatalf("恢复码形态不对（应 4-4-4 分组、Crockford 字母表）: %q", c)
		}
		// 易混淆字符 I/L/O/U 绝不允许出现
		for _, bad := range []string{"I", "L", "O", "U"} {
			if regexp.MustCompile(bad).MatchString(c) {
				t.Errorf("恢复码含易混淆字符 %s: %q", bad, c)
			}
		}
		if seen[c] {
			t.Fatalf("恢复码重复: %q", c)
		}
		seen[c] = true
	}
}

func TestGenerateRecoveryCodesUniqueInBatch(t *testing.T) {
	codes := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		c, err := GenerateRecoveryCode()
		if err != nil {
			t.Fatal(err)
		}
		codes = append(codes, c)
	}
	uniq := map[string]bool{}
	for _, c := range codes {
		uniq[c] = true
	}
	if len(uniq) != len(codes) {
		t.Errorf("一批 50 枚里出现重复: %d unique / %d total", len(uniq), len(codes))
	}
}
