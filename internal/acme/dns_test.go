package acme

import "testing"

// TestDNSProvidersRegistered 锁死 DNS-01 空注册表缺陷：
// providers.go 早期只有注册机制、从未注册实现，导致 DNS-01 永远报
// "未注册的 DNS 提供商"，通配符证书因此无法签发。
func TestDNSProvidersRegistered(t *testing.T) {
	names := DNSProviderNames()
	if len(names) == 0 {
		t.Fatal("没有任何 DNS provider 被注册")
	}
	want := map[string]bool{
		"alidns":       false,
		"dnspod":       false,
		"tencentcloud": false,
		"cloudflare":   false,
	}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("DNS provider %q 未注册", k)
		}
	}
}

// TestValidateDNSConfig 必填凭据缺失必须明确报错。
func TestValidateDNSConfig(t *testing.T) {
	// 未注册的 provider
	if err := ValidateDNSConfig("nope", nil); err == nil {
		t.Error("未注册的 provider 应报错")
	}
	// 缺少必填凭据
	if err := ValidateDNSConfig("alidns", map[string]string{}); err == nil {
		t.Error("缺少凭据应报错")
	}
	// 凭据齐全
	ok := map[string]string{"API_KEY": "ak", "SECRET_KEY": "sk"}
	if err := ValidateDNSConfig("alidns", ok); err != nil {
		t.Errorf("凭据齐全不应报错: %v", err)
	}
}

// TestDNSProviderInfosShape 前端依赖 fields 渲染表单，结构必须稳定。
func TestDNSProviderInfosShape(t *testing.T) {
	infos := DNSProviderInfos()
	if len(infos) == 0 {
		t.Fatal("DNSProviderInfos 为空")
	}
	for _, info := range infos {
		for _, key := range []string{"key", "name", "fields"} {
			if _, ok := info[key]; !ok {
				t.Errorf("provider 信息缺少字段 %q: %v", key, info)
			}
		}
		fields, ok := info["fields"].([]map[string]string)
		if !ok || len(fields) == 0 {
			t.Errorf("provider %v 的 fields 应为非空数组", info["key"])
			continue
		}
		for _, f := range fields {
			if f["key"] == "" || f["label"] == "" {
				t.Errorf("字段说明不完整: %v", f)
			}
		}
	}
}

// TestDNSProviderBuildable 每个已注册 provider 都要能真正构造出实例
// （凭据可以是假值——构造阶段不联网，只在 Solve 时才调用 API）。
func TestDNSProviderBuildable(t *testing.T) {
	fakeCreds := map[string]string{
		"API_KEY":       "fake",
		"SECRET_KEY":    "fake",
		"SECRET_ID":     "fake",
		"DNS_API_TOKEN": "fake",
	}
	for _, name := range DNSProviderNames() {
		if _, err := ResolveDNSProvider(name, fakeCreds); err != nil {
			t.Errorf("provider %q 构造失败: %v", name, err)
		}
	}
}
