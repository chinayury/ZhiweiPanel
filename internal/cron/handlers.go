package cron

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/backup"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/security"
	"github.com/zhiwei/panel/internal/service"
)

// Handler 任务处理器接口。
//
// 每个内置任务类型实现一个 handler，调度器按 Type 字段查表执行。
// handler 必须能 cancel（接 ctx）、不能阻塞超过 ctx timeout。
type Handler interface {
	Type() model.CronJobType
	Run(ctx context.Context, job *model.CronJob, svc *service.Service) error
}

// handlerFor 按 Type 查 handler。
func handlerFor(t model.CronJobType) (Handler, error) {
	switch t {
	case model.CronShell:
		return &shellHandler{}, nil
	case model.CronBackupSite:
		return &backupSiteHandler{}, nil
	case model.CronBackupDB:
		return &backupDBHandler{}, nil
	case model.CronLogSplit:
		return &logSplitHandler{}, nil
	case model.CronReleaseMem:
		return &releaseMemHandler{}, nil
	case model.CronAccessURL:
		return &accessURLHandler{}, nil
	}
	return nil, fmt.Errorf("不支持的任务类型: %s", t)
}

// 任务类型——结构体必须在 handlerFor 之前声明（Go 是单遍编译）。
type (
	shellHandler      struct{}
	backupSiteHandler struct{}
	backupDBHandler   struct{}
	logSplitHandler   struct{}
	releaseMemHandler struct{}
	accessURLHandler  struct{}
)

// ShellPayload shell 任务的 payload。
type ShellPayload struct {
	Script  string `json:"script"`  // shell 脚本内容
	Timeout int    `json:"timeout"` // 秒，0 用默认 1h
	Workdir string `json:"workdir"` // 执行目录，空 = /tmp
}

// shellHandler 自由 shell 任务。
//
// 安全：执行前会过一遍**禁用词黑名单**（见 checkForbiddenCmd）。
// 不允许的典型操作：shutdown / reboot / halt / poweroff / init、
// mkfs* / mke2fs / dd / wipefs / fdisk / parted / sgdisk / mkswap、
// passwd / chpasswd / useradd / userdel / usermod、
// insmod / rmmod / modprobe / sysctl、
// kill PID 1、systemctl poweroff|reboot|isolate、rm -rf /、
// fork 炸弹、sysrq-trigger、curl|wget 管道给解释器（下载即执行）。
//
// ⚠️ 这**不是沙箱**——面板以 root 运行，无法彻底隔离。
// 它只是一道减少误操作的护栏，挡不住有意绕过。
// 真正的授权边界在 API 层：执行 shell 任务需要独立的 shell scope，
// 泄漏的 write Token 到不了这里。
//
// 另外：检查放在**执行前**而不是只在创建时，所以历史遗留的任务
// （早期版本创建的、或直接改库塞进去的）同样会被拦。

func (h *shellHandler) Type() model.CronJobType { return model.CronShell }

func (h *shellHandler) Run(ctx context.Context, job *model.CronJob, svc *service.Service) error {
	var p ShellPayload
	if err := DecodePayload(job.Payload, &p); err != nil {
		return fmt.Errorf("payload 解析失败: %w", err)
	}
	if strings.TrimSpace(p.Script) == "" {
		return fmt.Errorf("脚本内容为空")
	}
	if err := checkForbiddenCmd(p.Script); err != nil {
		return err
	}
	timeout := time.Duration(p.Timeout) * time.Second
	if timeout <= 0 {
		timeout = time.Hour
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workdir := strings.TrimSpace(p.Workdir)
	if workdir == "" {
		workdir = os.TempDir()
	}
	cmd := exec.CommandContext(cctx, "bash", "-c", p.Script)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(),
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Printf("计划任务 shell 输出: job=%s out=%s", job.ID, truncateMsg(string(out), 500))
	}
	return err
}

// ---------------------------------------------------------------------------
// Shell 脚本的危险命令检查
//
// 面板以 root 运行，"Shell 脚本"类型的计划任务等于把 root shell 交出去。
// 这里做不了真正的沙箱，只能挡住"一键作死"的操作：关机、重启、抹盘、
// 改密码、fork 炸弹、下载即执行。
//
// 早期实现是**纯子串黑名单**（strings.Contains），实测有 6 类绕过：
//
//	a. 文档说拦、实际没拦：dd if=/dev/zero of=/dev/sda —— 黑名单里根本没有 dd
//	b. 尾随空格：printf 'root\n...' | passwd —— 黑名单项是 "passwd "（带尾空格）
//	c. 字面量拼接：sh''utdown -h now —— bash 会把引号拼成 shutdown 再执行
//	d. base64 编码：echo <b64> | base64 -d | bash —— 文本里根本没有 reboot
//	e. fork 炸弹变体：:(){ :|:& };: —— 黑名单只匹配无空格的精确串
//	f. 压根没进黑名单的一键停机：kill -9 1、echo b > /proc/sysrq-trigger
//
// 现在改成：**归一化 → 词法切分 → 按命令词匹配**。
// 归一化去掉引号、反斜杠、$、反引号，于是 "sh''utdown" 与 "shutdown"
// 归一化后完全一致（解掉 c 类）；按命令词而不是子串匹配（解掉 b 类）；
// fork 炸弹与 sysrq 改成模式匹配（解掉 e、f 的一部分）。
//
// 需要说清楚的是：**这不是安全边界，只是一道减少误操作的护栏。**
// 真正的边界是 API Token 的独立 shell scope（见 api.requireShellScope）——
// 泄漏的 write Token 到不了这里。
// ---------------------------------------------------------------------------

// dangerousCommands 禁止出现在脚本里的命令词（按 basename 匹配）。
var dangerousCommands = map[string]string{
	// 停机 / 重启
	"shutdown": "关机",
	"reboot":   "重启",
	"halt":     "关机",
	"poweroff": "关机",
	"init":     "切换运行级别",
	// 抹盘
	"mke2fs":     "格式化文件系统",
	"dd":         "原始磁盘写入",
	"wipefs":     "抹除文件系统",
	"fdisk":      "分区工具",
	"parted":     "分区工具",
	"sgdisk":     "分区工具",
	"mkswap":     "创建交换分区",
	"blkdiscard": "块设备擦除",
	// 账号与凭据
	"passwd":   "修改密码",
	"chpasswd": "批量修改密码",
	"useradd":  "创建用户",
	"userdel":  "删除用户",
	"usermod":  "修改用户",
	"groupadd": "创建用户组",
	"groupdel": "删除用户组",
	// 内核 / 系统级
	"insmod":   "加载内核模块",
	"rmmod":    "卸载内核模块",
	"modprobe": "加载内核模块",
	"sysctl":   "修改内核参数",
}

// interpreters 能执行任意代码的解释器。
//
// 与 curl / wget 组合时构成"下载即执行"，是供应链攻击的常见落地方式。
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"python": true, "python2": true, "python3": true,
	"perl": true, "php": true, "ruby": true, "node": true,
}

// systemctlDangerous systemctl 的危险子命令。
var systemctlDangerous = map[string]bool{
	"poweroff": true, "reboot": true, "halt": true, "kexec": true,
	"isolate": true, "suspend": true, "hibernate": true, "emergency": true,
}

// checkForbiddenCmd 检查脚本里是否含有危险命令。
func checkForbiddenCmd(s string) error {
	norm := normalizeScript(s)

	// fork 炸弹：按模式匹配，覆盖 `:(){ :|:& };:` 及其变体。
	if strings.Contains(norm, ":()") {
		return fmt.Errorf("脚本包含 fork 炸弹模式")
	}
	// sysrq：echo b > /proc/sysrq-trigger 会立刻重启机器。
	if strings.Contains(norm, "sysrq-trigger") {
		return fmt.Errorf("脚本包含 sysrq-trigger（可立即重启机器）")
	}

	segments := scriptSegments(s)
	for _, seg := range segments {
		if len(seg) == 0 {
			continue
		}
		for _, w := range seg {
			base := strings.ToLower(path.Base(w))
			if reason, bad := dangerousCommands[base]; bad {
				return fmt.Errorf("脚本包含禁用命令: %s（%s）", base, reason)
			}
			// mkfs.ext4 / mkfs.xfs / mkfs.btrfs 等变体
			if strings.HasPrefix(base, "mkfs") {
				return fmt.Errorf("脚本包含禁用命令: %s（格式化文件系统）", base)
			}
		}
		if err := checkKillInit(seg); err != nil {
			return err
		}
		if err := checkSystemctl(seg); err != nil {
			return err
		}
		if err := checkRecursiveRootDelete(seg); err != nil {
			return err
		}
	}

	// 下载即执行：curl / wget 的输出被管道交给解释器。
	for i := 0; i+1 < len(segments); i++ {
		if len(segments[i]) == 0 || len(segments[i+1]) == 0 {
			continue
		}
		downloader := strings.ToLower(path.Base(segments[i][0]))
		interp := strings.ToLower(path.Base(segments[i+1][0]))
		if (downloader == "curl" || downloader == "wget") && interpreters[interp] {
			return fmt.Errorf("脚本包含「下载后直接执行」（%s | %s）", downloader, interp)
		}
	}
	return nil
}

// normalizeScript 归一化脚本文本，用于危险命令匹配。
//
// 去掉所有引号、反斜杠、$、反引号：
//
//	sh''utdown -h now   → shutdown -h now
//	"shutdown" -h now   → shutdown -h now
//	sh\utdown -h now    → shutdown -h now
//
// 这样"字面量拼接"类绕过就失效了：bash 执行时也会把引号拼掉，
// 归一化后的结果与 bash 实际执行的命令一致。
func normalizeScript(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\'', '"', '\\', '$', '`':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// scriptSegments 把脚本按管道分成若干段，每段是命令词列表。
//
// 分段是为了识别"下载即执行"这种跨管道的关系。
func scriptSegments(s string) [][]string {
	normalized := normalizeScript(s)
	raw := strings.Split(normalized, "|")
	segments := make([][]string, 0, len(raw))
	for _, seg := range raw {
		words := strings.FieldsFunc(seg, func(r rune) bool {
			switch r {
			case ' ', '\t', '\n', '\r', ';', '&', '(', ')', '{', '}', '<', '>', '=', ',':
				return true
			}
			return false
		})
		segments = append(segments, words)
	}
	return segments
}

// checkKillInit 拦截 kill 掉 PID 1 的操作。
//
// kill -9 1 在 systemd 下等于让整个系统 panic 重启；
// kill -1 会给同用户的所有进程发信号。
// 但普通的 `kill $(cat x.pid)` 是合法运维操作，所以只拦这两种。
func checkKillInit(seg []string) error {
	if len(seg) == 0 || strings.ToLower(path.Base(seg[0])) != "kill" {
		return nil
	}
	for _, a := range seg[1:] {
		switch a {
		case "1", "-1", "0":
			return fmt.Errorf("脚本包含禁用操作: kill %s（会杀掉 init / 整个用户会话）", a)
		}
	}
	return nil
}

// checkSystemctl 拦截 systemctl 的危险子命令。
func checkSystemctl(seg []string) error {
	if len(seg) == 0 || strings.ToLower(path.Base(seg[0])) != "systemctl" {
		return nil
	}
	for _, a := range seg[1:] {
		if systemctlDangerous[strings.ToLower(a)] {
			return fmt.Errorf("脚本包含禁用操作: systemctl %s", a)
		}
	}
	return nil
}

// checkRecursiveRootDelete 拦截 rm -rf / 这类操作。
//
// 普通的 `rm -rf /tmp/xxx` 是合法清理，只有目标是根目录或家目录时才危险。
func checkRecursiveRootDelete(seg []string) error {
	if len(seg) == 0 || strings.ToLower(path.Base(seg[0])) != "rm" {
		return nil
	}
	recursive := false
	for _, a := range seg[1:] {
		if strings.HasPrefix(a, "-") && (strings.Contains(a, "r") || strings.Contains(a, "R")) {
			recursive = true
			continue
		}
		if !recursive {
			continue
		}
		switch a {
		case "/", "/*", "~", "~/*", "$HOME":
			return fmt.Errorf("脚本包含禁用操作: rm 递归删除根目录/家目录")
		}
	}
	return nil
}

// BackupPayload 通用备份任务 payload。
type BackupPayload struct {
	Retain int    `json:"retain"` // 保留份数，0 = 永久保留
	Note   string `json:"note"`   // 备注
}

func (h *backupSiteHandler) Type() model.CronJobType { return model.CronBackupSite }

func (h *backupSiteHandler) Run(ctx context.Context, job *model.CronJob, svc *service.Service) error {
	var p BackupPayload
	if err := DecodePayload(job.Payload, &p); err != nil {
		return fmt.Errorf("payload 解析失败: %w", err)
	}
	site, err := svc.DB().GetSite(job.Target)
	if err != nil {
		return fmt.Errorf("站点不存在: %s", err)
	}
	b, err := backup.BackupSite(ctx, svc, site)
	if err != nil {
		return err
	}
	b.Note = p.Note
	if err := svc.DB().SaveBackup(b); err != nil {
		return err
	}
	if p.Retain > 0 {
		// 顺序很关键：**先**算出超出保留份数的备份并删掉磁盘文件，
		// **再**裁剪数据库记录。
		//
		// 早期版本是反过来的（先 TrimBackups 再 FindStaleBackups）：
		// TrimBackups 把超额的数据库记录删掉之后，FindStaleBackups
		// 再从数据库列记录，自然已经只剩 keepN 条 → 直接返回空 →
		// 那个 os.Remove 循环一次都不执行 → 磁盘上的 tar.gz 一个没删。
		//
		// 结果是：界面上备份稳定在 N 份，磁盘却一天涨一份。
		// 站点备份动辄几十上百 MB，几天就能把根分区写满，
		// 接着 nginx 写日志失败、SQLite 写入失败，整个面板不可用。
		if stale, err := backup.FindStaleBackups(svc, model.BackupSite, site.ID, p.Retain); err == nil {
			for _, bk := range stale {
				if bk.Path != "" {
					_ = os.Remove(bk.Path)
				}
			}
		}
		_ = svc.DB().TrimBackups(string(model.BackupSite), site.ID, p.Retain)
		// 顺带清掉历史遗留的孤儿文件（数据库里已经没有对应记录，
		// 但文件还躺在磁盘上——那正是旧版顺序错误留下的垃圾）。
		if n, err := backup.PruneOrphanFiles(svc, model.BackupSite); err == nil && n > 0 {
			log.Printf("[cron] 清理孤儿备份文件: kind=%s count=%d", model.BackupSite, n)
		}
	}
	return nil
}

func (h *backupDBHandler) Type() model.CronJobType { return model.CronBackupDB }

func (h *backupDBHandler) Run(ctx context.Context, job *model.CronJob, svc *service.Service) error {
	var p BackupPayload
	if err := DecodePayload(job.Payload, &p); err != nil {
		return fmt.Errorf("payload 解析失败: %w", err)
	}
	if svc.MySQL() == nil {
		return fmt.Errorf("MySQL 子系统未启用")
	}
	dbName := strings.TrimSpace(job.Target)
	if dbName == "" {
		return fmt.Errorf("数据库名为空")
	}
	b, err := backup.BackupDatabase(ctx, svc, dbName)
	if err != nil {
		return err
	}
	b.Note = p.Note
	if err := svc.DB().SaveBackup(b); err != nil {
		return err
	}
	if p.Retain > 0 {
		// 顺序同 backupSiteHandler：先删磁盘文件，再裁剪数据库记录。
		// 反过来做的话，磁盘上的 sql.gz 永远删不掉。
		if stale, err := backup.FindStaleBackups(svc, model.BackupDatabase, dbName, p.Retain); err == nil {
			for _, bk := range stale {
				if bk.Path != "" {
					_ = os.Remove(bk.Path)
				}
			}
		}
		_ = svc.DB().TrimBackups(string(model.BackupDatabase), dbName, p.Retain)
		if n, err := backup.PruneOrphanFiles(svc, model.BackupDatabase); err == nil && n > 0 {
			log.Printf("[cron] 清理孤儿备份文件: kind=%s count=%d", model.BackupDatabase, n)
		}
	}
	return nil
}

// LogSplitPayload 切割访问日志 payload。
type LogSplitPayload struct {
	Retain int `json:"retain"` // 保留份数，0=不删
}

func (h *logSplitHandler) Type() model.CronJobType { return model.CronLogSplit }

func (h *logSplitHandler) Run(ctx context.Context, job *model.CronJob, svc *service.Service) error {
	var p LogSplitPayload
	if err := DecodePayload(job.Payload, &p); err != nil {
		return fmt.Errorf("payload 解析失败: %w", err)
	}
	site, err := svc.DB().GetSite(job.Target)
	if err != nil {
		return fmt.Errorf("站点不存在: %s", err)
	}
	if site.Root.Path == "" {
		return fmt.Errorf("站点根目录为空")
	}
	logsDir := filepath.Join(site.Root.Path, "logs")
	if _, err := os.Stat(logsDir); err != nil {
		return fmt.Errorf("日志目录不存在: %s", logsDir)
	}
	// 找最大的 access.log* 文件
	pattern := filepath.Join(logsDir, "access.log*")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	moved := 0
	for _, f := range files {
		// 跳过已切过的（access.log.YYYY_MM_DD）
		base := filepath.Base(f)
		if strings.Contains(base, ".") && base != "access.log" {
			continue
		}
		ts := time.Now().Format("2006_01_02_150405")
		archive := filepath.Join(logsDir, "access.log."+ts)
		if err := copyTruncate(f, archive); err != nil {
			return fmt.Errorf("切分 %s 失败: %w", f, err)
		}
		moved++
	}
	if p.Retain > 0 {
		// 删多余的归档
		archives, _ := filepath.Glob(filepath.Join(logsDir, "access.log.*"))
		// 按修改时间倒序
		type fi struct {
			path    string
			modTime time.Time
		}
		var list []fi
		for _, a := range archives {
			if st, err := os.Stat(a); err == nil {
				list = append(list, fi{a, st.ModTime()})
			}
		}
		// 排序
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				if list[j].modTime.After(list[i].modTime) {
					list[i], list[j] = list[j], list[i]
				}
			}
		}
		if len(list) > p.Retain {
			for _, old := range list[p.Retain:] {
				_ = os.Remove(old.path)
			}
		}
	}
	if moved == 0 {
		return fmt.Errorf("无访问日志可切分")
	}
	return nil
}

// copyTruncate 切分日志：把 src 的内容复制到 dst，再把 src 截断为 0。
//
// 为什么不能直接 os.Rename：
//
// nginx 的 access_log 写的是**普通路径**，但 worker 进程持有的是
// **文件描述符**（指向 inode）。rename 之后，nginx 仍然往那个被改名的
// inode 写，而新建的 access.log 没有任何进程会写它（"touch 一个新空文件
// 避免 nginx 写入报错"的假设不成立——nginx 不会因为文件消失而报错，
// 它的 fd 还在）。后果是：
//
//   - 站点目录里的 access.log 变成 0 字节 → 流量统计面板"今日请求/流量"
//     持续显示 0，5xx 错误率告警再也不触发，日志查看器读到空文件；
//   - 真实流量全部写进了那个 access.log.<时间戳> 归档文件，
//     而它的 mtime 一直最新 → 保留期按 mtime 倒序保留 → 永远不删
//     → 磁盘持续增长。
//
// copytruncate 没有这个问题：原文件的 inode 不变，nginx 继续往同一个
// inode 写（nginx 用 O_APPEND 打开日志，每次写都落在文件末尾，
// 所以截断后不会产生稀疏空洞），我们只是把内容清零。
//
// 代价：复制与截断之间写入的那一小段会丢——这是 logrotate 的
// copytruncate 模式的固有代价，对访问日志完全可以接受。
func copyTruncate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// 截断原文件（inode 不变，nginx 的 fd 继续有效）
	return os.Truncate(src, 0)
}

// releaseMemHandler 释放内存。
//
// 参考宝塔"释放内存"任务：sync + drop_caches（仅 Linux）。
// 这两个操作对生产数据库不安全，所以 task type 是 release_mem 而不是
// 通用 mem_free——名字告诉你它在干嘛。

func (h *releaseMemHandler) Type() model.CronJobType { return model.CronReleaseMem }

func (h *releaseMemHandler) Run(ctx context.Context, job *model.CronJob, svc *service.Service) error {
	// Linux only
	if _, err := os.Stat("/proc/sys/vm/drop_caches"); err != nil {
		return fmt.Errorf("释放内存仅在 Linux 上支持: %w", err)
	}
	// 同步文件系统
	cmd := exec.CommandContext(ctx, "sync")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sync 失败: %w (out=%s)", err, string(out))
	}
	// 3 = drop pagecache + dentries + inodes
	if err := os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0644); err != nil {
		return fmt.Errorf("drop_caches 失败: %w", err)
	}
	return nil
}

// AccessURLPayload 访问 URL payload。
type AccessURLPayload struct {
	URL     string `json:"url"`
	Method  string `json:"method"`  // GET / HEAD / POST，缺省 GET
	Timeout int    `json:"timeout"` // 秒
}

func (h *accessURLHandler) Type() model.CronJobType { return model.CronAccessURL }

func (h *accessURLHandler) Run(ctx context.Context, job *model.CronJob, svc *service.Service) error {
	var p AccessURLPayload
	if err := DecodePayload(job.Payload, &p); err != nil {
		return fmt.Errorf("payload 解析失败: %w", err)
	}
	if p.URL == "" {
		return fmt.Errorf("URL 为空")
	}
	// 防止 SSRF：拒绝非 http(s) 与内网地址（含 DNS 重绑定与重定向跳转）。
	if err := security.CheckExternalURL(p.URL); err != nil {
		return err
	}
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "HEAD" && method != "POST" {
		return fmt.Errorf("不支持的 method: %s", method)
	}
	timeout := time.Duration(p.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, p.URL, nil)
	if err != nil {
		return err
	}
	cli := &http.Client{
		Timeout:       timeout,
		CheckRedirect: security.NoRedirectToInternal,
	}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
