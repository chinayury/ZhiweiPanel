package mysql

import (
	"strings"
	"testing"
)

func TestValidateManagedDatabaseName(t *testing.T) {
	valid := []string{"site_abc", "site_0123456789ab", "site_A_b"}
	for _, name := range valid {
		if err := ValidateManagedDatabaseName(name); err != nil {
			t.Fatalf("%q 应合法: %v", name, err)
		}
	}
	invalid := []string{"", "mysql", "sys", "performance_schema", "site-abc", "site_abc;drop", "site_中文", strings.Repeat("x", 70)}
	for _, name := range invalid {
		if err := ValidateManagedDatabaseName(name); err == nil {
			t.Fatalf("%q 应被拒绝", name)
		}
	}
}
