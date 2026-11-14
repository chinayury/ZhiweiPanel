package hostdriver

import (
	"path/filepath"
	"testing"
)

// TestPHPSocketNameVariants 锁死 PHP-FPM socket 命名差异。
// Ubuntu/Debian 的 php8.1-fpm 包产出 /run/php/php8.1-fpm.sock（无连字符），
// 早期 glob 与正则都写死成 php-*-fpm.sock，导致 Ubuntu 上 PHP 版本列表恒为空。
func TestPHPSocketNameVariants(t *testing.T) {
	cases := []struct {
		file    string
		wantVer string // 空表示不应匹配
	}{
		{"php8.1-fpm.sock", "8.1"},  // Ubuntu/Debian 实际形态
		{"php-8.1-fpm.sock", "8.1"}, // 源码编译安装形态
		{"php7.4-fpm.sock", "7.4"},
		{"php-7.4-fpm.sock", "7.4"},
		{"php8.2-fpm.sock", "8.2"},
		{"php-fpm.sock", ""},   // alternatives 符号链接，无版本号
		{"php8.1-fpm.pid", ""}, // 不是 socket
		{"nginx.sock", ""},
	}
	for _, c := range cases {
		m := sockRe.FindStringSubmatch(c.file)
		if c.wantVer == "" {
			if m != nil {
				t.Errorf("%q 不应匹配，却解析出版本 %q", c.file, m[1])
			}
			continue
		}
		if m == nil {
			t.Errorf("%q 应匹配到版本 %q，实际未匹配", c.file, c.wantVer)
			continue
		}
		if m[1] != c.wantVer {
			t.Errorf("%q 解析出版本 %q，期望 %q", c.file, m[1], c.wantVer)
		}
	}
}

// TestDefaultPHPGlobCoversBothForms 默认 glob 必须同时覆盖两种命名。
func TestDefaultPHPGlobCoversBothForms(t *testing.T) {
	d := New(Config{})
	for _, name := range []string{"php8.1-fpm.sock", "php-8.1-fpm.sock"} {
		matched := false
		for _, cand := range []string{"/run/php/" + name} {
			if globMatch(d.phpGlob, cand) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("默认 glob %q 匹配不到 %q", d.phpGlob, name)
		}
	}
}

// globMatch 用 filepath.Match 语义判断（与 filepath.Glob 一致）。
func globMatch(pattern, name string) bool {
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}
