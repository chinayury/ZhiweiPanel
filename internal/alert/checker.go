package alert

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/zhiwei/panel/internal/driver"
	"github.com/zhiwei/panel/internal/logmgr"
	"github.com/zhiwei/panel/internal/model"
)

// Checker 周期性检查站点健康度并产生告警。
type Checker struct {
	drv   driver.Driver
	eng   *Engine
	sites func() ([]*model.Site, error)
}

// NewChecker 创建检查器。
func NewChecker(drv driver.Driver, eng *Engine, sites func() ([]*model.Site, error)) *Checker {
	return &Checker{drv: drv, eng: eng, sites: sites}
}

// Run 启动周期性检查。
func (c *Checker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.CheckOnce(ctx)
		}
	}
}

// CheckOnce 执行一轮检查。
func (c *Checker) CheckOnce(ctx context.Context) {
	sites, err := c.sites()
	if err != nil {
		return
	}

	// nginx 整体状态检查（仅当任意站点开启了相关告警）
	ngStatus, _ := c.drv.NginxStatus(ctx)
	if !ngStatus.ConfigOK && ngStatus.Installed {
		for _, s := range sites {
			for _, rule := range s.Alert.Rules {
				if rule.Type == "nginx_config" && rule.Enabled {
					c.eng.Raise(s, rule, fmt.Sprintf("nginx 配置检测失败: %s", ngStatus.Error))
					break
				}
			}
		}
	}

	// 配置漂移（conf.d 与最近一次成功下发的快照不一致）
	c.checkConfigDrift(sites)

	for _, s := range sites {
		c.checkSite(ctx, s)
	}
}

// checkConfigDrift 配置漂移检查。
//
// 漂移是**全局**状态（conf.d 目录 vs 最近一次成功下发的快照），
// 但告警规则挂在站点上：沿用 nginx_config 的形态，对每个启用了
// config_drift 规则的站点各告一条。Engine 有 30 分钟的站点级抑制窗口，
// 漂移持续存在也不会每个周期都刷屏。
func (c *Checker) checkConfigDrift(sites []*model.Site) {
	if configDriftChecker == nil {
		return
	}
	drift, summary := configDriftChecker()
	if !drift {
		return
	}
	for _, s := range sites {
		for _, rule := range s.Alert.Rules {
			if rule.Type == "config_drift" && rule.Enabled {
				c.eng.Raise(s, rule, fmt.Sprintf(
					"检测到 nginx 配置被面板以外的途径修改（与最近一次成功下发的快照不一致）: %s。"+
						"若改动合法，请在面板内重新保存配置以刷新基线；否则请排查改动来源", summary))
				break
			}
		}
	}
}

// configDriftChecker 由 service 层注入：返回 (是否漂移, 漂移摘要)。
//
// 摘要列出与快照不一致的配置文件名（超长截断），供告警消息直接使用。
var configDriftChecker func() (bool, string)

// SetConfigDriftChecker 注入配置漂移检测实现。
// 未注入时静默跳过，不影响其它告警（与 tamperChecker 同款约定）。
func SetConfigDriftChecker(f func() (bool, string)) {
	if f != nil {
		configDriftChecker = f
	}
}

func (c *Checker) checkSite(ctx context.Context, s *model.Site) {
	// 5xx 错误率
	c.check5xx(s)

	// 流量配额
	c.checkQuota(s)

	// 证书临期
	c.checkCert(s)

	// 站点下线
	c.checkSiteDown(s)

	// 防篡改
	c.checkTamper(s)
}

func (c *Checker) check5xx(s *model.Site) {
	if !s.Logs.AccessLog {
		return
	}
	for _, rule := range s.Alert.Rules {
		if rule.Type != "status_5xx" || !rule.Enabled {
			continue
		}
		logPath := s.Root.Path + "/logs/access.log"
		records, err := logmgr.ReadAccessLog(logPath, 20000)
		if err != nil || len(records) == 0 {
			continue
		}
		window := time.Now().Add(-time.Duration(rule.WindowMinutes) * time.Minute)
		var total, err5xx int64
		for _, r := range records {
			if r.Time.After(window) {
				total++
				if r.Status >= 500 {
					err5xx++
				}
			}
		}
		if total == 0 {
			continue
		}
		threshold := int64(rule.Value)
		if threshold <= 0 {
			threshold = 10
		}
		if err5xx >= threshold {
			c.eng.Raise(s, rule, fmt.Sprintf(
				"最近 %d 分钟内 5xx 错误 %d 次（阈值 %d，总请求 %d）",
				rule.WindowMinutes, err5xx, threshold, total))
			return
		}
	}
}

func (c *Checker) checkQuota(s *model.Site) {
	if s.Traffic.MonthlyQuotaMB <= 0 {
		return
	}
	for _, rule := range s.Alert.Rules {
		if rule.Type != "traffic_quota" || !rule.Enabled {
			continue
		}
		// 规则值作为配额使用百分比阈值，默认 80
		threshold := rule.Value
		if threshold <= 0 {
			threshold = 80
		}
		stat := quotaStat(s)
		if stat <= 0 || s.Traffic.MonthlyQuotaMB <= 0 {
			continue
		}
		// 单位必须对齐：quotaStat 注入的是月流量【字节数】，
		// MonthlyQuotaMB 是【MB】。早期直接 bytes/MB*100，
		// 1KB 流量对 1GB 配额都能算出 100%，任何站点一有流量就误报。
		quotaBytes := s.Traffic.MonthlyQuotaMB * 1024 * 1024
		pct := float64(stat) / float64(quotaBytes) * 100
		if pct >= threshold {
			c.eng.Raise(s, rule, fmt.Sprintf(
				"本月流量已用 %.1f%%（%.2f GB / %.2f GB），超过阈值 %.0f%%",
				pct, float64(stat)/(1024*1024*1024), float64(s.Traffic.MonthlyQuotaMB)/1024, threshold))
			return
		}
	}
}

// quotaStat 由上层注入（月流量字节数）。
var quotaStat = func(s *model.Site) int64 { return 0 }

// SetQuotaStatProvider 注入月流量统计提供者。
func SetQuotaStatProvider(f func(*model.Site) int64) {
	if f != nil {
		quotaStat = f
	}
}

func (c *Checker) checkCert(s *model.Site) {
	if !s.SSL.Enabled || s.Cert == nil {
		return
	}
	for _, rule := range s.Alert.Rules {
		if rule.Type != "cert_expiry" || !rule.Enabled {
			continue
		}
		days := int(rule.Value)
		if days <= 0 {
			days = 15
		}
		if s.Cert.IsExpiring(days) {
			c.eng.Raise(s, rule, fmt.Sprintf(
				"SSL 证书将于 %s 到期（剩余 %d 天），请尽快续签",
				s.Cert.NotAfter.Format("2006-01-02"), s.Cert.DaysLeft()))
			return
		}
	}
}

func (c *Checker) checkSiteDown(s *model.Site) {
	if s.Status != model.SiteStatusRunning {
		return
	}
	for _, rule := range s.Alert.Rules {
		if rule.Type != "site_down" || !rule.Enabled {
			continue
		}
		// 通过本地解析 + 连通性探测判断站点是否活着
		if !probeSite(s) {
			c.eng.Raise(s, rule, "站点无法访问，请检查 nginx 配置与网站目录")
			return
		}
	}
}

// probeSite 探测站点可用性。
//
// 默认用 HTTP 请求主域名（跟随跳转、跳过证书校验）判断。
// service 层可通过 SetSiteProbeProvider 注入更精确的实现
// （例如同时探测 nginx 配置是否有效）。
var probeSite = func(s *model.Site) bool {
	if len(s.Domains) == 0 {
		return false
	}
	scheme := "http"
	if s.SSL.Enabled {
		scheme = "https"
	}
	url := scheme + "://" + s.Domains[0] + "/"

	client := &http.Client{
		Timeout: 10 * time.Second,
		// 自签证书或域名未解析到本机时也要能探测，因此跳过校验
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	// 不自动跟随跳转：跳转本身说明 nginx 活着
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// 5xx 视为不可用；3xx/4xx 说明服务在应答
	return resp.StatusCode < 500
}

// SetSiteProbeProvider 注入站点探测实现（由 service 层提供）。
func SetSiteProbeProvider(f func(*model.Site) bool) {
	if f != nil {
		probeSite = f
	}
}

func (c *Checker) checkTamper(s *model.Site) {
	if !s.Lock.Enabled || !s.Lock.AlertOnTamper {
		return
	}
	// 防篡改检测需要读 DB 里的基线，由 service 层注入实现。
	// 默认空操作：未注入时静默跳过，不影响其它告警。
	if tamperChecker == nil {
		return
	}
	if tamperChecker(s) {
		for _, rule := range s.Alert.Rules {
			if rule.Type == "tamper" && rule.Enabled {
				c.eng.Raise(s, rule, "检测到网站文件被篡改，请立即检查")
				return
			}
		}
	}
}

// tamperChecker 由 service 层注入：返回 true 表示检测到篡改。
var tamperChecker func(*model.Site) bool

// SetTamperChecker 注入防篡改检测实现。
func SetTamperChecker(f func(*model.Site) bool) {
	tamperChecker = f
}
