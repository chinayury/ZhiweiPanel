package tlsgen

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureForPanelSAN(t *testing.T) {
	cert := filepath.Join(t.TempDir(), "panel.crt")
	key := filepath.Join(t.TempDir(), "panel.key")
	hosts := []string{"panel.local", "198.51.100.10", "127.0.0.1", "::1"}
	if _, _, _, err := EnsureForPanel(cert, key, hosts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("PEM decode failed")
	}
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(crt.DNSNames, "panel.local") {
		t.Errorf("DNS SAN missing panel.local: %v", crt.DNSNames)
	}
	if !containsIP(crt.IPAddresses, "198.51.100.10") {
		t.Errorf("IP SAN missing 198.51.100.10: %v", crt.IPAddresses)
	}
	if !containsIP(crt.IPAddresses, "127.0.0.1") {
		t.Errorf("IP SAN missing 127.0.0.1: %v", crt.IPAddresses)
	}
}

func TestEnsureForPanelEmptyHosts(t *testing.T) {
	cert := filepath.Join(t.TempDir(), "panel.crt")
	key := filepath.Join(t.TempDir(), "panel.key")
	// 不传 hosts → 兜底为 localhost / 127.0.0.1
	if _, _, _, err := EnsureForPanel(cert, key, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cert)
	block, _ := pem.Decode(data)
	crt, _ := x509.ParseCertificate(block.Bytes)
	if !containsStr(crt.DNSNames, "localhost") {
		t.Errorf("fallback DNS missing: %v", crt.DNSNames)
	}
}

// TestEnsureForPanelReuseExisting 已存在且可用 → 复用，不重生成
func TestEnsureForPanelReuseExisting(t *testing.T) {
	cert := filepath.Join(t.TempDir(), "panel.crt")
	key := filepath.Join(t.TempDir(), "panel.key")
	_, _, created, err := EnsureForPanel(cert, key, []string{"a.test"})
	if err != nil || created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	// 第二次：应返回 existed=true
	_, _, existed, err := EnsureForPanel(cert, key, []string{"b.test"})
	if err != nil || !existed {
		t.Fatalf("second call: existed=%v err=%v", existed, err)
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func containsIP(ips []net.IP, want string) bool {
	target := net.ParseIP(want)
	if target == nil {
		return false
	}
	for _, ip := range ips {
		if ip.Equal(target) {
			return true
		}
	}
	return false
}
