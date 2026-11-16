package alert

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
)

func newDriftTestEngine(t *testing.T) *Engine {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewEngine(db)
}

func driftSite(alertEnabled bool) *model.Site {
	return &model.Site{
		ID:     "site-1",
		Name:   "drift-test",
		Status: model.SiteStatusRunning,
		Alert: model.AlertConf{
			Enabled: alertEnabled,
			Rules:   []model.AlertRule{{Type: "config_drift", Enabled: true, Level: "warning"}},
		},
	}
}

// restoreDriftChecker 保证用例之间不互相污染注入的实现。
func restoreDriftChecker(t *testing.T) {
	t.Helper()
	orig := configDriftChecker
	t.Cleanup(func() { configDriftChecker = orig })
}

// TestConfigDriftCheckerNotInjected 未注入 provider 时不 panic、不产生告警
// （与 tamperChecker 同款约定：辅助钩子缺席必须静默跳过）。
func TestConfigDriftCheckerNotInjected(t *testing.T) {
	restoreDriftChecker(t)
	configDriftChecker = nil

	eng := newDriftTestEngine(t)
	c := &Checker{eng: eng}
	c.checkConfigDrift([]*model.Site{driftSite(true)})

	logs, _ := eng.db.ListAlerts(10, false)
	if len(logs) != 0 {
		t.Fatalf("未注入 driftChecker 时不应产生告警，实际 %d 条", len(logs))
	}
}

// TestConfigDriftRaisesAndDedups 注入"发现漂移"后，链路必须走到 Raise 并落库；
// Engine 的 30 分钟站点级抑制窗口内不得重复告警（漂移持续存在也不刷屏）。
func TestConfigDriftRaisesAndDedups(t *testing.T) {
	restoreDriftChecker(t)
	SetConfigDriftChecker(func() (bool, string) { return true, "10-site-abc.conf" })

	eng := newDriftTestEngine(t)
	c := &Checker{eng: eng}
	s := driftSite(true)

	c.checkConfigDrift([]*model.Site{s})
	logs, _ := eng.db.ListAlerts(10, false)
	if len(logs) != 1 {
		t.Fatalf("漂移应产生 1 条告警，实际 %d 条", len(logs))
	}
	if logs[0].Type != "config_drift" {
		t.Errorf("告警类型应为 config_drift，实际 %q", logs[0].Type)
	}
	if !strings.Contains(logs[0].Message, "10-site-abc.conf") {
		t.Errorf("告警消息应包含漂移文件名，实际 %q", logs[0].Message)
	}

	c.checkConfigDrift([]*model.Site{s})
	logs, _ = eng.db.ListAlerts(10, false)
	if len(logs) != 1 {
		t.Errorf("抑制窗口内不应重复告警，实际 %d 条", len(logs))
	}
}

// TestConfigDriftDisabledSite 站点告警总开关关闭时不得告警
// （Raise 对 !site.Alert.Enabled 直接短路）。
func TestConfigDriftDisabledSite(t *testing.T) {
	restoreDriftChecker(t)
	SetConfigDriftChecker(func() (bool, string) { return true, "10-site-abc.conf" })

	eng := newDriftTestEngine(t)
	c := &Checker{eng: eng}
	c.checkConfigDrift([]*model.Site{driftSite(false)})

	logs, _ := eng.db.ListAlerts(10, false)
	if len(logs) != 0 {
		t.Errorf("站点告警总开关关闭时不应告警，实际 %d 条", len(logs))
	}
}

// TestConfigDriftNoDrift 无漂移时不告警。
func TestConfigDriftNoDrift(t *testing.T) {
	restoreDriftChecker(t)
	SetConfigDriftChecker(func() (bool, string) { return false, "" })

	eng := newDriftTestEngine(t)
	c := &Checker{eng: eng}
	c.checkConfigDrift([]*model.Site{driftSite(true)})

	logs, _ := eng.db.ListAlerts(10, false)
	if len(logs) != 0 {
		t.Errorf("无漂移时不应产生告警，实际 %d 条", len(logs))
	}
}

// TestConfigDriftRuleTypeIsValid config_drift 必须在受支持类型清单里——
// 清单是前端下拉、后端校验、检查器三方的唯一事实来源。
func TestConfigDriftRuleTypeIsValid(t *testing.T) {
	if !model.IsValidAlertRuleType("config_drift") {
		t.Error("config_drift 应是受支持的告警规则类型（漏登记会导致规则被校验拒绝、永不触发）")
	}
}
