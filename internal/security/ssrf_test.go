package security

import (
	"net/http"
	"testing"
)

func TestCheckExternalURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"loopback 127.0.0.1", "http://127.0.0.1:3306/", true},
		{"private 10.x", "http://10.0.0.1/", true},
		{"private 192.168.x", "http://192.168.1.1/", true},
		{"private 172.16.x", "http://172.16.0.1/", true},
		{"link-local 元数据", "http://169.254.169.254/latest/meta-data/", true},
		{"ipv6 loopback", "http://[::1]/", true},
		{"localhost 域名解析", "http://localhost/", true},
		{"非 http 协议", "ftp://example.com/", true},
		{"空 host", "http:///", true},
		{"公网域名", "http://example.com/", false},
		{"公网 IP", "http://8.8.8.8/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckExternalURL(tt.url)
			if tt.wantErr && err == nil {
				t.Fatalf("CheckExternalURL(%q) 期望拒绝，但放行了", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("CheckExternalURL(%q) 期望放行，但拒绝了: %v", tt.url, err)
			}
		})
	}
}

func TestNoRedirectToInternal(t *testing.T) {
	internalReq, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:6379/", nil)
	if err := NoRedirectToInternal(internalReq, nil); err == nil {
		t.Fatal("内网重定向目标应被拒绝")
	}

	externalReq, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err := NoRedirectToInternal(externalReq, nil); err != nil {
		t.Fatalf("公网重定向目标应放行: %v", err)
	}

	// 重定向次数超限应拒绝
	var via []*http.Request
	for i := 0; i < 10; i++ {
		via = append(via, externalReq)
	}
	if err := NoRedirectToInternal(externalReq, via); err == nil {
		t.Fatal("重定向次数超限应被拒绝")
	}
}
