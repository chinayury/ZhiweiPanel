package service

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/driver"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
)

// fakeDriver is an in-memory driver for ApplyAllConfig tests.
// It records all written files so tests can assert file presence + content.
type fakeDriver struct {
	siteConfDir string
	files       map[string]string
	confs       map[string]string
	removed     []string
	testOK      bool
	reloadCount int
}

func newFakeDriver(dir string) *fakeDriver {
	return &fakeDriver{
		siteConfDir: dir,
		files:       map[string]string{},
		confs:       map[string]string{},
		testOK:      true,
	}
}

func (f *fakeDriver) Runtime() driver.Runtime { return driver.RuntimeHost }
func (f *fakeDriver) Paths() driver.Paths {
	return driver.Paths{SiteConfDir: f.siteConfDir, ConfDir: f.siteConfDir}
}
func (f *fakeDriver) NginxStatus(context.Context) (driver.NginxStatus, error) {
	return driver.NginxStatus{}, nil
}
func (f *fakeDriver) NginxTest(context.Context) (bool, string, error) {
	return f.testOK, "ok", nil
}
func (f *fakeDriver) Reload(context.Context) error { f.reloadCount++; return nil }
func (f *fakeDriver) WriteConf(_ context.Context, name, content string) error {
	f.confs[name] = content
	return nil
}
func (f *fakeDriver) RemoveConf(_ context.Context, name string) error {
	delete(f.confs, name)
	return nil
}
func (f *fakeDriver) WriteSiteConf(_ context.Context, siteID, content string) error {
	f.files[driver.SiteConfName(siteID)] = content
	return nil
}
func (f *fakeDriver) RemoveSiteConf(_ context.Context, siteID string) error {
	name := driver.SiteConfName(siteID)
	f.removed = append(f.removed, siteID)
	delete(f.files, name)
	return nil
}
func (f *fakeDriver) ReadConf(_ context.Context, name string) (string, error) {
	return f.confs[name], nil
}
func (f *fakeDriver) ListSiteConfs(context.Context) ([]string, error) {
	// Strip .conf suffix to match real ListSiteConfs output.
	var out []string
	for name := range f.files {
		out = append(out, strings.TrimSuffix(name, ".conf"))
	}
	sort.Strings(out)
	return out, nil
}
func (f *fakeDriver) PHPVersions(context.Context) ([]driver.PHPVersion, error) {
	return nil, nil
}
func (f *fakeDriver) SystemStatus(context.Context) (driver.SystemStatus, error) {
	return driver.SystemStatus{}, nil
}

// TestApplyAllConfigRemovesOrphanSiteConf locks down a real bug:
// after deleting a site, its 10-site-<id>.conf remains in conf.d, nginx
// continues to load it, and the deleted site keeps serving requests
// (delete returns 200, curl still gets 200). ApplyAllConfig must
// remove configs for sites no longer in the database.
func TestApplyAllConfigRemovesOrphanSiteConf(t *testing.T) {
	dir := t.TempDir()
	drv := newFakeDriver(dir)

	db, err := store.Open(filepath.Join(t.TempDir(), "orphan.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	root := t.TempDir()
	if err := db.SaveSite(&model.Site{
		ID: "site-live", Name: "live", Status: model.SiteStatusRunning,
		Domains: model.DomainList{"live.test"},
		Root:    model.RootConf{Path: root},
	}); err != nil {
		t.Fatalf("SaveSite: %v", err)
	}

	// Simulate a disk-orphan site config (DB no longer has this site).
	drv.files[driver.SiteConfName("site-deleted")] = "server { listen 80; }"
	// And a hand-written file the panel must NEVER touch.
	drv.files["my-manual-notes"] = "server { listen 8080; }"

	svc := New(db, drv, nil, nil, nil, nil, t.TempDir(), t.TempDir(), 0, false)
	if err := svc.ApplyAllConfig(context.Background()); err != nil {
		t.Fatalf("ApplyAllConfig: %v", err)
	}

	// Orphan must be deleted.
	if _, ok := drv.files[driver.SiteConfName("site-deleted")]; ok {
		t.Error("orphan site config not cleaned (site would keep serving)")
	}
	found := false
	for _, id := range drv.removed {
		if id == "site-deleted" {
			found = true
		}
	}
	if !found {
		t.Errorf("RemoveSiteConf did not record site-deleted; removed=%v", drv.removed)
	}

	// Hand-written file must be preserved.
	if _, ok := drv.files["my-manual-notes"]; !ok {
		t.Error("non-panel file my-manual-notes was deleted by panel")
	}

	// Live site must be written.
	if _, ok := drv.files[driver.SiteConfName("site-live")]; !ok {
		t.Error("live site config not written")
	}

	// http-level config must use 00- prefix.
	if _, ok := drv.confs["00-zhiweipanel-http.conf"]; !ok {
		t.Errorf("http-level conf not found; confs=%v", drv.confs)
	}
	if drv.reloadCount != 1 {
		t.Errorf("reloadCount = %d, want 1", drv.reloadCount)
	}
}

// TestApplyAllConfigStopsDisabledSite verifies that stopping a site really
// takes its server block out of service (every line of its server { ... }
// is commented out so a stray match can't accidentally serve traffic).
func TestApplyAllConfigStopsDisabledSite(t *testing.T) {
	dir := t.TempDir()
	drv := newFakeDriver(dir)

	db, err := store.Open(filepath.Join(t.TempDir(), "stop.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.SaveSite(&model.Site{
		ID: "site-stopped", Name: "stopped", Status: model.SiteStatusStopped,
		Domains: model.DomainList{"stopped.test"},
		Root:    model.RootConf{Path: t.TempDir()},
	}); err != nil {
		t.Fatalf("SaveSite: %v", err)
	}

	svc := New(db, drv, nil, nil, nil, nil, t.TempDir(), t.TempDir(), 0, false)
	if err := svc.ApplyAllConfig(context.Background()); err != nil {
		t.Fatalf("ApplyAllConfig: %v", err)
	}

	content, ok := drv.files[driver.SiteConfName("site-stopped")]
	if !ok {
		t.Fatal("stopped site config not written")
	}
	// No live directive must remain. If any of these is uncommented,
	// nginx will treat it as active and serve the deleted site.
	for _, line := range splitLines(content) {
		trimmed := trimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == "server {" || trimmed == "listen 80;" || trimmed == "}" {
			t.Errorf("stopped site still has live directive: %q", line)
		}
	}
}

// TestApplyAllConfigWritesCrossSiteIsolation locks down the "catch-all default_server"
// requirement. ApplyAllConfig must write a catch-all default_server so unmatched
// Host/IP direct connections fall to it instead of leaking into a real site.
//
// Two invariants checked:
//  1. default_server file 01-zhiweipanel-default-server.conf exists
//  2. SSL 80 + 443 are both present, with
//     ssl_reject_handshake for the SSL 80 case
//     (so a client sending HTTPS to the plain-80 port gets rejected, not leaked)
func TestApplyAllConfigWritesCrossSiteIsolation(t *testing.T) {
	dir := t.TempDir()
	drv := newFakeDriver(dir)

	db, err := store.Open(filepath.Join(t.TempDir(), "iso.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	root := t.TempDir()
	if err := db.SaveSite(&model.Site{
		ID: "site-ssl", Name: "with-ssl", Status: model.SiteStatusRunning,
		Domains: model.DomainList{"ssl.test"},
		Root:    model.RootConf{Path: root},
		SSL:     model.SSLConf{Enabled: true, CertFile: "/c.pem", KeyFile: "/k.pem"},
	}); err != nil {
		t.Fatalf("SaveSite: %v", err)
	}
	if err := db.SaveSite(&model.Site{
		ID: "site-plain", Name: "plain", Status: model.SiteStatusRunning,
		Domains: model.DomainList{"plain.test"},
		Root:    model.RootConf{Path: root},
	}); err != nil {
		t.Fatalf("SaveSite: %v", err)
	}

	svc := New(db, drv, nil, nil, nil, nil, t.TempDir(), t.TempDir(), 0, false)
	if err := svc.ApplyAllConfig(context.Background()); err != nil {
		t.Fatalf("ApplyAllConfig: %v", err)
	}

	got, ok := drv.confs["01-zhiweipanel-default-server.conf"]
	if !ok {
		t.Fatalf("catch-all default_server not written; confs: %v", keysOf(drv.confs))
	}
	lines := directiveLines(got)
	for _, want := range []string{
		"server_name _;",
		"listen 80 default_server;",
		"return 444;",
		// SSL 80 should also have ssl_reject_handshake; listen 443 ssl
		"listen 443 ssl default_server;",
	} {
		if !containsLine(lines, want) {
			t.Errorf("catch-all missing %q\ncontent:\n%s", want, got)
		}
	}
	// 443 listen must point at a real dummy cert (served by service.sslDir).
	if !strings.Contains(got, "ssl_certificate ") || !strings.Contains(got, "dummy.crt") {
		t.Errorf("catch-all missing dummy ssl_certificate path:\n%s", got)
	}
	if !strings.Contains(got, "ssl_certificate_key ") || !strings.Contains(got, "dummy.key") {
		t.Errorf("catch-all missing dummy ssl_certificate_key path:\n%s", got)
	}
}

// TestApplyAllConfigDefaultServerNoSSLHasNoSSLListen makes sure when no site
// has SSL, the default_server doesn't include listen 443 or ssl_reject_handshake
// directives (which would cause nginx to fail on the missing cert).
func TestApplyAllConfigDefaultServerNoSSLHasNoSSLListen(t *testing.T) {
	dir := t.TempDir()
	drv := newFakeDriver(dir)

	db, err := store.Open(filepath.Join(t.TempDir(), "iso-plain.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.SaveSite(&model.Site{
		ID: "site-only", Name: "only", Status: model.SiteStatusRunning,
		Domains: model.DomainList{"only.test"},
		Root:    model.RootConf{Path: t.TempDir()},
	}); err != nil {
		t.Fatalf("SaveSite: %v", err)
	}

	svc := New(db, drv, nil, nil, nil, nil, t.TempDir(), t.TempDir(), 0, false)
	if err := svc.ApplyAllConfig(context.Background()); err != nil {
		t.Fatalf("ApplyAllConfig: %v", err)
	}

	got, ok := drv.confs["01-zhiweipanel-default-server.conf"]
	if !ok {
		t.Fatal("catch-all conf not written")
	}
	lines := directiveLines(got)
	for _, bad := range []string{"listen 443", "ssl_reject_handshake"} {
		if containsLine(lines, bad) {
			t.Errorf("plain (no-SSL) catch-all must not have %q:\n%s", bad, got)
		}
	}
}

// TestApplyAllConfigDefaultServerSurvivesRollback verifies that on a failed
// nginx -t, the previously-good catch-all default_server is NOT clobbered.
func TestApplyAllConfigDefaultServerSurvivesRollback(t *testing.T) {
	dir := t.TempDir()
	drv := newFakeDriver(dir)
	drv.testOK = false // force nginx -t to fail

	db, err := store.Open(filepath.Join(t.TempDir(), "iso-rollback.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.SaveSite(&model.Site{
		ID: "site-x", Name: "x", Status: model.SiteStatusRunning,
		Domains: model.DomainList{"x.test"},
		Root:    model.RootConf{Path: t.TempDir()},
	}); err != nil {
		t.Fatalf("SaveSite: %v", err)
	}

	// baseline: run with testOK=true to establish a clean default_server
	drv2 := newFakeDriver(dir)
	svcBase := New(db, drv2, nil, nil, nil, nil, t.TempDir(), t.TempDir(), 0, false)
	if err := svcBase.ApplyAllConfig(context.Background()); err != nil {
		t.Fatalf("baseline ApplyAllConfig: %v", err)
	}
	baseline, ok := drv2.confs["01-zhiweipanel-default-server.conf"]
	if !ok {
		t.Fatal("baseline run: default_server not written")
	}

	// seed the failing driver with baseline (so its "read" of the existing
	// conf returns a valid file), then run with testOK=false.
	drv.confs["01-zhiweipanel-default-server.conf"] = baseline

	svc := New(db, drv, nil, nil, nil, nil, t.TempDir(), t.TempDir(), 0, false)
	if err := svc.ApplyAllConfig(context.Background()); err == nil {
		t.Fatal("expected error when testOK=false, but ApplyAllConfig succeeded")
	}
	// catch-all must still equal baseline (no clobber on failure).
	if got := drv.confs["01-zhiweipanel-default-server.conf"]; got != baseline {
		t.Errorf("catch-all was clobbered on rollback\n\nafter failure:\n%s\n\nbaseline:\n%s", got, baseline)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// directiveLines extracts the meaningful directive lines from an nginx config.
// Mirrors internal/nginx/default_server_test.go.
func directiveLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	return strings.Split(s, "\n")
}

func trimSpace(s string) string {
	return strings.TrimSpace(s)
}
