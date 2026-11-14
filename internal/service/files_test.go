package service

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
)

func newFileTestService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Service{db: db, dataDir: t.TempDir(), sslDir: filepath.Join(t.TempDir(), "ssl")}
}

func fileTestSite(root string) *model.Site {
	return &model.Site{
		ID:      "site-fm",
		Name:    "fm",
		Domains: model.DomainList{"fm.test"},
		Root:    model.RootConf{Path: root, User: "", Group: ""},
	}
}

// TestResolveInRootBlocksTraversal 路径穿越必须被拦下。
func TestResolveInRootBlocksTraversal(t *testing.T) {
	s := newFileTestService(t)
	site := fileTestSite(t.TempDir())

	bad := []string{
		"../etc/passwd",
		"../../etc/passwd",
		"foo/../../etc/passwd",
		"foo/../../../etc/passwd",
		"./../etc",
	}
	for _, p := range bad {
		_, err := s.resolveInRoot(site, p)
		if err == nil {
			t.Errorf("expected %q to be rejected, but it passed", p)
		}
	}
}

// TestResolveInRootBlocksProtected logs/ 与 .well-known/ 业务层必须拦。
func TestResolveInRootBlocksProtected(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "logs"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".well-known", "acme-challenge"), 0o755)
	site := fileTestSite(root)

	if _, err := s.ListFiles(context.Background(), site, "logs"); err != ErrForbiddenPath {
		t.Errorf("ListFiles logs should be blocked: %v", err)
	}
	if _, err := s.ListFiles(context.Background(), site, ".well-known/acme-challenge"); err != ErrForbiddenPath {
		t.Errorf("ListFiles .well-known should be blocked: %v", err)
	}
	if err := s.WriteFile(context.Background(), site, "logs/evil.log", []byte("x")); err != ErrForbiddenPath {
		t.Errorf("WriteFile logs should be blocked: %v", err)
	}
	if err := s.WriteFile(context.Background(), site, ".well-known/x", []byte("x")); err != ErrForbiddenPath {
		t.Errorf("WriteFile .well-known should be blocked: %v", err)
	}
}

// TestListFilesDirListing 列表必须含正确条目、目录优先、字母序。
func TestListFilesDirListing(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	for _, p := range []string{"zeta.txt", "alpha.txt", "mydir", "another"} {
		if strings.HasSuffix(p, ".txt") {
			if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	site := fileTestSite(root)

	entries, err := s.ListFiles(context.Background(), site, "")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name+":"+e.Type)
	}
	want := []string{"another:dir", "mydir:dir", "alpha.txt:file", "zeta.txt:file"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("sort wrong:\n  got  %v\n  want %v", names, want)
	}
}

// TestWriteAndReadFile 写读往返。
func TestWriteAndReadFile(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	site := fileTestSite(root)

	want := "hello zhiweipanel"
	if err := s.WriteFile(context.Background(), site, "hello.txt", []byte(want)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, isText, _, err := s.ReadFile(context.Background(), site, "hello.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !isText {
		t.Errorf("plain text should be detected as isText")
	}
	if string(got) != want {
		t.Errorf("content mismatch: %q vs %q", got, want)
	}
}

// TestDeleteFileRefusesNonEmptyDir 删非空目录必须被拒。
func TestDeleteFileRefusesNonEmptyDir(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	site := fileTestSite(root)

	if err := s.DeleteFile(context.Background(), site, "sub"); err == nil {
		t.Fatal("delete non-empty dir should fail")
	}
	if err := s.DeleteFile(context.Background(), site, "sub/f.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFile(context.Background(), site, "sub"); err != nil {
		t.Errorf("empty dir should be deletable: %v", err)
	}
}

// TestRenameSameRoot 重命名必须限在同 root 内，且不进入受保护子目录。
func TestRenameSameRoot(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	site := fileTestSite(root)

	if err := s.Rename(context.Background(), site, "old.txt", "new.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err != nil {
		t.Errorf("new.txt missing after rename: %v", err)
	}
	if err := s.Mkdir(context.Background(), site, "sub"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(context.Background(), site, "new.txt", "sub/moved.txt"); err != nil {
		t.Errorf("rename to subdir should work: %v", err)
	}
	if err := s.Rename(context.Background(), site, "sub/moved.txt", "logs/x"); err != ErrForbiddenPath {
		t.Errorf("rename into logs must be blocked: %v", err)
	}
}

// TestEnsureDefaultIndexEmptyRootOnly 已有 index 文件不覆盖。
func TestEnsureDefaultIndexEmptyRootOnly(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	site := fileTestSite(root)

	// 第一次：root 空 → 应写
	s.EnsureDefaultIndex(site)
	data, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatalf("default index not written: %v", err)
	}
	if !strings.Contains(string(data), site.Name) {
		t.Errorf("default index should mention site name %q", site.Name)
	}

	// 第二次：用户已放 → 不覆盖
	custom := "<html>my page</html>"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	s.EnsureDefaultIndex(site)
	got, _ := os.ReadFile(filepath.Join(root, "index.html"))
	if string(got) != custom {
		t.Errorf("user content was overwritten")
	}

	// 第三次：用户放的是 index.htm → 也不应被覆盖
	if err := os.WriteFile(filepath.Join(root, "index.htm"), []byte("htm"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.EnsureDefaultIndex(site)
	got, _ = os.ReadFile(filepath.Join(root, "index.htm"))
	if string(got) != "htm" {
		t.Errorf("index.htm was overwritten")
	}
}

// 在 t.TempDir() 构造一个 zip，返回路径。
func writeTestZip(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestUnzipArchiveBasic 解压 + 解压到指定 dest。
func TestUnzipArchiveBasic(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	site := fileTestSite(root)

	zipPath := writeTestZip(t, map[string]string{
		"index.html": "<h1>hi</h1>",
		"a/b/c.txt":  "deep",
		"a/d.txt":    "shallow",
	})
	// 把 zip 复制到 site root
	if err := os.WriteFile(filepath.Join(root, "site.zip"), []byte(mustRead(t, zipPath)), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := s.UnzipArchive(context.Background(), site, "site.zip", "public")
	if err != nil {
		t.Fatalf("unzip: %v", err)
	}
	if n != 3 {
		t.Errorf("extracted = %d, want 3", n)
	}
	for _, want := range []string{"public/index.html", "public/a/b/c.txt", "public/a/d.txt"} {
		if _, err := os.Stat(filepath.Join(root, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

// TestUnzipArchiveDefaultDest 不传 dest 时解压到 zip 同目录（与 unzip 命令一致）。
func TestUnzipArchiveDefaultDest(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	site := fileTestSite(root)

	zipPath := writeTestZip(t, map[string]string{"x.txt": "y"})
	if err := os.MkdirAll(filepath.Join(root, "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "uploads", "site.zip"), []byte(mustRead(t, zipPath)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnzipArchive(context.Background(), site, "uploads/site.zip", ""); err != nil {
		t.Fatal(err)
	}
	// 默认 dest = zip 父目录 (uploads/)，entry "x.txt" → uploads/x.txt
	if _, err := os.Stat(filepath.Join(root, "uploads", "x.txt")); err != nil {
		t.Errorf("default dest unzip failed: %v", err)
	}
}

// TestUnzipArchiveRejectsZipSlip 含 ../ 的 entry 必须被拒（zip-slip）。
func TestUnzipArchiveRejectsZipSlip(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	site := fileTestSite(root)

	zipPath := filepath.Join(root, "evil.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// 先写一个干净 entry，再写一个 zip-slip entry —— 顺序无关，
	// 因为我们的检查在循环最前面。
	for _, entry := range []struct{ n, b string }{
		{"good.txt", "ok"},
		{"../escape.txt", "evil"},
	} {
		w, err := zw.Create(entry.n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(entry.b))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, err := s.UnzipArchive(context.Background(), site, "evil.zip", ""); err == nil {
		t.Errorf("zip-slip ../escape.txt should be rejected")
	}
}

// TestUnzipArchiveRejectsSymlink 含 symlink 模式的 entry 必须被拒。
func TestUnzipArchiveRejectsSymlink(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	site := fileTestSite(root)

	zipPath := filepath.Join(root, "symlink.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// 手工设 ExternalAttrs 高 16 位为 symlink 模式 0xA1FF
	// （zip.FileHeader.SetMode 不会写 ExternalAttrs，必须直接设字段）
	hdr := &zip.FileHeader{
		Name:           "lnk",
		Method:         zip.Deflate,
		CreatorVersion: 3 << 8, // creatorUnix
		ExternalAttrs:  (0xA000 | 0x1FF) << 16,
	}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("/etc/passwd"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, err := s.UnzipArchive(context.Background(), site, "symlink.zip", ""); err == nil {
		t.Errorf("symlink entry should be rejected")
	}
}

// TestUnzipArchiveRejectsProtectedDest 解压到 logs/ / .well-known/ 必须被拒。
func TestUnzipArchiveRejectsProtectedDest(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	site := fileTestSite(root)

	zipPath := writeTestZip(t, map[string]string{"a.txt": "x"})
	if err := os.WriteFile(filepath.Join(root, "site.zip"), []byte(mustRead(t, zipPath)), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UnzipArchive(context.Background(), site, "site.zip", "logs"); err != ErrForbiddenPath {
		t.Errorf("dest=logs should be rejected: %v", err)
	}
	if _, err := s.UnzipArchive(context.Background(), site, "site.zip", ".well-known"); err != ErrForbiddenPath {
		t.Errorf("dest=.well-known should be rejected: %v", err)
	}
}

// TestUnzipArchiveEntrySizeLimit 单 entry 超限必须被拒（仅创建会超上限的 entry）。
func TestUnzipArchiveEntrySizeLimit(t *testing.T) {
	s := newFileTestService(t)
	root := t.TempDir()
	site := fileTestSite(root)

	zipPath := filepath.Join(root, "big.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("huge.bin")
	if err != nil {
		t.Fatal(err)
	}
	// 写出超过 maxUnzipEntrySize 的数据
	big := bytes.Repeat([]byte{0x42}, maxUnzipEntrySize+1024)
	_, _ = w.Write(big)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, err := s.UnzipArchive(context.Background(), site, "big.zip", ""); err == nil {
		t.Errorf("huge entry should be rejected (over %d bytes)", maxUnzipEntrySize)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
