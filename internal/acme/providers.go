package acme

import (
	"fmt"
	"strings"
	"sync"

	"github.com/go-acme/lego/v4/challenge"
)

// dnsProviders DNS-01 提供商注册表。
var dnsProviders = map[string]func(map[string]string) (challenge.Provider, error){}

// registryMu 保护注册表并发访问。
var registryMu sync.Mutex

// DNSProviderNames 返回已注册的 DNS 提供商名称。
func DNSProviderNames() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]string, 0, len(dnsProviders))
	for name := range dnsProviders {
		out = append(out, name)
	}
	return out
}

// RegisterDNSProvider 注册 DNS-01 提供商工厂。
func RegisterDNSProvider(name string, fn func(map[string]string) (challenge.Provider, error)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	dnsProviders[name] = fn
}

// ResolveDNSProvider 解析 DNS-01 提供商。
func ResolveDNSProvider(name string, cfg map[string]string) (challenge.Provider, error) {
	registryMu.Lock()
	fn, ok := dnsProviders[name]
	registryMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("未注册的 DNS 提供商: %s（当前支持: %s）",
			name, strings.Join(DNSProviderNames(), ", "))
	}
	return fn(cfg)
}
