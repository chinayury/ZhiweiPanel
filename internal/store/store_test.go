package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// TestSaveSitePersistsTimestamps 锁死一个真实缺陷：
// SaveSite 原先先 json.Marshal(s) 再赋 CreatedAt/UpdatedAt，
// 导致 config blob 里存的是零值时间；而 GetSite/ListSites 只读 config，
// API 于是返回 0001-01-01T00:00:00Z。
func TestSaveSitePersistsTimestamps(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ts.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	site := &model.Site{
		ID:      "site-ts-1",
		Name:    "ts-test",
		Status:  model.SiteStatusRunning,
		Domains: model.DomainList{"ts.test"},
	}
	if err := db.SaveSite(site); err != nil {
		t.Fatalf("SaveSite: %v", err)
	}

	// SaveSite 应当回填到传入对象上
	if site.CreatedAt.IsZero() {
		t.Error("SaveSite 未回填 CreatedAt")
	}
	if site.UpdatedAt.IsZero() {
		t.Error("SaveSite 未回填 UpdatedAt")
	}

	// 关键：从库里读回来的也必须带时间戳
	got, err := db.GetSite(site.ID)
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if got.CreatedAt.IsZero() {
		t.Error("GetSite 返回的 CreatedAt 是零值（config blob 未包含时间戳）")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("GetSite 返回的 UpdatedAt 是零值（config blob 未包含时间戳）")
	}
	if !got.CreatedAt.Equal(site.CreatedAt) {
		t.Errorf("CreatedAt 不一致: got %v want %v", got.CreatedAt, site.CreatedAt)
	}

	// 更新时 CreatedAt 必须保持，UpdatedAt 必须前进。
	// 注意：SaveSite 收指针，会原地改写 s.UpdatedAt，所以要先存一份旧值再比。
	prevUpdated := got.UpdatedAt
	time.Sleep(5 * time.Millisecond)
	got.Name = "ts-test-renamed"
	if err := db.SaveSite(got); err != nil {
		t.Fatalf("SaveSite(update): %v", err)
	}
	again, err := db.GetSite(site.ID)
	if err != nil {
		t.Fatalf("GetSite(2): %v", err)
	}
	if !again.CreatedAt.Equal(site.CreatedAt) {
		t.Errorf("更新后 CreatedAt 被改写: got %v want %v", again.CreatedAt, site.CreatedAt)
	}
	if !again.UpdatedAt.After(prevUpdated) {
		t.Errorf("更新后 UpdatedAt 未前进: got %v prev %v", again.UpdatedAt, prevUpdated)
	}
	if again.Name != "ts-test-renamed" {
		t.Errorf("Name 未更新: %q", again.Name)
	}
}

// TestListSitesCarriesTimestamps ListSites 同样只读 config，必须一并覆盖。
func TestListSitesCarriesTimestamps(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ts2.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, id := range []string{"s-a", "s-b"} {
		if err := db.SaveSite(&model.Site{
			ID: id, Name: id, Status: model.SiteStatusRunning,
			Domains: model.DomainList{id + ".test"},
		}); err != nil {
			t.Fatalf("SaveSite(%s): %v", id, err)
		}
	}

	sites, err := db.ListSites()
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("期望 2 个站点，得到 %d", len(sites))
	}
	for _, s := range sites {
		if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
			t.Errorf("站点 %s 时间戳是零值", s.ID)
		}
	}
}

// 老库的 users.email 是 ALTER 补的 nullable 列，新用户没有邮箱时是 NULL。
// 早期 GetUserProfile 直接 Scan 到 string，设置页会报
// "converting NULL to string is unsupported" 并整页加载失败。
func TestGetUserProfileNullEmail(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "profile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveUser("admin", "hash", "admin"); err != nil {
		t.Fatal(err)
	}
	p, err := db.GetUserProfile("admin")
	if err != nil {
		t.Fatalf("GetUserProfile: %v", err)
	}
	if p.Email != "" || p.EmailVerified {
		t.Fatalf("空邮箱应得零值，得到 %+v", p)
	}
}
