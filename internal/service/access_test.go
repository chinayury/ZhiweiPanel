package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/htpasswd"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
)

// newAccessService 造一个只用于访问认证测试的服务实例。
func newAccessService(t *testing.T) (*Service, *store.DB) {
	t.Helper()
	base := t.TempDir()
	db, err := store.Open(filepath.Join(base, "panel.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	drv := newFakeDriver(filepath.Join(base, "conf"))
	svc := New(db, drv, nil, nil, nil, nil, filepath.Join(base, "data"), filepath.Join(base, "certs"), 0, false)
	return svc, db
}

// TestApplyAccessAuthHashesAndDropsPlaintext 确认明文口令被换成哈希且立刻丢弃。
//
// 这是本轮修掉的一个真实信息泄露：早期实现把访问认证口令当普通字段存进
// SQLite（站点配置 JSON 里），而 GET /api/sites/{id} 会把整份站点配置返回——
// 任何能读站点接口的账号（含只读 API Token）都能拿到全站访问密码。
func TestApplyAccessAuthHashesAndDropsPlaintext(t *testing.T) {
	svc, _ := newAccessService(t)
	site := &model.Site{
		ID: "abc123",
		Access: model.AccessConf{
			AuthEnabled: true,
			AuthUser:    "admin",
			AuthPass:    "s3cret",
		},
	}
	if err := svc.applyAccessAuth(site, ""); err != nil {
		t.Fatalf("applyAccessAuth: %v", err)
	}
	if site.Access.AuthPass != "" {
		t.Errorf("明文口令没有被丢弃: %q", site.Access.AuthPass)
	}
	if !strings.HasPrefix(site.Access.AuthHash, "$apr1$") {
		t.Errorf("口令哈希格式不对: %q", site.Access.AuthHash)
	}
	if !htpasswd.Verify("s3cret", site.Access.AuthHash) {
		t.Error("哈希校验不通过")
	}
	if strings.Contains(site.Access.AuthHash, "s3cret") {
		t.Error("哈希里出现了明文口令")
	}
}

// TestApplyAccessAuthIgnoresClientSuppliedHash 确认客户端提交的哈希一律丢弃。
//
// 否则调用方可以塞进明文口令或 {PLAIN} 之类的弱格式做降级，
// 绕过面板统一的哈希策略。
//
// 丢弃之后站点就"没有口令"了，因此开启认证时必须报错——
// 这正是期望行为：伪造的哈希不该被当成有效口令。
func TestApplyAccessAuthIgnoresClientSuppliedHash(t *testing.T) {
	svc, _ := newAccessService(t)
	site := &model.Site{
		ID: "abc123",
		Access: model.AccessConf{
			AuthEnabled: true,
			AuthUser:    "admin",
			AuthHash:    "admin:plaintext", // 客户端伪造
		},
	}
	err := svc.applyAccessAuth(site, "")
	if err == nil {
		t.Error("客户端伪造的哈希被当成了有效口令")
	}
	if site.Access.AuthHash != "" {
		t.Errorf("客户端提交的哈希被采纳了: %q", site.Access.AuthHash)
	}
}

// TestApplyAccessAuthKeepsOldHashWhenPasswordEmpty 确认"留空 = 不修改"。
//
// PUT 是全量替换语义：前端保存其它字段时不会带上密码（后端也不回显），
// 如果此时把哈希清掉，用户每改一次配置就要重设一次密码。
func TestApplyAccessAuthKeepsOldHashWhenPasswordEmpty(t *testing.T) {
	svc, _ := newAccessService(t)
	old := "$apr1$abcdefgh$EfExgQSMBXioDhIVk8IOb1"
	site := &model.Site{
		ID: "abc123",
		Access: model.AccessConf{
			AuthEnabled: true,
			AuthUser:    "admin",
		},
	}
	if err := svc.applyAccessAuth(site, old); err != nil {
		t.Fatalf("applyAccessAuth: %v", err)
	}
	if site.Access.AuthHash != old {
		t.Errorf("旧哈希没有保留: %q", site.Access.AuthHash)
	}
}

// TestApplyAccessAuthRequiresCredentials 确认开启认证必须给用户名与口令。
func TestApplyAccessAuthRequiresCredentials(t *testing.T) {
	svc, _ := newAccessService(t)

	site := &model.Site{ID: "abc123", Access: model.AccessConf{AuthEnabled: true}}
	if err := svc.applyAccessAuth(site, ""); err == nil {
		t.Error("开启认证但没有用户名/口令时应当报错")
	}

	site = &model.Site{ID: "abc123", Access: model.AccessConf{
		AuthEnabled: true, AuthUser: "admin",
	}}
	if err := svc.applyAccessAuth(site, ""); err == nil {
		t.Error("开启认证但没有口令时应当报错")
	}
}

// TestSyncAccessFileWritesAndRemoves 确认口令文件的落盘与清理。
func TestSyncAccessFileWritesAndRemoves(t *testing.T) {
	svc, _ := newAccessService(t)
	site := &model.Site{
		ID: "abc123",
		Access: model.AccessConf{
			AuthEnabled: true,
			AuthUser:    "admin",
			AuthPass:    "s3cret",
		},
	}
	if err := svc.applyAccessAuth(site, ""); err != nil {
		t.Fatalf("applyAccessAuth: %v", err)
	}
	if err := svc.syncAccessFile(site); err != nil {
		t.Fatalf("syncAccessFile: %v", err)
	}

	path := svc.accessFilePath(site.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取口令文件失败: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "admin:$apr1$") {
		t.Errorf("口令文件内容格式不对: %q", content)
	}
	if strings.Contains(content, "s3cret") {
		t.Error("口令文件里出现了明文口令")
	}
	if !strings.Contains(content, site.Access.AuthHash) {
		t.Errorf("口令文件里的哈希与站点配置不一致: %q", content)
	}

	// 幂等：再同步一次不应改变内容
	before, _ := os.Stat(path)
	if err := svc.syncAccessFile(site); err != nil {
		t.Fatalf("syncAccessFile 二次: %v", err)
	}
	after, _ := os.Stat(path)
	if before.ModTime() != after.ModTime() {
		t.Error("内容未变时不应重写口令文件")
	}

	// 关闭认证 → 文件必须被删掉（不能留一份没人知道内容的哈希在盘上）
	site.Access.AuthEnabled = false
	if err := svc.syncAccessFile(site); err != nil {
		t.Fatalf("syncAccessFile 关闭: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("关闭认证后口令文件仍然存在: %v", err)
	}
}

// TestAccessFileLivesOutsideSiteRoot 确认口令文件不在站点根目录里。
//
// 放在站点根目录里的话，nginx 会把它当静态文件伺服出去——
// 等于把口令哈希公开下载。
func TestAccessFileLivesOutsideSiteRoot(t *testing.T) {
	svc, _ := newAccessService(t)
	path := svc.accessFilePath("abc123")
	if !strings.HasPrefix(path, svc.DataDir()) {
		t.Errorf("口令文件不在数据目录下: %s", path)
	}
	if strings.Contains(path, "wwwroot") {
		t.Errorf("口令文件落在了站点根目录里: %s", path)
	}
}

// TestSiteSanitizeForReadHidesAccessSecrets 确认读路径不回显口令材料。
func TestSiteSanitizeForReadHidesAccessSecrets(t *testing.T) {
	site := &model.Site{
		ID: "abc123",
		Access: model.AccessConf{
			AuthEnabled: true,
			AuthUser:    "admin",
			AuthPass:    "s3cret",
			AuthHash:    "$apr1$abcdefgh$EfExgQSMBXioDhIVk8IOb1",
		},
	}
	cp := site.SanitizeForRead()
	if cp.Access.AuthPass != "" {
		t.Errorf("读路径回显了明文口令: %q", cp.Access.AuthPass)
	}
	if cp.Access.AuthHash != "" {
		t.Errorf("读路径回显了口令哈希: %q", cp.Access.AuthHash)
	}
	// 原对象不能被就地改坏
	if site.Access.AuthPass != "s3cret" {
		t.Error("SanitizeForRead 改动了原对象")
	}

	// 建库密码要保留（它只回显一次），访问口令不能保留
	site.Mysql = &model.Database{Name: "db", User: "u", Password: "verysecret"}
	cp2 := site.SanitizeAccessSecrets()
	if cp2.Access.AuthPass != "" || cp2.Access.AuthHash != "" {
		t.Error("SanitizeAccessSecrets 没有清掉访问认证口令")
	}
	if cp2.Mysql == nil || cp2.Mysql.Password != "verysecret" {
		t.Error("SanitizeAccessSecrets 不应清掉建库密码")
	}
}

// TestAccessHashNotPersistedAsPlaintext 确认库里不存明文口令。
func TestAccessHashNotPersistedAsPlaintext(t *testing.T) {
	svc, db := newAccessService(t)
	site := &model.Site{
		ID:      "abc123",
		Name:    "auth",
		Domains: model.DomainList{"auth.test"},
		Access: model.AccessConf{
			AuthEnabled: true,
			AuthUser:    "admin",
			AuthPass:    "s3cret",
		},
	}
	if err := svc.applyAccessAuth(site, ""); err != nil {
		t.Fatalf("applyAccessAuth: %v", err)
	}
	if err := db.SaveSite(site); err != nil {
		t.Fatalf("SaveSite: %v", err)
	}
	got, err := db.GetSite("abc123")
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if got.Access.AuthPass != "" {
		t.Errorf("库里存了明文口令: %q", got.Access.AuthPass)
	}
	if !htpasswd.Verify("s3cret", got.Access.AuthHash) {
		t.Error("库里存的哈希校验不通过")
	}
}
