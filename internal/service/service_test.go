package service

import (
	"strings"
	"testing"
)

// TestCommentOutCoversEveryLine 锁死一个真实缺陷：
// 早期实现只在配置首行前加 "# 站点已停止"，server {} 块其余部分仍然生效，
// 结果是面板显示"已停止"但站点照常返回 200。
// 停用必须逐行注释，让整段 server 块被 nginx 忽略。
func TestCommentOutCoversEveryLine(t *testing.T) {
	src := "# header comment\nserver {\n    listen 80;\n    server_name a.test;\n    location / {\n        root /www;\n    }\n}\n"
	out := commentOut(src)

	for _, line := range strings.Split(src, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// commentOut 是 "# " + 原行（保留原缩进），所以要按未 trim 的整行比对
		if !strings.Contains(out, "# "+line) {
			t.Errorf("原行未被注释: %q", line)
		}
	}

	// 关键断言：server 块的语法行必须都带上 # 前缀
	for _, must := range []string{"# server {", "#     listen 80;", "#     server_name a.test;", "# }"} {
		if !strings.Contains(out, must) {
			t.Errorf("缺少注释行 %q\n实际输出:\n%s", must, out)
		}
	}

	// 空行保持原样，避免注释块里出现大量 "# " 噪声
	if strings.Contains(out, "# \n\n") {
		t.Errorf("空行不应被注释")
	}
}

// TestCommentOutIdempotentShape 注释结果本身仍是合法的"全注释"文本。
func TestCommentOutIdempotentShape(t *testing.T) {
	out := commentOut("server {\n    listen 80;\n}\n")
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(line, "# ") {
			t.Errorf("第 %d 行缺少 '# ' 前缀: %q", i, line)
		}
	}
}
