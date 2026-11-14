// Package tlsgen 生成面板内部需要的自签证书。
//
// 用途：catch-all default_server 在 443 端口需要一份 SSL 证书才能让
// nginx 完成握手并立即 return 444（防止真实站点的证书被 IP 直连者拿到）。
// 不暴露任何用户真实证书，dummy 自签证书被客户端识别为"非受信 CA 签发"，
// 浏览器会显示警告但服务真实存在性也以"自签证书"形式被掩盖。
//
// 一次生成、永久复用。重新生成条件：文件缺失、读取失败、PEM 解析失败、
// 或证书有效期<30 天。
package tlsgen

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Validity dummy 证书有效期。
// 10 年足以覆盖面板整个生命周期，且不需要自动续签（无业务用途）。
const Validity = 10 * 365 * 24 * time.Hour

// EnsureDefault 生成或读取 catch-all 用的自签证书到 (certPath, keyPath)。
//
// 返回值：
//   - certPath: 证书文件最终路径
//   - keyPath: 私钥文件最终路径
//   - existed: true 表示文件已存在且有效，false 表示本次新建
//
// 错误：
//   - certPath/keyPath 任一为空 → fmt.Errorf
//   - 读现有证书失败或 PEM/x509 解析失败 → 视为不存在，会覆盖重生成
//   - 写文件失败 → 上抛
//
// 安全性：私钥文件生成后 chmod 0600，仅 root 可读。cert 文件 0644，
// 让 nginx worker 进程读取。
func EnsureDefault(certPath, keyPath string) (string, string, bool, error) {
	if certPath == "" || keyPath == "" {
		return "", "", false, fmt.Errorf("证书路径不能为空")
	}
	// 已存在且近期有效 → 直接复用
	if usable(certPath, keyPath) {
		return certPath, keyPath, true, nil
	}
	// 生成自签
	certPEM, keyPEM, err := generate()
	if err != nil {
		return "", "", false, fmt.Errorf("生成 dummy 证书失败: %w", err)
	}
	// 先写私钥（更敏感）：确保目录存在，原子写入
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

// usable 判断现有证书文件是否可用：能读、能 PEM 解码、x509 解析、
// 有效期 >30 天、私钥 PEM 可解码。
func usable(certPath, keyPath string) bool {
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return false
	}
	// cert 必须能解 PEM 并解析 x509
	certBlock, _ := pem.Decode(certBytes)
	if certBlock == nil {
		return false
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return false
	}
	if time.Until(cert.NotAfter) < 30*24*time.Hour {
		return false
	}
	// key 必须能解 PEM 并解析 RSA
	keyBlock, _ := pem.Decode(keyBytes)
	if keyBlock == nil {
		return false
	}
	if _, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err != nil {
		return false
	}
	return true
}

func generate() (certPEM, keyPEM []byte, err error) {
	// 2048-bit RSA：nginx + 主流浏览器都接受；生成 ~0.05s。
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "ZhiweiPanel catch-all (dummy)"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(Validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// IsCA=false；无 SubjectAltId，故意让客户端识别为无效证书
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

func writeFile(path string, data []byte, mode int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, os.FileMode(mode)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
