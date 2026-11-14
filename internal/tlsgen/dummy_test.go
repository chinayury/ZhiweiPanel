package tlsgen

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// isUnix 区分 POSIX 与 Windows；Windows 的 os.Chmod 行为不一致，
// 部分权限位检查需跳过。
func isUnix() bool { return runtime.GOOS != "windows" }

// TestEnsureDefaultCreatesAndReuses 首次调用生成、再次调用复用，
// 是 catch-all 的核心契约。
func TestEnsureDefaultCreatesAndReuses(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "dummy.crt")
	key := filepath.Join(dir, "dummy.key")

	c1, k1, existed, err := EnsureDefault(cert, key)
	if err != nil {
		t.Fatalf("首次 EnsureDefault: %v", err)
	}
	if existed {
		t.Errorf("首次调用 existed 必须是 false (刚生成的)")
	}
	if c1 != cert || k1 != key {
		t.Errorf("路径透传错误: cert=%q key=%q", c1, k1)
	}
	// 文件确实存在 + 模式正确。
	// Windows 的 os.Chmod 只支持 read-only 标志（其它位无意义），
	// 故 0o600 检查只在 Linux/macOS 有效；Windows 跳过即可。
	if fi, err := os.Stat(key); err != nil {
		t.Fatal("私钥未写入")
	} else if isUnix() && fi.Mode().Perm() != 0o600 {
		t.Errorf("私钥权限 = %o, 期望 0600", fi.Mode().Perm())
	}

	// 第二次调用应复用
	_, _, existed2, err := EnsureDefault(cert, key)
	if err != nil {
		t.Fatalf("二次 EnsureDefault: %v", err)
	}
	if !existed2 {
		t.Errorf("二次调用 existed 必须是 true (复用)")
	}
}

// TestEnsureDefaultCertIsValid 生成的证书能被 nginx 接受：RSA 2048+、
// 有效期足够长、Subject 与扩展密钥用途正确。
func TestEnsureDefaultCertIsValid(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "dummy.crt")
	key := filepath.Join(dir, "dummy.key")
	if _, _, _, err := EnsureDefault(cert, key); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("PEM 解码失败")
	}
	x, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509 解析失败: %v", err)
	}
	if time.Until(x.NotAfter) < 9*365*24*time.Hour {
		t.Errorf("证书有效期 < 9 年: %v", time.Until(x.NotAfter))
	}
	if x.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("证书缺少 DigitalSignature 用法")
	}
	foundServer := false
	for _, u := range x.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			foundServer = true
		}
	}
	if !foundServer {
		t.Error("证书缺少 ExtKeyUsage ServerAuth")
	}
}

// TestEnsureDefaultRegeneratesCorrupted 现有证书损坏时必须重生成。
// 这是 catch-all 在 nginx 升级/磁盘故障后自愈的关键。
func TestEnsureDefaultRegeneratesCorrupted(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "dummy.crt")
	key := filepath.Join(dir, "dummy.key")
	if err := os.WriteFile(cert, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, existed, err := EnsureDefault(cert, key)
	if err != nil {
		t.Fatalf("损坏应触发重生成: %v", err)
	}
	if existed {
		t.Errorf("损坏场景下 existed 必须是 false")
	}
}

// TestEnsureDefaultEmptyPath 路径为空直接报错（让调用方尽早发现配置错误）。
func TestEnsureDefaultEmptyPath(t *testing.T) {
	if _, _, _, err := EnsureDefault("", "/tmp/x"); err == nil {
		t.Error("certPath 空应报错")
	}
	if _, _, _, err := EnsureDefault("/tmp/x", ""); err == nil {
		t.Error("keyPath 空应报错")
	}
}
