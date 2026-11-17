// Package api 提供 REST 接口与 WebSocket 实时日志。
package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/zhiwei/panel/internal/acme"
	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/mysql"
	"github.com/zhiwei/panel/internal/service"
)

// Server HTTP 服务。
type Server struct {
	svc          *service.Service
	secureCookie bool
	// trustedProxies 受信反向代理网段。
	//
	// 只有直连方落在这些网段里时，才采信 X-Forwarded-For 首段作为客户端 IP。
	// 留空 = 不信任任何代理，一律使用直连地址（这是最安全的默认）。
	// 详见 clientip.go。
	trustedProxies *trustedProxySet
}

// NewServer 创建 HTTP 服务。
//
// trustedProxies 是受信反向代理的 CIDR 列表；留空表示不采信
// X-Forwarded-For（默认，也是最安全的形态）。
func NewServer(svc *service.Service, secureCookie bool, trustedProxies []string) *Server {
	return &Server{
		svc:            svc,
		secureCookie:   secureCookie,
		trustedProxies: newTrustedProxySet(trustedProxies),
	}
}

// Routes 注册全部路由，返回 ServeMux 以便上层继续挂载 WebSocket 与静态资源。
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	// 认证
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/api/auth/me", s.handleMe)
	mux.HandleFunc("/api/auth/password", s.handleChangePassword)
	mux.HandleFunc("/api/auth/verify-challenge", s.handleVerifyChallenge)
	mux.HandleFunc("/api/auth/password-reset/request", s.handlePasswordResetRequest)
	mux.HandleFunc("/api/auth/password-reset/confirm", s.handlePasswordResetConfirm)

	// 需要鉴权的接口：统一包一层鉴权中间件。
	protected := http.NewServeMux()
	reg := func(pattern string, h http.HandlerFunc) {
		// 审计中间件放在鉴权之后：这样拿得到调用者身份，也拿得到响应码。
		protected.Handle(pattern, s.authMiddleware(s.auditMiddleware(h)))
	}
	reg("/api/sites", s.handleSites)
	reg("/api/sites/", s.handleSiteByID)
	reg("/api/certs", s.handleCerts)
	reg("/api/certs/issue", s.handleCertIssue)
	reg("/api/certs/import", s.handleCertImport)
	reg("/api/certs/", s.handleCertByID)
	reg("/api/acme/accounts", s.handleACMEAccounts)
	reg("/api/alerts", s.handleAlerts)
	reg("/api/alerts/test", s.handleAlertTest)
	reg("/api/alerts/", s.handleAlertByID)
	reg("/api/settings", s.handleSettings)
	reg("/api/system/status", s.handleSystemStatus)
	reg("/api/system/info", s.handleSystemInfo)
	reg("/api/system/mysql", s.handleMySQLStatus)
	reg("/api/system/nginx-config", s.handleNginxConfig)
	reg("/api/system/nginx-test", s.handleNginxTest)
	reg("/api/system/config-apply", s.handleConfigApply)
	reg("/api/system/logs/tail", s.handleTail)

	// v0.2.0: 计划任务 + 备份 + 站点批量操作。
	reg("/api/cron/jobs", s.handleCronJobs)
	reg("/api/cron/jobs/", s.handleCronJobByID)
	reg("/api/cron/jobs/run", s.handleCronJobRun)
	reg("/api/backups", s.handleBackups)
	reg("/api/backups/", s.handleBackupByID)
	reg("/api/sites/batch", s.handleSitesBatch)

	// v0.2.2：API Token（机器访问）+ 操作审计 + 系统运行指标 + 配置历史。
	reg("/api/tokens", s.handleTokens)
	reg("/api/tokens/", s.handleTokenByID)
	reg("/api/audit-logs", s.handleAuditLogs)
	reg("/api/audit-logs/export", s.handleAuditLogsExport)
	reg("/api/system/metrics", s.handleMetrics)
	reg("/api/system/config-history", s.handleConfigHistory)
	reg("/api/system/config-history/", s.handleConfigHistoryByID)

	// 管理员账户读 + 邮箱验证（已登录态）
	// handleGetAccount / handleSendEmailVerify 自管 setup 鉴权
	// （未登录也能读 + 触发发邮件，方便脚本/被锁管理员操作）
	mux.HandleFunc("/api/admin/account", s.handleGetAccount)
	mux.HandleFunc("/api/admin/account/email/verify", s.handleSendEmailVerify)
	reg("/api/admin/smtp/test", s.handleTestSMTP)

	// 邮箱写 + SMTP 读写：挂到主 mux 自管鉴权，PUT 允许未登录 + setup 鉴权
	// （被异常登录锁住的管理员仍能配）。GET SMTP 仍需登录。
	mux.HandleFunc("/api/admin/account/email", s.handleSetEmail)
	mux.HandleFunc("/api/admin/smtp", s.handleGetOrSetSMTP)
	reg("/api/dns-providers", s.handleDNSProviders)
	reg("/api/alert-types", s.handleAlertTypes)
	reg("/api/apps", s.handleApps)

	mux.Handle("/api/", protected)
	return mux
}

// ---------- 站点 ----------

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sums, err := s.svc.SiteSummaries()
		if err != nil {
			writeModelError(w, err)
			return
		}
		// 脱敏：列表视图不返回 site.Mysql.Password 明文，
		// 也不返回访问认证口令（明文与哈希都不返回）。
		for _, sum := range sums {
			if sum != nil && sum.Site != nil {
				sum.Site = sum.Site.SanitizeForRead()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"sites": sums})
	case http.MethodPost:
		site, err := bindSite(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// 记下"客户端请求时是否带了 Mysql"——用来在响应里决定要不要
		// 一次性明文回显密码。后续 GET 不再返回明文。
		wantDB := site.Mysql != nil
		created, applyErr := s.svc.CreateSite(service.WithApplyReason(r.Context(), "site.create"), site)
		if created == nil {
			writeModelError(w, applyErr)
			return
		}
		// 建库成功时响应体带 show_password:true + mysql.password 明文。
		// 这是密码唯一一次明文返回，前端要明显提示用户保存。
		// 之后所有读路径（GET /api/sites, GET /api/sites/{id}）都走 SafeCopy
		// 把 password 置空。
		//
		// 注意：这里用 SanitizeAccessSecrets 而不是 SanitizeForRead——
		// 前者只清访问认证口令，保留 mysql.password 明文；
		// 后者会把 mysql.password 也清掉，建库密码就永远拿不到了。
		if wantDB && created.Mysql != nil {
			writeJSON(w, http.StatusCreated, map[string]any{
				"site":          created.SanitizeAccessSecrets(),
				"show_password": true, // 前端据此展示"请保存密码"横幅
			})
			return
		}
		if applyErr != nil {
			// 站点已落库，只是配置没下发成功。用 202 明确区分，
			// 避免前端把它当成失败而丢弃用户的编辑。
			writeJSON(w, http.StatusAccepted, map[string]any{
				"site":    created.SanitizeForRead(),
				"warning": applyErr.Error(),
			})
			return
		}
		writeJSON(w, http.StatusCreated, created.SanitizeForRead())
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// handleSiteByID 处理 /api/sites/{id} 及子路径。
func (s *Server) handleSiteByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sites/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			site, err := s.svc.DB().GetSite(id)
			if err != nil {
				writeModelError(w, err)
				return
			}
			// 脱敏：详情视图不返回 site.Mysql.Password 明文
			writeJSON(w, http.StatusOK, site.SanitizeForRead())
		case http.MethodPut:
			site, err := bindSite(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			updated, applyErr := s.svc.UpdateSite(service.WithApplyReason(r.Context(), "site.update"), id, site)
			if updated == nil {
				writeModelError(w, applyErr)
				return
			}
			if applyErr != nil {
				writeJSON(w, http.StatusAccepted, map[string]any{
					"site":    updated.SanitizeForRead(),
					"warning": applyErr.Error(),
				})
				return
			}
			writeJSON(w, http.StatusOK, updated.SanitizeForRead())
		case http.MethodDelete:
			if err := s.svc.DeleteSite(service.WithApplyReason(r.Context(), "site.delete"), id); err != nil {
				writeModelError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		}

	case "toggle":
		// 有副作用的子动作必须 POST：GET 改状态会让"只读 Token"直接获得写权限。
		if !requirePost(w, r) {
			return
		}
		var body struct {
			Status model.SiteStatus `json:"status"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.svc.ToggleSite(service.WithApplyReason(r.Context(), "site.toggle"), id, body.Status); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "reload":
		// 有副作用的子动作必须 POST：GET 改状态会让"只读 Token"直接获得写权限。
		if !requirePost(w, r) {
			return
		}
		if err := s.svc.ApplyAllConfig(r.Context()); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "config":
		res, ok, msg, err := s.svc.SiteConfig(r.Context(), id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"site_conf":  res.SiteConf,
			"http_conf":  res.HTTPConf,
			"nginx_test": map[string]any{"ok": ok, "message": msg},
		})

	case "logs":
		s.handleSiteLogs(w, r, id)

	case "stats":
		days := 7
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
				days = n
			}
		}
		stats, err := s.svc.DB().GetTrafficStats(id, days)
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"stats": stats})

	case "files":
		s.handleSiteFiles(w, r, id)

	case "baseline":
		// 有副作用的子动作必须 POST。
		//
		// EstablishBaseline 会 ReplaceIntegrity —— 直接改写防篡改基线表。
		// 如果允许 GET，只读 Token 就能把基线刷成"当前状态"，
		// 于是已经发生的篡改会被洗成正常，防篡改检测形同虚设。
		if !requirePost(w, r) {
			return
		}
		site, err := s.svc.DB().GetSite(id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		n, err := s.svc.EstablishBaseline(site)
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": n})

	case "tamper":
		// 有副作用的子动作必须 POST。
		//
		// CheckTamper 名义上是"检测"，实际上会写库（SaveTamperEvent）
		// 并触发告警通知（Raise）。允许 GET 的话，只读 Token 就能：
		//   - 无限往 tamper_events 表插行；
		//   - 反复触发邮件 / webhook 通知，把告警通道刷爆。
		if !requirePost(w, r) {
			return
		}
		site, err := s.svc.DB().GetSite(id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		diff, err := s.svc.CheckTamper(site)
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"modified": diff.Modified,
			"added":    diff.Added,
			"deleted":  diff.Deleted,
			"total":    diff.Total(),
		})

	default:
		writeError(w, http.StatusNotFound, model.ErrNotFound)
	}
}

// ---------- 系统 ----------

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.Driver().SystemStatus(r.Context())
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleSystemInfo 返回面板运行参数（v0.2.1）。
// 给前端"系统状态"卡片和"新建站点"表单用：知道当前是不是全局 -public-port 模式。
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"public_port":  s.svc.PublicPort(), // 0 = 各站用 site.ListenPorts
		"disable_ipv6": s.svc.DisableIPv6(),
	})
}

// handleMySQLStatus 返回 MySQL/MariaDB 子系统状态。
// 不在鉴权后路由列表里也行——但放在 /api/system/mysql 跟 handleSystemStatus
// 并列，前端"系统状态卡片"能统一拉取。
func (s *Server) handleMySQLStatus(w http.ResponseWriter, r *http.Request) {
	mc := s.svc.MySQL()
	if mc == nil {
		// 子系统未初始化（未装 / 凭据文件缺失 / 连接失败）
		writeJSON(w, http.StatusOK, mysql.Status{Installed: false})
		return
	}
	st, err := mc.Status(r.Context())
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleNginxConfig(w http.ResponseWriter, r *http.Request) {
	content, err := s.svc.Driver().ReadConf(r.Context(), "nginx.conf")
	if err != nil {
		// 宿主机主配置位置因发行版而异，这里返回站点级汇总配置
		content, _ = s.svc.Driver().ReadConf(r.Context(), "00-zhiweipanel-http.conf")
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": content})
}

func (s *Server) handleNginxTest(w http.ResponseWriter, r *http.Request) {
	ok, msg, err := s.svc.Driver().NginxTest(r.Context())
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": msg})
}

func (s *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	// 全量下发配置是有副作用的操作，必须 POST。
	if !requirePost(w, r) {
		return
	}
	if err := s.svc.ApplyAllConfig(r.Context()); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDNSProviders(w http.ResponseWriter, r *http.Request) {
	// 返回完整信息（key / 中文名 / 凭据字段说明），前端据此动态渲染表单。
	writeJSON(w, http.StatusOK, map[string]any{"providers": acme.DNSProviderInfos()})
}

// handleAlertTypes 返回受支持的站点告警规则类型。
//
// 单一事实来源：清单定义在 internal/model.AlertRuleTypes，
// 与 internal/alert 的检查器一一对应。前端据此动态渲染下拉框，
// 不再在前端硬编码一份枚举——历史上两边各写一份，
// 前端多出的四种类型后端根本不认识，规则永远不触发。
func (s *Server) handleAlertTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": model.AlertRuleTypes})
}

// handleApps 返回软件商店目录与安装状态（已安装 / 可安装）。
func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	list, err := s.svc.ListApps(r.Context())
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": list})
}

// handlerCtx 提取请求上下文。
func handlerCtx(r *http.Request) context.Context { return r.Context() }
