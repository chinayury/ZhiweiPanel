// Package acme 封装 Let's Encrypt 证书申请与续签。
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
)

// Manager ACME 管理器。
type Manager struct {
	db      *store.DB
	certDir string
}

// NewManager 创建 ACME 管理器。
func NewManager(db *store.DB, certDir string) *Manager {
	return &Manager{db: db, certDir: certDir}
}

// CA 类型。
const (
	CAProduction = "letsencrypt"
	CAStaging    = "letsencrypt_staging"
)

// IssueRequest 签发请求。
type IssueRequest struct {
	Domains   []string
	SiteID    string
	AccountID string
	// Method "http"（HTTP-01）或 "dns"（DNS-01）。
	Method string
	// WebrootRoots 域名 → 站点根目录映射，HTTP-01 验证时使用。
	WebrootRoots map[string]string
	// WebrootPath 单根目录快捷方式（多域名时统一使用）。
	WebrootPath string
	// DNSProvider DNS-01 提供商名称。
	DNSProvider string
	DNSConfig   map[string]string
	AutoRenew   bool
}

// IssueResult 签发结果。
type IssueResult struct {
	Cert *model.Cert
}

// acmeUser 实现 lego 的 registration.User 接口。
type acmeUser struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// Issue 申请证书。
func (m *Manager) Issue(ctx context.Context, req IssueRequest) (*IssueResult, error) {
	if len(req.Domains) == 0 {
		return nil, model.ErrBadRequest
	}
	for _, d := range req.Domains {
		if strings.TrimSpace(d) == "" {
			return nil, model.ErrInvalidDomain
		}
	}

	account, err := m.resolveAccount(req.AccountID)
	if err != nil {
		return nil, err
	}
	privKey, err := m.loadOrCreateKey(account)
	if err != nil {
		return nil, err
	}

	user := &acmeUser{email: account.Email, key: privKey, reg: &registration.Resource{}}
	config := lego.NewConfig(user)
	config.CADirURL = cadirURL(account.CA)
	config.Certificate.KeyType = keyTypeOf(account.KeyType)

	client, err := lego.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("创建 ACME 客户端失败: %w", err)
	}

	// 注册账户（已存在时忽略）
	if _, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true}); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "already") {
			return nil, fmt.Errorf("ACME 账户注册失败: %w", err)
		}
	}

	// 配置挑战方式
	switch req.Method {
	case "dns":
		if req.DNSProvider == "" {
			return nil, fmt.Errorf("DNS-01 方式必须指定 DNS 提供商")
		}
		provider, err := ResolveDNSProvider(req.DNSProvider, req.DNSConfig)
		if err != nil {
			return nil, err
		}
		if err := client.Challenge.SetDNS01Provider(provider); err != nil {
			return nil, fmt.Errorf("配置 DNS-01 失败: %w", err)
		}
	default:
		roots := req.WebrootRoots
		if len(roots) == 0 && req.WebrootPath != "" {
			roots = map[string]string{}
			for _, d := range req.Domains {
				roots[d] = req.WebrootPath
			}
		}
		if len(roots) == 0 {
			return nil, fmt.Errorf("HTTP-01 方式必须指定站点根目录")
		}
		// 把挑战文件写进站点根目录，由 nginx 直接提供，无需占用 80 端口。
		if err := client.Challenge.SetHTTP01Provider(NewWebrootProvider(roots)); err != nil {
			return nil, fmt.Errorf("配置 HTTP-01 失败: %w", err)
		}
	}

	resource, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: req.Domains,
		Bundle:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("证书签发失败: %w", err)
	}

	cert, err := m.saveCertificate(resource, req, "")
	if err != nil {
		return nil, err
	}
	return &IssueResult{Cert: cert}, nil
}

// Renew 续签证书。renewRoots 为域名 → 站点根目录映射。
func (m *Manager) Renew(ctx context.Context, certID string, renewRoots map[string]string) (*model.Cert, error) {
	existing, err := m.db.GetCert(certID)
	if err != nil {
		return nil, err
	}

	account, err := m.resolveAccount(existing.ACMEAccount)
	if err != nil {
		return nil, err
	}
	privKey, err := m.loadOrCreateKey(account)
	if err != nil {
		return nil, err
	}

	user := &acmeUser{email: account.Email, key: privKey, reg: &registration.Resource{}}
	config := lego.NewConfig(user)
	config.CADirURL = cadirURL(account.CA)
	config.Certificate.KeyType = keyTypeOf(account.KeyType)

	client, err := lego.NewClient(config)
	if err != nil {
		return nil, err
	}
	if _, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true}); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "already") {
			return nil, err
		}
	}

	// 续签同样需要挑战提供者，否则必然失败。
	// 必须按证书当初的验证方式重建：DNS-01 证书（含通配符）在 HTTP-01 下
	// 根本无法续期，反之亦然。早期这里无条件走 HTTP-01，
	// 导致通配符证书到期后续签必败。
	req := IssueRequest{
		Domains:      existing.Domains,
		AccountID:    existing.ACMEAccount,
		Method:       existing.Method,
		DNSProvider:  existing.DNSProvider,
		DNSConfig:    existing.DNSConfig,
		WebrootRoots: renewRoots,
		AutoRenew:    existing.AutoRenew,
	}
	if req.Method == "" {
		req.Method = "http"
	}

	switch req.Method {
	case "dns":
		if req.DNSProvider == "" {
			return nil, fmt.Errorf("证书 %s 是 DNS-01 签发但未记录 DNS 提供商，无法续签", existing.Name)
		}
		if len(req.DNSConfig) == 0 {
			return nil, fmt.Errorf("证书 %s 缺少 DNS 凭据，无法重建 %s provider", existing.Name, req.DNSProvider)
		}
		provider, perr := ResolveDNSProvider(req.DNSProvider, req.DNSConfig)
		if perr != nil {
			return nil, perr
		}
		if err := client.Challenge.SetDNS01Provider(provider); err != nil {
			return nil, fmt.Errorf("配置 DNS-01 失败: %w", err)
		}
	default:
		if len(renewRoots) == 0 {
			return nil, fmt.Errorf("HTTP-01 续签必须提供站点根目录（挑战文件写入点）")
		}
		if err := client.Challenge.SetHTTP01Provider(NewWebrootProvider(renewRoots)); err != nil {
			return nil, fmt.Errorf("配置 HTTP-01 失败: %w", err)
		}
	}

	resource, err := client.Certificate.Renew(certificate.Resource{
		Domain:      existing.Domains[0],
		Certificate: mustRead(existing.CertFile),
		PrivateKey:  mustRead(existing.KeyFile),
	}, true, false, "")
	if err != nil {
		return nil, fmt.Errorf("证书续签失败: %w", err)
	}

	// 复用原证书 ID：站点绑定不用改（文件路径不变，内容被覆盖），
	// 也不会在 certs 表里堆出永远"到期待续签"的重复记录。
	return m.saveCertificate(resource, req, existing.ID)
}

// firstLeafCert 从 PEM 证书链里解析出第一张证书（叶子证书）。
// ACME 返回的 Certificate 字段是完整链，不能直接喂 x509.ParseCertificate。
func firstLeafCert(pemBytes []byte) *x509.Certificate {
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if c, err := x509.ParseCertificate(block.Bytes); err == nil {
			return c
		}
	}
}

// saveCertificate 落盘并写入数据库。
//
// certID 为空 = 首次签发，生成新 ID；续签时传入原证书 ID。
//
// **续签必须复用原 ID**，这同时解决两个真实故障：
//
//  1. 站点绑定不用改。site.Cert.CertID 与 site.SSL.CertFile/KeyFile
//     指向的就是同一目录下的同一组文件，续签只是把内容覆盖掉，
//     nginx 配置一个字都不用改，reload 之后立刻用上新证书。
//     早期版本的续签产出的是全新 ID、全新路径，而手动续签的 API 路径
//     又没有重绑站点 —— 于是接口返回 200、新证书也进了库，
//     nginx 却还在 serve 旧证书；等旧证书自然过期，站点 HTTPS 直接挂掉，
//     而用户完全看不出原因（"续签"明明是成功的）。
//
//  2. 不会在 certs 表里堆重复记录。早期版本每次续签都 INSERT 一条新行，
//     旧行的 auto_renew 仍是 1、not_after 仍是旧值（早已过期）
//     → 永远"到期待续签" → 每 12 小时重复签发一次
//     → 撞上 Let's Encrypt "同一域名集合每周 5 张"的重复签发限额
//     → 之后证书再也续不出来，站点 HTTPS 下线且一周内无法恢复；
//     同时证书目录里堆出一堆孤儿目录，旧私钥永久残留。
func (m *Manager) saveCertificate(resource *certificate.Resource, req IssueRequest, certID string) (*model.Cert, error) {
	if certID == "" {
		certID = store.NewID()
	}
	id := certID
	dir := filepath.Join(m.certDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")

	if err := os.WriteFile(certPath, resource.Certificate, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, resource.PrivateKey, 0o600); err != nil {
		return nil, err
	}

	var notBefore, notAfter time.Time
	issuer := ""
	// 注意：lego 的 resource.Certificate 是 PEM 证书链（fullchain），
	// 而 x509.ParseCertificate 要的是 DER。直接喂 PEM 会报错，
	// 早期版本因此把有效期和颁发者全留成零值——前端随后算出
	// "已过期 74 万天" 这种数字。必须先 pem.Decode。
	if leaf := firstLeafCert(resource.Certificate); leaf != nil {
		notBefore = leaf.NotBefore
		notAfter = leaf.NotAfter
		issuer = firstNonEmpty(leaf.Issuer.Organization)
		if issuer == "" {
			issuer = leaf.Issuer.CommonName
		}
	}

	name := req.Domains[0]
	if len(req.Domains) > 1 {
		name = fmt.Sprintf("%s 等 %d 个域名", req.Domains[0], len(req.Domains))
	}

	c := &model.Cert{
		ID:          id,
		Name:        name,
		Domains:     req.Domains,
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		Issuer:      issuer,
		Source:      "letsencrypt",
		CertFile:    certPath,
		KeyFile:     keyPath,
		AutoRenew:   req.AutoRenew,
		ACMEAccount: req.AccountID,
		// 持久化验证方式与 DNS 凭据，续签时才能重建同样的 provider。
		// 缺失会导致通配符证书到期后无法自动续期。
		DNSProvider: req.DNSProvider,
		DNSConfig:   req.DNSConfig,
		Method:      req.Method,
	}
	if err := m.db.SaveCert(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Import 导入外部证书。
func (m *Manager) Import(name string, domains []string, certPEM, keyPEM []byte) (*model.Cert, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("证书 PEM 格式错误")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("证书解析失败: %w", err)
	}

	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("私钥 PEM 格式错误")
	}
	if _, err := x509.ParsePKCS8PrivateKey(kb.Bytes); err != nil {
		if _, err2 := x509.ParsePKCS1PrivateKey(kb.Bytes); err2 != nil {
			return nil, fmt.Errorf("私钥解析失败: %w", err)
		}
	}

	id := store.NewID()
	dir := filepath.Join(m.certDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}

	issuer := firstNonEmpty(leaf.Issuer.Organization)
	if issuer == "" {
		issuer = leaf.Issuer.CommonName
	}
	c := &model.Cert{
		ID:        id,
		Name:      name,
		Domains:   domains,
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
		Issuer:    issuer,
		Source:    "imported",
		CertFile:  certPath,
		KeyFile:   keyPath,
	}
	if err := m.db.SaveCert(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Revoke 吊销证书。
func (m *Manager) Revoke(certID string) error {
	c, err := m.db.GetCert(certID)
	if err != nil {
		return err
	}
	if c.Source != "letsencrypt" {
		return fmt.Errorf("非本面板签发的证书无法吊销")
	}
	account, err := m.resolveAccount(c.ACMEAccount)
	if err != nil {
		return err
	}
	privKey, err := m.loadOrCreateKey(account)
	if err != nil {
		return err
	}

	user := &acmeUser{email: account.Email, key: privKey, reg: &registration.Resource{}}
	config := lego.NewConfig(user)
	config.CADirURL = cadirURL(account.CA)
	config.Certificate.KeyType = keyTypeOf(account.KeyType)

	client, err := lego.NewClient(config)
	if err != nil {
		return err
	}
	if _, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true}); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "already") {
			return err
		}
	}
	if err := client.Certificate.Revoke(mustRead(c.CertFile)); err != nil {
		return fmt.Errorf("吊销证书失败: %w", err)
	}
	return m.db.DeleteCert(certID)
}

// resolveAccount 取指定账户，未指定则取第一个。
func (m *Manager) resolveAccount(accountID string) (*model.ACMEAccount, error) {
	if accountID != "" {
		return m.db.GetACMEAccount(accountID)
	}
	accounts, err := m.db.ListACMEAccounts()
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("尚未创建 ACME 账户，请先在证书页面添加")
	}
	return accounts[0], nil
}

// loadOrCreateKey 加载或生成账户私钥。
func (m *Manager) loadOrCreateKey(account *model.ACMEAccount) (crypto.PrivateKey, error) {
	path := account.KeyPath
	if path == "" {
		path = filepath.Join(m.certDir, "_accounts", account.ID+"_key.pem")
	}
	if data, err := os.ReadFile(path); err == nil {
		if key, err := certcrypto.ParsePEMPrivateKey(data); err == nil {
			return key, nil
		}
	}

	var priv crypto.PrivateKey
	var err error
	switch account.KeyType {
	case "rsa2048":
		priv, err = rsa.GenerateKey(cryptorand.Reader, 2048)
	default:
		priv, err = ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := encodeKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, err
	}
	account.KeyPath = path
	if err := m.db.SaveACMEAccount(account); err != nil {
		return nil, err
	}
	return priv, nil
}

// CreateAccount 创建 ACME 账户。
func (m *Manager) CreateAccount(ctx context.Context, email, ca, keyType string) (*model.ACMEAccount, error) {
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("邮箱格式非法")
	}
	if ca != CAProduction && ca != CAStaging {
		return nil, fmt.Errorf("不支持的 CA: %s", ca)
	}
	if keyType == "" {
		keyType = "ec256"
	}
	acc := &model.ACMEAccount{
		ID:      store.NewID(),
		Email:   email,
		CA:      ca,
		KeyType: keyType,
	}
	if err := m.db.SaveACMEAccount(acc); err != nil {
		return nil, err
	}
	if _, err := m.loadOrCreateKey(acc); err != nil {
		return nil, err
	}
	return acc, nil
}

// AutoRenewDue 到期需要续签的证书（剩余天数 <= renewBeforeDays）。
func (m *Manager) AutoRenewDue(renewBeforeDays int) ([]*model.Cert, error) {
	certs, err := m.db.ListCerts()
	if err != nil {
		return nil, err
	}
	var due []*model.Cert
	for _, c := range certs {
		if !c.AutoRenew || c.Source != "letsencrypt" {
			continue
		}
		if c.DaysLeft() <= renewBeforeDays {
			due = append(due, c)
		}
	}
	return due, nil
}

func mustRead(path string) []byte {
	b, _ := os.ReadFile(path)
	return b
}

// firstNonEmpty 返回切片中第一个非空字符串。
func firstNonEmpty(items []string) string {
	for _, s := range items {
		if s != "" {
			return s
		}
	}
	return ""
}

func cadirURL(ca string) string {
	if ca == CAStaging {
		return lego.LEDirectoryStaging
	}
	return lego.LEDirectoryProduction
}

func keyTypeOf(t string) certcrypto.KeyType {
	if t == "rsa2048" {
		return certcrypto.RSA2048
	}
	return certcrypto.EC256
}

// encodeKey 导出 PEM 私钥。
func encodeKey(priv crypto.PrivateKey) ([]byte, error) {
	switch k := priv.(type) {
	case *ecdsa.PrivateKey:
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
	case *rsa.PrivateKey:
		return pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k),
		}), nil
	default:
		return nil, fmt.Errorf("不支持的密钥类型 %T", priv)
	}
}

// GenerateCSR 生成证书签名请求（供外部 CA 使用）。
func GenerateCSR(domains []string, key crypto.PrivateKey) ([]byte, error) {
	tpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domains[0]},
		DNSNames: domains,
	}
	return x509.CreateCertificateRequest(cryptorand.Reader, tpl, key)
}

// 确保引用 challenge 包。
var _ challenge.Provider = (*WebrootProvider)(nil)
