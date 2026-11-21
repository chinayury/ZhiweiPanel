// Package service 是业务编排层：把模型、渲染器、存储、驱动粘合成完整用例。
package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhiwei/panel/internal/acme"
	"github.com/zhiwei/panel/internal/alert"
	"github.com/zhiwei/panel/internal/apps"
	"github.com/zhiwei/panel/internal/driver"
	"github.com/zhiwei/panel/internal/logmgr"
	"github.com/zhiwei/panel/internal/mailer"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/mysql"
	"github.com/zhiwei/panel/internal/nginx"
	"github.com/zhiwei/panel/internal/store"
	"github.com/zhiwei/panel/internal/sysinfo"
)

// Service 业务服务。
type Service struct {
	db     *store.DB
	drv    driver.Driver
	acme   *acme.Manager
	eng    *alert.Engine
	mailer *mailer.Mailer
	// mysql MySQL/MariaDB 子系统客户端，nil 表示未安装（业务层降级：
	// 站点建库请求直接拒，返回明确错误）
	mysql *mysql.Client

	// reloadMu 串行化配置下发，避免并发写同一批文件。
	reloadMu sync.Mutex

	// configIssues 记录最近一次配置下发中被隔离的问题站点。
	//
	// 为什么要"隔离"而不是"整体失败"：站点配置渲染失败只说明**这一个**
	// 站点有问题（比如历史脏数据、配置 JSON 损坏）。早期实现直接 return，
	// 于是 nginx -t 失败 → ApplyAllConfig 整体回滚 → **所有站点**的配置
	// 都停在旧版本：用户改 A 站点，B、C、D 站点一起不生效，而且完全
	// 不知道是哪一格填错了。
	//
	// 现在改成：坏站点的配置整体注释掉（等价于停用），其余站点照常下发。
	// 问题记录在这里，由 API 暴露给前端提示用户。
	configIssuesMu sync.Mutex
	configIssues   []string

	// paths 相关配置
	dataDir string
	certDir string
	// backupDirCustom 是 -backup-dir 显式注入的自定义备份根目录。
	// 空 = 用历史默认 <data>/backup；真实备份落盘与孤儿清理都以
	// BackupRoot()（backup 包）为准，见该方法注释。
	backupDirCustom string
	// sslDir 是 dummy 自签证书的存放目录。
	sslDir string
	// publicPort 全局对外端口（v0.2.1）。0 = 用 site.ListenPorts。
	// 用于整站搬迁到非标端口的场景；面板启动 flag -public-port 注入。
	publicPort int
	// disableIPv6 关闭 v6 listen（老机器无 IPv6 时 nginx -t 会报 emerg）。
	disableIPv6 bool
	// metrics 宿主机指标采集器（v0.2.2）。
	// 后台定时采样 + 内存缓存，HTTP 查询零阻塞。
	metrics *sysinfo.Collector
}

// New 创建业务服务。
// mysqlClient 可传 nil——MySQL 未装时 service 自动降级，调用 CreateSite
// 带 site.Mysql 会返回 model.ErrMySQLNotAvailable。
//
// publicPort / disableIPv6 是 v0.2.1 全局端口/协议开关，传 0/false 表示用站点自身配置。
func New(db *store.DB, drv driver.Driver, acmeMgr *acme.Manager, eng *alert.Engine, m *mailer.Mailer, mysqlClient *mysql.Client, dataDir, certDir string, publicPort int, disableIPv6 bool) *Service {
	return &Service{
		db:          db,
		drv:         drv,
		acme:        acmeMgr,
		eng:         eng,
		mailer:      m,
		mysql:       mysqlClient,
		dataDir:     dataDir,
		certDir:     certDir,
		sslDir:      filepath.Join(dataDir, "ssl"),
		publicPort:  publicPort,
		disableIPv6: disableIPv6,
		// 磁盘用量统计面板数据目录所在的卷——备份目录通常在数据目录旁边，
		// 看根分区可能看不到真正的瓶颈卷。
		metrics: sysinfo.NewCollector(dataDir),
	}
}

// DB 暴露存储层。
func (s *Service) DB() *store.DB { return s.db }

// DataDir 暴露数据目录（备份、根凭据、证书等的根路径）。
func (s *Service) DataDir() string { return s.dataDir }

// SetBackupDir 设置自定义备份根目录（main.go 用 -backup-dir 注入）。
// 留空 = 保持历史默认 `<data>/backup`（老部署的备份记录都指向那里，
// 改默认值会让旧记录的下载/恢复失效，所以只在显式设置时生效）。
func (s *Service) SetBackupDir(dir string) {
	if dir != "" {
		s.backupDirCustom = dir
	}
}

// BackupRoot 返回站点/数据库备份的落盘根目录：
// 显式设置过 -backup-dir 时用它，否则 `<data>/backup`（历史默认）。
// backup 包的写入（BackupSite/BackupDatabase）与孤儿文件清理
// （PruneOrphanFiles）都以这里为准。
func (s *Service) BackupRoot() string {
	if s.backupDirCustom != "" {
		return s.backupDirCustom
	}
	return filepath.Join(s.dataDir, "backup")
}

// Driver 暴露运行时驱动。
func (s *Service) Driver() driver.Driver { return s.drv }

// ACME 暴露证书管理器。
func (s *Service) ACME() *acme.Manager { return s.acme }

// MySQL 暴露 MySQL 子系统客户端（可能为 nil，表示未装）。
// API 层用这个判断"MySQL 状态卡片"和"建库选项"的可见性。
func (s *Service) MySQL() *mysql.Client { return s.mysql }

// AlertEngine 暴露告警引擎。
func (s *Service) AlertEngine() *alert.Engine { return s.eng }

// PublicPort 暴露全局对外端口（0 = 用 site.ListenPorts）。
// 给 API 层 GET /api/system/info 用，让前端知道当前面板是"全局非标端口"还是"按站配置"。
func (s *Service) PublicPort() int { return s.publicPort }

// DisableIPv6 暴露 IPv6 开关。
func (s *Service) DisableIPv6() bool { return s.disableIPv6 }

// RenderSiteConfig 渲染一个站点的 nginx vhost 字符串（不写盘）。
// 备份场景需要把当前生效的 conf 一并打进 tar.gz。
func (s *Service) RenderSiteConfig(site *model.Site) (string, error) {
	res, err := nginx.Render(site, s.renderOptions(site))
	if err != nil {
		return "", err
	}
	return res.SiteConf, nil
}

// ApplyAllConfig 把全部站点的配置下发到运行时并重载。
// reloadMu 保证串行执行：写配置 → nginx -t → reload，失败则回滚。
func (s *Service) ApplyAllConfig(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	sites, err := s.db.ListSites()
	if err != nil {
		return err
	}

	// 记录旧配置用于回滚
	var backups []configBackup

	var httpParts []string
	var isolated []string
	for _, site := range sites {
		// 自愈：口令文件可能被外部删掉（清理脚本、手工运维、磁盘恢复），
		// 而 nginx 在解析配置时就会打开它——缺文件 → nginx -t 失败 →
		// 整批配置回滚，所有站点一起停摆。先补齐再渲染。
		if err := s.syncAccessFile(site); err != nil {
			s.rollback(ctx, backups)
			return fmt.Errorf("同步站点 %s 的访问认证口令文件失败: %w", safeOneLine(site.Name), err)
		}
		opt := s.renderOptions(site)
		res, err := nginx.Render(site, opt)
		if err != nil {
			// 隔离而不是整体失败：只把这个站点注释掉，其余站点照常下发。
			//
			// 早期实现在这里直接 return，后果是 nginx -t 失败 → 整体回滚 →
			// 所有站点的配置都停在旧版本。一个站点的历史脏数据能把整台
			// 面板的其它站点一起拖下水。
			msg := fmt.Sprintf("站点 %s 配置渲染失败，已临时禁用（其它站点不受影响）：%v",
				safeOneLine(site.Name), err)
			log.Printf("[service] %s", msg)
			isolated = append(isolated, msg)
			content := fmt.Sprintf("# %s\n# 该站点已临时禁用；修复配置后点击“重新下发配置”即可恢复。\n",
				safeOneLine(msg))
			path := filepath.Join(s.drv.Paths().SiteConfDir, SiteConfFile(site.ID))
			old, readErr := os.ReadFile(path)
			backups = append(backups, configBackup{
				siteID:  site.ID,
				content: string(old),
				existed: readErr == nil,
			})
			if writeErr := s.drv.WriteSiteConf(ctx, site.ID, content); writeErr != nil {
				s.rollback(ctx, backups)
				return fmt.Errorf("写入站点 %s 配置失败: %w", safeOneLine(site.Name), writeErr)
			}
			continue
		}
		// 停用/停止的站点：注释掉整个 server 块而不是删除配置，便于随时恢复。
		// 必须逐行注释——只注释首行的话 server {} 块依旧生效，站点照常提供服务
		// （实测：stopped 后 curl 仍返回 200）。
		content := res.SiteConf
		if site.Status == model.SiteStatusStopped || site.Status == model.SiteStatusDisabled {
			content = "# 站点已停用（status=" + string(site.Status) +
				"），配置已注释，恢复运行后自动生效\n" + commentOut(res.SiteConf)
		}

		path := filepath.Join(s.drv.Paths().SiteConfDir, SiteConfFile(site.ID))
		old, readErr := os.ReadFile(path)
		backups = append(backups, configBackup{
			siteID:  site.ID,
			content: string(old),
			existed: readErr == nil,
		})

		if err := s.drv.WriteSiteConf(ctx, site.ID, content); err != nil {
			s.rollback(ctx, backups)
			return fmt.Errorf("写入站点 %s 配置失败: %w", safeOneLine(site.Name), err)
		}
		if res.HTTPConf != "" {
			httpParts = append(httpParts, res.HTTPConf)
		}
	}
	s.setConfigIssues(isolated)

	// 汇总 http 级配置
	httpConf := buildHTTPConf(httpParts)
	// 文件名必须带 00- 前缀：nginx 按文件名升序加载 conf.d/*，
	// 而 log_format / limit_req_zone 等 http 级指令必须先于站点配置定义，
	// 否则站点里引用它们时会报 "unknown log format"。
	//
	// http 级配置的旧内容也要纳入快照：早期只快照各站点 conf，
	// 一旦后续步骤失败，盘上会留下新的坏 http conf 与旧的站点 conf 混合态，
	// 此后任何外部触发的 nginx reload 都会失败，直到下次面板成功下发。
	httpOld, httpExisted, _ := readConfIfExists(s.drv, ctx, HTTPConfFile)
	if err := s.drv.WriteConf(ctx, HTTPConfFile, httpConf); err != nil {
		s.rollback(ctx, backups)
		return fmt.Errorf("写入 http 级配置失败: %w", err)
	}
	backups = append(backups, configBackup{
		siteID:  httpConfKey,
		content: httpOld,
		existed: httpExisted,
	})

	// 防跨站隔离：写一份 catch-all default_server。
	// 任一站点启用 SSL 时同时 listen 443（ssl_reject_handshake 兜底）；
	// 否则只 listen 80，避免在没有证书的环境里被迫配证书。
	// 文件名前缀 01- 排在 10-site-*.conf 之前，确保 default_server
	// 标记先于真实站点生效——否则 nginx 会把第一个真实站点作为
	// default_server，访问 IP 时直接吐那个站点的根目录内容。
	hasSSL := false
	for _, site := range sites {
		if site.SSL.Enabled && site.SSL.CertFile != "" && site.SSL.KeyFile != "" {
			hasSSL = true
			break
		}
	}
	defaultConf := nginx.RenderDefaultServer(hasSSL, nginx.DefaultServerTLS{
		CertFile: filepath.Join(s.sslDir, "dummy.crt"),
		KeyFile:  filepath.Join(s.sslDir, "dummy.key"),
	})
	defaultOld, defaultExisted, _ := readConfIfExists(s.drv, ctx, nginx.DefaultServerConfFile)
	if err := s.drv.WriteConf(ctx, nginx.DefaultServerConfFile, defaultConf); err != nil {
		s.rollback(ctx, backups)
		return fmt.Errorf("写入默认 server 配置失败: %w", err)
	}
	backups = append(backups, configBackup{
		siteID:  defaultServerKey,
		content: defaultOld,
		existed: defaultExisted,
	})

	// 清理孤儿站点配置。
	// 必须做：ApplyAllConfig 只会为数据库里存在的站点写配置，从不删除文件。
	// 于是删站后 10-site-<id>.conf 留在 conf.d 里继续被 nginx 加载，
	// 已删除的站点照常对外服务（实测：delete 返回 200 后 curl 仍是 200）。
	// 只认 10-site- 前缀，避免误删手写在 conf.d 里的其他配置。
	//
	// config 损坏的站点也在 known 集合里：ListSites 对坏数据返回占位 Site，
	// 若把它当孤儿删掉配置，等于一次 schema 演进就连带废掉一个线上站点。
	known := make(map[string]bool, len(sites))
	for _, site := range sites {
		known[site.ID] = true
	}
	if existing, lerr := s.drv.ListSiteConfs(ctx); lerr == nil {
		for _, name := range existing {
			// ListSiteConfs 的契约是"配置文件基名"，但两种驱动都只是
			// TrimSuffix(".conf")，这里对带/不带后缀都容忍，
			// 避免因约定漂移把 siteID 拼错（那会删不掉该删的文件）。
			id := strings.TrimPrefix(name, driver.SiteConfPrefix)
			id = strings.TrimSuffix(id, ".conf")
			if id == name || id == "" {
				continue // 非面板生成的文件，不碰
			}
			if !known[id] {
				orphanOld, orphanExisted, _ := readConfIfExists(s.drv, ctx, driver.SiteConfName(id))
				if rmErr := s.drv.RemoveSiteConf(ctx, id); rmErr == nil && orphanExisted {
					backups = append(backups, configBackup{
						siteID:  id,
						content: orphanOld,
						existed: true,
					})
				}
			}
		}
	}

	// 关键一步：配置检测通过后才重载，检测失败立刻回滚。
	ok, msg, err := s.drv.NginxTest(ctx)
	if err != nil || !ok {
		s.rollback(ctx, backups)
		return fmt.Errorf("nginx 配置检测失败: %s", msg)
	}

	if err := s.drv.Reload(ctx); err != nil {
		return fmt.Errorf("重载 nginx 失败: %w", err)
	}

	// 站点目录与默认文件
	for _, site := range sites {
		s.ensureSiteDirs(site)
	}

	// v0.2.2：配置下发成功后留一份快照。
	// 只有"成功"才留——失败的中间态没有参考价值，
	// 反而会让人在回滚时选错版本。
	s.SnapshotConfig(applyReason(ctx), "")
	return nil
}

// applyReasonKey 是配置下发原因在 context 里的键。
type applyReasonKey struct{}

// WithApplyReason 给配置下发打上原因标记（site.create / config.apply ...）。
//
// 快照列表里光有时间戳说不清"这次是建站还是改站"，
// 有了原因才能在几十份快照里快速定位到真正相关的那一份。
func WithApplyReason(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, applyReasonKey{}, reason)
}

// applyReason 取出配置下发原因。
func applyReason(ctx context.Context) string {
	if r, ok := ctx.Value(applyReasonKey{}).(string); ok && r != "" {
		return r
	}
	return "config.apply"
}

// configBackup 配置回滚快照。
type configBackup struct {
	siteID  string
	content string
	existed bool
}

// HTTPConfFile http 级配置文件名。
//
// 前缀 00- 是必须的：nginx 按文件名升序加载 conf.d 下的文件，
// log_format / limit_req_zone 等指令必须先于站点配置加载，
// 否则站点配置引用它们时会报 unknown log format。
const HTTPConfFile = "00-zhiweipanel-http.conf"

// SiteConfFile 站点配置文件名。
// 前缀 10- 保证站点配置排在 http 级配置（00-）之后，同时加载顺序稳定。
func SiteConfFile(siteID string) string {
	return driver.SiteConfName(siteID)
}

// commentOut 把整段配置逐行注释掉。
// 用于停用站点：nginx 会忽略这些行，server 块随之失效；
// 而配置文件仍然保留在磁盘上，取消停用即可原样恢复。
func commentOut(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		lines[i] = "# " + ln
	}
	return strings.Join(lines, "\n")
}

// httpConfKey 是 http 级配置在回滚快照里的占位 ID。
// 用一个不可能是真实 siteID 的值，避免与站点配置混淆。
const httpConfKey = "@http-level@"

// defaultServerKey 是 catch-all default_server 配置的回滚快照占位 ID。
// 同样用不可能是真实 siteID 的值，与普通站点和 httpConfKey 三向区分。
const defaultServerKey = "@default-server@"

// readConfIfExists 读取驱动上的配置文件；不存在时返回 ("", false, nil)。
func readConfIfExists(drv driver.Driver, ctx context.Context, name string) (string, bool, error) {
	content, err := drv.ReadConf(ctx, name)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return content, true, nil
}

// rollback 回滚配置写入。
//
// 快照里可能有四类条目，用 siteID 区分：
//   - 普通站点 ID：写回/删除 10-site-<id>.conf
//   - httpConfKey：写回/删除 00-zhiweipanel-http.conf
//   - defaultServerKey：写回/删除 01-zhiweipanel-default-server.conf
//   - 孤儿站点 ID：与普通站点相同（删除后又被删掉的，写回即可）
func (s *Service) rollback(ctx context.Context, backups []configBackup) {
	for _, b := range backups {
		if b.siteID == httpConfKey {
			if b.existed {
				_ = s.drv.WriteConf(ctx, HTTPConfFile, b.content)
			} else {
				_ = s.drv.RemoveConf(ctx, HTTPConfFile)
			}
			continue
		}
		if b.siteID == defaultServerKey {
			if b.existed {
				_ = s.drv.WriteConf(ctx, nginx.DefaultServerConfFile, b.content)
			} else {
				_ = s.drv.RemoveConf(ctx, nginx.DefaultServerConfFile)
			}
			continue
		}
		if b.existed {
			_ = s.drv.WriteSiteConf(ctx, b.siteID, b.content)
		} else {
			_ = s.drv.RemoveSiteConf(ctx, b.siteID)
		}
	}
}

// buildHTTPConf 组装 http 级配置文件内容。
func buildHTTPConf(parts []string) string {
	var sb strings.Builder
	sb.WriteString("# ============================================\n")
	sb.WriteString("# ZhiweiPanel http-level config\n")
	sb.WriteString("# limit_req_zone / map 等只能在 http 上下文生效\n")
	sb.WriteString("# ============================================\n\n")
	// log_format 必须全局唯一，只在这里定义一次
	sb.WriteString("log_format " + nginx.LogFormatName + " '" + logmgr.AccessLogFormat() + "';\n\n")
	for _, p := range parts {
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderOptions 构造渲染上下文。
func (s *Service) renderOptions(site *model.Site) nginx.RenderOptions {
	opt := nginx.RenderOptions{
		SiteID:      site.ID,
		PublicPort:  s.publicPort,
		DisableIPv6: s.disableIPv6,
		AccessFile:  s.accessFilePath(site.ID),
	}
	if site.Rewrite.Enabled && site.Rewrite.Rules != "" {
		opt.PathRewrites = site.Rewrite.Rules
	}
	if site.PHP.Socket != "" {
		opt.DefaultPHPSocket = site.PHP.Socket
	}
	// 月配额超额判定
	if site.Traffic.MonthlyQuotaMB > 0 && site.Traffic.EnforceQuota {
		if _, monthBytes, err := s.db.GetTrafficSummary(site.ID); err == nil {
			opt.QuotaExceeded = monthBytes > site.Traffic.MonthlyQuotaMB*1024*1024
		}
	}
	return opt
}

// ensureSiteDirs 确保站点目录与日志目录存在。
func (s *Service) ensureSiteDirs(site *model.Site) {
	if site.Root.Path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Join(site.Root.Path, "logs"), 0o755)
	if site.PHP.Enabled && site.Root.Path != "" {
		_ = os.MkdirAll(filepath.Join(site.Root.Path, "public"), 0o755)
	}
	// 默认欢迎页：用户未放 index 文件时由面板写入一份
	//（覆盖既有内容不发生，写入失败仅日志告警不阻塞）。
	s.EnsureDefaultIndex(site)
}

// prevalidateSite 在落库前校验站点的新配置能被 nginx 接受。
//
// 这是 P0「自定义 Nginx 配置」注入点的核心保障：自定义片段是用户手写的
// 自由 nginx 文本，任何语法错误（缺右括号、http 级指令误入 server 块、
// 引用了不存在的证书……）都会让 nginx -t 失败。若沿用早期的「先 SaveSite
// 再 ApplyAllConfig」，校验失败时 DB 已经写进坏配置、磁盘却回滚成旧配置——
// 两者从此不一致，之后任何一次全量下发都会再次卡死在这个坏配置上。
//
// 流程（顺序不能错）：
//
//	写正式文件（WriteFileAtomic：临时文件 → rename 原子替换）→ nginx -t
//	  → 失败：恢复旧文件，返回 nginx -t 原文；不落库、不 reload
//	  → 通过：什么都不落（后续 SaveSite + ApplyAllConfig 做全量下发）
//
// 这里只测试「这一站点」的新配置在真实 conf.d 里的正确性；http 级配置、
// default server、孤儿清理等全量同步仍由 ApplyAllConfig 收口。
func (s *Service) prevalidateSite(ctx context.Context, site *model.Site) error {
	// 访问认证口令文件必须先就位：nginx 解析配置时就会打开它，
	// 文件缺失会让 nginx -t 直接报 open() failed。
	if err := s.syncAccessFile(site); err != nil {
		return err
	}
	res, err := nginx.Render(site, s.renderOptions(site))
	if err != nil {
		return err
	}

	path := filepath.Join(s.drv.Paths().SiteConfDir, SiteConfFile(site.ID))
	old, readErr := os.ReadFile(path)
	existed := readErr == nil

	if err := s.drv.WriteSiteConf(ctx, site.ID, res.SiteConf); err != nil {
		return fmt.Errorf("写入站点 %s 配置失败: %w", safeOneLine(site.Name), err)
	}
	ok, msg, err := s.drv.NginxTest(ctx)
	if err != nil || !ok {
		// 恢复旧文件：新写进来的坏配置必须撤回，保证 md5 与保存前一致。
		if existed {
			_ = s.drv.WriteSiteConf(ctx, site.ID, string(old))
		} else {
			_ = s.drv.RemoveSiteConf(ctx, site.ID)
		}
		raw := strings.TrimSpace(msg)
		if raw == "" {
			raw = "nginx 配置检测失败"
		}
		return fmt.Errorf("%w: %s", model.ErrNginxConfig, raw)
	}
	return nil
}

// CreateSite 新建站点。
func (s *Service) CreateSite(ctx context.Context, site *model.Site) (*model.Site, error) {
	if len(site.Domains) == 0 {
		return nil, model.ErrInvalidDomain
	}
	if err := site.Domains.Validate(); err != nil {
		return nil, err
	}
	// 校验监听端口（v0.2.1）。全局 -public-port 非 0 时跳过，render 用全局值。
	if s.publicPort == 0 {
		cleaned, err := model.ValidateListenPorts(site.ListenPorts)
		if err != nil {
			return nil, err
		}
		site.ListenPorts = cleaned
	} else {
		site.ListenPorts = nil
	}
	// 全字段校验：IP 名单 / UA 屏蔽词 / 反代目标 / 重定向 / 证书路径……
	// 这些值会被原样写进 nginx 配置文本，非法值要么改变配置语义，
	// 要么让 nginx -t 失败进而拖垮所有站点的配置下发。
	// 必须在落库前拦住，并把出错字段精确告诉用户。
	if err := model.ValidateSite(site); err != nil {
		return nil, err
	}
	// MySQL 绑定参数同样要过白名单（库名/用户名/字符集/端口）。
	if err := model.ValidateMysql(site.Mysql); err != nil {
		return nil, err
	}
	// 域名冲突检查
	existing, err := s.db.ListSites()
	if err != nil {
		return nil, err
	}
	for _, e := range existing {
		for _, d := range e.Domains {
			for _, nd := range site.Domains {
				if strings.EqualFold(d, nd) {
					return nil, fmt.Errorf("%w: 域名 %s 已被站点 %s 占用", model.ErrConflict, nd, e.Name)
				}
			}
		}
	}

	if site.ID == "" {
		site.ID = store.NewID()
	}
	// 客户端传来的 ID 一律不采用：ID 参与构成配置文件名与备份路径，
	// 任由请求体提供等于开放任意文件写（见 model.ValidateID 注释）。
	// 此处恒重新生成，确保落库的 ID 一定是服务端产生的十六进制串。
	site.ID = store.NewID()
	if site.Root.Path == "" && len(site.Domains) > 0 {
		site.Root.Path = "/www/wwwroot/" + strings.ReplaceAll(site.Domains[0], "*.", "_")
	}
	if err := validateSitePaths(site); err != nil {
		return nil, err
	}
	// 访问认证：明文口令换成 apr1 哈希，明文即刻丢弃（不入库、不回显）。
	// 必须放在 ID 生成之后：口令文件名就是站点 ID。
	if err := s.applyAccessAuth(site, ""); err != nil {
		return nil, err
	}
	if site.Status == "" {
		site.Status = model.SiteStatusRunning
	}
	s.ensureSiteDirs(site)

	// P0：落库前先校验新配置能被 nginx 接受。失败即返回——DB 不落库、
	// 不建 MySQL，磁盘上的旧 conf 已恢复，nginx 原文随错误回显给前端。
	if err := s.prevalidateSite(ctx, site); err != nil {
		return nil, err
	}

	// MySQL 联动：site.Mysql != nil 表示用户在前端勾了"创建数据库"。
	// 1) MySQL 子系统未装 → 直接拒绝（明确错误，不要"假装成功"）
	// 2) 子系统装了 → 调驱动建库+建用户+授全权；任意一步失败回滚（删已建部分）
	// 3) 密码由面板生成，命名由 siteID 推导，保证唯一+不可猜
	//
	// 顺序：**先落库，再真建**。
	//
	// 早期实现是反过来的（先建库后落库），于是落库失败（磁盘满 / SQLite
	// busy / 序列化异常）时，MySQL 上已经留下一个孤儿库 + 一个孤儿用户，
	// 而密码只存在于内存里的 site.Mysql.Password，随错误一起丢弃——
	// 用户永远拿不到这个密码，资源泄漏 + 数据不可达。
	//
	// 先落库还有个好处：建库失败时"要建库"的意图仍然留在库里，
	// 用户可以在面板上重试（错误用 wrap 透传，调用方可决定是
	// "重试建库"还是"清掉 site.Mysql 字段"）。
	if site.Mysql != nil {
		// 第一次落库：把"要建库"的意图先持久化（此时还没有密码）。
		if err := s.db.SaveSite(site); err != nil {
			return nil, err
		}
		if err := s.provisionMySQL(ctx, site); err != nil {
			return nil, fmt.Errorf("站点已创建，但 MySQL 建库失败: %w", err)
		}
		// 第二次落库：补上建库成功后生成的密码。
		if err := s.db.SaveSite(site); err != nil {
			return nil, fmt.Errorf("站点已创建，但保存数据库密码失败: %w", err)
		}
	} else if err := s.db.SaveSite(site); err != nil {
		return nil, err
	}
	// 口令文件必须在下发配置**之前**就位：nginx 解析配置时会打开它，
	// 文件不存在 → nginx -t 失败 → 整批配置回滚。
	if err := s.syncAccessFile(site); err != nil {
		return nil, err
	}
	// 配置下发失败不回滚站点数据：
	// 生产环境中 nginx 可能尚未就绪或配置有误，此时用户仍应能把站点建起来、
	// 在面板里继续编辑，修好后再手动重试下发。直接丢弃数据反而更糟。
	if err := s.ApplyAllConfig(ctx); err != nil {
		return site, fmt.Errorf("站点已创建，但配置下发失败: %w", err)
	}
	return site, nil
}

// validateSitePaths 校验站点涉及的全部路径。
//
// 面板以 root 运行，且这些路径会直接进入 nginx 配置（root/alias/log/cert），
// 所以必须在落库前拦住：
//   - Root.Path：站点根目录，也是最敏感的（nginx root 指向哪就能对外伺服哪）
//   - SubDir.Rules[].Root：子目录绑定目标，用 alias 注入
//   - Lock.Files：防篡改扫描目标，会被 filepath.Walk 全量读取
func validateSitePaths(site *model.Site) error {
	// 根目录与子目录绑定目标：既查文件系统语义（绝对路径、不许穿越、
	// 不许指向系统目录），也查 nginx 文本语义（不许带空格/引号/分号）。
	if err := model.ValidateSitePaths(site); err != nil {
		return err
	}
	for _, f := range site.Lock.Files {
		// 受保护文件允许相对路径（相对于站点根），但同样禁止穿越
		if err := model.ValidatePath(f); err != nil {
			if !filepath.IsAbs(f) {
				clean := filepath.Clean(f)
				if !strings.HasPrefix(clean, "..") && clean != ".." {
					continue
				}
			}
			return fmt.Errorf("%w: 防篡改目标 %q", err, f)
		}
	}
	return nil
}

// UpdateSite 更新站点。
func (s *Service) UpdateSite(ctx context.Context, id string, site *model.Site) (*model.Site, error) {
	old, err := s.db.GetSite(id)
	if err != nil {
		return nil, err
	}
	if len(site.Domains) == 0 {
		return nil, model.ErrInvalidDomain
	}
	if err := site.Domains.Validate(); err != nil {
		return nil, err
	}
	// 端口校验（同 CreateSite）。
	if s.publicPort == 0 {
		cleaned, err := model.ValidateListenPorts(site.ListenPorts)
		if err != nil {
			return nil, err
		}
		site.ListenPorts = cleaned
	} else {
		site.ListenPorts = nil
	}
	// 全字段校验：IP 名单 / UA 屏蔽词 / 反代目标 / 重定向 / 证书路径……
	// 这些值会被原样写进 nginx 配置文本，非法值要么改变配置语义，
	// 要么让 nginx -t 失败进而拖垮所有站点的配置下发。
	// 必须在落库前拦住，并把出错字段精确告诉用户。
	if err := model.ValidateSite(site); err != nil {
		return nil, err
	}
	// MySQL 绑定参数同样要过白名单（库名/用户名/字符集/端口）。
	if err := model.ValidateMysql(site.Mysql); err != nil {
		return nil, err
	}
	// 域名冲突检查（排除自身）
	existing, err := s.db.ListSites()
	if err != nil {
		return nil, err
	}
	for _, e := range existing {
		if e.ID == id {
			continue
		}
		for _, d := range e.Domains {
			for _, nd := range site.Domains {
				if strings.EqualFold(d, nd) {
					return nil, fmt.Errorf("%w: 域名 %s 已被站点 %s 占用", model.ErrConflict, nd, e.Name)
				}
			}
		}
	}

	// 保留不可变字段。
	//
	// PUT 是全量替换语义：请求体里没带的字段会被清零。
	// 下面这几个字段必须显式从 old 回填，否则会出现"改个名字，
	// 站点却自己活过来了"这类静默故障：
	//
	//   Status：被人工停用的站点会被当成"运行中"重新写盘并 reload，
	//           站点自动恢复对外服务——停用形同虚设。
	//   Edate：站点到期时间静默丢失。
	//   Mysql：站点与 MySQL 库的绑定丢失，之后 DeleteSite 的
	//           `if site.Mysql != nil` 判断不成立，MySQL 库与用户
	//           永久残留且面板再无从清理。
	//
	// Mysql 额外说明：它是**系统托管**的绑定（库名/用户名由站点 ID 推导、
	// 密码由面板生成），本就不该由客户端指定，所以这里直接沿用库里的值，
	// 既修了丢失问题，也堵住了"用更新接口改 MySQL 参数做注入"的路径。
	site.ID = id
	site.CreatedAt = old.CreatedAt
	if site.Root.Path == "" {
		site.Root.Path = old.Root.Path
	}
	if site.Status == "" {
		site.Status = old.Status
	}
	if site.Status == "" {
		site.Status = model.SiteStatusRunning
	}
	if site.Edate == nil {
		site.Edate = old.Edate
	}
	site.Mysql = old.Mysql
	// 访问认证：明文口令换成 apr1 哈希。
	// 请求体里 auth_pass 为空 = "不修改密码"，沿用库里的旧哈希；
	// 客户端提交的 auth_hash 一律丢弃（防止塞明文/弱格式做降级）。
	if err := s.applyAccessAuth(site, old.Access.AuthHash); err != nil {
		return nil, err
	}
	// cert 是跨实体引用（由证书绑定写入），全量替换的 PUT 语义下
	// 请求体不带该字段会把它清空，导致站点 SSL 配置静默丢失。
	if site.Cert == nil {
		site.Cert = old.Cert
	}
	if err := validateSitePaths(site); err != nil {
		return nil, err
	}
	s.ensureSiteDirs(site)

	// P0：落库前先校验新配置能被 nginx 接受。失败即返回——DB 不落库、
	// 磁盘上的旧 conf 已恢复，nginx 原文随错误回显给前端。
	if err := s.prevalidateSite(ctx, site); err != nil {
		return nil, err
	}

	if err := s.db.SaveSite(site); err != nil {
		return nil, err
	}
	// 口令文件先于配置下发就位（nginx 解析配置时就要读它）。
	if err := s.syncAccessFile(site); err != nil {
		return nil, err
	}
	// 同 CreateSite：更新已落库时不因下发失败而回滚。
	if err := s.ApplyAllConfig(ctx); err != nil {
		return site, fmt.Errorf("站点已更新，但配置下发失败: %w", err)
	}
	return site, nil
}

// DeleteSite 删除站点并清除配置。
func (s *Service) DeleteSite(ctx context.Context, id string) error {
	site, err := s.db.GetSite(id)
	if err != nil {
		return err
	}
	// MySQL 联动：先删库删用户，再删站点记录。
	// 顺序很关键——如果先删站点记录，site.Mysql 信息就丢了，
	// 残留的 MySQL 库永远成为孤儿。
	if site.Mysql != nil {
		if err := s.deprovisionMySQL(ctx, site); err != nil {
			// 删库失败时**不**回滚站点删除——把错误 wrap 返回，
			// 让 API 层告诉用户"站点已删，MySQL 库残留，请手动处理"。
			// 死锁在"因为 MySQL 删不掉所以站点也删不掉"更糟。
			log.Printf("[site/delete] MySQL 清理失败: site=%s db=%s err=%v",
				site.ID, site.Mysql.Name, err)
		}
	}
	if err := s.db.DeleteSite(id); err != nil {
		return err
	}
	// 已有备份不随删站连坐删除：备份记录与文件独立于站点存活，
	// 用户可以下载/恢复（站点没了备份还在是备份的本意）。
	// 无人认领的备份文件由 backup.PruneOrphanFiles（计划任务）按记录裁剪。
	// （历史遗留：这里曾 RemoveAll 一个按站点分目录的路径——该目录自 v0.1
	// 起就没有任何写入方，属于死代码，v0.7.1 清理。）
	// 清理访问认证口令文件：留一份没人知道内容的哈希在盘上没有意义。
	_ = s.removeAccessFile(id)
	return s.ApplyAllConfig(ctx)
}

// provisionMySQL 为新站点建库+建用户+授权。
//
// 调用前提：site.ID 已确定、site.Mysql 非 nil。
// 副作用：填充 site.Mysql 的 Name/User/Host/Port/Charset/Password 字段。
//
// 错误语义：任一步失败时回滚已建的部分（建了 user 就 drop user，
// 建了 db 就 drop db），不留半残局。
func (s *Service) provisionMySQL(ctx context.Context, site *model.Site) error {
	if s.mysql == nil {
		return model.ErrMySQLNotAvailable
	}

	// 用户名 / 密码 / 字符集一律由面板决定，**不接受客户端指定**。
	//
	// 原因：这三个值最终会原样拼进 MySQL 的 DDL
	//（CREATE USER / CREATE DATABASE / GRANT），
	// 而 MySQL 协议里 DDL 不支持 prepared statement 的占位符参数化，
	// 只能靠字符串拼接。客户端可控的值一旦拼进去就是注入面。
	//
	// 早期版本只在字段为空时才填充，等于"客户端传什么就用什么"，
	// 结果一个 write scope 的 Token 就能造出带 SUPER 权限的 MySQL 账号，
	// 甚至在删站时把面板自己的 root 账号删掉。
	//
	// 前端本来就只传空对象（web/js/views/site-create.js: `payload.mysql = {}`），
	// 所以这条约束不会影响正常流程。
	if site.Mysql.User != "" {
		return fmt.Errorf("%w: 数据库用户名由面板按站点 ID 自动生成，不接受自定义",
			model.ErrBadRequest)
	}
	if site.Mysql.Password != "" {
		return fmt.Errorf("%w: 数据库密码由面板自动生成，不接受自定义",
			model.ErrBadRequest)
	}
	if site.Mysql.Host != "" && site.Mysql.Host != "localhost" && site.Mysql.Host != "127.0.0.1" {
		return fmt.Errorf("%w: 数据库地址仅支持 localhost", model.ErrBadRequest)
	}
	if site.Mysql.Charset != "" && !model.ValidMysqlCharset(site.Mysql.Charset) {
		return fmt.Errorf("%w: 数据库字符集仅支持 %s", model.ErrBadRequest,
			strings.Join(model.MysqlAllowedCharsets, " / "))
	}

	// 推导 db/user 名（基于 siteID 前 12 hex，足够唯一+不可猜）
	name, err := mysql.NameForSite(site.ID)
	if err != nil {
		return fmt.Errorf("推导数据库名失败: %w", err)
	}
	// 用户名跟 db 名一致（BT 也是这么干的，简化心智：删 db 必删 user）
	site.Mysql.User = name
	if site.Mysql.Host == "" {
		site.Mysql.Host = "localhost"
	}
	// Host 安全检查：service 层再做一次，driver 层拒过但这里再保险一道
	if site.Mysql.Host != "localhost" && site.Mysql.Host != "127.0.0.1" {
		return fmt.Errorf("%w: host 仅支持 localhost", model.ErrBadRequest)
	}
	if site.Mysql.Port == 0 {
		site.Mysql.Port = 3306
	}
	if site.Mysql.Charset == "" {
		site.Mysql.Charset = "utf8mb4"
	}
	// 密码：一律面板生成（客户端不可指定，见上面的说明）。
	// 生成后立即生效在响应里返回（一次性明文）。
	site.Mysql.Password = mysql.GeneratePassword()
	site.Mysql.Name = name

	// 真建：DB → USER → GRANT，任一失败回滚
	if err := s.mysql.CreateDatabase(ctx, site.Mysql.Name, site.Mysql.Charset); err != nil {
		return fmt.Errorf("建库失败: %w", err)
	}
	if err := s.mysql.CreateUser(ctx, site.Mysql.User, site.Mysql.Password, site.Mysql.Host); err != nil {
		// user 失败：把刚建的 db 删掉
		_ = s.mysql.DropDatabase(ctx, site.Mysql.Name)
		return fmt.Errorf("建用户失败: %w", err)
	}
	if err := s.mysql.GrantPrivileges(ctx, site.Mysql.User, site.Mysql.Name, site.Mysql.Host); err != nil {
		// grant 失败：连 db+user 一起删
		_ = s.mysql.DropUser(ctx, site.Mysql.User, site.Mysql.Host)
		_ = s.mysql.DropDatabase(ctx, site.Mysql.Name)
		return fmt.Errorf("授权失败: %w", err)
	}
	return nil
}

// deprovisionMySQL 删站点时联动删 db+user。
// 即便 MySQL 子系统当前不可用（已下线/重启中），也不报错：
// 站点本身的删除不应该被 MySQL 拖死。
// 调用方已经记下"残留待清理"的状态。
func (s *Service) deprovisionMySQL(ctx context.Context, site *model.Site) error {
	if s.mysql == nil {
		return nil // 子系统下线，跳过
	}
	if site.Mysql == nil {
		return nil
	}
	// 先 revoke 权限（防止删 user 时还有 GRANT 引用）
	if err := s.mysql.RevokePrivileges(ctx, site.Mysql.User, site.Mysql.Host); err != nil {
		log.Printf("[mysql/revoke] %s: %v", site.Mysql.User, err)
	}
	// 再删 user
	if err := s.mysql.DropUser(ctx, site.Mysql.User, site.Mysql.Host); err != nil {
		return fmt.Errorf("删用户失败: %w", err)
	}
	// 最后删 db
	if err := s.mysql.DropDatabase(ctx, site.Mysql.Name); err != nil {
		return fmt.Errorf("删库失败: %w", err)
	}
	return nil
}

// ToggleSite 启停站点。
func (s *Service) ToggleSite(ctx context.Context, id string, status model.SiteStatus) error {
	site, err := s.db.GetSite(id)
	if err != nil {
		return err
	}
	if status != model.SiteStatusRunning && status != model.SiteStatusStopped {
		return model.ErrBadRequest
	}
	site.Status = status
	if err := s.db.SaveSite(site); err != nil {
		return err
	}
	return s.ApplyAllConfig(ctx)
}

// SiteSummaries 返回站点列表（带流量/证书/告警汇总）。
func (s *Service) SiteSummaries() ([]*model.SiteSummary, error) {
	sites, err := s.db.ListSites()
	if err != nil {
		return nil, err
	}
	alertCount, _ := s.db.CountOpenAlertsBySite()

	var out []*model.SiteSummary
	for _, site := range sites {
		sum := &model.SiteSummary{Site: site}
		reqs, monthBytes, err := s.db.GetTrafficSummary(site.ID)
		if err == nil {
			sum.TodayReqs = reqs
			sum.MonthBytes = monthBytes
			sum.TodayBytes = monthBytes // 简化：以当月均值近似展示，实际值由 stats 接口提供
		}
		if site.Cert != nil {
			sum.CertDays = site.Cert.DaysLeft()
		}
		sum.TamperCount, _ = s.db.CountTamperEvents(site.ID)
		sum.OpenAlerts = alertCount[site.ID]
		out = append(out, sum)
	}
	return out, nil
}

// SiteConfig 预览站点渲染后的配置。
func (s *Service) SiteConfig(ctx context.Context, id string) (nginx.RenderResult, bool, string, error) {
	site, err := s.db.GetSite(id)
	if err != nil {
		return nginx.RenderResult{}, false, "", err
	}
	res, err := nginx.Render(site, s.renderOptions(site))
	if err != nil {
		return nginx.RenderResult{}, false, "", err
	}
	ok, msg, _ := s.drv.NginxTest(ctx)
	return res, ok, msg, nil
}

// CollectTraffic 汇总站点访问日志到统计表。
func (s *Service) CollectTraffic(site *model.Site) error {
	if !site.Logs.AccessLog {
		return nil
	}
	logPath := filepath.Join(site.Root.Path, "logs", "access.log")
	stat, err := logmgr.ReadAndAggregate(logPath, site.ID)
	if err != nil || stat == nil {
		return err
	}
	return s.db.SaveTrafficStat(stat)
}

// EstablishBaseline 建立防篡改基线（防复改）。
func (s *Service) EstablishBaseline(site *model.Site) (int, error) {
	if !site.Lock.Enabled || len(site.Lock.Files) == 0 {
		return 0, model.ErrBadRequest
	}
	// 注意：Diff 返回 (TamperDiff, []Fingerprint)，建立基线要的是第二个返回值。
	// 早期写成 diff, _ := ... 然后遍历 diff.Current，而 TamperDiff.Current
	// 永远不会被 Diff 填充，于是基线恒为空——后续检测把所有文件都报成"新增"，
	// 真正的"内容被改"反而检测不出来。
	_, current := logmgr.Diff(site.Root.Path, site.Lock.Files, nil)

	files := map[string]store.FileHash{}
	for _, fp := range current {
		files[fp.Path] = store.FileHash{Hash: fp.Hash, Size: fp.Size, Mode: fp.Mode}
	}
	if err := s.db.ReplaceIntegrity(site.ID, files); err != nil {
		return 0, err
	}
	return len(files), nil
}

// CheckTamper 检测站点文件篡改。
func (s *Service) CheckTamper(site *model.Site) (logmgr.TamperDiff, error) {
	if !site.Lock.Enabled {
		return logmgr.TamperDiff{}, nil
	}
	base, err := s.db.GetIntegrity(site.ID)
	if err != nil {
		return logmgr.TamperDiff{}, err
	}
	baseline := map[string]logmgr.Fingerprint{}
	for path, fh := range base {
		baseline[path] = logmgr.Fingerprint{Hash: fh.Hash, Size: fh.Size, Mode: fh.Mode}
	}

	diff, _ := logmgr.Diff(site.Root.Path, site.Lock.Files, baseline)
	if diff.IsClean() {
		return diff, nil
	}

	// 记录篡改事件
	now := time.Now()
	for _, p := range diff.Modified {
		_ = s.db.SaveTamperEvent(&store.TamperEvent{
			ID: store.NewID(), SiteID: site.ID, Path: p,
			OldHash: baseline[p].Hash, Kind: "modified", CreatedAt: now,
		})
	}
	for _, p := range diff.Added {
		_ = s.db.SaveTamperEvent(&store.TamperEvent{
			ID: store.NewID(), SiteID: site.ID, Path: p, Kind: "added", CreatedAt: now,
		})
	}
	for _, p := range diff.Deleted {
		_ = s.db.SaveTamperEvent(&store.TamperEvent{
			ID: store.NewID(), SiteID: site.ID, Path: p,
			OldHash: baseline[p].Hash, Kind: "deleted", CreatedAt: now,
		})
	}

	// 触发告警
	if site.Lock.AlertOnTamper {
		for _, rule := range site.Alert.Rules {
			if rule.Type == "tamper" && rule.Enabled {
				_ = s.eng.Raise(site, rule, fmt.Sprintf(
					"检测到 %d 个文件被篡改（修改 %d / 新增 %d / 删除 %d）",
					diff.Total(), len(diff.Modified), len(diff.Added), len(diff.Deleted)))
				break
			}
		}
	}
	return diff, nil
}

// ---------------------------------------------------------------------------
// 配置下发问题记录
// ---------------------------------------------------------------------------

// setConfigIssues 记录本次配置下发中被隔离的问题站点。
func (s *Service) setConfigIssues(issues []string) {
	s.configIssuesMu.Lock()
	defer s.configIssuesMu.Unlock()
	if len(issues) == 0 {
		s.configIssues = nil
		return
	}
	s.configIssues = append([]string(nil), issues...)
}

// ConfigIssues 返回最近一次配置下发中被隔离的问题站点说明。
//
// 为空表示全部站点配置都已正常下发。
func (s *Service) ConfigIssues() []string {
	s.configIssuesMu.Lock()
	defer s.configIssuesMu.Unlock()
	return append([]string(nil), s.configIssues...)
}

// safeOneLine 把任意文本压成单行。
//
// 用于写进 nginx 注释行与错误信息：站点名里如果带换行，注释会被截断，
// 后面那行会被 nginx 当成真正的指令执行。
func safeOneLine(v string) string {
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	return strings.TrimSpace(v)
}

// ---------------------------------------------------------------------------
// 软件商店（目录 + 已安装检测）
// ---------------------------------------------------------------------------

// ListApps 返回软件商店目录与每条软件的安装状态。
//
// 只做检测，不执行安装/卸载：安装动作以 InstallHint 命令提示的形式交给运维。
// 探测复用现有子系统（NginxStatus / PHPVersions / mysql.Status），
// 其余走 exec 固定命令白名单（无任何用户输入拼接），超时 3 秒。
func (s *Service) ListApps(ctx context.Context) ([]model.App, error) {
	out := make([]model.App, 0, len(apps.Catalog))
	for _, e := range apps.Catalog {
		app := model.App{
			ID:          e.ID,
			Name:        e.Name,
			Category:    e.Category,
			Description: e.Description,
			InstallHint: e.InstallHint,
		}
		switch e.ID {
		case "nginx":
			if st, err := s.drv.NginxStatus(ctx); err == nil && st.Installed {
				app.Status = model.AppInstalled
				app.Version = st.Version
			}
		case "php":
			if vers, err := s.drv.PHPVersions(ctx); err == nil && len(vers) > 0 {
				app.Status = model.AppInstalled
				app.Version = joinVersions(vers)
			}
		case "mysql":
			if s.mysql != nil {
				if st, err := s.mysql.Status(ctx); err == nil && st.Installed {
					app.Status = model.AppInstalled
					app.Version = st.Version
					if app.Version == "" {
						app.Version = st.Server
					}
				}
			}
		case "redis":
			if v, ok := probeVersion(ctx, "redis-server", "--version"); ok {
				app.Status, app.Version = model.AppInstalled, v
			}
		case "docker":
			if v, ok := probeVersion(ctx, "docker", "--version"); ok {
				app.Status, app.Version = model.AppInstalled, v
			}
		case "nodejs":
			if v, ok := probeVersion(ctx, "node", "--version"); ok {
				app.Status, app.Version = model.AppInstalled, v
			}
		case "memcached":
			if v, ok := probeVersion(ctx, "memcached", "--version"); ok {
				app.Status, app.Version = model.AppInstalled, v
			}
		}
		if app.Status == "" {
			app.Status = model.AppAvailable
		}
		out = append(out, app)
	}
	return out, nil
}

// joinVersions 把 PHP 版本列表拼成逗号分隔串。
func joinVersions(vers []driver.PHPVersion) string {
	parts := make([]string, 0, len(vers))
	for _, v := range vers {
		if v.Version != "" {
			parts = append(parts, v.Version)
		}
	}
	return strings.Join(parts, ", ")
}

// probeVersion 用固定命令探测软件版本（白名单，无用户输入）。
// 失败/超时返回 ("", false)。只取第一行，避免多行输出污染展示。
func probeVersion(ctx context.Context, bin string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(out))
	if i := strings.IndexByte(v, '\n'); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v, v != ""
}
