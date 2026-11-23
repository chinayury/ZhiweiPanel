package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
	"github.com/zhiwei/panel/internal/twofa"
)

// ---------------------------------------------------------------------------
// 两步验证（TOTP）管理
//
// 约束：
//   - 端点都要求**浏览器会话**——API Token 一律 403。泄漏的 write Token
//     不能关闭登录的第二因子，也不能给账户凭空挂上第二因子。
//   - setup/enable 两步走：先生成密钥（enabled=0，pending），用户用认证器
//     出首码验证通过才启用——避免"生成了密钥但没扫成码，把自己锁在门外"。
//   - 关闭也要求当前有效口令：能出示动态口令的人才有资格降级自己的防护。
//   - setup/enable/disable/recovery-codes 都是 POST → 自动进审计。
//   - 恢复码（v0.8.0）：启用即发一批，明文只在响应里出现一次；重新生成
//     即使旧码全部作废；关闭 2FA 时清空。
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

// handle2FAStatus GET /api/2fa/status —— 当前账户是否已启用两步验证，
// 以及剩余未消费的恢复码数量（v0.8.0）。
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
	remaining := 0
	if enabled {
		if n, cerr := s.svc.DB().CountUnusedRecoveryCodes(username); cerr == nil {
			remaining = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":            enabled,
		"recovery_remaining": remaining,
	})
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
	// v0.8.0: 启用成功即发放第一批恢复码（明文只在本次响应里出现）。
	// 手机丢失时用户唯一能自救登录的通道——必须在开启的同一时刻交给用户，
	// 不能等用户"以后想要时再生成"（那时可能已经进不来了）。
	codes, err := s.generateRecoveryCodes(username)
	if err != nil {
		// 2FA 已启用成功，恢复码失败不该让整个请求失败——告知用户稍后手动生成。
		writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "recovery_codes": []string{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        true,
		"recovery_codes": codes,
	})
}

// generateRecoveryCodes 生成一批新恢复码并整体替换旧码（旧码立即作废）。
// 返回明文码（仅此一次），库里只留 SHA-256 摘要。
func (s *Server) generateRecoveryCodes(username string) ([]string, error) {
	codes := make([]string, 0, store.RecoveryCodeCount)
	hashes := make([]string, 0, store.RecoveryCodeCount)
	for i := 0; i < store.RecoveryCodeCount; i++ {
		c, err := twofa.GenerateRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, c)
		hashes = append(hashes, store.HashRecoveryCode(c))
	}
	if err := s.svc.DB().ReplaceRecoveryCodes(username, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// handle2FARegenerateRecovery POST /api/2fa/recovery-codes —— 重新生成恢复码。
// 旧码立即全部作废（ReplaceRecoveryCodes 语义）：怀疑泄漏时这是止损手段。
// 明文只在本次响应里返回，前端必须弹窗让用户当场抄录。
func (s *Server) handle2FARegenerateRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	username, ok := require2FASession(w, r)
	if !ok {
		return
	}
	if _, enabled, _ := s.svc.DB().GetTOTP(username); !enabled {
		writeError(w, http.StatusBadRequest, errors.New("两步验证未启用，无需恢复码"))
		return
	}
	codes, err := s.generateRecoveryCodes(username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	log.Printf("[2fa] %s 重新生成了 %d 枚恢复码（旧码作废）", username, len(codes))
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
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
	// 密钥清空后恢复码失去存在意义（登录不再要求第二因子），一并清掉。
	if err := s.svc.DB().DeleteRecoveryCodes(username); err != nil {
		log.Printf("[2fa] 关闭两步验证后清理恢复码失败: %s: %v", username, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}
