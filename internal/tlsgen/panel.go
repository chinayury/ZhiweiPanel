package tlsgen

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnsureForPanel 生成或读取面板 HTTPS 用的自签证书到 (certPath, keyPath)。
//
// 与 EnsureDefault 的核心区别：本函数会写入 SubjectAltName（DNS 与 IP），
// 让浏览器在用户接受"不安全证书"后能正常访问面板。EnsureDefault 故意
// 不带 SAN —— 它给 catch-all 用，根本没用户会去主动访问。
//
// hosts：SAN 列表，可含 DNS 域名或 IP 字符串。空时回退 ["localhost", "127.0.0.1", "::1"]。
//
// 复用 EnsureDefault 的"已存在则复用 / 损坏则重生成"框架：
// usable() 检查证书可解析且私钥匹配，省去每次启动 RSA 生成的 ~50ms。
func EnsureForPanel(certPath, keyPath string, hosts []string) (string, string, bool, error) {
	if certPath == "" || keyPath == "" {
		return "", "", false, fmt.Errorf("证书路径不能为空")
	}
	if usable(certPath, keyPath) {
		return certPath, keyPath, true, nil
	}
	if len(hosts) == 0 {
		hosts = []string{"localhost", "127.0.0.1", "::1"}
	}
	certPEM, keyPEM, err := generateForHosts(hosts)
	if err != nil {
		return "", "", false, fmt.Errorf("生成面板证书失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return "", "", false, err
	}
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", false, fmt.Errorf("写私钥失败: %w", err)
	}
	if err := writeFile(certPath, certPEM, 0o644); err != nil {
		return "", "", false, fmt.Errorf("写证书失败: %w", err)
	}
	return certPath, keyPath, false, nil
}

// generateForPanelHosts 是 EnsureForPanel 内部使用的证书生成器。
//
// 抽出来独立函数是为了方便测试 —— 单元测试可以只调它，校验 SAN 写入。
func generateForHosts(hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ZhiweiPanel"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(Validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	// 兜底：若用户传的 hosts 全部无法解析为空，至少带 localhost，
	// 否则 Chrome 仍然会因为 SAN 为空而拒绝。
	if len(tmpl.DNSNames) == 0 && len(tmpl.IPAddresses) == 0 {
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return certPEM, keyPEM, nil
}
