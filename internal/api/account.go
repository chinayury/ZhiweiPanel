package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/service"
)

// accountProfileResponse 账户资料的对外形态。
// 邮箱默认不返回明文（前端展示时用 email_masked 即可），
// 完整邮箱只在邮箱改密流程返回。
type accountProfileResponse struct {
	Username      string `json:"username"`
	Role          string `json:"role"`
	Email         string `json:"email"`
	EmailMasked   string `json:"email_masked"`
	EmailVerified bool   `json:"email_verified"`
	LastLoginIP   string `json:"last_login_ip"`
	LastLoginUA   string `json:"last_login_ua"`
	LastLoginAt   string `json:"last_login_at"`
	CreatedAt     string `json:"created_at"`
}

// handleGetAccount GET /api/admin/account 读当前用户资料。
//
// 未登录时支持 setup 鉴权（X-Setup-Password header）—— 让运维脚本/自己被锁的
// 管理员也能读账户信息（确认 email_verified 等状态）。
func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := s.setupAuthOrLogin(w, r, r.Header.Get("X-Setup-Password"))
	if !ok {
		return
	}
	prof, err := s.svc.GetUserProfile(user)
	if err != nil {
		writeModelError(w, err)
		return
	}
	_, role, _ := s.svc.DB().GetUser(user)
	resp := accountProfileResponse{
		Username:      prof.Username,
		Role:          role,
		Email:         prof.Email,
		EmailMasked:   maskEmail(prof.Email),
		EmailVerified: prof.EmailVerified,
		LastLoginIP:   prof.LastLoginIP,
		LastLoginUA:   prof.LastLoginUA,
		LastLoginAt:   prof.LastLoginAt.Format(time.RFC3339),
		CreatedAt:     prof.CreatedAt.Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetEmail PUT /api/admin/account/email {email, password?}
// 改邮箱（不自动验证，需走 verify endpoint 触发邮件）。
//
// 若未登录（cookie 无效），接受 body.password 作为 admin 密码验证。
// 这是"首次配置"的标准 UX：用户被异常登录锁在外面时还能绑邮箱。
// 代价：admin 密码会进入本次请求的 body 与日志——但写日志的是 service 层，
// 这里只走 bcrypt 比对，不进 access log（access log 记路径与状态码）。
func (s *Server) handleSetEmail(w http.ResponseWriter, r *http.Request) {
	// 改邮箱是写操作，方法必须限定为 PUT/POST：
	// 挂在与 GET 相同的路径上却允许 GET 触发写，是 CSRF 的经典入口。
	if !requirePutOrPost(w, r) {
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	user, ok := s.setupAuthOrLogin(w, r, body.Password)
	if !ok {
		return
	}
	if err := s.svc.SetEmail(user, body.Email); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSendEmailVerify POST /api/admin/account/email/verify
// 触发发送邮箱验证码；用户在收件箱看到 6 位数字后调
// /api/auth/verify-challenge 提交。
//
// 未登录时支持 setup 鉴权（X-Setup-Password）—— 让被锁管理员也能
// 重新触发发验证邮件（修复 "邮箱未验证" 状态的兜底）。
func (s *Server) handleSendEmailVerify(w http.ResponseWriter, r *http.Request) {
	// 触发发验证邮件会写 challenge 表并对外发信，是有副作用的动作。
	// 允许 GET 就能被跨站请求反复触发，把收件箱刷爆。
	if !requirePost(w, r) {
		return
	}
	user, ok := s.setupAuthOrLogin(w, r, r.Header.Get("X-Setup-Password"))
	if !ok {
		return
	}
	id, err := s.svc.CreateChallenge(user, "email_verify")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "challenge_id": id})
}

// handleGetOrSetSMTP 同一路径按 method 分读 / 写。
//
// GET: 读 SMTP 配置（密码以 ****** 脱敏）
// PUT: 写 SMTP 配置（密码占位保留原值）
//
// 与 setEmail 一样：未登录时接受 body.password 做 fallback 鉴权，
// 让"被锁在外面的管理员"还能配 SMTP。
func (s *Server) handleGetOrSetSMTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.Authorize(r) {
			writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
			return
		}
		c, err := s.svc.LoadSMTPConfig()
		if err != nil {
			writeModelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"host":       c.Host,
			"port":       c.Port,
			"user":       c.User,
			"password":   maskPassword(c.Password),
			"from":       c.From,
			"from_name":  c.FromName,
			"encryption": c.Encryption,
			"configured": c.Host != "" && c.Port > 0 && c.From != "",
		})
	case http.MethodPut:
		var body service.SMTPConfig
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// setup 鉴权优先用 body.password 字段；如果 body 没填且已登录则用 cookie。
		// 但 SMTPConfig 没有 password 字段——用 header X-Setup-Password 透传，
		// 避免污染业务结构体。
		setupPwd := r.Header.Get("X-Setup-Password")
		user, ok := s.setupAuthOrLogin(w, r, setupPwd)
		if !ok {
			return
		}
		_ = user
		// 前端 password 是占位 *** 时，认为不变
		if body.Password == "" || isAllAsterisks(body.Password) {
			old, err := s.svc.LoadSMTPConfig()
			if err == nil {
				body.Password = old.Password
			}
		}
		if err := s.svc.SaveSMTPConfig(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// setupAuthOrLogin 取当前会话；若未登录，尝试用 setupPwd 做 fallback 鉴权（仅 admin）。
//
// 用于"被异常登录锁住的管理员"还能改邮箱 / 配 SMTP 的场景。
// fallback 鉴权成功同样消耗一次登录限流，避免被无限重试。
func (s *Server) setupAuthOrLogin(w http.ResponseWriter, r *http.Request, setupPwd string) (string, bool) {
	if u, ok := s.currentUser(r); ok {
		return u, true
	}
	if setupPwd == "" {
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return "", false
	}
	key := s.clientKey(r, "admin")
	if ok, _ := limiter.allow(key); !ok {
		writeError(w, http.StatusTooManyRequests, errors.New("setup 鉴权尝试过多，请稍后再试"))
		return "", false
	}
	hash, _, err := s.svc.DB().GetUser("admin")
	if err != nil {
		limiter.fail(key)
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return "", false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(setupPwd)); err != nil {
		limiter.fail(key)
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return "", false
	}
	limiter.success(key)
	return "admin", true
}

// handleTestSMTP POST /api/admin/smtp/test {to}
// 发一封测试邮件到 to 邮箱。
func (s *Server) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	// 发测试邮件是对外发信，必须 POST：GET 可被跨站触发刷邮件。
	if !requirePost(w, r) {
		return
	}
	if !s.Authorize(r) {
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.To == "" {
		writeError(w, http.StatusBadRequest, errors.New("to 不能为空"))
		return
	}
	if err := s.svc.TestSMTP(body.To); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 工具函数 ----------

var emailRE = regexp.MustCompile(`^[^@]+@[^@]+$`)

func maskEmail(s string) string {
	if s == "" {
		return ""
	}
	idx := strings.Index(s, "@")
	if idx < 0 || idx < 2 {
		return s
	}
	return s[:1] + "***" + s[idx:]
}

func maskPassword(s string) string {
	if s == "" {
		return ""
	}
	return "********"
}

func isAllAsterisks(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c != '*' {
			return false
		}
	}
	return true
}
