// Package main 是 ZhiweiPanel 的启动入口。
//
// 设计目标：编译产物是一个不依赖运行时的单文件二进制，
// scp 到服务器 chmod +x 即可运行，前端资源已内嵌。
package main

import (
	"context"
	"crypto/tls"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/zhiwei/panel/internal/acme"
	"github.com/zhiwei/panel/internal/alert"
	"github.com/zhiwei/panel/internal/api"
	"github.com/zhiwei/panel/internal/cron"
	"github.com/zhiwei/panel/internal/driver"
	"github.com/zhiwei/panel/internal/driver/dockerdriver"
	"github.com/zhiwei/panel/internal/driver/hostdriver"
	"github.com/zhiwei/panel/internal/mailer"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/mysql"
	"github.com/zhiwei/panel/internal/service"
	"github.com/zhiwei/panel/internal/store"
	"github.com/zhiwei/panel/internal/tlsgen"
	"github.com/zhiwei/panel/internal/version"
)

// webFS 内嵌前端静态资源。
//
//go:embed all:web
var webFS embed.FS

// Version 面板版本号。
//
// 默认值来自 internal/version —— 全仓唯一的版本号权威源。
//
// 之所以是 var 而不是 const：构建时可以用
// -ldflags "-X main.Version=x.y.z" 覆盖（deploy/Dockerfile 就是这么做的，
// 配合 build-arg 可以在不改源码的情况下打不同版本）。
// 但门禁（internal/version/version_test.go）会检查 Dockerfile 里
// 写死的默认值与权威源一致，避免"源码一个版本、镜像另一个版本"。
var Version = version.Version

func main() {
	var (
		port         = flag.Int("port", 8888, "面板 HTTPS 监听端口（启用 -tls 后）")
		httpRedirect = flag.Int("http-port", 8887, "HTTP 重定向端口（仅做 301 跳转 → HTTPS，0 表示禁用）")
		tlsEnable    = flag.Bool("tls", false, "启用 HTTPS（自签证书自动生成到 dataDir）")
		publicPort   = flag.Int("public-port", 0, "全局对外 HTTP 端口（0 = 用各站点自己的 listen_ports；非 0 = 所有站点都用这个端口，如 8080 用于整站搬迁到非标端口）")
		disableIPv6  = flag.Bool("disable-ipv6", false, "关闭 v6 listen 指令（部分老机器没 IPv6，nginx -t 会报 emerg）")
		tlsCert      = flag.String("tls-cert", "", "TLS 证书路径（留空则用 data/panel.crt）")
		tlsKey       = flag.String("tls-key", "", "TLS 私钥路径（留空则用 data/panel.key）")
		tlsHosts     = flag.String("tls-hosts", "localhost,127.0.0.1", "证书 SAN 列表（逗号分隔，DNS 域名或 IP）")
		dataDir      = flag.String("data", "/opt/zhiweipanel/data", "数据目录（数据库/证书/备份）")
		confDir      = flag.String("conf", "/opt/zhiweipanel/nginx/conf.d", "nginx conf.d 目录（宿主机模式）")
		siteConfDir  = flag.String("site-conf", "", "站点配置目录，留空则用 conf")
		certDir      = flag.String("cert-dir", "", "证书目录，留空则用 data/certs")
		backupDir    = flag.String("backup-dir", "", "备份目录，留空则用 data/backups")
		runtimeOpt   = flag.String("runtime", "host", "运行时：host 或 docker")
		nginxBin     = flag.String("nginx", "nginx", "nginx 可执行文件")
		nginxUnit    = flag.String("nginx-unit", "", "nginx 的 systemd 服务名（留空 = nginx）。nginx 由自定义单元托管时（如 aa_nginx.service、openresty.service）必须填，否则状态显示为未运行")
		container    = flag.String("container", "nginx", "docker 模式下的 nginx 容器名")
		hostConf     = flag.String("host-conf", "/opt/zhiweipanel/nginx/conf.d", "docker 模式宿主机挂载的 conf.d")
		adminUser    = flag.String("admin", "admin", "初始管理员用户名")
		adminPass    = flag.String("admin-pass", "", "初始管理员密码（仅首次启动生效）")
		showVersion  = flag.Bool("version", false, "显示版本号")
		trustedProxy = flag.String("trusted-proxy", "", "受信反向代理网段（CIDR，逗号分隔）。只有直连方落在这些网段里时才采信 X-Forwarded-For 作为客户端 IP；留空 = 一律使用直连地址（默认，最安全）")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("ZhiweiPanel %s\n", Version)
		return
	}

	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[zhiweipanel] ")

	// 目录准备
	if *siteConfDir == "" {
		*siteConfDir = *confDir
	}
	if *certDir == "" {
		*certDir = filepath.Join(*dataDir, "certs")
	}
	if *backupDir == "" {
		*backupDir = filepath.Join(*dataDir, "backups")
	}
	for _, dir := range []string{*dataDir, *confDir, *siteConfDir, *certDir, *backupDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("创建目录 %s 失败: %v", dir, err)
		}
	}

	// 数据库
	db, err := store.Open(filepath.Join(*dataDir, "zhiweipanel.db"))
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	// 管理员账户
	if *adminPass != "" {
		if err := api.EnsureAdminUser(db, *adminUser, *adminPass); err != nil {
			log.Fatalf("初始化管理员账户失败: %v", err)
		}
		log.Printf("管理员账户已就绪: %s", *adminUser)
		// 账户已就绪，明文密码不再需要：立即擦除 argv 中的密码，
		// 防止残留在 /proc/<pid>/cmdline（权限 0444，任何本地用户可读）。
		scrubAdminPassArg()
	} else if _, _, err := db.GetUser(*adminUser); err != nil {
		log.Printf("警告: 未指定 -admin-pass 且管理员账户不存在，首次登录将不可用")
	}

	// 把版本号写进 settings，前端顶栏与设置页才能显示。
	// 早期 Version 常量与 settings 之间没有通路，版本号位置恒为空。
	if err := db.SetSetting("version", Version); err != nil {
		log.Printf("警告: 写入版本号到 settings 失败: %v", err)
	}

	// 运行时驱动
	var drv driver.Driver
	switch strings.ToLower(*runtimeOpt) {
	case "docker":
		drv = dockerdriver.New(dockerdriver.Config{
			ContainerName:    *container,
			ContainerConfDir: "/etc/nginx/conf.d",
			HostConfDir:      *hostConf,
			HostSiteConfDir:  *siteConfDir,
			CertDir:          *certDir,
			DataDir:          *dataDir,
			LogDir:           filepath.Join(*dataDir, "logs"),
		})
	default:
		drv = hostdriver.New(hostdriver.Config{
			NginxBin:    *nginxBin,
			UnitName:    *nginxUnit,
			ConfDir:     *confDir,
			SiteConfDir: *siteConfDir,
			CertDir:     *certDir,
			DataDir:     *dataDir,
			LogDir:      filepath.Join(*dataDir, "logs"),
		})
	}
	log.Printf("运行时驱动: %s", drv.Runtime())

	// 组装服务
	acmeMgr := acme.NewManager(db, *certDir)
	alertEngine := alert.NewEngine(db)
	// mailer：从 settings 表里读 SMTP 配置初始化。若未配置则 IsConfigured()=false，
	// 后续 service.TestSMTP/CreateChallenge 会显式报错而不是闷头失败。
	mailerInst := mailer.New(mailer.Config{})
	if all, err := db.AllSettings(); err == nil {
		mailerInst = mailer.New(mailer.Config{
			Host:       all["smtp.host"],
			User:       all["smtp.user"],
			Password:   all["smtp.password"],
			From:       all["smtp.from"],
			FromName:   all["smtp.from_name"],
			Encryption: all["smtp.encryption"],
		})
		if p := all["smtp.port"]; p != "" {
			var port int
			fmt.Sscanf(p, "%d", &port)
			// mailer.Config 不可变，重新构造
			mailerInst = mailer.New(mailer.Config{
				Host: all["smtp.host"], Port: port,
				User: all["smtp.user"], Password: all["smtp.password"],
				From: all["smtp.from"], FromName: all["smtp.from_name"],
				Encryption: all["smtp.encryption"],
			})
		}
	}
	// MySQL/MariaDB 子系统：凭据文件存在才初始化客户端。文件由 install.sh
	// 写出（0600 权限），用户不安装 MySQL 时 service 收到 nil 自动降级。
	var mysqlClient *mysql.Client
	mysqlCredFile := mysql.CredFilePath(*dataDir)
	if _, statErr := os.Stat(mysqlCredFile); statErr == nil {
		if c, err := mysql.NewClient(mysqlCredFile); err == nil {
			mysqlClient = c
			log.Printf("[main] MySQL 子系统已就绪: %s", mysqlCredFile)
		} else {
			log.Printf("[main] MySQL 凭据存在但连接失败: %v（site.Mysql 建库会拒）", err)
		}
	} else {
		log.Printf("[main] MySQL 凭据文件 %s 不存在，子系统未启用（INSTALL_MYSQL=1 后重装可启用）", mysqlCredFile)
	}

	svc := service.New(db, drv, acmeMgr, alertEngine, mailerInst, mysqlClient, *dataDir, *certDir, *publicPort, *disableIPv6)
	defer func() {
		if mysqlClient != nil {
			_ = mysqlClient.Close()
		}
	}()

	// 防跨站隔离：生成或复用 catch-all default_server 用的 dummy 自签证书。
	// 没有这份证书就只能挡 80 端口，443 上 IP 直连会落到第一个真实站点
	// server 块并把用户证书品牌暴露给攻击者。
	sslDir := filepath.Join(*dataDir, "ssl")
	if _, _, _, err := tlsgen.EnsureDefault(
		filepath.Join(sslDir, "dummy.crt"),
		filepath.Join(sslDir, "dummy.key"),
	); err != nil {
		log.Fatalf("生成 catch-all dummy 证书失败: %v", err)
	}

	// 注入月流量统计（供配额告警使用）
	alert.SetQuotaStatProvider(func(site *modelSite) int64 {
		_, monthBytes, err := db.GetTrafficSummary(site.ID)
		if err != nil {
			return 0
		}
		return monthBytes
	})

	// 注入站点探测：用主域名做一次真实 HTTP 请求。
	// 不注入的话 site_down 规则恒返回 true（空钩子），永远不告警。
	alert.SetSiteProbeProvider(func(site *modelSite) bool {
		if len(site.Domains) == 0 {
			return false
		}
		scheme := "http"
		if site.SSL.Enabled {
			scheme = "https"
		}
		client := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := client.Get(scheme + "://" + site.Domains[0] + "/")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode < 500
	})

	// 注入防篡改检测：checker 本身没有 DB 访问能力，
	// 由 service 执行比对后回传是否发现篡改。
	alert.SetTamperChecker(func(site *modelSite) bool {
		if !site.Lock.Enabled || !site.Lock.AlertOnTamper {
			return false
		}
		diff, err := svc.CheckTamper(site)
		if err != nil {
			log.Printf("防篡改检测：站点 %s 失败: %v", site.Name, err)
			return false
		}
		return !diff.IsClean()
	})

	// 注入配置漂移检测：对比 conf.d 当前内容与最近一次成功下发的快照，
	// 有面板以外的改动（certbot / 手工编辑）时触发 config_drift 告警。
	alert.SetConfigDriftChecker(func() (bool, string) {
		return svc.CheckConfigDrift()
	})

	// 首次启动下发一次配置
	if err := svc.ApplyAllConfig(context.Background()); err != nil {
		log.Printf("初始配置下发失败（可稍后在控制台手动重试）: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// v0.2.2: 启动宿主机指标采集。后台定时采样 + 内存缓存，
	// HTTP 查询直接读缓存，不会阻塞。
	svc.StartMetricsCollector(ctx)

	// v0.2.0: 启动计划任务调度器。后台 goroutine 每 30s 扫一次 DB，
	// 把 enabled=1 且 next_run_at<=now 的任务 fork 执行。
	cronSched := cron.New(svc, db, alertEngine)
	cronSched.Start(ctx)
	defer cronSched.Stop()

	// 后台任务：告警检查
	checker := alert.NewChecker(drv, alertEngine, func() ([]*modelSite, error) {
		return db.ListSites()
	})
	go checker.Run(ctx, 5*time.Minute)

	// 后台任务：采集访问日志到流量统计表。
	// 必须接上：CollectTraffic 早期实现了却没有调用方，导致 traffic_stats
	// 恒为空 → 今日请求/流量恒显示 0、月配额强制永不生效、配额告警永不触发。
	go runTrafficCollector(ctx, svc, db)

	// 后台任务：自动续签证书
	go runAutoRenew(ctx, svc, acmeMgr)

	// 后台任务：清理过期会话
	go runSessionCleaner(ctx, db)

	// HTTP 服务
	// secureCookie 必须与 TLS 状态联动：开了 HTTPS 才给 session cookie 加
	// Secure 标志（Secure cookie 在纯 HTTP 下浏览器会拒绝写入 → 用户登不上）。
	srv := api.NewServer(svc, *tlsEnable, splitAndTrim(*trustedProxy))
	mux := srv.Routes()
	mux.Handle("/ws/logs", authWS(srv, http.HandlerFunc(srv.HandleWebSocket)))
	mux.Handle("/", staticHandler())

	handler := securityHeaders(mux, *tlsEnable)
	// 超时必须配齐：只有 ReadHeaderTimeout 时，慢速发送请求体或慢速
	// 接收响应的连接会一直占着 fd 与 goroutine（Slowloris）。
	// WriteTimeout 对 WebSocket 长连接不适用——升级后由 gorilla 自行管理，
	// 因此这里只覆盖普通 HTTP 请求。
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", *port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 可变集合记录需要 Shutdown 的 server。HTTP 301 重定向 server 视 -http-port 决定。
	var httpSrvs []*http.Server

	if *tlsEnable {
		// TLS 模式：先确保自签证书存在，再起 HTTPS 主服务
		certPath := *tlsCert
		if certPath == "" {
			certPath = filepath.Join(*dataDir, "panel.crt")
		}
		keyPath := *tlsKey
		if keyPath == "" {
			keyPath = filepath.Join(*dataDir, "panel.key")
		}
		hosts := splitCSV(*tlsHosts)
		if _, _, created, err := tlsgen.EnsureForPanel(certPath, keyPath, hosts); err != nil {
			log.Fatalf("准备面板证书失败: %v", err)
		} else if created {
			log.Printf("已生成面板自签证书 → %s", certPath)
		}
		httpServer.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		go func() {
			log.Printf("ZhiweiPanel %s HTTPS 已启动 → https://0.0.0.0:%d (SAN=%v)", Version, *port, hosts)
			if err := httpServer.ListenAndServeTLS(certPath, keyPath); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTPS 服务启动失败: %v", err)
			}
		}()

		// HTTP 重定向 server：仅 301 到 HTTPS，强制把管理员从明文升级到 TLS。
		// -http-port=0 → 不起（运维明确不要重定向的场景）。
		if *httpRedirect > 0 {
			redirSrv := &http.Server{
				Addr: fmt.Sprintf(":%d", *httpRedirect),
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// 用 r.Host 而不是仅靠 -port：用户可能绑了域名。
					host := r.Host
					if i := strings.LastIndex(host, ":"); i >= 0 {
						host = host[:i]
					}
					httpsURL := "https://" + host
					if *port != 443 {
						httpsURL += fmt.Sprintf(":%d", *port)
					}
					httpsURL += r.URL.RequestURI()
					// 308 永久：保留方法（POST 不会被改 GET）。缓存友好。
					w.Header().Set("Location", httpsURL)
					w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
					w.WriteHeader(http.StatusPermanentRedirect)
				}),
				ReadHeaderTimeout: 5 * time.Second,
				IdleTimeout:       30 * time.Second,
			}
			httpSrvs = append(httpSrvs, redirSrv)
			go func() {
				log.Printf("ZhiweiPanel HTTP 重定向已启动 → http://0.0.0.0:%d (301→https://...:%d)", *httpRedirect, *port)
				if err := redirSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Printf("HTTP 重定向服务退出: %v", err)
				}
			}()
		}
	} else {
		go func() {
			log.Printf("ZhiweiPanel %s 已启动 → http://0.0.0.0:%d (未启用 TLS，登录凭据明文传输)", Version, *port)
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTP 服务启动失败: %v", err)
			}
		}()
	}

	// 优雅退出
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("正在关闭…")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = httpServer.Shutdown(shutCtx)
	for _, s := range httpSrvs {
		_ = s.Shutdown(shutCtx)
	}
}

// scrubAdminPassArg 擦除命令行参数中的 -admin-pass 明文密码。
//
// 面板以 root 运行，-admin-pass 一旦进入 argv，就会残留在
// /proc/<pid>/cmdline（权限 0444，任何本地用户可读）。同机跑着
// 多个低权限 web 应用，任意一个被打穿即可读面板管理员密码，
// 完成"低权限 → 整机 root"的提权。
//
// EnsureAdminUser 完成后密码不再需要，直接改写 argv 底层内存清零，
// 使 /proc/cmdline 不再暴露明文。必须用 unsafe：Go string 不可变，
// 常规赋值改不了内核在 exec 时保留的 argv 原始内存。
func scrubAdminPassArg() {
	for i := 0; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "-admin-pass":
			wipeArg(i)
			if i+1 < len(os.Args) {
				wipeArg(i + 1)
			}
		case strings.HasPrefix(arg, "-admin-pass="):
			wipeArg(i)
		}
	}
}

// wipeArg 把 os.Args[i] 的底层字节清零。
func wipeArg(i int) {
	if i < 0 || i >= len(os.Args) {
		return
	}
	n := len(os.Args[i])
	if n == 0 {
		return
	}
	b := unsafe.Slice(unsafe.StringData(os.Args[i]), n)
	for j := range b {
		b[j] = 0
	}
}

// splitCSV 按逗号分割字符串，去前后空白，丢空段。
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// runTrafficCollector 定期把各站点访问日志聚合进流量统计表。
// 间隔 5 分钟：ReadAccessLog 是尾部窗口读取，重复执行是幂等的
// （同一行会被重复累加，因此这里用"自上次偏移以来的增量"由 logmgr 的
// ReadAndAggregate 保证——它按行解析并累加，调用方需接受近似值）。
func runTrafficCollector(ctx context.Context, svc *service.Service, db *store.DB) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sites, err := db.ListSites()
		if err != nil {
			log.Printf("流量采集：读取站点列表失败: %v", err)
			continue
		}
		for _, site := range sites {
			if err := svc.CollectTraffic(site); err != nil {
				log.Printf("流量采集：站点 %s 失败: %v", site.Name, err)
			}
		}
	}
}

// runAutoRenew 自动续签到期证书。
func runAutoRenew(ctx context.Context, svc *service.Service, mgr *acme.Manager) {
	// 启动后 1 分钟执行一次，之后每 12 小时一次
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	time.Sleep(1 * time.Minute)

	for {
		due, err := mgr.AutoRenewDue(15)
		if err != nil {
			log.Printf("检查待续签证书失败: %v", err)
		} else {
			for _, c := range due {
				log.Printf("证书 %s 剩余 %d 天，开始自动续签", c.Name, c.DaysLeft())
				sites, lerr := svc.DB().ListSites()
				if lerr != nil {
					log.Printf("证书 %s 续签中止：读取站点列表失败: %v", c.Name, lerr)
					continue
				}
				roots := map[string]string{}
				for _, site := range sites {
					for _, d := range site.Domains {
						for _, cd := range c.Domains {
							if strings.EqualFold(d, cd) {
								roots[cd] = site.Root.Path
							}
						}
					}
				}
				renewed, rerr := mgr.Renew(ctx, c.ID, roots)
				if rerr != nil {
					log.Printf("证书 %s 自动续签失败: %v", c.Name, rerr)
					continue
				}
				log.Printf("证书 %s 续签成功（证书 %s，有效期至 %s）",
					c.Name, renewed.ID, renewed.NotAfter.Format("2006-01-02"))

				// 关键一步：把站点的证书绑定刷新到新证书并重新下发配置。
				// Renew 现在复用原证书 ID，所以文件路径不变、内容被覆盖；
				// 站点侧的 CertRef 元数据与 nginx reload 由 RebindRenewedCert 负责。
				// 早期三个调用方都丢弃返回值，nginx 继续 serve 旧证——
				// 日志全绿、DB 有新证，站点却在旧证到期时静默下线，
				// 且每 12 小时泄漏一份孤儿证书文件。
				if err := svc.RebindRenewedCert(ctx, renewed); err != nil {
					log.Printf("证书 %s 续签后重绑站点失败: %v", c.Name, err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runSessionCleaner 定期清理过期会话与验证码。
func runSessionCleaner(ctx context.Context, db *store.DB) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = db.CleanSessions()
			_ = db.CleanExpiredChallenges()
		}
	}
}

// authWS 为 WebSocket 复用 Cookie 鉴权。
func authWS(srv *api.Server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WebSocket 升级请求无法依赖中间件链，这里必须做真实会话校验。
		// 早期只检查 cookie 是否存在（r.Cookie 的 error），那等于没鉴权：
		// 任意客户端带一个垃圾 token 就能实时拖取任意站点的访问日志，
		// 里面含访客 IP、UA、请求路径。
		if !srv.Authorize(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// staticHandler 提供内嵌前端资源，并把未知路径回退到 index.html（支持前端 hash 路由之外的路径）。
//
// 响应统一加 Cache-Control: no-cache：前端代码通过 //go:embed 嵌入 binary，
// 重新部署时 binary 改了但 URL 不变，浏览器可能用磁盘缓存的旧 JS 解析新 HTML，
// 引出 "Unexpected token '}'" 这类 SyntaxError。no-cache 强制每次向服务器
// 询问（304 命中时不下载），不存到磁盘缓存。对面板这种内部工具，
// 一致性 > 节省一个 304 请求。
func staticHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("加载前端资源失败: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 在 fileServer 之前 set，因为 fileServer 内部会调 w.WriteHeader
		// 让 add_header 模式跟 server 块的缓存策略隔离，避免误给
		// 公开站点 /api/system/version 这类端点加了 no-cache 头。
		w.Header().Set("Cache-Control", "no-cache")
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(sub, clean); err != nil {
			// SPA 回退
			data, rerr := fs.ReadFile(sub, "index.html")
			if rerr != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// securityHeaders 添加基础安全响应头。
//
// secure=true 时额外下发 HSTS：浏览器一年内的同 host 请求会强制 HTTPS，
// 防被动降级（咖啡店 MITM 强插 HTTP）。HSTS 不可下发到 HTTP 响应（部分
// 浏览器忽略），所以 HTTP 301 重定向那一跳单独在 redir handler 里也下发了一份。
func securityHeaders(next http.Handler, secure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		if secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// modelSite 是 site 的类型别名，减少重复书写。
type modelSite = model.Site

// splitAndTrim 把逗号分隔的列表拆成去空格的切片。
//
// 用于 -trusted-proxy 这类"逗号分隔多值"的启动参数。
func splitAndTrim(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
