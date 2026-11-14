package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// newTestDB 开一个临时数据库。
func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestHashToken 摘要必须确定、与原文无关。
func TestHashToken(t *testing.T) {
	h1 := HashToken("sp_abc")
	h2 := HashToken("sp_abc")
	if h1 != h2 {
		t.Error("同一 token 两次摘要不一致")
	}
	if h1 == HashToken("sp_abd") {
		t.Error("不同 token 摘要相同")
	}
	if len(h1) != 64 {
		t.Errorf("摘要长度 = %d, want 64", len(h1))
	}
	// 摘要里绝不能出现原文
	if h1 == "sp_abc" {
		t.Error("摘要不应等于原文")
	}
}

// TestTokenPrefix 前缀只用于展示，长度受限。
func TestTokenPrefix(t *testing.T) {
	// 前缀固定取前 8 位
	if got := TokenPrefix("sp_abcdefghijkl"); got != "sp_abcde" {
		t.Errorf("prefix = %q, want sp_abcde", got)
	}
	// 短 token 原样返回，不能切片越界
	if got := TokenPrefix("abc"); got != "abc" {
		t.Errorf("prefix = %q, want abc", got)
	}
	if got := TokenPrefix(""); got != "" {
		t.Errorf("prefix = %q, want 空", got)
	}
}

// TestSaveAndGetAPIToken 存取闭环，且任何读路径都不返回明文。
func TestSaveAndGetAPIToken(t *testing.T) {
	db := newTestDB(t)

	tok := &model.APIToken{
		ID:        "tok-1",
		Name:      "CI 机器人",
		Prefix:    TokenPrefix("sp_plaintoken"),
		Token:     "sp_plaintoken",
		Scopes:    []string{"read", "write"},
		CreatedBy: "admin",
		CreatedAt: time.Now(),
	}
	if err := db.SaveAPIToken(tok); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}

	got, err := db.GetAPITokenByHash("sp_plaintoken")
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if got.ID != "tok-1" || got.Name != "CI 机器人" || got.CreatedBy != "admin" {
		t.Errorf("字段不对: %+v", got)
	}
	// 读路径绝不返回明文
	if got.Token != "" {
		t.Errorf("读路径返回了明文 token: %q", got.Token)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "read" || got.Scopes[1] != "write" {
		t.Errorf("scopes = %v, want [read write]", got.Scopes)
	}
}

// TestGetAPITokenByHashUnknown 查不到时返回 ErrUnauthorized，
// 而不是 ErrNotFound —— 认证路径上不该给探测者区分信息。
func TestGetAPITokenByHashUnknown(t *testing.T) {
	db := newTestDB(t)
	_, err := db.GetAPITokenByHash("sp_nosuchtoken")
	if !errors.Is(err, model.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

// TestGetAPITokenByHashWrongToken 错误的 token 必须查不到。
func TestGetAPITokenByHashWrongToken(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveAPIToken(&model.APIToken{
		ID: "tok-1", Name: "n", Prefix: "sp_abc", Token: "sp_correct", CreatedBy: "admin",
	}); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}
	if _, err := db.GetAPITokenByHash("sp_wrong"); !errors.Is(err, model.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

// TestListAPITokens 列表按创建时间倒序，且不含明文。
func TestListAPITokens(t *testing.T) {
	db := newTestDB(t)
	for i, name := range []string{"first", "second"} {
		if err := db.SaveAPIToken(&model.APIToken{
			ID:        "tok-" + name,
			Name:      name,
			Prefix:    "sp_abc",
			Token:     "sp_secret_" + name,
			CreatedBy: "admin",
			CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("SaveAPIToken: %v", err)
		}
	}
	list, err := db.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2", len(list))
	}
	if list[0].Name != "second" {
		t.Errorf("列表未按创建时间倒序: 首位是 %q", list[0].Name)
	}
	for _, item := range list {
		if item.Token != "" {
			t.Errorf("列表返回了明文 token: %q", item.Token)
		}
	}
}

// TestTouchAPIToken 最后使用时间要落库。
func TestTouchAPIToken(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveAPIToken(&model.APIToken{
		ID: "tok-1", Name: "n", Prefix: "sp_abc", Token: "sp_t", CreatedBy: "admin",
	}); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}
	db.TouchAPIToken("tok-1")

	got, err := db.GetAPITokenByHash("sp_t")
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if got.LastUsedAt == nil {
		t.Fatal("LastUsedAt 未更新")
	}
}

// TestRevokeAPIToken 吊销即删行，删完就查不到。
func TestRevokeAPIToken(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveAPIToken(&model.APIToken{
		ID: "tok-1", Name: "n", Prefix: "sp_abc", Token: "sp_t", CreatedBy: "admin",
	}); err != nil {
		t.Fatalf("SaveAPIToken: %v", err)
	}
	if err := db.RevokeAPIToken("tok-1"); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := db.GetAPITokenByHash("sp_t"); !errors.Is(err, model.ErrUnauthorized) {
		t.Errorf("吊销后仍可查到: err = %v", err)
	}
	list, _ := db.ListAPITokens()
	if len(list) != 0 {
		t.Errorf("吊销后仍有 %d 条", len(list))
	}
	// 吊销不存在的 token 不该报错（幂等）
	if err := db.RevokeAPIToken("nosuch"); err != nil {
		t.Errorf("吊销不存在的 token 报错: %v", err)
	}
}

// TestAuditLogRoundTrip 审计记录存取 + 倒序。
func TestAuditLogRoundTrip(t *testing.T) {
	db := newTestDB(t)
	for i, action := range []string{"site.create", "site.update", "token.delete"} {
		if err := db.SaveAuditLog(&model.AuditLog{
			Username: "admin",
			Action:   action,
			Target:   "t" + action,
			Method:   "POST",
			Path:     "/api/" + action,
			Status:   200,
			IP:       "127.0.0.1",
			UA:       "test",
			ViaToken: i == 2,
			// 显式给递增的时间，避免 Windows 低精度时钟导致排序不稳定
			CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("SaveAuditLog: %v", err)
		}
	}

	n, err := db.CountAuditLogs()
	if err != nil {
		t.Fatalf("CountAuditLogs: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}

	logs, err := db.ListAuditLogs(200, "")
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("len = %d, want 3", len(logs))
	}
	// 倒序：最新的在前
	if logs[0].Action != "token.delete" {
		t.Errorf("未按时间倒序: 首位是 %q", logs[0].Action)
	}
	// via_token 必须正确还原
	if !logs[0].ViaToken {
		t.Error("ViaToken 未还原")
	}
	if logs[1].ViaToken {
		t.Error("ViaToken 还原错误")
	}
}

// TestListAuditLogsFilter 按 action 前缀过滤。
func TestListAuditLogsFilter(t *testing.T) {
	db := newTestDB(t)
	for _, action := range []string{"site.create", "site.update", "token.delete"} {
		if err := db.SaveAuditLog(&model.AuditLog{
			Username: "admin", Action: action, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("SaveAuditLog: %v", err)
		}
	}
	logs, err := db.ListAuditLogs(200, "site")
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Errorf("过滤结果 len = %d, want 2", len(logs))
	}
	for _, l := range logs {
		if l.Action != "site.create" && l.Action != "site.update" {
			t.Errorf("过滤泄漏了 %q", l.Action)
		}
	}
}

// TestListAuditLogsLimit limit 必须真的生效，且不接受超大值。
func TestListAuditLogsLimit(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 10; i++ {
		if err := db.SaveAuditLog(&model.AuditLog{
			Username: "admin", Action: "site.update", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("SaveAuditLog: %v", err)
		}
	}
	logs, err := db.ListAuditLogs(3, "")
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("len = %d, want 3", len(logs))
	}
	// limit<=0 与 limit>1000 都要回落到保守默认值，不能拉回全表
	all, _ := db.ListAuditLogs(0, "")
	if len(all) != 10 {
		t.Errorf("limit=0 时 len = %d, want 10（默认值 200）", len(all))
	}
	huge, _ := db.ListAuditLogs(999999, "")
	if len(huge) != 10 {
		t.Errorf("limit 超大时 len = %d, want 10", len(huge))
	}
}

// TestTrimAuditLogs 只保留最近的 keep 条。
func TestTrimAuditLogs(t *testing.T) {
	db := newTestDB(t)
	// 固定基准时间：断言时再取 time.Now() 会比创建时晚几毫秒，
	// 边界判断会被这个漂移误伤。
	base := time.Now()
	for i := 0; i < 10; i++ {
		if err := db.SaveAuditLog(&model.AuditLog{
			Username:  "admin",
			Action:    "site.update",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("SaveAuditLog: %v", err)
		}
	}
	if err := db.TrimAuditLogs(4); err != nil {
		t.Fatalf("TrimAuditLogs: %v", err)
	}
	n, _ := db.CountAuditLogs()
	if n != 4 {
		t.Errorf("count = %d, want 4", n)
	}
	// 保留的必须是最新的 4 条（i=6..9）
	logs, _ := db.ListAuditLogs(200, "")
	for _, l := range logs {
		if l.CreatedAt.Before(base.Add(6 * time.Second)) {
			t.Errorf("保留了旧记录: %v", l.CreatedAt)
		}
	}
	// keep<=0 表示不清理
	if err := db.TrimAuditLogs(0); err != nil {
		t.Fatalf("TrimAuditLogs(0): %v", err)
	}
	n, _ = db.CountAuditLogs()
	if n != 4 {
		t.Errorf("keep=0 时不该清理, count = %d", n)
	}
}
