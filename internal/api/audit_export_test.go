package api

import (
	"encoding/json"
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

// newAuditExportServer 建一个带真实 SQLite 的 Server，并塞 3 条审计记录。
//
// 第二条的 Detail 故意带逗号与双引号——CSV 转义是导出端点最容易翻车的地方
// （Excel 打开列错位），必须用真实行锁死行为，不能只测 200。
func newAuditExportServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	rows := []*model.AuditLog{
		{Username: "admin", Action: "site.create", Target: "s1", Detail: "普通记录",
			Method: "POST", Path: "/api/sites", Status: 201, IP: "127.0.0.1",
			CreatedAt: time.Now().Add(-3 * time.Minute)},
		{Username: "admin", Action: "site.delete", Target: "s1", Detail: "含,逗号\"引号",
			Method: "DELETE", Path: "/api/sites/s1", Status: 200, IP: "127.0.0.1",
			CreatedAt: time.Now().Add(-2 * time.Minute)},
		{Username: "ci-bot", Action: "cron.run", Target: "backup", Detail: "",
			Method: "POST", Path: "/api/cron/jobs/run", Status: 200, IP: "198.51.100.10",
			ViaToken: true, CreatedAt: time.Now().Add(-time.Minute)},
	}
	for _, l := range rows {
		if err := db.SaveAuditLog(l); err != nil {
			t.Fatalf("写测试审计记录失败: %v", err)
		}
	}
	// 导出端点只走 DB()，其余依赖全部可 nil（与 New 的降级约定一致）。
	svc := service.New(db, nil, nil, nil, nil, nil, dir, dir, 0, false)
	return NewServer(svc, true, nil), db
}

// exportReq 发起导出请求。authed=false 时模拟未认证调用。
//
// 认证走真实路径：往库里写一条会话再带 session cookie——authMiddleware
// 是从请求凭据查库解析 principal 的，直接往 context 塞 principal 骗不过它。
// （往 context 塞 withPrincipal(...) 的写法编译都过不了：参数名会遮蔽
// 包级同名辅助函数，本文件第一版就栽在这里。）
func exportReq(s *Server, db *store.DB, method, target string, authed bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if authed {
		token := store.NewID() + store.NewID()
		if err := db.SaveSession(token, "admin", sessionTTL); err != nil {
			panic(err)
		}
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	return w
}

// TestAuditExportRequiresAuth 未认证必须 401：审计内容含操作者身份与来源 IP，
// 等保场景下属于敏感数据，不能匿名拉。
func TestAuditExportRequiresAuth(t *testing.T) {
	s, _ := newAuditExportServer(t)
	w := exportReq(s, nil, http.MethodGet, "/api/audit-logs/export", false)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未认证应 401，实际 %d", w.Code)
	}
}

// TestAuditExportMethodGate 导出是 GET；POST 必须被方法闸门拦成 405。
// 这条同时锁死路由注册走的是 reg()（带鉴权），不是裸 mux.HandleFunc。
func TestAuditExportMethodGate(t *testing.T) {
	s, db := newAuditExportServer(t)
	w := exportReq(s, db, http.MethodPost, "/api/audit-logs/export", true)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应 405，实际 %d", w.Code)
	}
}

// TestAuditExportBadFormat 只支持 csv / json，其它一律 400，
// 防止把任意字符串拼进 Content-Disposition 文件名。
func TestAuditExportBadFormat(t *testing.T) {
	s, db := newAuditExportServer(t)
	w := exportReq(s, db, http.MethodGet, "/api/audit-logs/export?format=xml", true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 format 应 400，实际 %d", w.Code)
	}
}

// TestAuditExportCSV 锁 CSV 输出的四个关键点：Content-Type / 附件头 /
// UTF-8 BOM（Excel 中文必需）/ 含逗号与引号字段的转义。
func TestAuditExportCSV(t *testing.T) {
	s, db := newAuditExportServer(t)
	w := exportReq(s, db, http.MethodGet, "/api/audit-logs/export?format=csv", true)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type 应为 text/csv，实际 %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".csv") {
		t.Errorf("Content-Disposition 应为 csv 附件，实际 %q", cd)
	}

	body := w.Body.String()
	if !strings.HasPrefix(body, "\ufeff") {
		t.Error("CSV 输出必须带 UTF-8 BOM（无 BOM 时 Excel 打开中文乱码）")
	}
	lines := strings.Split(strings.TrimPrefix(body, "\ufeff"), "\n")
	// 表头 + 3 行数据；csv.Writer 以 \n 结尾，末行后会有一个空段
	if len(lines) < 4 {
		t.Fatalf("应有表头+3 行，实际 %d 行", len(lines))
	}
	if !strings.HasPrefix(lines[0], "id,time,username,action") {
		t.Errorf("表头不对: %q", lines[0])
	}
	// 含 , 与 " 的字段必须被引号包裹且内部引号翻倍
	if !strings.Contains(body, "\"含,逗号\"\"引号\"") {
		t.Errorf("含逗号/引号的字段未被正确转义，body 片段: %q", body)
	}
}

// TestAuditExportJSON JSON 形态必须能被正常解码，行数与库一致。
func TestAuditExportJSON(t *testing.T) {
	s, db := newAuditExportServer(t)
	w := exportReq(s, db, http.MethodGet, "/api/audit-logs/export?format=json", true)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type 应为 application/json，实际 %q", ct)
	}
	var out struct {
		Logs  []model.AuditLog `json:"logs"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("JSON 解码失败: %v", err)
	}
	if out.Total != 3 || len(out.Logs) != 3 {
		t.Errorf("应导出 3 条，实际 total=%d logs=%d", out.Total, len(out.Logs))
	}
}

// TestAuditExportIsAudited 导出审计日志本身必须留痕：
// "谁把审计日志拿走了"与"谁动了配置"同等重要。
func TestAuditExportIsAudited(t *testing.T) {
	s, db := newAuditExportServer(t)
	if w := exportReq(s, db, http.MethodGet, "/api/audit-logs/export?format=csv", true); w.Code != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d", w.Code)
	}
	logs, err := db.ListAuditLogsExport("audit.export")
	if err != nil {
		t.Fatalf("查留痕失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("导出后应有 1 条 audit.export 记录，实际 %d", len(logs))
	}
	if logs[0].Username != "admin" {
		t.Errorf("留痕操作者应为 admin，实际 %q", logs[0].Username)
	}
}

// TestAuditExportActionFilter action 过滤参数与列表接口同语义（前缀匹配）。
func TestAuditExportActionFilter(t *testing.T) {
	s, db := newAuditExportServer(t)
	w := exportReq(s, db, http.MethodGet, "/api/audit-logs/export?format=json&action=site", true)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", w.Code)
	}
	var out struct {
		Logs  []model.AuditLog `json:"logs"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("JSON 解码失败: %v", err)
	}
	if out.Total != 2 {
		t.Errorf("action=site 应命中 2 条（site.create/site.delete），实际 %d", out.Total)
	}
}
