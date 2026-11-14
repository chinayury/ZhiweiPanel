package model

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSiteUnmarshalCertEmptyTimeStrings 回归测试（2026-11-01 线上故障）：
// 未绑定证书的站点，前端详情页的 mergeDefaults 曾回填
// {not_before:"", not_after:""} 的默认 cert 分组。空字符串无法解码进
// time.Time，导致这类站点的每次 PUT 都在 bindSite 解码阶段 400——详情页
// 永远无法保存，且与用户所改字段无关（线上站点连续 9 次 PUT 全部 400）。
//
// 本测试锁死两条防线：
//  1. 后端 CertRef.UnmarshalJSON 必须容忍空时间字符串（归一为零值）；
//  2. 整个 Site JSON（bindSite 的解码目标）携带该形状时必须解码成功。
func TestSiteUnmarshalCertEmptyTimeStrings(t *testing.T) {
	body := `{
		"id":"9e47990ad9a08aacd12fcb8ffe90bd85",
		"name":"SuperArmorWB",
		"status":"running",
		"domains":["licapp.example.com"],
		"cert":{"cert_id":"","domains":[],"not_before":"","not_after":"","issuer":"","source":""}
	}`
	var s Site
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("站点 JSON 解码失败（旧版前端 payload 必须被容忍）: %v", err)
	}
	if s.Cert == nil {
		t.Fatal("cert 分组应解码为非 nil（空时间字符串不等于没有分组）")
	}
	if !s.Cert.NotBefore.IsZero() || !s.Cert.NotAfter.IsZero() {
		t.Errorf("空时间字符串应归一为零值时间, got before=%v after=%v",
			s.Cert.NotBefore, s.Cert.NotAfter)
	}
	if s.Cert.DaysLeft() != 0 || s.Cert.IsExpiring(30) {
		t.Errorf("零值时间的证书应视为无到期信息: days=%d expiring=%v",
			s.Cert.DaysLeft(), s.Cert.IsExpiring(30))
	}
}

// TestSiteUnmarshalNoCertGroup 未绑定证书的站点在 JSON 里整个 cert 分组
// 缺失（新版前端行为）时必须解码成功，Cert 保持 nil。
func TestSiteUnmarshalNoCertGroup(t *testing.T) {
	var s Site
	if err := json.Unmarshal([]byte(`{"id":"x","name":"n","domains":["a.com"]}`), &s); err != nil {
		t.Fatalf("缺少 cert 分组的站点 JSON 解码失败: %v", err)
	}
	if s.Cert != nil {
		t.Error("没有 cert 分组时 Site.Cert 应为 nil")
	}
}

// TestCertRefUnmarshalJSON 覆盖 CertRef 自定义解码的各分支：
// 合法时间照常解析、空串/缺失/null 归一为零值、脏值仍然报错。
func TestCertRefUnmarshalJSON(t *testing.T) {
	t.Run("合法RFC3339含纳秒", func(t *testing.T) {
		var c CertRef
		// Go time.Time 的默认序列化形态（RFC3339Nano）必须能原样解回。
		if err := json.Unmarshal([]byte(
			`{"cert_id":"abc","domains":["a.com"],"not_before":"2026-10-16T00:00:00Z","not_after":"2027-01-14T12:34:56.789012345Z"}`,
		), &c); err != nil {
			t.Fatalf("合法时间解码失败: %v", err)
		}
		if c.CertID != "abc" || len(c.Domains) != 1 || c.Domains[0] != "a.com" {
			t.Errorf("标量/列表字段解码不正确: %+v", c)
		}
		if want := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC); !c.NotBefore.Equal(want) {
			t.Errorf("not_before = %v, want %v", c.NotBefore, want)
		}
		if want := time.Date(2027, 1, 14, 12, 34, 56, 789012345, time.UTC); !c.NotAfter.Equal(want) {
			t.Errorf("not_after = %v, want %v", c.NotAfter, want)
		}
	})

	t.Run("空字符串归一为零值", func(t *testing.T) {
		var c CertRef
		if err := json.Unmarshal([]byte(
			`{"cert_id":"","domains":[],"not_before":"","not_after":"","issuer":"","source":""}`,
		), &c); err != nil {
			t.Fatalf("空字符串时间应被容忍: %v", err)
		}
		if !c.NotBefore.IsZero() || !c.NotAfter.IsZero() {
			t.Errorf("空字符串应归一为零值, got %v / %v", c.NotBefore, c.NotAfter)
		}
	})

	t.Run("字段缺失与null", func(t *testing.T) {
		var c CertRef
		if err := json.Unmarshal([]byte(`{"cert_id":"y"}`), &c); err != nil {
			t.Fatalf("字段缺失应 tolerated: %v", err)
		}
		if !c.NotBefore.IsZero() || !c.NotAfter.IsZero() {
			t.Error("缺失的时间字段应为零值")
		}
		var c2 CertRef
		if err := json.Unmarshal([]byte(`null`), &c2); err != nil {
			t.Fatalf("null 应被容忍: %v", err)
		}
	})

	t.Run("非空脏值仍然报错", func(t *testing.T) {
		var c CertRef
		if err := json.Unmarshal([]byte(`{"not_before":"not-a-time"}`), &c); err == nil {
			t.Fatal("非空但非法的时间值应报错（不放松对真实脏数据的拦截）")
		}
		var c2 CertRef
		if err := json.Unmarshal([]byte(`{"not_after":"2026-13-45T99:99:99Z"}`), &c2); err == nil {
			t.Fatal("越界的合法格式时间值应报错")
		}
	})

	t.Run("marshal后unmarshal往返一致", func(t *testing.T) {
		orig := CertRef{
			CertID:    "r1",
			Domains:   []string{"a.com", "b.com"},
			NotBefore: time.Date(2026, 10, 16, 8, 30, 0, 0, time.UTC),
			NotAfter:  time.Date(2027, 1, 14, 8, 30, 0, 0, time.UTC),
			Issuer:    "R3",
			Source:    "acme",
		}
		blob, err := json.Marshal(orig)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back CertRef
		if err := json.Unmarshal(blob, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !back.NotBefore.Equal(orig.NotBefore) || !back.NotAfter.Equal(orig.NotAfter) {
			t.Errorf("往返后时间不一致: %v / %v", back.NotBefore, back.NotAfter)
		}
		if back.CertID != orig.CertID || back.Issuer != orig.Issuer || back.Source != orig.Source {
			t.Errorf("往返后标量字段不一致: %+v", back)
		}
	})
}
