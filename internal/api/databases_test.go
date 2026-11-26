package api

import (
	"net/http"
	"testing"
)

// MySQL 未启用时，数据库中心应返回可识别的 503，而不是通用 500。
func TestDatabasesWithoutMySQL(t *testing.T) {
	srv, db := new2FAServer(t)

	rec := withSession(t, srv, db, http.MethodGet, "/api/databases", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用 MySQL 时列表应 503，得到 %d: %s", rec.Code, rec.Body.String())
	}

	// 非受管库名（mysql/sys 等）一律 404，避免被当成操作目标。
	rec = withSession(t, srv, db, http.MethodPost, "/api/databases/mysql/password", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("系统库应 404，得到 %d: %s", rec.Code, rec.Body.String())
	}

	rec = withSession(t, srv, db, http.MethodPost, "/api/databases/site_abc/password", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("受管库但无 MySQL 时应 503，得到 %d: %s", rec.Code, rec.Body.String())
	}
}
