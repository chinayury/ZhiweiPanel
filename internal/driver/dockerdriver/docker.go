// Package dockerdriver 实现 Docker 运行时的驱动。
//
// 与宿主机驱动的关键差异：nginx 运行在容器里，
// 因此配置测试/重载通过 docker exec 触发，配置文件通过 volume 挂载可见。
package dockerdriver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/driver"
	"github.com/zhiwei/panel/internal/model"
)

// Driver docker 驱动。
type Driver struct {
	// ContainerName 承载 nginx 的容器名。
	ContainerName string
	// ConfDir 容器内 nginx conf.d 路径。
	ContainerConfDir string
	// HostConfDir 宿主机上与容器内 conf.d 对应的挂载路径。
	HostConfDir string
	// HostSiteConfDir 宿主机上与容器内站点目录对应的路径。
	HostSiteConfDir string
	// CertDir 宿主机证书目录。
	CertDir string
	DataDir string
	LogDir  string
}

// Config docker 驱动配置。
type Config struct {
	ContainerName    string
	ContainerConfDir string
	HostConfDir      string
	HostSiteConfDir  string
	CertDir          string
	DataDir          string
	LogDir           string
}

// New 创建 docker 驱动。
func New(cfg Config) *Driver {
	d := &Driver{
		ContainerName:    cfg.ContainerName,
		ContainerConfDir: cfg.ContainerConfDir,
		HostConfDir:      cfg.HostConfDir,
		HostSiteConfDir:  cfg.HostSiteConfDir,
		CertDir:          cfg.CertDir,
		DataDir:          cfg.DataDir,
		LogDir:           cfg.LogDir,
	}
	if d.ContainerConfDir == "" {
		d.ContainerConfDir = "/etc/nginx/conf.d"
	}
	if d.ContainerName == "" {
		d.ContainerName = "nginx"
	}
	return d
}

func (d *Driver) Runtime() driver.Runtime { return driver.RuntimeDocker }

func (d *Driver) Paths() driver.Paths {
	return driver.Paths{
		ConfDir:     d.HostConfDir,
		SiteConfDir: d.HostSiteConfDir,
		CertDir:     d.CertDir,
		LogDir:      d.LogDir,
		DataDir:     d.DataDir,
	}
}

// dockerRun 在宿主上执行 docker 命令。
func (d *Driver) dockerRun(ctx context.Context, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// execIn 容器内执行命令。
func (d *Driver) execIn(ctx context.Context, args ...string) (string, string, error) {
	full := append([]string{"exec", d.ContainerName}, args...)
	return d.dockerRun(ctx, full...)
}

func (d *Driver) containerExists() bool {
	out, _, err := d.dockerRun(context.Background(), "ps", "-a",
		"--filter", "name=^"+d.ContainerName+"$", "--format", "{{.Names}}")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

var versionRe = regexp.MustCompile(`nginx version: nginx/([0-9]+\.[0-9]+\.[0-9]+)`)

func (d *Driver) NginxStatus(ctx context.Context) (driver.NginxStatus, error) {
	st := driver.NginxStatus{}
	if _, err := exec.LookPath("docker"); err != nil {
		st.Error = "未安装 docker"
		return st, nil
	}
	if !d.containerExists() {
		st.Error = fmt.Sprintf("容器 %s 不存在", d.ContainerName)
		return st, nil
	}
	st.Installed = true

	// 运行状态
	if out, _, err := d.dockerRun(ctx, "ps", "--filter",
		"name=^"+d.ContainerName+"$", "--format", "{{.State}}"); err == nil {
		st.Running = strings.TrimSpace(out) == "running"
	}

	// 版本
	if out, errOut, err := d.execIn(ctx, "nginx", "-v"); err == nil || out != "" || errOut != "" {
		if m := versionRe.FindStringSubmatch(out + errOut); len(m) == 2 {
			st.Version = m[1]
		}
	}

	ok, msg, _ := d.NginxTest(ctx)
	st.ConfigOK = ok
	if !ok && msg != "" {
		st.Error = msg
	}
	return st, nil
}

func (d *Driver) NginxTest(ctx context.Context) (bool, string, error) {
	out, errOut, err := d.execIn(ctx, "nginx", "-t")
	msg := strings.TrimSpace(out + "\n" + errOut)
	if err != nil {
		return false, msg, nil
	}
	return true, msg, nil
}

func (d *Driver) Reload(ctx context.Context) error {
	out, errOut, err := d.execIn(ctx, "nginx", "-s", "reload")
	if err != nil {
		return fmt.Errorf("容器内 reload 失败: %s%s", out, errOut)
	}
	return nil
}

func (d *Driver) WriteConf(ctx context.Context, name, content string) error {
	if err := os.MkdirAll(d.HostConfDir, 0o755); err != nil {
		return err
	}
	return driver.WriteFileAtomic(filepath.Join(d.HostConfDir, name), content, 0o644)
}

func (d *Driver) RemoveConf(ctx context.Context, name string) error {
	err := os.Remove(filepath.Join(d.HostConfDir, name))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *Driver) WriteSiteConf(ctx context.Context, siteID, content string) error {
	if err := os.MkdirAll(d.HostSiteConfDir, 0o755); err != nil {
		return err
	}
	return driver.WriteFileAtomic(filepath.Join(d.HostSiteConfDir, driver.SiteConfName(siteID)), content, 0o644)
}

func (d *Driver) RemoveSiteConf(ctx context.Context, siteID string) error {
	err := os.Remove(filepath.Join(d.HostSiteConfDir, driver.SiteConfName(siteID)))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *Driver) ReadConf(ctx context.Context, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(d.HostConfDir, name))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (d *Driver) ListSiteConfs(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(d.HostSiteConfDir)
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

// sockRe 匹配 PHP-FPM socket 文件名。
//
// 不同发行版/镜像的命名不一样：
//
//	php-8.2-fpm.sock   （部分镜像）
//	php8.2-fpm.sock    （Debian/Ubuntu 官方包）
//	php-fpm.sock       （无版本号）
//
// 两种都匹配，版本号（如果有）从文件名里取。
var sockRe = regexp.MustCompile(`php-?([0-9]+\.[0-9]+)?-?fpm\.sock$`)

func (d *Driver) PHPVersions(ctx context.Context) ([]driver.PHPVersion, error) {
	// PHP-FPM 通常在独立容器里，这里按常见命名探测。
	var out []driver.PHPVersion
	for _, name := range []string{"php-fpm", "php74-fpm", "php80-fpm", "php81-fpm", "php82-fpm", "php83-fpm"} {
		o, _, err := d.dockerRun(ctx, "ps", "-a", "--filter", "name="+name, "--format", "{{.State}}")
		if err != nil || strings.TrimSpace(o) == "" {
			continue
		}
		state := strings.TrimSpace(o)
		out = append(out, driver.PHPVersion{
			Version: phpVersionFromContainerName(name),
			// socket 路径必须去容器里问，不能猜——猜错就是 502。
			Socket:  d.findPHPSocket(ctx, name),
			Running: state == "running",
		})
	}
	// 兜底：容器内直接列 socket（PHP-FPM 与 nginx 同容器部署的形态）
	if len(out) == 0 {
		if o, _, err := d.execIn(ctx, "sh", "-c", "ls /run/php/*.sock 2>/dev/null"); err == nil {
			for _, line := range strings.Split(o, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				out = append(out, driver.PHPVersion{
					Version: phpVersionFromSocket(line),
					Socket:  line,
					Running: true,
				})
			}
		}
	}
	return out, nil
}

// findPHPSocket 在 PHP-FPM 容器里找真实的 socket 路径。
//
// 早期版本靠猜：strings.TrimPrefix(ver, "7") 想把 "74" 变成 "4"，
// 结果 php74-fpm 被拼成 /run/php/php-4-fpm.sock —— 一个不存在的路径，
// Docker 模式下所有 PHP 站点全部 502 Bad Gateway。
// php-fpm 更是拼成 /run/php/php-default-fpm.sock。
//
// 命名规则各发行版不统一，猜不对；进容器 ls 一下就是准确答案。
// 找不到就返回空串，由上层回退到自己的推导逻辑。
func (d *Driver) findPHPSocket(ctx context.Context, container string) string {
	o, _, err := d.dockerRun(ctx, "exec", container, "sh", "-c",
		"ls /run/php/*.sock 2>/dev/null | head -1")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(o)
}

// phpVersionFromContainerName 从容器名推导 PHP 版本。
//
//	php74-fpm → 7.4
//	php82-fpm → 8.2
//	php-fpm   → default
func phpVersionFromContainerName(name string) string {
	v := strings.TrimPrefix(name, "php")
	v = strings.TrimSuffix(v, "-fpm")
	if v == "" {
		return "default"
	}
	if strings.Contains(v, ".") {
		return v
	}
	if len(v) >= 2 && isAllDigits(v) {
		return v[:1] + "." + v[1:]
	}
	return v
}

// phpVersionFromSocket 从 socket 文件名推导版本（取不到就是 default）。
func phpVersionFromSocket(path string) string {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	if m := sockRe.FindStringSubmatch(base); m != nil && m[1] != "" {
		return m[1]
	}
	return "default"
}

// isAllDigits 判断字符串是否全为数字。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type psEntry struct {
	Names    string `json:"Names"`
	Image    string `json:"Image"`
	State    string `json:"State"`
	Ports    string `json:"Ports"`
	Networks string `json:"Networks"`
}

func (d *Driver) SystemStatus(ctx context.Context) (driver.SystemStatus, error) {
	st := driver.SystemStatus{Runtime: driver.RuntimeDocker}
	ng, err := d.NginxStatus(ctx)
	if err != nil {
		return st, err
	}
	st.Nginx = ng
	php, _ := d.PHPVersions(ctx)
	st.PHP = php
	st.Docker = d.dockerInfo(ctx)
	return st, nil
}

func (d *Driver) dockerInfo(ctx context.Context) driver.DockerInfo {
	info := driver.DockerInfo{Available: false}
	if _, err := exec.LookPath("docker"); err != nil {
		return info
	}
	info.Available = true
	out, _, err := d.dockerRun(ctx, "ps", "-a",
		"--format", `{"Names":"{{.Names}}","Image":"{{.Image}}","State":"{{.State}}","Ports":"{{.Ports}}","Networks":"{{.Networks}}"}`)
	if err != nil {
		return info
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e psEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		info.Containers = append(info.Containers, driver.DockerContainer{
			Name: e.Names, Image: e.Image, State: e.State, Ports: e.Ports, Networks: e.Networks,
		})
	}
	return info
}

var _ driver.Driver = (*Driver)(nil)
var _ = model.ErrUnavailable
