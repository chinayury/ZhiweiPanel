package nginx

import (
	"regexp"
	"strings"
	"testing"
)

// TestWAFPatternCompiles 验证生成的 WAF 正则本身是合法的。
func TestWAFPatternCompiles(t *testing.T) {
	pattern := wafPattern(`union\s+select|insert\s+into|drop\s+table|delete\s+from|<script|javascript:|onerror\s*=|onload\s*=|\.\./`)
	t.Logf("生成的模式: %s", pattern)

	full := "(?i)(" + pattern + ")"
	if _, err := regexp.Compile(full); err != nil {
		t.Fatalf("正则无法编译: %v\n完整模式: %s", err, full)
	}
}

// TestWAFPatternSurvivesNginxUnescape 锁死一个只在生产暴露的坑：
// 这段正则会写进 nginx 的双引号字符串，而 nginx 会把串里的 \\ 反转义成 \。
// Go 的 regexp 检查不出问题，但 nginx 会报
//
//	pcre_compile() failed: missing )
//
// 于是整份站点配置下发失败并被回滚，伪静态/黑名单等一起停在旧状态。
// 这里模拟 nginx 的反转义后再编译一次，提前抓住这类错误。
func TestWAFPatternSurvivesNginxUnescape(t *testing.T) {
	pattern := wafPattern(`union\s+select|insert\s+into|drop\s+table|delete\s+from|<script|javascript:|onerror\s*=|onload\s*=|\.\./`)

	// nginx 对双引号串的反转义：\\ -> \
	unescaped := strings.ReplaceAll(pattern, `\\`, `\`)
	t.Logf("nginx 实际收到的正则: %s", unescaped)

	if _, err := regexp.Compile("(?i)(" + unescaped + ")"); err != nil {
		t.Fatalf("经 nginx 反转义后正则失效: %v\n模式: %s", err, unescaped)
	}

	// 另一个高信号特征：反转义后不能出现"紧跟右括号的单个反斜杠"，
	// 那说明有个 \ 把分组括号转义成了字面量。
	if strings.Contains(unescaped, `\)`) {
		t.Errorf("出现 \\)，分组的右括号被转义成字面量了: %s", unescaped)
	}
}

// TestWAFPatternNoTrailingBackslashBeforeQuote 结尾不能是奇数个反斜杠，
// 否则它会逃逸出双引号，把整个配置的引号配对搞乱。
func TestWAFPatternNoTrailingBackslashBeforeQuote(t *testing.T) {
	pattern := wafPattern(`union\s+select|<script|\.\./`)
	n := 0
	for i := len(pattern) - 1; i >= 0 && pattern[i] == '\\'; i-- {
		n++
	}
	if n%2 == 1 {
		t.Errorf("模式以奇数个反斜杠结尾（%d 个），会逃逸双引号: %s", n, pattern)
	}
}
