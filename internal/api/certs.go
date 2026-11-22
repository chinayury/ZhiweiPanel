package api

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/zhiwei/panel/internal/acme"
	"github.com/zhiwei/panel/internal/model"
)

// ---------- 证书 ----------

func (s *Server) handleCerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	certs, err := s.svc.DB().ListCerts()
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"certs": certs})
}

// issueRequestBody 证书申请请求。
type issueRequestBody struct {
	Domains     []string          `json:"domains"`
	SiteID      string            `json:"site_id"`
	AccountID   string            `json:"account_id"`
	Method      string            `json:"method"`
	AutoRenew   bool              `json:"auto_renew"`
	DNSProvider string            `json:"dns_provider"`
	DNSConfig   map[string]string `json:"dns_config"`
}

// handleCertIssue 申请证书。
func (s *Server) handleCertIssue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body issueRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(body.Domains) == 0 {
		writeError(w, http.StatusBadRequest, model.ErrInvalidDomain)
		return
	}

	// DNS-01 必须校验 provider 与凭据，否则要到 lego 内部才报错，
	// 错误信息难以定位。
	if body.Method == "dns" {
		if err := acme.ValidateDNSConfig(body.DNSProvider, body.DNSConfig); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	req := acme.IssueRequest{
		Domains:     body.Domains,
		SiteID:      body.SiteID,
		AccountID:   body.AccountID,
		Method:      body.Method,
		AutoRenew:   body.AutoRenew,
		DNSProvider: body.DNSProvider,
		DNSConfig:   body.DNSConfig,
	}

	// HTTP-01 需要把挑战文件写进站点根目录，因此必须定位到站点。
	if req.Method != "dns" {
		roots := map[string]string{}
		if body.SiteID != "" {
			site, err := s.svc.DB().GetSite(body.SiteID)
			if err != nil {
				writeModelError(w, err)
				return
			}
			for _, d := range body.Domains {
				roots[d] = site.Root.Path
			}
		} else {
			// 未指定站点时，按域名自动匹配已存在的站点根目录
			sites, err := s.svc.DB().ListSites()
			if err != nil {
				writeModelError(w, err)
				return
			}
			for _, d := range body.Domains {
				found := false
				for _, site := range sites {
					for _, sd := range site.Domains {
						if strings.EqualFold(sd, d) {
							roots[d] = site.Root.Path
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					writeError(w, http.StatusBadRequest,
						model.ErrBadRequest)
					return
				}
			}
		}
		req.WebrootRoots = roots
	}

	res, err := s.svc.ACME().Issue(r.Context(), req)
	if err != nil {
		writeModelError(w, err)
		return
	}

	// 申请成功后自动绑定到对应站点并开启 SSL
	if body.SiteID != "" {
		s.bindCertToSite(body.SiteID, res.Cert)
	}

	writeJSON(w, http.StatusCreated, res.Cert)
}

// bindCertToSite 把证书绑定到站点并重载配置。
func (s *Server) bindCertToSite(siteID string, cert *model.Cert) {
	site, err := s.svc.DB().GetSite(siteID)
	if err != nil {
		return
	}
	site.Cert = &model.CertRef{
		CertID:    cert.ID,
		Domains:   cert.Domains,
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		Issuer:    cert.Issuer,
		Source:    cert.Source,
	}
	site.SSL.Enabled = true
	site.SSL.CertFile = cert.CertFile
	site.SSL.KeyFile = cert.KeyFile
	if len(site.SSL.Protocols) == 0 {
		site.SSL.Protocols = []string{"TLSv1.2", "TLSv1.3"}
	}
	if _, err := s.svc.UpdateSite(contextTODO(), siteID, site); err != nil {
		// 绑定失败不影响证书本身已签发的事实；但绝不能静默——
		// 早期版本这里直接 return，用户拿到 201 却发现站点没绑上证书，
		// 排查只能翻 DB（2026-11-22 实测踩坑：UpdateSite 的 prevalidateSite
		// 因 docker 全局 nginx.conf 与站点渲染的 shared:SSL 尺寸冲突而失败，
		// 现场毫无线索）。至少把失败原因记进日志。
		log.Printf("[api] 证书 %s 自动绑定站点 %s 失败: %v", cert.ID, siteID, err)
		return
	}
}

// handleCertByID 处理 /api/certs/{id} 及其子动作。
func (s *Server) handleCertByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/certs/")
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
		cert, err := s.svc.DB().GetCert(id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cert)

	case "renew":
		if !requirePost(w, r) {
			return
		}
		cert, err := s.svc.DB().GetCert(id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		// 续签同样需要站点根目录承载挑战文件
		roots := s.webrootsFor(cert.Domains)
		renewed, err := s.svc.ACME().Renew(r.Context(), id, roots)
		if err != nil {
			writeModelError(w, err)
			return
		}
		// 必须把站点刷新到新证书并重新下发配置。
		//
		// 早期版本这里只把 renewed 原样写回响应，既没有重绑站点、
		// 也没有触发配置下发 —— 于是接口返回 200、界面能看到新的到期时间，
		// nginx 却继续 serve **旧证书**。等旧证书自然过期，
		// 站点 HTTPS 直接挂掉，而用户完全看不出原因（"续签"明明是成功的）。
		if err := s.svc.RebindRenewedCert(r.Context(), renewed); err != nil {
			// 重绑失败不回滚续签结果：新证书已经在库里且内容已覆盖，
			// 只是站点侧的元数据没刷新。告诉用户，让其手动重试。
			log.Printf("[api] 证书 %s 续签后重绑站点失败: %v", id, err)
			writeJSON(w, http.StatusOK, map[string]any{
				"cert":    renewed,
				"warning": "证书已续签成功，但站点绑定刷新失败，请检查站点配置后手动重新下发配置",
			})
			return
		}
		writeJSON(w, http.StatusOK, renewed)

	case "revoke":
		if !requirePost(w, r) {
			return
		}
		if err := s.svc.ACME().Revoke(id); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "download":
		cert, err := s.svc.DB().GetCert(id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		pem := readFileString(cert.CertFile)
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pem))

	case "bind":
		if !requirePost(w, r) {
			return
		}
		var body struct {
			SiteID string `json:"site_id"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cert, err := s.svc.DB().GetCert(id)
		if err != nil {
			writeModelError(w, err)
			return
		}
		s.bindCertToSite(body.SiteID, cert)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "delete":
		if !requirePost(w, r) {
			return
		}
		if err := s.svc.DB().DeleteCert(id); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeError(w, http.StatusNotFound, model.ErrNotFound)
	}
}

// webrootsFor 按域名匹配已存在站点的根目录。
func (s *Server) webrootsFor(domains []string) map[string]string {
	roots := map[string]string{}
	sites, err := s.svc.DB().ListSites()
	if err != nil {
		return roots
	}
	for _, d := range domains {
		for _, site := range sites {
			for _, sd := range site.Domains {
				if strings.EqualFold(sd, d) {
					roots[d] = site.Root.Path
					break
				}
			}
			if roots[d] != "" {
				break
			}
		}
	}
	return roots
}

// importRequestBody 导入证书请求。
type importRequestBody struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	CertPEM string   `json:"cert_pem"`
	KeyPEM  string   `json:"key_pem"`
}

// handleCertImport 导入外部证书。
func (s *Server) handleCertImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body importRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.CertPEM == "" || body.KeyPEM == "" {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	cert, err := s.svc.ACME().Import(body.Name, body.Domains,
		[]byte(body.CertPEM), []byte(body.KeyPEM))
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, cert)
}

// ---------- ACME 账户 ----------

func (s *Server) handleACMEAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		accs, err := s.svc.DB().ListACMEAccounts()
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accounts": accs})
	case http.MethodPost:
		var body struct {
			Email   string `json:"email"`
			CA      string `json:"ca"`
			KeyType string `json:"key_type"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		acc, err := s.svc.ACME().CreateAccount(r.Context(), body.Email, body.CA, body.KeyType)
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, acc)
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// ---------- 告警 ----------

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	unresolved := r.URL.Query().Get("resolved") == "false"
	alerts, err := s.svc.DB().ListAlerts(limit, unresolved)
	if err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (s *Server) handleAlertByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/alerts/")
	id := strings.Trim(rest, "/")
	if id == "" {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	if strings.HasSuffix(id, "/resolve") {
		// 有副作用的子动作必须 POST：GET 改状态会让"只读 Token"直接获得写权限。
		if !requirePost(w, r) {
			return
		}
		alertID := strings.TrimSuffix(id, "/resolve")
		if err := s.svc.DB().ResolveAlert(alertID); err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeError(w, http.StatusNotFound, model.ErrNotFound)
}

// handleAlertTest 测试通知渠道。
func (s *Server) handleAlertTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body struct {
		Channel model.AlertChannel `json:"channel"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.svc.AlertEngine().TestChannel(r.Context(), body.Channel); err != nil {
		writeModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
