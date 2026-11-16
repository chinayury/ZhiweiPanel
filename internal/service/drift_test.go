package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/driver/hostdriver"
	"github.com/zhiwei/panel/internal/store"
)

func newDriftTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	drv := hostdriver.New(hostdriver.Config{
		ConfDir:     dir,
		SiteConfDir: dir,
		DataDir:     dir,
		CertDir:     filepath.Join(dir, "certs"),
		LogDir:      filepath.Join(dir, "logs"),
	})
	return New(db, drv, nil, nil, nil, nil, dir, dir, 0, false)
}

// TestCheckConfigDrift 锁死漂移检测的四个状态转换：
// 无快照不算漂移 → 快照后未改动不算 → 面板以外改动算 → 刷新快照消除 → 外部删除算。
//
// "certbot 改了站点配置、手工编辑被覆盖"这类事故只能靠漂移告警发现，
// 检测逻辑一旦失效就是静默的，所以每个转换都要有断言。
func TestCheckConfigDrift(t *testing.T) {
	svc := newDriftTestService(t)
	confDir := svc.drv.Paths().SiteConfDir

	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(confDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写配置文件失败: %v", err)
		}
	}

	// 无任何快照：没有基线就谈不上偏离，不算漂移
	if drift, _ := svc.CheckConfigDrift(); drift {
		t.Error("无快照时不应报告漂移")
	}

	write("10-site-a.conf", "server { listen 80; }")
	svc.SnapshotConfig("test", "")

	// 快照后未改动 → 无漂移
	if drift, _ := svc.CheckConfigDrift(); drift {
		t.Error("快照后未改动不应报告漂移")
	}

	// 面板以外的改动：改内容 + 新增文件 → 漂移，摘要含文件名
	write("10-site-a.conf", "server { listen 8080; }")
	write("zz-external.conf", "# certbot 写入的")
	drift, summary := svc.CheckConfigDrift()
	if !drift {
		t.Fatal("内容被外部修改应报告漂移")
	}
	if !strings.Contains(summary, "10-site-a.conf") || !strings.Contains(summary, "zz-external.conf") {
		t.Errorf("摘要应包含被改文件名，实际 %q", summary)
	}

	// 面板重新保存配置（刷新快照）→ 漂移消除
	svc.SnapshotConfig("test", "")
	if drift, _ := svc.CheckConfigDrift(); drift {
		t.Error("刷新快照后不应再报告漂移")
	}

	// 外部删除配置文件 → 漂移
	if err := os.Remove(filepath.Join(confDir, "10-site-a.conf")); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	drift, summary = svc.CheckConfigDrift()
	if !drift {
		t.Fatal("配置文件被外部删除应报告漂移")
	}
	if !strings.Contains(summary, "10-site-a.conf") {
		t.Errorf("摘要应包含被删文件名，实际 %q", summary)
	}
}

// TestCheckConfigDriftSummaryTruncation 超过 5 个不一致文件时摘要截断为计数，
// 防止告警消息被几十个文件名撑爆。
func TestCheckConfigDriftSummaryTruncation(t *testing.T) {
	svc := newDriftTestService(t)
	confDir := svc.drv.Paths().SiteConfDir

	// 一个只含单个文件的快照，随后新增 6 个文件 → 6 个不一致
	if err := os.WriteFile(filepath.Join(confDir, "a.conf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.SnapshotConfig("test", "")
	for _, n := range []string{"b", "c", "d", "e", "f", "g"} {
		if err := os.WriteFile(filepath.Join(confDir, n+".conf"), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	drift, summary := svc.CheckConfigDrift()
	if !drift {
		t.Fatal("新增 6 个文件应报告漂移")
	}
	if !strings.Contains(summary, "等 6 个文件") {
		t.Errorf("超长摘要应截断为计数形式，实际 %q", summary)
	}
}
