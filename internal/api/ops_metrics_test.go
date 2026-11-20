package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/service"
	"github.com/zhiwei/panel/internal/store"
)

// /api/system/metrics/history（v0.7.0 指标持久化）端点测试：
// 鉴权、空数据、区间钳位、升序返回。

func newMetricsServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureAdminUser(db, "admin", "testpassword123"); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	svc := service.New(db, nil, nil, nil, nil, nil, dir, dir, 0, false)
	return NewServer(svc, true, nil), db
}

func TestMetricsHistoryEndpoint(t *testing.T) {
	s, db := newMetricsServer(t)

	// 未认证 → 401
	req := httptest.NewRequest(http.MethodGet, "/api/system/metrics/history", nil)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未认证应 401，实际 %d", w.Code)
	}

	// 认证后空数据 → 200 + 空 history
	rec := withSession(t, s, db, http.MethodGet, "/api/system/metrics/history", "")
	if rec.Code != 200 {
		t.Fatalf("空数据应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"hours":24`) {
		t.Errorf("缺省 hours 应为 24: %s", rec.Body.String())
	}

	// 灌数据：窗口内 2 条 + 保留期外 1 条
	now := time.Now().Unix()
	seed := []model.MetricsSample{
		{TS: now - 7200, CPU: 10.1, Mem: 30.1, Load1: 0.2, Disk: 15, NetRx: 100, NetTx: 200},
		{TS: now - 3600, CPU: 20.2, Mem: 31.2, Load1: 0.4, Disk: 16, NetRx: 300, NetTx: 400},
		{TS: now - 200*3600, CPU: 99, Mem: 99, Load1: 9, Disk: 99, NetRx: 0, NetTx: 0},
	}
	for _, m := range seed {
		if err := db.SaveMetricsSample(m); err != nil {
			t.Fatal(err)
		}
	}

	rec = withSession(t, s, db, http.MethodGet, "/api/system/metrics/history", "")
	if rec.Code != 200 {
		t.Fatalf("有数据应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"cpu":99`) {
		t.Errorf("保留期外的采样不应出现: %s", body)
	}
	if !strings.Contains(body, `"cpu":10.1`) || !strings.Contains(body, `"cpu":20.2`) {
		t.Errorf("窗口内采样缺失: %s", body)
	}
	// 升序：10.1（较旧）必须出现在 20.2 之前
	if idxOld := strings.Index(body, `"cpu":10.1`); idxOld >= 0 {
		if idxNew := strings.Index(body, `"cpu":20.2`); idxNew >= 0 && idxNew < idxOld {
			t.Errorf("采样应旧 → 新排序: %s", body)
		}
	}

	// hours=1 → 只剩最近一小时内的那一条
	rec = withSession(t, s, db, http.MethodGet, "/api/system/metrics/history?hours=1", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"cpu":10.1`) {
		t.Errorf("hours=1 不应包含 2 小时前的采样: %d %s", rec.Code, rec.Body.String())
	}
	// hours 非法 → 按缺省 24
	rec = withSession(t, s, db, http.MethodGet, "/api/system/metrics/history?hours=abc", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cpu":10.1`) {
		t.Errorf("hours=abc 应按缺省 24 处理: %d %s", rec.Code, rec.Body.String())
	}
	// hours=2000 → 钳到上限 72，响应回显钳位后的值（与实际窗口一致）
	rec = withSession(t, s, db, http.MethodGet, "/api/system/metrics/history?hours=2000", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"hours":72`) {
		t.Errorf("hours=2000 应钳位回显 72: %d %s", rec.Code, rec.Body.String())
	}

	// POST 拒绝（只读端点）
	rec = withSession(t, s, db, http.MethodPost, "/api/system/metrics/history", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 应 405，实际 %d", rec.Code)
	}
}
