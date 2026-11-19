package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/twofa"
)

// ---------------------------------------------------------------------------
// 两步验证（TOTP）管理
//
// 约束：
//   - 四个端点都要求**浏览器会话**——API Token 一律 403。泄漏的 write Token
//     不能关闭登录的第二因子，也不能给账户凭空挂上第二因子。
//   - setup/enable 两步走：先生成密钥（enabled=0，pending），用户用认证器
//     出首码验证通过才启用——避免"生成了密钥但没扫成码，把自己锁在门外"。
//   - 关闭也要求当前有效口令：能出示动态口令的人才有资格降级自己的防护。
//   - setup/enable/disable 都是 POST → 自动进审计（auditMiddleware）。
// ---------------------------------------------------------------------------

// require2FASession 2FA 管理只允许浏览器会话，返回当前用户名。
func require2FASession(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := principalFrom(r.Context())
	if !ok || p.ViaToken {
		writeError(w, http.StatusForbidden, errors.New("两步验证管理只允许浏览器会话操作，API Token 无权"))
		return "", false
	}
	return p.Username, true
}

// handle2FAStatus GET /api/2fa/status —— 当前账户是否已启用两步验证。
func (s *Server) handle2FAStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	username, ok := require2FASession(w, r)
	if !ok {
		return
	}
	_, enabled, err := s.svc.DB().GetTOTP(username)
	if err != nil && err != model.ErrNotFound {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled})
}

// handle2FASetup POST /api/2fa/setup —— 生成新密钥（pending，未启用）。
// 重复调用会覆盖上一个 pending 密钥（重新扫码场景）。
func (s *Server) handle2FASetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	username, ok := require2FASession(w, r)
	if !ok {
		return
	}
	if _, enabled, _ := s.svc.DB().GetTOTP(username); enabled {
		writeError(w, http.StatusBadRequest, errors.New("两步验证已启用，如需重新绑定请先关闭"))
		return
	}
	secret, err := twofa.GenerateSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	if err := s.svc.DB().SetTOTPSecret(username, secret); err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":  secret,
		"otpauth": twofa.OTPAuthURI("ZhiweiPanel", username, secret),
		"note":    "请用认证器 App 扫描或手动录入，然后提交一个当前动态口令完成启用",
	})
}

// handle2FAEnable POST /api/2fa/enable {code} —— 校验首码并启用。
func (s *Server) handle2FAEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	username, ok := require2FASession(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	secret, enabled, err := s.svc.DB().GetTOTP(username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	if enabled || secret == "" {
		writeError(w, http.StatusBadRequest, errors.New("两步验证未处于待启用状态，请先调用 setup"))
		return
	}
	if !twofa.Validate(secret, body.Code, time.Now()) {
		writeError(w, http.StatusBadRequest, errors.New("动态口令错误，请确认认证器时间与密钥"))
		return
	}
	if err := s.svc.DB().EnableTOTP(username); err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true})
}

// handle2FADisable POST /api/2fa/disable {code} —— 关闭两步验证。
// 要求当前有效口令：能出示动态口令的人才有资格降级自己的防护。
func (s *Server) handle2FADisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	username, ok := require2FASession(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	secret, enabled, err := s.svc.DB().GetTOTP(username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	if !enabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if !twofa.Validate(secret, body.Code, time.Now()) {
		writeError(w, http.StatusUnauthorized, errors.New("动态口令错误"))
		return
	}
	if err := s.svc.DB().DisableTOTP(username); err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}
