package mysql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestLoadRootDSN 锁死 cnf 解析格式：
// install.sh 写出的凭据文件必须能被 LoadRootDSN 正确读出 user/password/socket。
// 任何字段缺失 / 多余空行 / 注释行 / [client] header 都得兼容。
func TestLoadRootDSN(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantDSN string
		wantErr bool
	}{
		{
			name: "标准格式",
			content: `# 注释
[client]
user=root
password=secret
socket=/run/mysqld/mysqld.sock
`,
			wantDSN: "root:secret@unix(/run/mysqld/mysqld.sock)/?parseTime=true&multiStatements=true",
		},
		{
			name: "字段顺序无关",
			content: `socket=/var/run/mysqld/mysqld.sock
user=mysql_root
password=p@ssw0rd!#
`,
			wantDSN: "mysql_root:p@ssw0rd!#@unix(/var/run/mysqld/mysqld.sock)/?parseTime=true&multiStatements=true",
		},
		{
			name: "缺 user",
			content: `password=x
socket=/tmp/s
`,
			wantErr: true,
		},
		{
			name: "缺 socket",
			content: `user=root
password=x
`,
			wantErr: true,
		},
		{
			name:    "空文件",
			content: ``,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "mysql-root.cnf")
			if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			dsn, err := LoadRootDSN(p)
			if tc.wantErr {
				if err == nil {
					t.Errorf("期望错误却拿到 DSN=%q", dsn)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadRootDSN: %v", err)
			}
			if dsn != tc.wantDSN {
				t.Errorf("DSN 不一致:\n got  %q\n want %q", dsn, tc.wantDSN)
			}
		})
	}
}

// TestNameForSite 锁死 db 名推导规则：site_<12位hex>。
// 这个名字直接进 MySQL，名字格式错会让驱动执行 SQL 时报语法错。
func TestNameForSite(t *testing.T) {
	cases := []struct {
		siteID  string
		want    string
		wantErr bool
	}{
		{siteID: "abcdef1234567890", want: "site_abcdef123456"}, // 取前 12
		{siteID: "abcdef123456", want: "site_abcdef123456"},     // 恰好 12
		{siteID: "abcdef12345", want: "site_abcdef12345"},       // 不到 12 但 ≥8
		{siteID: "abc", wantErr: true},                          // <8 拒绝
		{siteID: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.siteID, func(t *testing.T) {
			got, err := NameForSite(tc.siteID)
			if tc.wantErr {
				if err == nil {
					t.Errorf("期望错误却拿到 name=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NameForSite: %v", err)
			}
			if got != tc.want {
				t.Errorf("name: got %q want %q", got, tc.want)
			}
			// 名字必须以前缀开头且不超过 MaxNameLen
			if !strings.HasPrefix(got, DBNamePrefix) {
				t.Errorf("name %q 缺前缀 %q", got, DBNamePrefix)
			}
			if len(got) > MaxNameLen {
				t.Errorf("name %q 超长 %d > %d", got, len(got), MaxNameLen)
			}
		})
	}
}

// TestGeneratePassword 锁死密码格式：24 位 hex、不能全相同、不能与上次相同。
func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := GeneratePassword()
		if len(p) != 24 {
			t.Errorf("密码长度 %d != 24: %q", len(p), p)
		}
		// 验证是 hex
		for _, c := range p {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("密码含非 hex 字符 %q: %q", c, p)
				break
			}
		}
		// 50 次生成应有高概率不重复（碰撞概率约 50*49 / 2^96 ≈ 0）
		if seen[p] {
			t.Errorf("密码重复: %q", p)
		}
		seen[p] = true
	}
}

// TestCreateUserStmt 锁死 SQL 模板：必须 inline 密码，不允许回退到 ? 占位符。
//
// 背景：MySQL/MariaDB 协议里 CREATE USER / ALTER USER / SET PASSWORD / GRANT
// 这类 DDL 不走 prepared statement 占位符，驱动会把 ? 当字面字符传给服务端
// （参考内部 bug case：站点创建时 1064 "near '?' at line 1"）。
// 生成函数 buildCreateUserStmt / buildAlterUserStmt 必须用 sprintf 内联密码；
// 密码是 GeneratePassword() 返回的 24 位 hex，inline 安全（只含 [0-9a-f]）。
func TestCreateUserStmt(t *testing.T) {
	cases := []struct {
		name, user, host, pw string
		wantContains         []string
		wantNot              []string
	}{
		{
			name:         "标准",
			user:         "site_abc",
			host:         "localhost",
			pw:           "0123456789abcdef01234567",
			wantContains: []string{"CREATE USER IF NOT EXISTS", "`site_abc`@`localhost`", "IDENTIFIED BY '0123456789abcdef01234567'"},
			wantNot:      []string{"?"},
		},
		{
			name:         "空 host 也走 inline（除非调用方先 translate）",
			user:         "site_xyz",
			host:         "localhost",
			pw:           "deadbeefcafebabefeedf00d",
			wantContains: []string{"`site_xyz`", "deadbeefcafebabefeedf00d"},
			wantNot:      []string{"?"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCreateUserStmt(tc.user, tc.host, tc.pw)
			for _, s := range tc.wantContains {
				if !strings.Contains(got, s) {
					t.Errorf("缺失 %q in %q", s, got)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(got, s) {
					t.Errorf("不应出现 %q 却出现 in %q", s, got)
				}
			}
		})
	}

	// ALTER 同源：复用同一约束。
	upd := buildAlterUserStmt("u", "localhost", "0123456789abcdef01234567")
	if strings.Contains(upd, "?") {
		t.Errorf("ALTER USER 不应使用 ? 占位符: %q", upd)
	}
	if !strings.Contains(upd, "ALTER USER `u`@`localhost` IDENTIFIED BY '0123456789abcdef01234567'") {
		t.Errorf("ALTER USER 模板格式异常: %q", upd)
	}
}

// TestDatabaseSafeCopyMasked 锁密码脱敏函数。
// SafeCopy: 整个 password 字段清空
// Masked:  保留前 2 后 2，中间 *，长度 ≤4 时变 ****
func TestDatabaseSafeCopyMasked(t *testing.T) {
	d := &model.Database{Name: "site_abc", User: "u", Password: "abcdef1234567890abcdef1234"}
	safe := d.SafeCopy()
	if safe.Password != "" {
		t.Errorf("SafeCopy 应清空密码，却保留 %q", safe.Password)
	}
	if safe.Name != "site_abc" {
		t.Errorf("SafeCopy 不能动其它字段")
	}

	masked := d.Masked()
	if len(masked.Password) != len(d.Password) {
		t.Errorf("Masked 应保持原长度")
	}
	if !strings.HasPrefix(masked.Password, "ab") || !strings.HasSuffix(masked.Password, "34") {
		t.Errorf("Masked 应保留前 2 后 2：%q", masked.Password)
	}
	if !strings.Contains(masked.Password, "*") {
		t.Errorf("Masked 中间应是 *: %q", masked.Password)
	}

	// 短密码 fallback
	short := &model.Database{Password: "ab"}
	m2 := short.Masked()
	if m2.Password != "****" {
		t.Errorf("短密码应降级为 ****，得到 %q", m2.Password)
	}

	// nil 安全
	var nilDB *model.Database
	if nilDB.SafeCopy() != nil || nilDB.Masked() != nil {
		t.Errorf("nil 调用必须返 nil")
	}
}
