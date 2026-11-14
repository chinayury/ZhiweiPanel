package alert

import (
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

// TestProbeSiteIsWired 锁死 site_down 空钩子缺陷：
// probeSite 早期恒返回 true，导致该告警规则永远不触发。
// 现在默认实现必须做真实 HTTP 探测（不可达返回 false）。
func TestProbeSiteIsWired(t *testing.T) {
	// 一个必然无法连接的地址（保留段，不会路由）
	s := &model.Site{
		Domains: []string{"192.0.2.1"},
		Status:  model.SiteStatusRunning,
	}
	if probeSite(s) {
		t.Error("192.0.2.1 不可达，probeSite 应返回 false（空钩子会返回 true）")
	}

	// 空域名列表也应判为不可用，而不是恒 true
	s2 := &model.Site{Domains: nil, Status: model.SiteStatusRunning}
	if probeSite(s2) {
		t.Error("无域名时 probeSite 应返回 false")
	}
}

// TestTamperCheckerInjection 防篡改告警必须能被注入实现驱动。
// 早期 checkTamper 是空函数，Lock.AlertOnTamper 开了也不通知。
func TestTamperCheckerInjection(t *testing.T) {
	// 未注入时不应 panic，且不触发
	orig := tamperChecker
	tamperChecker = nil
	defer func() { tamperChecker = orig }()

	s := &model.Site{
		Name:   "t",
		Lock:   model.LockConf{Enabled: true, AlertOnTamper: true},
		Status: model.SiteStatusRunning,
		Alert: model.AlertConf{
			Rules: []model.AlertRule{{Type: "tamper", Enabled: true, Level: "critical"}},
		},
	}
	c := &Checker{}
	c.checkTamper(s) // 不应 panic

	// 注入一个"发现篡改"的实现，验证链路能走到 Raise
	called := false
	SetTamperChecker(func(st *model.Site) bool { called = true; return true })
	defer func() { tamperChecker = orig }()

	c.checkTamper(s)
	if !called {
		t.Error("注入了 tamperChecker 但未被调用，防篡改告警仍是断的")
	}
}

// TestSiteProbeProviderInjection 验证探测实现可被覆盖。
func TestSiteProbeProviderInjection(t *testing.T) {
	orig := probeSite
	defer func() { probeSite = orig }()

	SetSiteProbeProvider(func(s *model.Site) bool { return false })
	s := &model.Site{Domains: []string{"x.test"}}
	if probeSite(s) {
		t.Error("注入的 provider 应被使用")
	}
}
