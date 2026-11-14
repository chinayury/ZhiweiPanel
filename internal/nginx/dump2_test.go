package nginx

import (
	"strings"
	"testing"
)

// TestSubDirAndStaticCacheNotNested 回归测试：
// 子目录绑定与静态缓存 location 都必须是 server 的直接子块，
// nginx 不允许 location 嵌套，写进 location / 内部会导致 nginx -t 失败。
func TestSubDirAndStaticCacheNotNested(t *testing.T) {
	s := fullFeatureSite()
	res, err := Render(s, RenderOptions{SiteID: s.ID, DefaultPHPSocket: "/run/php/php-8.2-fpm.sock"})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 用真实的 http 级配置（含 log_format 定义）校验
	v := Validate(res.SiteConf, testHTTPConf())
	if !v.OK() {
		t.Fatalf("配置校验失败:\n%s", v.Summary())
	}

	// 校验器必须确认没有任何嵌套
	if strings.Contains(v.Summary(), "嵌套") {
		t.Errorf("检测到嵌套块:\n%s", v.Summary())
	}
}

// testHTTPConf 构造与生产环境一致的 http 级配置。
func testHTTPConf() string {
	return "log_format " + LogFormatName + " '$remote_addr $host $status';\n"
}

// TestExpiresHasNoUnitSuffix 回归测试：expires 指令不能带 s/d/w 等单位后缀。
func TestExpiresHasNoUnitSuffix(t *testing.T) {
	s := fullFeatureSite()
	res, _ := Render(s, RenderOptions{SiteID: s.ID})

	for _, line := range strings.Split(res.SiteConf, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "expires ") {
			continue
		}
		val := strings.TrimSuffix(strings.TrimPrefix(trimmed, "expires "), ";")
		if strings.HasSuffix(val, "s") || strings.HasSuffix(val, "m") ||
			strings.HasSuffix(val, "h") || strings.HasSuffix(val, "d") ||
			strings.HasSuffix(val, "w") {
			t.Errorf("expires 带了单位后缀，nginx 会报语法错误: %q", trimmed)
		}
	}
}

// TestAccessLogUsesFormatName 回归测试：
// access_log 的第二个参数必须是 http 级定义的 log_format 名称。
// 写成 $host 这类变量会让真实 nginx 报 "unknown log format"，
// 该错误只有跑真实 nginx 才能发现，纯 Go 校验器已加入对应规则。
func TestAccessLogUsesFormatName(t *testing.T) {
	s := fullFeatureSite()
	s.Logs.AccessLog = true
	res, _ := Render(s, RenderOptions{SiteID: s.ID})

	if strings.Contains(res.SiteConf, "access_log /www/wwwroot/a.com/logs/access.log $") {
		t.Errorf("access_log 第二个参数误用了变量:\n%s", res.SiteConf)
	}
	if !strings.Contains(res.SiteConf, "access_log /www/wwwroot/a.com/logs/access.log zhiweipanel_access;") {
		t.Errorf("access_log 未引用面板日志格式:\n%s", res.SiteConf)
	}

	// http 级必须定义该格式
	httpConf := "log_format zhiweipanel_access '$remote_addr $status';\n"
	v := Validate(res.SiteConf, httpConf)
	if !v.OK() {
		t.Errorf("校验器误报:\n%s", v.Summary())
	}
}

// TestValidatorCatchesUnknownLogFormat 校验器必须能抓出未定义的格式引用。
func TestValidatorCatchesUnknownLogFormat(t *testing.T) {
	bad := "server {\n    access_log /var/log/a.log $host;\n}\n"
	v := Validate(bad, "")
	if v.OK() {
		t.Fatalf("校验器未抓到 access_log 误用变量")
	}
	if !strings.Contains(v.Summary(), "unknown log format") {
		t.Errorf("错误信息不明确: %s", v.Summary())
	}

	bad2 := "server {\n    access_log /var/log/a.log my_custom;\n}\n"
	v2 := Validate(bad2, "")
	if v2.OK() || !strings.Contains(v2.Summary(), "未定义") {
		t.Errorf("校验器未抓到未定义的 log_format: %s", v2.Summary())
	}
}

// TestSingleScriptFilename 回归测试：fastcgi_param SCRIPT_FILENAME 只能出现一次。
func TestSingleScriptFilename(t *testing.T) {
	s := fullFeatureSite()
	res, _ := Render(s, RenderOptions{SiteID: s.ID})

	// 每个 location 内只应有一条；但整份配置里主站 + 子目录各一条是正常的。
	// 关键是不能在同一个 location 块内重复出现。
	for _, block := range strings.Split(res.SiteConf, "location ") {
		count := strings.Count(block, "SCRIPT_FILENAME")
		if count > 1 {
			idx := strings.Index(block, "}")
			snippet := block
			if idx > 0 {
				snippet = block[:idx]
			}
			t.Errorf("同一 location 内出现 %d 次 SCRIPT_FILENAME:\n%s", count, snippet)
		}
	}
}
