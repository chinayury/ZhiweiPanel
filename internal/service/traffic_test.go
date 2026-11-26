package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
)

// 流量采集回归测试：同一批日志不能被重复计入。
// 这是 v0.9.0 修复的核心缺陷——旧实现每 5 分钟重读末尾 50k 行并累加。
func TestCollectTrafficDoesNotDoubleCount(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "access.log")
	site := &model.Site{
		ID:   "site1234567890ab",
		Name: "traffic-test",
		Root: model.RootConf{Path: dir},
		Logs: model.LogsConf{AccessLog: true},
	}
	// 新站点：建站时会预置 offset=0，首轮从文件头读。
	if err := db.PrimeTrafficOffset(site.ID, logPath); err != nil {
		t.Fatal(err)
	}
	svc := &Service{db: db, dataDir: dir}

	now := time.Now()
	line := func(ip, status string) string {
		return fmt.Sprintf(`%s - example.com [%s] "GET / HTTP/1.1" %s 100 0.010 "-" "ua" "-"`,
			ip, now.Format("02/Jan/2006:15:04:05 -0700"), status)
	}
	appendLines := func(lines ...string) {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			if _, err := f.WriteString(l + "\n"); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	appendLines(line("1.1.1.1", "200"), line("2.2.2.2", "404"))
	if err := svc.CollectTraffic(site); err != nil {
		t.Fatal(err)
	}
	reqs, _, err := db.GetTrafficSummary(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs != 2 {
		t.Fatalf("首轮应统计 2 条，得到 %d", reqs)
	}

	// 没有新增：必须仍为 2（旧实现这里会变成 4）。
	if err := svc.CollectTraffic(site); err != nil {
		t.Fatal(err)
	}
	reqs, _, _ = db.GetTrafficSummary(site.ID)
	if reqs != 2 {
		t.Fatalf("重复采集不应累加，得到 %d", reqs)
	}

	appendLines(line("3.3.3.3", "500"))
	if err := svc.CollectTraffic(site); err != nil {
		t.Fatal(err)
	}
	reqs, _, _ = db.GetTrafficSummary(site.ID)
	if reqs != 3 {
		t.Fatalf("新增 1 条后应为 3，得到 %d", reqs)
	}

	stats, err := db.GetTrafficStats(site.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].UniqueIPs != 3 || stats[0].Status5xx != 1 {
		t.Fatalf("统计明细错误: %+v", stats)
	}
}
