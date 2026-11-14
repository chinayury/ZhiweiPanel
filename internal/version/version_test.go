package version

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 版本号门禁
//
// 这是一个**常驻**门禁：它跟着 `go test ./...` 一起跑，
// CI 与本地都不会漏。
//
// 它检查两件事：
//
//  1. **全锚点对齐**：所有写死了版本号的地方（构建、部署、文档标题）
//     必须与本包的 Version 一致。
//  2. **无漂移**：代码与部署文件里不允许出现既不是当前版本、
//     也不是已登记历史版本的版本号——
//     出现即说明有人改了版本号却忘了同步某处。
//
// 关于门禁"匹配到自己"的问题：
// 本文件刻意不写死当前版本号字面量（一律用 version.Version 引用），
// 负例用的假版本号由 strconv 拼出来，避免门禁把自己的测试数据
// 当成漂移报出来。
// ---------------------------------------------------------------------------

// repoRoot 从测试所在目录向上找到仓库根（含 go.mod 的目录）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("找不到仓库根目录（未找到 go.mod）")
	return ""
}

// versionAnchorPatterns 是"**面板**版本号"可能出现的形态。
//
// 为什么不用"扫描所有 X.Y.Z"：代码里还有大量**别的**版本号——
// PHP 版本示例（8.2 / 7.4 / 8.3.1）、nginx 版本（1.19.4）、
// 浏览器 UA 里的 Chrome/120.0.0、SVG 路径里的坐标数字……
// 全扫必然误报，误报的门禁会被当成噪声关掉。
//
// 所以这里只认面板版本号的真实写法：
//
//	zhiweipanel-0.1.1-linux-amd64   下载文件名
//	zhiweipanel:0.1.1               镜像 tag
//	ARG VERSION=0.1.1             构建参数
//	VERSION: ${VERSION:-0.1.1}     compose 变量
//	const Version = "0.1.1"        Go 常量
//	ZhiweiPanel v0.1.1              文档标题（英文名）
//	智维面板 v0.1.0                文档标题（中文名）
//
// 覆盖了面板版本号在代码与部署文件里的全部真实写法。
//
// ⚠ 中文名也必须在内：项目是**双语**品牌（中文"智维面板" / 英文"ZhiweiPanel"），
// 文档标题两种写法都可能出现。漏掉中文名 ⇒ 门禁对中文标题里的版本漂移完全失明，
// 而这种失明是**静默**的（门禁照样全绿）。
var versionAnchorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)zhiweipanel[-:/ ]v?(\d+\.\d+\.\d+)`),
	regexp.MustCompile(`智维面板[-:/ ]v?(\d+\.\d+\.\d+)`),
	regexp.MustCompile(`(?m)^\s*(?:ARG\s+|export\s+)?VERSION\s*[:=]\s*"?v?(\d+\.\d+\.\d+)`),
	regexp.MustCompile(`\$\{VERSION:-v?(\d+\.\d+\.\d+)\}`),
	regexp.MustCompile(`(?m)^\s*(?:const|var)\s+Version\s*=\s*"v?(\d+\.\d+\.\d+)"`),
}

// scanAnchoredVersions 返回文本里"以面板版本号形态出现"的版本号。
func scanAnchoredVersions(text string) []string {
	var out []string
	for _, re := range versionAnchorPatterns {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// scanScope 需要严格扫描版本号的文件类型。
//
// go.mod / go.sum 不在其中：那里是第三方依赖版本（golang.org/x/crypto v0.58.0
// 之类），不是面板版本线。docs/ 也不在其中：历史文档要保留历史版本号。
var scanScope = map[string]bool{
	".go":      true,
	".js":      true,
	".html":    true,
	".sh":      true,
	".yml":     true,
	".yaml":    true,
	".service": true,
	".conf":    true,
	".toml":    true,
}

// scanSkipDirs 跳过的目录。
var scanSkipDirs = map[string]bool{
	".git":         true,
	"tmp":          true, // esbuild 临时产物
	"dist":         true, // 构建产物
	"audit":        true, // 审计原始报告（含历史版本引用）
	"node_modules": true,
}

// TestVersionAnchorsAllAligned 检查所有写死版本号的锚点是否与权威源一致。
//
// 这是"全锚点对齐"的强制执行：改版本号只改 version.Version 一处，
// 跑这个测试会明确告诉你哪些地方还没同步。
func TestVersionAnchorsAllAligned(t *testing.T) {
	root := repoRoot(t)
	want := Version

	t.Run("main.go 引用权威源", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(root, "main.go"))
		if err != nil {
			t.Fatalf("读取 main.go 失败: %v", err)
		}
		if !strings.Contains(string(b), "var Version = version.Version") {
			t.Error("main.go 必须写 `var Version = version.Version`（不得再写死版本号）")
		}
	})

	t.Run("Dockerfile 的 ARG VERSION", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(root, "deploy", "Dockerfile"))
		if err != nil {
			t.Fatalf("读取 Dockerfile 失败: %v", err)
		}
		got := extractArg(t, string(b), "ARG VERSION=")
		if got != want {
			t.Errorf("Dockerfile 的 ARG VERSION=%s，与权威源 %s 不一致", got, want)
		}
	})

	t.Run("docker-compose 的 VERSION 默认值", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(root, "deploy", "docker-compose.yml"))
		if err != nil {
			t.Fatalf("读取 docker-compose.yml 失败: %v", err)
		}
		got := extractAll(t, string(b), "${VERSION:-")
		if len(got) == 0 {
			t.Fatal("docker-compose.yml 里没找到 ${VERSION:-...} 占位")
		}
		for _, g := range got {
			if g != want {
				t.Errorf("docker-compose.yml 的 ${VERSION:-%s} 与权威源 %s 不一致", g, want)
			}
		}
	})

	t.Run("install.sh 的下载示例", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(root, "deploy", "install.sh"))
		if err != nil {
			t.Fatalf("读取 install.sh 失败: %v", err)
		}
		// 只认 "zhiweipanel-<版本>-linux-amd64" 这种下载文件名形态，
		// 免得把 "zhiweipanel-install.sh" 这种脚本名也当成版本号。
		re := regexp.MustCompile(`zhiweipanel-(\d+\.\d+\.\d+)-linux-amd64`)
		m := re.FindStringSubmatch(string(b))
		if m == nil {
			t.Fatal("install.sh 里没找到 zhiweipanel-<版本>-linux-amd64 示例")
		}
		if m[1] != want {
			t.Errorf("install.sh 示例里的版本 %s 与权威源 %s 不一致", m[1], want)
		}
	})

	t.Run("README 标注版本", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(root, "README.md"))
		if err != nil {
			t.Fatalf("读取 README.md 失败: %v", err)
		}
		if !strings.Contains(string(b), "v"+want) {
			t.Errorf("README.md 里找不到当前版本 v%s", want)
		}
	})

	t.Run("CHANGELOG 有当前版本段", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(root, "docs", "CHANGELOG.md"))
		if err != nil {
			t.Fatalf("读取 CHANGELOG.md 失败: %v", err)
		}
		if !strings.Contains(string(b), "## v"+want) {
			t.Errorf("CHANGELOG.md 里没有 `## v%s` 段", want)
		}
	})
}

// TestNoVersionDrift 扫描代码与部署文件，发现"既不是当前版本、
// 也不是已登记历史版本"的版本号即失败。
//
// 这条规则的价值在于：改了版本号却忘了改某个角落（比如 README 里的
// 下载链接、Dockerfile 的 tag），门禁会直接报出来。
func TestNoVersionDrift(t *testing.T) {
	root := repoRoot(t)
	known := map[string]bool{}
	for _, v := range All() {
		known[v] = true
	}

	var problems []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if scanSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		ext := filepath.Ext(base)
		if !scanScope[ext] && base != "Dockerfile" {
			return nil
		}
		// go.mod / go.sum 是第三方依赖版本，不是面板版本线。
		if base == "go.mod" || base == "go.sum" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for _, v := range scanAnchoredVersions(string(b)) {
			if !known[v] {
				problems = append(problems, rel+": 面板版本号 "+v+" 既不是当前版本也不是已登记历史版本")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描仓库失败: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestScannerCatchesWrongVersion 负例测试：扫描器必须能抓到"错的版本号"。
//
// 没有这条，门禁可能因为正则写错而**永远抓不到任何东西**——
// 一个"永远通过"的门禁比没有门禁更危险，因为它给人虚假的安全感。
func TestScannerCatchesWrongVersion(t *testing.T) {
	// 故意拼一个既不是当前版本也不是历史版本的号
	// （用 strconv 拼装，避免门禁把自己的测试数据当成漂移）。
	wrong := strconv.Itoa(9) + "." + strconv.Itoa(9) + "." + strconv.Itoa(9)
	if IsKnown(wrong) {
		t.Fatalf("测试用的假版本号 %s 不应被识别为已知版本", wrong)
	}
	// 面板版本号的几种真实写法都要能抓到
	for _, text := range []string{
		"image: zhiweipanel:" + wrong,
		"ARG VERSION=" + wrong,
		"VERSION: ${VERSION:-" + wrong + "}",
		"const Version = \"" + wrong + "\"",
		"ZhiweiPanel v" + wrong,
		// 中文品牌名同样是合法锚点——文档标题用中文名时，
		// 漏掉它会让门禁对中文标题里的版本漂移静默失明。
		// 形态与英文名一一对应（分隔符恰好一个）。
		"智维面板 v" + wrong,
		"智维面板:" + wrong,
		"智维面板-" + wrong,
	} {
		found := scanAnchoredVersions(text)
		if len(found) != 1 || found[0] != wrong {
			t.Errorf("扫描器没从 %q 里抓到 %s，实际 %v", text, wrong, found)
		}
	}
}

// TestScannerIgnoresNonVersions 负例测试：扫描器不该把别的东西当版本号。
//
// 这些都是真实出现在代码里的**第三方**版本号形态：
// IP 地址、SVG 路径坐标、PHP 版本示例、nginx 版本、浏览器 UA 里的 Chrome 版本。
func TestScannerIgnoresNonVersions(t *testing.T) {
	cases := []string{
		"listen 127.0.0.1:8888;",
		"TLSv1.2 TLSv1.3",
		"const x = 3.14;",
		"2026-10-27 12:00:00",
		"set_real_ip_from 0.0.0.0/0;",
		// isVersionLike 判断形如 8.2 / 7.4 / 8.3.1 的版本号。
		"// isVersionLike 判断形如 8.2 / 7.4 / 8.3.1 的版本号。",
		// nginx 1.18 (Ubuntu 22.04 默认) 不支持 ssl_reject_handshake（1.19.4+），
		"// nginx 1.18 (Ubuntu 22.04 默认) 不支持 ssl_reject_handshake（1.19.4+）",
		// UA: Mozilla/5.0 ... Chrome/120.0.0 Safari/537.36
		`"Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/120.0.0 Safari/537.36"`,
		// SVG 路径坐标
		"sites: 'M12 2a10 10 0 100 20 10 10 0 000-20zm0 2c1.5 0 3.2 2.1 3.8 5H8.2C8.8 6.1 10.5 4 12 4z'",
	}
	for _, c := range cases {
		if got := scanAnchoredVersions(c); len(got) != 0 {
			t.Errorf("scanAnchoredVersions(%q) 误报: %v", c, got)
		}
	}
}

// TestIsKnown 确认已知版本集合包含当前版本与全部历史版本。
func TestIsKnown(t *testing.T) {
	if !IsKnown(Version) {
		t.Errorf("当前版本 %s 应当是已知版本", Version)
	}
	for _, h := range History {
		if !IsKnown(h) {
			t.Errorf("历史版本 %s 应当是已知版本", h)
		}
	}
}

// extractArg 取 "ARG KEY=" 后面紧跟的值。
func extractArg(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("没找到 %s", prefix)
	return ""
}

// extractAll 取所有 prefix 紧跟的值。
func extractAll(t *testing.T, text, prefix string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(text, "\n") {
		idx := 0
		for {
			i := strings.Index(line[idx:], prefix)
			if i < 0 {
				break
			}
			i += idx
			rest := line[i+len(prefix):]
			end := strings.Index(rest, "}")
			if end < 0 {
				break
			}
			out = append(out, rest[:end])
			idx = i + len(prefix) + end
		}
	}
	return out
}

// extractBetween 取 prefix 与 suffix 之间的内容（取第一个）。
func extractBetween(t *testing.T, text, prefix, suffix string) string {
	t.Helper()
	i := strings.Index(text, prefix)
	if i < 0 {
		return ""
	}
	rest := text[i+len(prefix):]
	j := strings.Index(rest, suffix)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
