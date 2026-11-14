package logmgr

import (
	"strings"
	"testing"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// LogRecord 是模型层的日志记录类型。
type LogRecord = model.LogRecord

// 样例行：$remote_addr - $host [$time_local] "$request" $status $body_bytes_sent $request_time "$referer" "$ua" $upstream_time
func TestParseAccessLine(t *testing.T) {
	line := `203.0.113.45 - example.com [12/Oct/2026:10:30:00 +0800] "GET /index.php HTTP/1.1" 200 1523 0.045 "https://a.com/" "Mozilla/5.0" 0.050`

	rec, err := ParseAccessLine(line)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if rec.RemoteAddr != "203.0.113.45" {
		t.Errorf("RemoteAddr = %q", rec.RemoteAddr)
	}
	if rec.Host != "example.com" {
		t.Errorf("Host = %q", rec.Host)
	}
	if rec.Method != "GET" || rec.Path != "/index.php" || rec.Protocol != "HTTP/1.1" {
		t.Errorf("请求行解析错误: %+v", rec)
	}
	if rec.Status != 200 {
		t.Errorf("Status = %d", rec.Status)
	}
	if rec.Size != 1523 {
		t.Errorf("Size = %d", rec.Size)
	}
	if rec.Referer != "https://a.com/" {
		t.Errorf("Referer = %q", rec.Referer)
	}
	if rec.UA != "Mozilla/5.0" {
		t.Errorf("UA = %q", rec.UA)
	}
	if rec.ReqTime < 0.04 || rec.ReqTime > 0.05 {
		t.Errorf("ReqTime = %f", rec.ReqTime)
	}
	expected := time.Date(2026, 10, 12, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	if !rec.Time.Equal(expected) {
		t.Errorf("时间解析错误: got %v, want %v", rec.Time, expected)
	}
}

func TestParseAccessLineEdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		isErr bool
	}{
		{"空行", "", true},
		{"非日志行", "garbage text without structure", true},
		{
			"破折号字节数",
			`1.1.1.1 - a.com [12/Oct/2026:10:30:00 +0800] "GET / HTTP/1.1" 204 - 0.001 "-" "curl" -`,
			false,
		},
		{
			"含空格的 UA",
			`1.1.1.1 - a.com [12/Oct/2026:10:30:00 +0800] "POST /api HTTP/1.1" 500 0 2.500 "-" "Mozilla/5.0 (Windows NT 10.0)" -`,
			false,
		},
	}
	for _, c := range cases {
		rec, err := ParseAccessLine(c.line)
		if c.isErr {
			if err == nil {
				t.Errorf("%s: 应当报错但解析成功: %+v", c.name, rec)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 解析失败: %v", c.name, err)
		}
	}
}

func TestAggregate(t *testing.T) {
	base := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	records := []*LogRecord{
		{RemoteAddr: "1.1.1.1", Time: base, Status: 200, Size: 100},
		{RemoteAddr: "1.1.1.1", Time: base, Status: 200, Size: 200},
		{RemoteAddr: "2.2.2.2", Time: base, Status: 404, Size: 50},
		{RemoteAddr: "3.3.3.3", Time: base, Status: 500, Size: 10},
		{RemoteAddr: "3.3.3.3", Time: base, Status: 301, Size: 0},
	}
	stat := Aggregate(records, "site1")

	if stat.Requests != 5 {
		t.Errorf("Requests = %d, want 5", stat.Requests)
	}
	if stat.Bytes != 360 {
		t.Errorf("Bytes = %d, want 360", stat.Bytes)
	}
	if stat.Status2xx != 2 {
		t.Errorf("Status2xx = %d, want 2", stat.Status2xx)
	}
	if stat.Status3xx != 1 {
		t.Errorf("Status3xx = %d", stat.Status3xx)
	}
	if stat.Status4xx != 1 {
		t.Errorf("Status4xx = %d", stat.Status4xx)
	}
	if stat.Status5xx != 1 {
		t.Errorf("Status5xx = %d", stat.Status5xx)
	}
	if stat.UniqueIPs != 3 {
		t.Errorf("UniqueIPs = %d, want 3", stat.UniqueIPs)
	}
	if stat.Date != "2026-10-12" {
		t.Errorf("Date = %q", stat.Date)
	}
}

func TestAccessLogFormatMatchesParser(t *testing.T) {
	// 校验器输出的格式串必须与解析器严格对应，否则日志永远解析不出来。
	format := AccessLogFormat()
	required := []string{
		"$remote_addr", "$host", "$time_local", "$request",
		"$status", "$body_bytes_sent", "$request_time",
		"$http_referer", "$http_user_agent", "$upstream_response_time",
	}
	for _, f := range required {
		if !contains(format, f) {
			t.Errorf("log_format 缺少字段 %s:\n%s", f, format)
		}
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
