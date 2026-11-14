package nginx

import (
	"fmt"
	"regexp"
	"strings"
)

// Validator 校验生成的 nginx 配置是否符合语法规则。
//
// 之所以要有它：nginx -t 需要真实 nginx 环境，而在没有 nginx 的
// 开发机上无法验证。这里实现一组真实生效的 nginx 规则检查，
// 覆盖最常见的"生成即失败"原因。
type Validator struct {
	errors   []string
	warnings []string
}

// Validate 校验配置文本。
func Validate(siteConf, httpConf string) *Validator {
	// 注释里的指令名会造成大量误报，先剥离整行注释再校验。
	// 保留原始内容用于返回，不影响调用方。
	siteConf = stripComments(siteConf)
	httpConf = stripComments(httpConf)

	v := &Validator{}
	v.checkBraces(siteConf, "站点配置")
	v.checkBraces(httpConf, "http 级配置")
	v.checkDirectives(siteConf)
	v.checkHttpOnly(httpConf)
	v.checkSemicolons(siteConf)
	v.checkNoNestedLocation(siteConf)
	v.checkNoNestingDirectives(siteConf)
	v.checkExpiresUnit(siteConf)
	v.checkAccessLogFormat(siteConf, httpConf)
	return v
}

// checkAccessLogFormat 校验 access_log 第二个参数必须是已定义的 log_format 名。
//
// access_log <path> $var; 会被 nginx 当成格式名查找并报
// "unknown log format $var"，属于只有真实 nginx 才能发现的语义错误。
func (v *Validator) checkAccessLogFormat(siteConf, httpConf string) {
	// 收集 http 级已定义的格式名
	defined := map[string]bool{}
	reDefine := regexp.MustCompile(`(?m)^\s*log_format\s+(\w+)\s`)
	for _, m := range reDefine.FindAllStringSubmatch(httpConf, -1) {
		defined[m[1]] = true
	}

	reUse := regexp.MustCompile(`(?m)^\s*access_log\s+(\S+)\s+(\S+)\s*;`)
	for _, m := range reUse.FindAllStringSubmatch(siteConf, -1) {
		path, format := m[1], m[2]
		if path == "off" {
			continue
		}
		// nginx 内置格式不需要定义
		switch format {
		case "combined", "common", "combinedifnone":
			continue
		}
		if strings.HasPrefix(format, "$") {
			v.err("access_log 的第二个参数必须是 log_format 名称，不能是变量 %q（nginx 会报 unknown log format）: access_log %s %s;", format, path, format)
			continue
		}
		if !defined[format] {
			v.err("access_log 引用了未定义的 log_format %q: access_log %s %s;", format, path, format)
		}
	}
}

// checkNoNestedLocation 校验 location 不得嵌套。
// nginx 不允许 location 里再嵌 location，这条一旦违反 nginx -t 必然失败。
func (v *Validator) checkNoNestedLocation(conf string) {
	lines := strings.Split(conf, "\n")
	var stack []string // 记录每层块的类型
	reOpen := regexp.MustCompile(`^\s*(\w+)\s*(.*?)\s*\{\s*$`)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "}") {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if m := reOpen.FindStringSubmatch(line); m != nil {
			kw := m[1]
			// 已经是 location 内部，又开了一个 location → 非法
			for _, parent := range stack {
				if parent == "location" && kw == "location" {
					v.err("检测到 location 嵌套: %s", trimmed)
					break
				}
			}
			stack = append(stack, kw)
		}
	}
}

// checkNoNestingDirectives 校验 location 内不得出现 server 专属指令。
func (v *Validator) checkNoNestingDirectives(conf string) {
	lines := strings.Split(conf, "\n")
	depth := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if depth > 0 {
			// server 块内部才允许 ssl_certificate
			if strings.HasPrefix(trimmed, "ssl_certificate") && depth > 1 {
				v.err("ssl_certificate 被放进了嵌套块内，nginx 会报 directive is not allowed here: %s", trimmed)
			}
			if strings.HasPrefix(trimmed, "server_name") && depth > 1 {
				v.err("server_name 被放进了嵌套块内: %s", trimmed)
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth < 0 {
			depth = 0
		}
	}
}

// checkExpiresUnit 校验 expires 不带单位后缀。
// expires 86400s 会被 nginx 判为 invalid number of arguments。
func (v *Validator) checkExpiresUnit(conf string) {
	re := regexp.MustCompile(`(?m)^\s*expires\s+\d+[smhdw]\s*;`)
	for _, m := range re.FindAllString(conf, -1) {
		v.err("expires 指令不能带单位后缀: %s", strings.TrimSpace(m))
	}
}

// Errors 返回错误列表。
func (v *Validator) Errors() []string { return v.errors }

// Warnings 返回警告列表。
func (v *Validator) Warnings() []string { return v.warnings }

// OK 是否无错误。
func (v *Validator) OK() bool { return len(v.errors) == 0 }

// Summary 返回可读摘要。
func (v *Validator) Summary() string {
	if v.OK() && len(v.warnings) == 0 {
		return "配置校验通过"
	}
	var sb strings.Builder
	for _, e := range v.errors {
		sb.WriteString("ERROR: " + e + "\n")
	}
	for _, w := range v.warnings {
		sb.WriteString("WARN:  " + w + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (v *Validator) err(format string, args ...any) {
	v.errors = append(v.errors, fmt.Sprintf(format, args...))
}

func (v *Validator) warn(format string, args ...any) {
	v.warnings = append(v.warnings, fmt.Sprintf(format, args...))
}

// checkBraces 大括号配平（忽略引号与注释内的括号）。
func (v *Validator) checkBraces(conf, name string) {
	depth := 0
	inSingle, inDouble := false, false
	inComment := false

	for i, r := range conf {
		if inComment {
			if r == '\n' {
				inComment = false
			}
			continue
		}
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			}
		case r == '\'':
			inSingle = true
		case r == '"':
			inDouble = true
		case r == '#':
			inComment = true
		case r == '{':
			depth++
		case r == '}':
			depth--
			if depth < 0 {
				v.err("%s: 第 %d 字节处出现多余的 '}'", name, i)
				return
			}
		}
	}
	if depth != 0 {
		v.err("%s: 大括号不配平，缺少 %d 个 '}'", name, depth)
	}
	if inSingle || inDouble {
		v.warn("%s: 存在未闭合的引号", name)
	}
}

// httpOnlyDirectives 只能在 http 上下文出现的指令。
var httpOnlyDirectives = []string{
	"limit_req_zone", "limit_conn_zone", "log_format",
	"upstream", "map", "geo", "server_names_hash_max_size",
}

// checkDirectives 校验 server 块内是否混入 http 级指令。
func (v *Validator) checkDirectives(siteConf string) {
	// 先剥掉注释，否则说明文字里的指令名会造成误报
	cleaned := stripComments(siteConf)
	for _, dir := range httpOnlyDirectives {
		re := regexp.MustCompile(`(?m)^\s*` + dir + `\s`)
		if re.MatchString(cleaned) {
			v.err("站点配置中出现只能在 http 上下文使用的指令: %s", dir)
		}
	}
}

// stripComments 去掉整行注释（# 开头）与行尾注释前的空白内容。
// 注意不处理引号内的 #，那是配置值的一部分。
func stripComments(conf string) string {
	var out []string
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// checkHttpOnly 校验 http 级配置里没有混入 server 专属指令。
func (v *Validator) checkHttpOnly(httpConf string) {
	for _, dir := range []string{"fastcgi_pass", "proxy_pass", "try_files", "return 301"} {
		if strings.Contains(httpConf, dir) {
			v.err("http 级配置中出现只能在 server/location 上下文使用的指令: %s", dir)
		}
	}
}

// checkSemicolons 校验简单指令行是否以分号结尾。
func (v *Validator) checkSemicolons(conf string) {
	for i, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "}" || strings.HasSuffix(trimmed, "{") || strings.HasSuffix(trimmed, "}") {
			continue
		}
		// location 内的多行块已由 {} 规则覆盖，这里只校验单行指令
		if strings.HasSuffix(trimmed, ";") {
			continue
		}
		// 块结束后的续行（如 location / { ... } 后跟指令）较难静态判定，给出警告而非错误
		v.warn("第 %d 行指令未以 ';' 结尾: %s", i+1, trimmed)
	}
}
