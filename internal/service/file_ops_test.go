package service

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zhiwei/panel/internal/model"
)

func newFileOpService(t *testing.T) (*Service, *model.Site, string) {
	t.Helper()
	root := t.TempDir()
	svc := &Service{dataDir: root}
	site := &model.Site{Root: model.RootConf{Path: root}}
	return svc, site, root
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompressAndExtractZipAndTarGz(t *testing.T) {
	for _, format := range []string{"zip", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			svc, site, root := newFileOpService(t)
			writeTestFile(t, root, "a.txt", "hello")
			writeTestFile(t, root, "dir/b.txt", "world")
			writeTestFile(t, root, "logs/access.log", "nginx-log")

			archive, err := svc.CompressPaths(context.Background(), site, "", []string{"a.txt", "dir"}, format)
			if err != nil {
				t.Fatalf("压缩失败: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(archive))); err != nil {
				t.Fatalf("压缩包不存在: %v", err)
			}

			n, err := svc.ExtractArchive(context.Background(), site, archive, "out")
			if err != nil {
				t.Fatalf("解压失败: %v", err)
			}
			if n < 2 {
				t.Fatalf("解压条目数过少: %d", n)
			}
			for _, rel := range []string{"out/a.txt", "out/dir/b.txt"} {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
					t.Fatalf("解压后缺少 %s: %v", rel, err)
				}
			}
			// 站点根 logs 必须被跳过（它不是用户内容，且可能很大）。
			if _, err := os.Stat(filepath.Join(root, "out", "logs")); !os.IsNotExist(err) {
				t.Fatalf("解压不应包含站点 logs")
			}
		})
	}
}

func TestExtractTarRejectsTraversalAndSymlink(t *testing.T) {
	svc, site, root := newFileOpService(t)
	makeTar := func(path string, write func(*tar.Writer)) {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(f)
		write(tw)
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// .. 穿越
	makeTar(filepath.Join(root, "evil.tar"), func(tw *tar.Writer) {
		_ = tw.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("bad"))
	})
	if _, err := svc.ExtractArchive(context.Background(), site, "evil.tar", "out"); err == nil {
		t.Fatal("含 .. 的 tar 应被拒绝")
	} else if !errors.Is(err, ErrInvalidArchiveEntry) {
		t.Fatalf("错误类型不对: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("穿越文件不应被写出")
	}

	// symlink
	makeTar(filepath.Join(root, "link.tar"), func(tw *tar.Writer) {
		_ = tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc/passwd", Mode: 0o777, Typeflag: tar.TypeSymlink})
	})
	if _, err := svc.ExtractArchive(context.Background(), site, "link.tar", "out2"); err == nil {
		t.Fatal("symlink tar 应被拒绝")
	}
}

func TestBatchDeleteValidationAndRecursive(t *testing.T) {
	svc, site, root := newFileOpService(t)
	writeTestFile(t, root, "dir/a.txt", "x")
	if _, err := svc.BatchDelete(context.Background(), site, []string{"dir"}, false); err == nil {
		t.Fatal("非空目录未开递归时应拒绝")
	}
	if _, err := os.Stat(filepath.Join(root, "dir", "a.txt")); err != nil {
		t.Fatal("校验失败时不应产生部分删除")
	}
	n, err := svc.BatchDelete(context.Background(), site, []string{"dir"}, true)
	if err != nil {
		t.Fatalf("递归删除失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d，期望 1", n)
	}
	if _, err := os.Stat(filepath.Join(root, "dir")); !os.IsNotExist(err) {
		t.Fatal("目录应已删除")
	}
	writeTestFile(t, root, "logs/a.log", "x")
	if _, err := svc.BatchDelete(context.Background(), site, []string{"logs/a.log"}, true); err == nil {
		t.Fatal("受保护目录应拒绝")
	}
}

func TestChmodAndDownloadAndSearch(t *testing.T) {
	svc, site, root := newFileOpService(t)
	writeTestFile(t, root, "index.php", "<?php")
	writeTestFile(t, root, "sub/app.php", "app")
	writeTestFile(t, root, "logs/access.log", "x")

	if err := svc.Chmod(context.Background(), site, "index.php", "0600"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(root, "index.php"))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("权限=%o，期望 0600", info.Mode().Perm())
		}
	}
	if err := svc.Chmod(context.Background(), site, "index.php", "999"); err == nil {
		t.Fatal("非法权限应拒绝")
	}

	full, size, err := svc.DownloadFile(context.Background(), site, "index.php")
	if err != nil || size != int64(len("<?php")) || full == "" {
		t.Fatalf("下载信息错误: %v %d %q", err, size, full)
	}
	if _, _, err := svc.DownloadFile(context.Background(), site, "sub"); err == nil {
		t.Fatal("目录不应可下载")
	}
	if _, _, err := svc.DownloadFile(context.Background(), site, "logs/access.log"); err == nil {
		t.Fatal("受保护目录不应可下载")
	}

	hits, err := svc.SearchFiles(context.Background(), site, "app", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "sub/app.php" {
		t.Fatalf("搜索结果错误: %+v", hits)
	}
	hits, _ = svc.SearchFiles(context.Background(), site, "access", 200)
	if len(hits) != 0 {
		t.Fatalf("logs 应被搜索跳过: %+v", hits)
	}
}

func TestRemoteDownloadRejectsForbiddenTargets(t *testing.T) {
	svc, site, _ := newFileOpService(t)
	if _, _, err := svc.RemoteDownload(context.Background(), site, "http://127.0.0.1:8080/x", "", ""); err == nil {
		t.Fatal("回环地址应被拒绝")
	}
	if _, _, err := svc.RemoteDownload(context.Background(), site, "http://169.254.169.254/latest/meta-data", "", ""); err == nil {
		t.Fatal("云元数据地址应被拒绝")
	}
	if _, _, err := svc.RemoteDownload(context.Background(), site, "file:///etc/passwd", "", ""); err == nil {
		t.Fatal("非 http(s) 协议应被拒绝")
	}
	if err := validateDownloadName("../evil"); err == nil {
		t.Fatal("非法文件名应被拒绝")
	}
	if err := validateDownloadName("a/b"); err == nil {
		t.Fatal("含路径分隔符的文件名应被拒绝")
	}
}
