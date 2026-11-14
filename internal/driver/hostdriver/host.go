// Package hostdriver 实现宿主机（systemd）运行时的驱动。
package hostdriver

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/driver"
	"github.com/zhiwei/panel/internal/model"
)

// Driver 宿主机驱动。
type Driver struct {
	nginxBin string
	confDir  string
	siteConf string
	certDir  string
	dataDir  string
	logDir   string
	unitName string
	phpGlob  string
}

// Config 宿主机驱动配置。
type Config struct {
	NginxBin    string
	ConfDir     string
	SiteConfDir string
	CertDir     string
	DataDir     string
	LogDir      string
	UnitName    string
	PHPGlob     string
}

// New 创建宿主机驱动。
func New(cfg Config) *Driver {
	d := &Driver{
		nginxBin: cfg.NginxBin,
		confDir:  cfg.ConfDir,
		siteConf: cfg.SiteConfDir,
		certDir:  cfg.CertDir,
		dataDir:  cfg.DataDir,
		logDir:   cfg.LogDir,
		unitName: cfg.UnitName,
		phpGlob:  cfg.PHPGlob,
	}
	if d.nginxBin == "" {
		d.nginxBin = "nginx"
	}
	if d.unitName == "" {
		d.unitName = "nginx"
	}
	if d.phpGlob == "" {
		// 两种命名都要覆盖：Debian/Ubuntu 的 php8.1-fpm 包产出
		// /run/php/php8.1-fpm.sock（无连字符），而某些源码编译安装是
		// /run/php/php-8.1-fpm.sock。早期只写后者，导致 Ubuntu 上
		// PHP 版本列表恒为空，面板里根本切不了 PHP。
		d.phpGlob = "/run/php/php*fpm.sock"
	}
	return d
}

func (d *Driver) Runtime() driver.Runtime { return driver.RuntimeHost }

func (d *Driver) Paths() driver.Paths {
	return driver.Paths{
		ConfDir:     d.confDir,
		SiteConfDir: d.siteConf,
		CertDir:     d.certDir,
		LogDir:      d.logDir,
		DataDir:     d.dataDir,
	}
}

func (d *Driver) run(ctx context.Context, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// 注意：不要改 cmd.Dir，也不要给 nginx 传 -p。
	// 发行版打包的 nginx（Debian/Ubuntu/RHEL）把 prefix、conf-path、
	// modules-path 全部编译进二进制，且 modules-enabled 里的
	// `load_module modules/xxx.so` 是相对 prefix 解析的。
	// 传 `-p /usr/sbin` 会让它去找 /usr/sbin/modules/ 从而 dlopen 失败，
	// 把一台健康的 nginx 误判成配置损坏。
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

var versionRe = regexp.MustCompile(`nginx version: nginx/([0-9]+\.[0-9]+\.[0-9]+)`)

func (d *Driver) NginxStatus(ctx context.Context) (driver.NginxStatus, error) {
	st := driver.NginxStatus{}

	// 是否安装
	if _, err := exec.LookPath(d.nginxBin); err != nil {
		// 也检查绝对路径
		if _, serr := os.Stat(d.nginxBin); serr != nil {
			st.Error = "未检测到 nginx 可执行文件"
			return st, nil
		}
	}

	out, errOut, err := d.run(ctx, d.nginxBin, "-v")
	versionOut := out + errOut
	if m := versionRe.FindStringSubmatch(versionOut); len(m) == 2 {
		st.Version = m[1]
		st.Installed = true
	}
	if err != nil && st.Version == "" {
		st.Error = strings.TrimSpace(versionOut)
	}

	// 是否运行中：优先 systemd，回退到进程探测
	if st.Installed {
		_, _, e := d.run(ctx, "systemctl", "is-active", d.unitName)
		if e == nil {
			st.Running = true
		} else {
			for _, p := range nginxPidFiles {
				if _, err := os.Stat(p); err == nil {
					st.Running = true
					break
				}
			}
		}
	}

	ok, msg, _ := d.NginxTest(ctx)
	st.ConfigOK = ok
	if !ok && msg != "" {
		st.Error = msg
	}
	return st, nil
}

// nginxPidFiles 是 nginx pid 文件的候选路径。
//
// Debian/Ubuntu 打包默认写 /run/nginx.pid；alpine 打包把它改成了
// /run/nginx/nginx.pid（容器内实测的报错信息即指向该路径）。
//
// 两者都得认：容器里没有 systemd，`systemctl is-active` 必然失败，
// pid 文件是唯一可用的运行状态兜底。只认 Debian 那条路径，会让
// Docker 模式下状态页把正在运行的 nginx 永远显示成"未运行"。
var nginxPidFiles = []string{"/run/nginx.pid", "/run/nginx/nginx.pid"}

func (d *Driver) NginxTest(ctx context.Context) (bool, string, error) {
	if _, err := exec.LookPath(d.nginxBin); err != nil {
		if _, serr := os.Stat(d.nginxBin); serr != nil {
			return false, "未安装 nginx", nil
		}
	}
	out, errOut, err := d.run(ctx, d.nginxBin, append(d.nginxPrefixArgs(), "-t")...)
	msg := strings.TrimSpace(out + "\n" + errOut)
	if err != nil {
		return false, msg, nil
	}
	return true, msg, nil
}

// nginxPrefixArgs 返回额外的前缀参数。
//
// 恒返回 nil：nginx 必须用它编译内置的 prefix / conf-path / modules-path。
// 早期版本这里返回 `-p <可执行文件所在目录>`，在 Ubuntu 22.04 (nginx 1.18)
// 上直接炸掉——`/etc/nginx/modules-enabled/50-mod-http-geoip2.conf` 里的
// `load_module modules/ngx_http_geoip2_module.so` 会被解析成
// `/usr/sbin/modules/...`，dlopen 失败，nginx -t 报 emerg。
// 而同一台机器上手敲 `nginx -t` 是正常的，所以现象极具迷惑性：
// 面板说配置坏了，运维查却查不出问题。
func (d *Driver) nginxPrefixArgs() []string {
	return nil
}

// Reload 重载 nginx。
//
// 降级顺序（每一级都比下一级风险小）：
//
//  1. systemctl reload <unit> —— 最标准，失败不影响服务；
//  2. nginx -s reload（信号重载）—— **单元名不对时的救命稻草**。
//     nginx 由谁托管并不影响信号重载：只要二进制路径对，
//     `nginx -s reload` 就能让运行中的 master 重新读配置。
//     早期实现把信号重载放在"systemctl 不存在"的分支里，
//     于是只要机器上有 systemctl、而单元名对不上（自定义编译的
//     aa_nginx.service、源码安装的 openresty.service……），
//     就永远走不到信号重载，直接返回失败——
//     **面板把配置写对了却永远不生效**，而用户看到的只是一句
//     "重载 nginx 失败"。
//  3. systemctl restart <unit> —— 最后手段。会中断服务，
//     所以只在前面两级都失败时才用，且必须留下明确日志。
//
// 关键点：第 2 级在第 3 级之前。restart 的失败语义与 reload 完全不同——
// reload 失败 nginx 仍以旧配置继续服务，restart 失败则整个 nginx 进程不存在，
// **所有站点同时不可用**。把有中断风险的手段排在最后。
func (d *Driver) Reload(ctx context.Context) error {
	if _, err := exec.LookPath("systemctl"); err == nil {
		_, out, err := d.run(ctx, "systemctl", "reload", d.unitName)
		if err == nil {
			return nil
		}
		// 单元名不对 / reload 不被支持 → 走信号重载（不影响服务）
		if serr := d.signalReload(ctx); serr == nil {
			log.Printf("[nginx] systemctl reload %s 失败（%s），已改用 %s -s reload",
				d.unitName, strings.TrimSpace(out), d.nginxBin)
			return nil
		}
		// 最后才考虑 restart：它会中断服务
		log.Printf("[nginx] 单元 %s reload 与信号重载均失败，尝试 restart（会短暂中断服务）", d.unitName)
		_, _, rerr := d.run(ctx, "systemctl", "restart", d.unitName)
		if rerr == nil {
			return nil
		}
		return fmt.Errorf("systemctl reload 失败: %s / %s", out, err)
	}
	// 没有 systemctl：直接信号重载
	return d.signalReload(ctx)
}

// signalReload 用 nginx 自身的信号重载。
func (d *Driver) signalReload(ctx context.Context) error {
	_, errOut, err := d.run(ctx, d.nginxBin, append(d.nginxPrefixArgs(), "-s", "reload")...)
	if err != nil {
		return fmt.Errorf("nginx -s reload 失败: %s", strings.TrimSpace(errOut))
	}
	return nil
}

func (d *Driver) ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func (d *Driver) WriteConf(ctx context.Context, name, content string) error {
	if err := d.ensureDir(d.confDir); err != nil {
		return err
	}
	return driver.WriteFileAtomic(filepath.Join(d.confDir, name), content, 0o644)
}

func (d *Driver) RemoveConf(ctx context.Context, name string) error {
	err := os.Remove(filepath.Join(d.confDir, name))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *Driver) WriteSiteConf(ctx context.Context, siteID, content string) error {
	if err := d.ensureDir(d.siteConf); err != nil {
		return err
	}
	return driver.WriteFileAtomic(filepath.Join(d.siteConf, driver.SiteConfName(siteID)), content, 0o644)
}

func (d *Driver) RemoveSiteConf(ctx context.Context, siteID string) error {
	err := os.Remove(filepath.Join(d.siteConf, driver.SiteConfName(siteID)))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *Driver) ReadConf(ctx context.Context, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(d.confDir, name))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (d *Driver) ListSiteConfs(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(d.siteConf)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".conf"))
	}
	return out, nil
}

// sockRe 匹配 FPM socket 文件名中的版本号。
// 连字符可选：Ubuntu 是 php8.1-fpm.sock，部分源码编译安装是 php-8.1-fpm.sock。
var sockRe = regexp.MustCompile(`php-?([0-9]+\.[0-9]+)-?fpm\.sock$`)

func (d *Driver) PHPVersions(ctx context.Context) ([]driver.PHPVersion, error) {
	matches, _ := filepath.Glob(d.phpGlob)
	if len(matches) == 0 {
		// 退化为常见目录探测
		for _, dir := range []string{"/run/php", "/var/run/php", "/tmp"} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if m := sockRe.FindStringSubmatch(e.Name()); m != nil {
					matches = append(matches, filepath.Join(dir, e.Name()))
				}
			}
		}
	}

	var out []driver.PHPVersion
	for _, sock := range matches {
		m := sockRe.FindStringSubmatch(sock)
		if m == nil {
			continue
		}
		// 真实存在才计入
		if _, err := os.Stat(sock); err != nil {
			continue
		}
		out = append(out, driver.PHPVersion{Version: m[1], Socket: sock, Running: true})
	}
	return out, nil
}

func (d *Driver) SystemStatus(ctx context.Context) (driver.SystemStatus, error) {
	st := driver.SystemStatus{Runtime: driver.RuntimeHost}
	ng, err := d.NginxStatus(ctx)
	if err != nil {
		return st, err
	}
	st.Nginx = ng
	php, err := d.PHPVersions(ctx)
	if err != nil {
		return st, err
	}
	st.PHP = php
	st.Disk = diskUsage(d.dataDir)
	return st, nil
}

func diskUsage(path string) driver.DiskInfo {
	return diskUsageOf(path)
}

// formatBytes 字节格式化，供上层展示。
func FormatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatUint(n, 10) + " B"
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	return fmt.Sprintf("%.2f %s", float64(n)/float64(div), units[exp])
}

var _ driver.Driver = (*Driver)(nil)
var _ = model.ErrUnavailable
