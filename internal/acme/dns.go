package acme

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/dnspod"
	"github.com/go-acme/lego/v4/providers/dns/tencentcloud"
)

// providerSpec 描述一个 DNS-01 提供商所需的凭据字段。
type providerSpec struct {
	// Name 展示名（中文，直接给前端下拉用）。
	Name string
	// Fields 凭据字段名 -> 人类可读说明。键名是**裸键**（不带厂商前缀），
	// 由 buildProvider 负责映射到 lego 的强类型 Config 字段。
	Fields map[string]string
	// build 用 cfg 构造 provider。
	build func(cfg map[string]string) (challenge.Provider, error)
}

// providerSpecs 内置 DNS-01 提供商。
//
// 只注册这四个：覆盖国内主流云厂商（阿里云 / DNSPod / 腾讯云）与海外
// Cloudflare，且都是纯凭据、无需额外配置文件。
//
// 刻意不注册 lego 的 exec / httpreq provider：
//   - exec 可指定任意可执行文件 = 直接 RCE
//   - httpreq 可指定任意 endpoint = SSRF
var providerSpecs = map[string]providerSpec{
	"alidns": {
		Name: "阿里云 DNS",
		Fields: map[string]string{
			"API_KEY":    "阿里云 AccessKey ID",
			"SECRET_KEY": "阿里云 AccessKey Secret",
		},
		build: func(cfg map[string]string) (challenge.Provider, error) {
			c := alidns.NewDefaultConfig()
			c.APIKey = cfg["API_KEY"]
			c.SecretKey = cfg["SECRET_KEY"]
			c.RegionID = orDefault(cfg["REGION_ID"], "cn-hangzhou")
			return alidns.NewDNSProviderConfig(c)
		},
	},
	"dnspod": {
		Name: "DNSPod（腾讯云）",
		Fields: map[string]string{
			"API_KEY": "DNSPod 凭据，格式 ID,Token（逗号分隔）",
		},
		build: func(cfg map[string]string) (challenge.Provider, error) {
			// lego 的 dnspod 要求 api_key 形如 "12345,abcdef"
			c := dnspod.NewDefaultConfig()
			c.LoginToken = cfg["API_KEY"]
			return dnspod.NewDNSProviderConfig(c)
		},
	},
	"tencentcloud": {
		Name: "腾讯云 DNS",
		Fields: map[string]string{
			"SECRET_ID":  "腾讯云 SecretId",
			"SECRET_KEY": "腾讯云 SecretKey",
		},
		build: func(cfg map[string]string) (challenge.Provider, error) {
			c := tencentcloud.NewDefaultConfig()
			c.SecretID = cfg["SECRET_ID"]
			c.SecretKey = cfg["SECRET_KEY"]
			return tencentcloud.NewDNSProviderConfig(c)
		},
	},
	"cloudflare": {
		Name: "Cloudflare",
		Fields: map[string]string{
			"DNS_API_TOKEN": "Cloudflare API Token（需 Zone.DNS 编辑权限）",
		},
		build: func(cfg map[string]string) (challenge.Provider, error) {
			c := cloudflare.NewDefaultConfig()
			c.AuthToken = cfg["DNS_API_TOKEN"]
			// 兼容旧版 AuthKey 方式
			if c.AuthToken == "" && cfg["AUTH_KEY"] != "" {
				c.AuthKey = cfg["AUTH_KEY"]
				c.AuthEmail = cfg["AUTH_EMAIL"]
			}
			return cloudflare.NewDNSProviderConfig(c)
		},
	},
}

// orDefault 返回 v，为空时返回 def。
func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// init 注册全部内置 DNS-01 提供商。
func init() {
	for key, spec := range providerSpecs {
		RegisterDNSProvider(key, spec.build)
	}
}

// DNSProviderSpec 返回某个 provider 的凭据字段说明。
func DNSProviderSpec(name string) (providerSpec, bool) {
	spec, ok := providerSpecs[name]
	return spec, ok
}

// DNSProviderInfos 返回全部 provider 的展示信息（含字段说明），供前端渲染表单。
func DNSProviderInfos() []map[string]any {
	out := make([]map[string]any, 0, len(providerSpecs))
	for key, spec := range providerSpecs {
		fields := make([]map[string]string, 0, len(spec.Fields))
		for fk, fv := range spec.Fields {
			fields = append(fields, map[string]string{"key": fk, "label": fv})
		}
		out = append(out, map[string]any{
			"key":    key,
			"name":   spec.Name,
			"fields": fields,
		})
	}
	return out
}

// ValidateDNSConfig 校验 DNS 凭据是否满足该 provider 的必填项。
// 只检查必填键存在且非空，不验证凭据真伪（那要真打一次 DNS API）。
func ValidateDNSConfig(provider string, cfg map[string]string) error {
	spec, ok := providerSpecs[provider]
	if !ok {
		return fmt.Errorf("未注册的 DNS 提供商: %s", provider)
	}
	for key := range spec.Fields {
		if strings.TrimSpace(cfg[key]) == "" {
			return fmt.Errorf("DNS 提供商 %s 缺少必填凭据 %s", spec.Name, key)
		}
	}
	return nil
}

// atoiDefault 解析整数，失败或为空时返回 def。
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}
