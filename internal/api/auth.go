package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/service"
	"github.com/zhiwei/panel/internal/store"
	"github.com/zhiwei/panel/internal/twofa"
)

// sessionTTL 会话有效期。
const sessionTTL = 12 * time.Hour

// sessionCookie 会话 Cookie 名。
const sessionCookie = "zhiweipanel_session"

// 登录限流参数。无限制时 bcrypt cost 10 单次约 50-100ms CPU，
// 并发打满可占满所有核，同时也能离线枚举弱口令。
const (
	loginMaxAttempts = 5                // 窗口内允许的尝试次数
	loginWindow      = time.Minute      // 统计窗口
	loginLockFor     = 15 * time.Minute // 触顶后锁定时长
)

// loginLimiter 按某个键维度的滑动窗口限流器。
//
// max / window / lockFor 由构造时决定，于是同一份实现既能当
// "IP + 用户名"限流器，也能当"仅用户名"限流器。
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
	max     int
	window  time.Duration
	lockFor time.Duration
}

type loginEntry struct {
	fails      int
	firstFail  time.Time
	lockedTill time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		entries: map[string]*loginEntry{},
		max:     loginMaxAttempts,
		window:  loginWindow,
		lockFor: loginLockFor,
	}
}

// newLoginLimiterWith 构造一个阈值不同的限流器。
func newLoginLimiterWith(max int, window, lockFor time.Duration) *loginLimiter {
	return &loginLimiter{
		entries: map[string]*loginEntry{},
		max:     max,
		window:  window,
		lockFor: lockFor,
	}
}

// allow 返回是否放行；不放行时第二个返回值为剩余锁定秒数。
func (l *loginLimiter) allow(key string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	e := l.entries[key]
	if e == nil {
		l.entries[key] = &loginEntry{fails: 0, firstFail: now}
		return true, 0
	}
	if now.Before(e.lockedTill) {
		return false, int(time.Until(e.lockedTill).Seconds()) + 1
	}
	// 窗口已过则重置
	if now.Sub(e.firstFail) > l.window {
		e.fails = 0
		e.firstFail = now
	}
	return true, 0
}

// fail 记录一次失败；返回是否已触发锁定。
func (l *loginLimiter) fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	e := l.entries[key]
	if e == nil {
		e = &loginEntry{firstFail: now}
		l.entries[key] = e
	}
	if now.Sub(e.firstFail) > l.window {
		e.fails = 0
		e.firstFail = now
	}
	e.fails++
	if e.fails >= l.max {
		e.lockedTill = now.Add(l.lockFor)
		e.fails = 0
		e.firstFail = now
		return true
	}
	return false
}

// success 清除失败计数。
func (l *loginLimiter) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

var limiter = newLoginLimiter()

// accountLimiter 按用户名（不含 IP）维度的限流器。
//
// 为什么需要第二层：只按 "IP + 用户名" 限流时，攻击者换一个来源 IP
// 就拿到一份全新的计数，5 次锁定形同虚设，可以对管理员口令做无限次猜测。
// 账号级计数让"暴力破解某一个账号"这件事本身有上限。
//
// 阈值比 IP 级宽松（3 倍）：同一个账号确实可能从多个 IP 登录
// （办公室 + 家里 + 手机网络），而账号级锁定的代价是
// "这个账号在一段时间内谁都登不上"。
var accountLimiter = newLoginLimiterWith(loginMaxAttempts*3, loginWindow, loginLockFor)

// clientKey 限流键：可信客户端 IP + 用户名。
//
// IP 由 s.clientIP 决定（只有直连方是受信代理时才采信 X-Forwarded-For），
// 详见 clientip.go 的说明。
func (s *Server) clientKey(r *http.Request, username string) string {
	return s.clientIP(r) + "|" + strings.ToLower(strings.TrimSpace(username))
}

// loginRequest 登录请求。
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// passwordRequest 修改密码请求。
type passwordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// loginResponse 第一步登录响应。ok=false + need_verify=true 时前端
// 弹"已发验证码"输入框。
type loginResponse struct {
	OK          bool   `json:"ok"`
	Username    string `json:"username,omitempty"`
	Role        string `json:"role,omitempty"`
	NeedVerify  bool   `json:"need_verify,omitempty"`
	ChallengeID string `json:"challenge_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
	// TOTPRequired/LoginToken：用户启用了两步验证——密码已通过但还差动态口令。
	// login_token 是 2 分钟内一次性有效的临时凭据，交给 verify-totp 换 session。
	TOTPRequired bool   `json:"totp_required,omitempty"`
	LoginToken   string `json:"login_token,omitempty"`
}

// handleLogin 登录第一步：密码校验。
//
// 返回三种结果：
//  1. 密码错 / 用户不存在 → 401
//  2. 正常登录 → 下发 session cookie
//  3. 异常登录（IP/UA 异常）→ 返回 challenge_id，密码正确但不发 session，
//     等待前端 POST /api/auth/verify-challenge 通过后下发
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}

	// 限流必须在任何 bcrypt 计算之前：否则攻击者先用并发把 CPU 打满。
	//
	// 两层限流：
	//   key    = 可信 IP + 用户名（基本限流）
	//   acctKey= 仅用户名（防换 IP 绕过）
	// 只做第一层的话，攻击者换一个来源 IP 就拿到一份全新计数，锁定形同虚设。
	key := s.clientKey(r, req.Username)
	acctKey := strings.ToLower(strings.TrimSpace(req.Username))
	if ok, wait := limiter.allow(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeJSON(w, http.StatusTooManyRequests, errorResponse{
			Error: "尝试次数过多，请稍后再试",
			Code:  http.StatusTooManyRequests,
		})
		return
	}
	if ok, wait := accountLimiter.allow(acctKey); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeJSON(w, http.StatusTooManyRequests, errorResponse{
			Error: "该账号登录尝试过多，请稍后再试",
			Code:  http.StatusTooManyRequests,
		})
		return
	}

	hash, role, err := s.svc.DB().GetUser(req.Username)
	if err != nil {
		// 用户不存在时也做一次哈希校验，避免通过响应时间枚举用户名
		bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"), []byte(req.Password))
		limiter.fail(key)
		accountLimiter.fail(acctKey)
		// v0.2.2：登录失败也要留痕。暴力破解是审计日志最有价值的用途之一。
		s.handleAuditLogin(r, req.Username, "auth.login_failed", "用户名或密码错误", http.StatusUnauthorized)
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		if locked := limiter.fail(key); locked {
			w.Header().Set("Retry-After", strconv.Itoa(int(loginLockFor.Seconds())))
		}
		accountLimiter.fail(acctKey)
		s.handleAuditLogin(r, req.Username, "auth.login_failed", "密码错误", http.StatusUnauthorized)
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return
	}
	limiter.success(key)
	accountLimiter.success(acctKey)

	// 两步验证：已启用 TOTP 的用户，密码通过后进入动态口令阶段。
	// 此时**不发 session**、也跳过异常登录邮件检测——动态口令证明持有认证器，
	// 是比 IP/UA 异常更强的信号（两个第二因子串联没有额外收益）。
	if _, enabled, gerr := s.svc.DB().GetTOTP(req.Username); gerr == nil && enabled {
		tok, terr := s.newTOTPPending(req.Username)
		if terr != nil {
			writeError(w, http.StatusInternalServerError, errInternal)
			return
		}
		writeJSON(w, http.StatusOK, loginResponse{
			OK:           false,
			TOTPRequired: true,
			LoginToken:   tok,
			Username:     req.Username,
		})
		return
	}

	// 密码通过 → 异常登录检测
	ip := s.clientIP(r)
	ua := r.UserAgent()
	prof, perr := s.svc.GetUserProfile(req.Username)
	if perr == nil {
		// 首次配置豁免：admin 还没绑邮箱 + SMTP 未配时，异常登录直接放行。
		// 没邮箱 → 验证码没法送达；没 SMTP → 同理。让用户进系统后再补配，
		// 比"锁在外面让用户继续尝试"友好。攻击者仍需通过密码第一道。
		notificationReady := prof.Email != "" && s.svc.MailerReady()
		if notificationReady {
			anomaly := service.IsAnomalousLogin(prof.LastLoginIP, prof.LastLoginUA, prof.LastLoginAt,
				ip, ua, time.Now())
			if anomaly != nil {
				cid, cerr := s.svc.CreateChallenge(req.Username, "login_verify")
				if cerr != nil {
					log.Printf("[auth] abnormal login but challenge unavailable for %s: %v", req.Username, cerr)
					writeJSON(w, http.StatusForbidden, errorResponse{
						Error: "异常登录需要邮件验证，但发送失败。请联系管理员或检查 SMTP 配置。",
						Code:  http.StatusForbidden,
					})
					return
				}
				log.Printf("[auth] abnormal login for %s: %s (ip=%s ua=%s)", req.Username, anomaly.Reason, ip, ua)
				writeJSON(w, http.StatusOK, loginResponse{
					OK:          false,
					NeedVerify:  true,
					ChallengeID: cid,
					Reason:      anomaly.Reason,
				})
				return
			}
		} else if prof.LastLoginIP != "" || prof.LastLoginUA != "" {
			// 有历史登录痕迹 + 没配通知 = 上次登录也没通知 = 用户已知在裸跑。
			// 跳过异常检测放行，仅记日志提示用户去配。
			log.Printf("[auth] %s: 异常登录跳过检测（未配通知通道），ip=%s ua=%s", req.Username, ip, ua)
		}
	}

	// 正常登录：写 last_login、发 session
	if err := s.svc.DB().UpdateLastLogin(req.Username, ip, ua); err != nil {
		log.Printf("[auth] update last_login failed: %v", err)
	}
	s.issueSession(w, r, req.Username, role)
	s.handleAuditLogin(r, req.Username, "auth.login", "登录成功", http.StatusOK)
	writeJSON(w, http.StatusOK, loginResponse{OK: true, Username: req.Username, Role: role})
}

// handleVerifyChallenge POST /api/auth/verify-challenge {challenge_id, code}
// 第二步：校验验证码。验证码通过后下发 session cookie（用于异常登录流程）。
//
// 注意：本端点不绑定登录态——它本身就是"密码已通过 + 验证码通过"的下发点。
// 但限流仍按 IP+username 走，避免恶意代码对单一 challenge 暴力尝试。
func (s *Server) handleVerifyChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// 拿 challenge 找出 username（用于限流 key）
	c, err := s.svc.DB().GetChallenge(body.ChallengeID)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// 限流：按 IP+username
	key := s.clientKey(r, c.Username)
	if ok, wait := limiter.allow(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeError(w, http.StatusTooManyRequests, errors.New("尝试次数过多，请稍后再试"))
		return
	}
	ok, verr := s.svc.VerifyChallenge(body.ChallengeID, body.Code)
	if verr != nil {
		limiter.fail(key)
		writeError(w, http.StatusUnauthorized, verr)
		return
	}
	limiter.success(key)
	if !ok {
		// 不可达，但保险
		writeError(w, http.StatusUnauthorized, errors.New("验证码错误"))
		return
	}
	// 通过：写 last_login、发 session
	ip := s.clientIP(r)
	ua := r.UserAgent()
	if err := s.svc.DB().UpdateLastLogin(c.Username, ip, ua); err != nil {
		log.Printf("[auth] update last_login failed: %v", err)
	}
	_, role, _ := s.svc.DB().GetUser(c.Username)
	s.issueSession(w, r, c.Username, role)
	log.Printf("[auth] challenge login OK for %s from %s", c.Username, ip)
	writeJSON(w, http.StatusOK, loginResponse{OK: true, Username: c.Username, Role: role})
}

// totpPendingTTL / totpMaxAttempts：pending 登录凭据的时效与错误上限。
// 动态口令是 6 位数字、30 秒一换，5 次尝试在 90 秒有效窗内的成功率约百万分之六，
// 远低于可接受阈值；超限作废 pending，逼攻击者重新过密码这一关。
const (
	totpPendingTTL  = 2 * time.Minute
	totpMaxAttempts = 5
)

// newTOTPPending 生成一次性 pending 凭据（密码已通过、等动态口令）。
func (s *Server) newTOTPPending(username string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	s.totpMu.Lock()
	defer s.totpMu.Unlock()
	s.totpPending[tok] = totpPendingLogin{
		username: username,
		expires:  time.Now().Add(totpPendingTTL),
	}
	return tok, nil
}

// peekTOTPPending 取 pending 并计一次尝试。
// 过期 / 不存在 / 超过 5 次尝试 → ok=false（pending 已被清掉）。
func (s *Server) peekTOTPPending(tok string) (totpPendingLogin, bool) {
	s.totpMu.Lock()
	defer s.totpMu.Unlock()
	p, ok := s.totpPending[tok]
	if !ok {
		return p, false
	}
	if time.Now().After(p.expires) {
		delete(s.totpPending, tok)
		return p, false
	}
	p.attempts++
	if p.attempts > totpMaxAttempts {
		delete(s.totpPending, tok)
		return totpPendingLogin{}, false
	}
	s.totpPending[tok] = p
	return p, true
}

// consumeTOTPPending 成功路径：消费掉 pending（一次性）。
func (s *Server) consumeTOTPPending(tok string) {
	s.totpMu.Lock()
	defer s.totpMu.Unlock()
	delete(s.totpPending, tok)
}

// handleVerifyTOTP POST /api/auth/verify-totp {login_token, code}
// 两步验证第二步：校验动态口令，通过后下发 session cookie。
//
// 前提：login_token 是密码通过后签发的 pending 凭据（2 分钟、一次性语义、
// 5 次错误作废）。动态口令校验基于用户**当前启用**的密钥——
// 若用户在两步之间关闭了 2FA，则密码通过即放行（密钥已清空）。
//
// 失败不消费 pending（剩余尝试次数内可重试），但 5 次后 pending 作废。
func (s *Server) handleVerifyTOTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body struct {
		LoginToken string `json:"login_token"`
		Code       string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p, ok := s.peekTOTPPending(body.LoginToken)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("登录会话已过期，请重新输入密码"))
		return
	}
	// 限流：按 IP+username（与登录同款），双保险
	key := s.clientKey(r, p.username)
	if ok, wait := limiter.allow(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeError(w, http.StatusTooManyRequests, errors.New("尝试次数过多，请稍后再试"))
		return
	}
	secret, enabled, gerr := s.svc.DB().GetTOTP(p.username)
	if gerr == model.ErrNotFound {
		// 用户在两步之间被删除——pending 作废，拒绝登录
		s.consumeTOTPPending(body.LoginToken)
		writeError(w, http.StatusUnauthorized, errors.New("登录会话已过期，请重新输入密码"))
		return
	}
	if gerr != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	if !enabled || secret == "" {
		// pending 有效但 2FA 已被关闭 → 直接放行（密钥都清了，没有第二因子可验）
		s.consumeTOTPPending(body.LoginToken)
		_, role, _ := s.svc.DB().GetUser(p.username)
		s.issueSession(w, r, p.username, role)
		log.Printf("[auth] totp skipped for %s (2fa disabled mid-login)", p.username)
		writeJSON(w, http.StatusOK, loginResponse{OK: true, Username: p.username, Role: role})
		return
	}
	if !twofa.Validate(secret, body.Code, time.Now()) {
		// v0.8.0: 动态口令不对时，再试恢复码——手机丢失/损坏时用户唯一的
		// 自救通道。恢复码同样受 pending 尝试上限与限流器约束（错误路径
		// 统一 fail），不存在"恢复码是绕过速率限制的后门"。
		used, rerr := s.svc.DB().UseRecoveryCode(p.username, body.Code)
		if rerr != nil {
			writeError(w, http.StatusInternalServerError, errInternal)
			return
		}
		if !used {
			limiter.fail(key)
			writeError(w, http.StatusUnauthorized, errors.New("动态口令或恢复码错误"))
			return
		}
		limiter.success(key)
		s.consumeTOTPPending(body.LoginToken)

		ip := s.clientIP(r)
		ua := r.UserAgent()
		if err := s.svc.DB().UpdateLastLogin(p.username, ip, ua); err != nil {
			log.Printf("[auth] update last_login failed: %v", err)
		}
		_, role, _ := s.svc.DB().GetUser(p.username)
		s.issueSession(w, r, p.username, role)
		s.handleAuditLogin(r, p.username, "auth.login", "登录成功（恢复码）", http.StatusOK)
		log.Printf("[auth] recovery-code login for %s from %s", p.username, ip)
		// v0.8.1: 恢复码登录 = 用户大概率丢了手机——邮件告知本人（best-effort，
		// 放 goroutine：SMTP 可能耗时数秒，不能拖慢登录响应；内部已容错，
		// 邮箱/SMTP 缺失只记日志，绝不影响已成功的登录）。
		go s.svc.NotifyRecoveryLogin(p.username, ip, ua)
		writeJSON(w, http.StatusOK, loginResponse{OK: true, Username: p.username, Role: role})
		return
	}
	limiter.success(key)
	s.consumeTOTPPending(body.LoginToken)

	ip := s.clientIP(r)
	ua := r.UserAgent()
	if err := s.svc.DB().UpdateLastLogin(p.username, ip, ua); err != nil {
		log.Printf("[auth] update last_login failed: %v", err)
	}
	_, role, _ := s.svc.DB().GetUser(p.username)
	s.issueSession(w, r, p.username, role)
	s.handleAuditLogin(r, p.username, "auth.login", "登录成功（含两步验证）", http.StatusOK)
	log.Printf("[auth] totp login OK for %s from %s", p.username, ip)
	writeJSON(w, http.StatusOK, loginResponse{OK: true, Username: p.username, Role: role})
}

// handlePasswordResetRequest POST /api/auth/password-reset/request {username}
// 申请密码重置：发邮件验证码到该用户名绑定的邮箱。
//
// 故意不区分"用户存在"——对不存在的用户名也返回 ok 200，避免枚举。
// 失败时 mailer 会记日志（不写明文地址）。
//
// 返回 challenge_id：前端需要把它带回到 confirm 端点 + 邮件里的 6 位码。
// 改密流程是两步：request (申请) → confirm (提交验证码+新密)。
func (s *Server) handlePasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" {
		writeError(w, http.StatusBadRequest, errors.New("username 必填"))
		return
	}
	// 用户不存在时静默成功（防枚举）。返回的 challenge_id 是空串——前端
	// 拿到 ok=true 之后还得靠"是否收到邮件"判断；这里返回 200 不区分。
	challengeID := ""
	if _, _, err := s.svc.DB().GetUser(body.Username); err == nil {
		// 不管邮件发没发成功都返 200：避免泄漏"SMTP 没配/邮箱没绑"等内部信息
		if id, cerr := s.svc.CreateChallenge(body.Username, "password_reset"); cerr == nil {
			challengeID = id
		} else {
			// 即使发邮件失败也返 200（防枚举）；但记录日志供运维查
			log.Printf("[reset] create challenge failed for %s: %v", body.Username, cerr)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"challenge_id": challengeID,
	})
}

// handlePasswordResetConfirm POST /api/auth/password-reset/confirm
//
//	{challenge_id, username, code, new_password}
//
// 用验证码 + 新密码完成重置。前端 body 必传 username（service 会校验与
// challenge 记录里的一致性），这是为了防"拿 A 用户的 challenge 改 B 用户的密"。
func (s *Server) handlePasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	var body struct {
		ChallengeID string `json:"challenge_id"`
		Username    string `json:"username"`
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.ChallengeID == "" || body.Username == "" || body.Code == "" {
		writeError(w, http.StatusBadRequest, errors.New("challenge_id / username / code 必填"))
		return
	}
	if err := s.svc.ResetPassword(body.ChallengeID, body.Username, body.Code, body.NewPassword); err != nil {
		// 业务错误：验证码错/过期/超限 → 401；其它 → 400
		msg := err.Error()
		if strings.Contains(msg, "验证码") || strings.Contains(msg, "已过期") {
			writeError(w, http.StatusUnauthorized, err)
		} else {
			writeError(w, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleLogout 处理登出。
//
// 必须 POST：登出会删掉会话记录（DeleteSession），是有副作用的。
// 允许 GET 的话，任意第三方页面用一个 <img src=".../api/auth/logout">
// 就能把管理员踢下线——典型的 CSRF 登出。
// 前端本来就用 POST（见 web/js/api.js 的 auth.logout）。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.svc.DB().DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMe 探测当前会话状态。
//
// 约定：永远返回 200，body 用 authenticated 字段区分登录与否。
// 不返回 401——前端 boot 流程每次刷新都会调一次 /auth/me，
// 4xx 响应会让浏览器在控制台弹一条红色 Fetch 警告（fetch 协议层行为，
// JS 改不掉），既污染日志也误导用户以为系统出错。
// 401 留给真正需要鉴权的业务接口（/sites、/certs 等）。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	_, role, _ := s.svc.DB().GetUser(user)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      user,
		"role":          role,
	})
}

// handleChangePassword 修改密码。
//
// 必须 POST：改密会写用户表（SaveUser）并作废该用户的其它会话。
// 允许 GET 的话，CSRF 就能触发改密流程（虽然还需要正确的旧密码，
// 但方法不严本身就是不该留的口子）。前端用 POST。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	user, ok := s.currentUser(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return
	}
	var req passwordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	hash, role, err := s.svc.DB().GetUser(user)
	if err != nil {
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeModelError(w, err)
		return
	}
	if err := s.svc.DB().SaveUser(user, string(newHash), role); err != nil {
		writeModelError(w, err)
		return
	}
	// 改密后失效该用户的其它会话（保留当前这个，避免把自己踢下线）。
	// 否则旧会话在密码泄露后依然可用，改密失去意义。
	if c, err := r.Cookie(sessionCookie); err == nil {
		if n, derr := s.svc.DB().DeleteUserSessionsExcept(user, c.Value); derr == nil && n > 0 {
			log.Printf("[auth] password changed for %s, invalidated %d other session(s)", user, n)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// issueSession 下发 session cookie + 写 DB。handleLogin / handleVerifyChallenge 共用。
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, username, role string) {
	// 拼接两个随机 ID 提升 token 强度
	token := store.NewID() + store.NewID()
	if err := s.svc.DB().SaveSession(token, username, sessionTTL); err != nil {
		log.Printf("[auth] save session failed: %v", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	if s.secureCookie {
		w.Header().Set("Set-Cookie", w.Header().Get("Set-Cookie")+"; Secure")
	}
	_ = r
}

// currentUser 从 Cookie 解析当前用户。
func (s *Server) currentUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	user, err := s.svc.DB().GetSession(c.Value)
	if err != nil {
		return "", false
	}
	return user, true
}

// Authorize 校验请求是否携带有效会话。
// 供 WebSocket 等无法走 authMiddleware 链的路径使用：
// WS 升级请求挂在 ServeMux 上，绕过 protected 中间件，
// 因此必须显式调用本方法做真实校验，而不是只检查 cookie 存在性。
//
// v0.2.2：同时接受 API Token（Authorization: Bearer / X-API-Token），
// 让外部脚本也能订阅日志等 WS 端点。
func (s *Server) Authorize(r *http.Request) bool {
	_, ok := s.currentPrincipal(r)
	return ok
}

// authMiddleware 鉴权中间件。
//
// v0.2.2 起同时接受两种凭据：
//   - 会话 cookie（浏览器）
//   - API Token（Authorization: Bearer <token> 或 X-API-Token）
//
// 并把解析出的 principal 放进 request context——下游的审计中间件
// 直接从 context 取，不用再查一次库。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.currentPrincipal(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, model.ErrUnauthorized)
			return
		}
		// 只读 token 不能改东西：写操作要求 write 权限。
		if !isReadMethod(r.Method) && !p.HasScope("write") {
			writeError(w, http.StatusForbidden, errTokenScopeDenied)
			return
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// EnsureAdminUser 确保管理员账户存在（首次启动时创建）。
func EnsureAdminUser(db *store.DB, username, password string) error {
	if _, _, err := db.GetUser(username); err == nil {
		return nil
	}
	if len(password) < 8 {
		return model.ErrBadRequest
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return db.SaveUser(username, string(hash), "admin")
}
