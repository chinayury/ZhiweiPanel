package model

import (
	"testing"
	"time"
)

// TestAPITokenHasScope scope 语义：空 = 全权限。
func TestAPITokenHasScope(t *testing.T) {
	cases := []struct {
		name      string
		scopes    []string
		wantRead  bool
		wantWrite bool
	}{
		{"无 scope = 全权限", nil, true, true},
		{"空 slice = 全权限", []string{}, true, true},
		{"只读 token", []string{"read"}, true, false},
		{"读写 token", []string{"read", "write"}, true, true},
		{"只给 write", []string{"write"}, false, true},
		{"星号通配", []string{"*"}, true, true},
	}
	for _, c := range cases {
		tok := &APIToken{Scopes: c.scopes}
		if got := tok.HasScope("read"); got != c.wantRead {
			t.Errorf("%s: HasScope(read) = %v, want %v", c.name, got, c.wantRead)
		}
		if got := tok.HasScope("write"); got != c.wantWrite {
			t.Errorf("%s: HasScope(write) = %v, want %v", c.name, got, c.wantWrite)
		}
	}
}

// TestAPITokenExpired 过期判定。
func TestAPITokenExpired(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	// 无过期时间 = 永不过期
	if (&APIToken{}).Expired(now) {
		t.Error("无 expires_at 的 token 不该算过期")
	}
	// 已过期
	if !(&APIToken{ExpiresAt: &past}).Expired(now) {
		t.Error("过去的 expires_at 应算过期")
	}
	// 未过期
	if (&APIToken{ExpiresAt: &future}).Expired(now) {
		t.Error("未来的 expires_at 不该算过期")
	}
	// 零值时间视为未设置
	zero := time.Time{}
	if (&APIToken{ExpiresAt: &zero}).Expired(now) {
		t.Error("零值 expires_at 不该算过期")
	}
}
