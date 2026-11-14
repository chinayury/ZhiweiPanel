package api

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/store"
)

// ---------------------------------------------------------------------------
// API Token
//
// 用途：把面板暴露给自动化（CI、监控、备份脚本、Ansible 等）。
// 现状是所有接口只能靠 session cookie 认证，而 cookie 由浏览器持有——
// 自动化要么登录再存 cookie（等于把管理员密码写进脚本），
// 要么根本没法用面板。
//
// 凭据格式：zw_<32 字节 base64url>
//
//	前缀 zw_ 让 token 在日志/终端里一眼能认出是面板凭据，
//	也便于将来做"扫描泄漏 token"这类检测。
// ---------------------------------------------------------------------------

// tokenPrefix 是 API Token 的可识别前缀。
const tokenPrefix = "zw_"

// tokenBytes 是 token 随机部分的长度。
const tokenBytes = 32

// handleTokens GET/POST /api/tokens
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tokens, err := s.svc.DB().ListAPITokens()
		if err != nil {
			writeError(w, http.StatusInternalServerError, errInternal)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
	case http.MethodPost:
		s.createToken(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
	}
}

// createTokenRequest 是创建 token 的请求体。
type createTokenRequest struct {
	Name string `json:"name"`
	// Scopes: ["read"] 只读；["read","write"] 可写；空 = 全权限。
	Scopes []string `json:"scopes"`
	// ExpiresInDays 有效期天数，0 = 永不过期。
	ExpiresInDays int `json:"expires_in_days"`
}

// createToken 创建一个 API Token。
//
// 明文只在这次响应里返回一次——库里只存 SHA-256 摘要，不可逆。
// 丢了只能重建，无法找回。
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	if len(req.Name) > 64 {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}

	// 校验 scope：只允许 read / write / shell 三个已知值。
	// 留空表示全权限（等价于管理员会话），不写进 scopes 字段。
	//
	// shell 是**独立**的一档：面板以 root 运行，"Shell 脚本"类型的计划任务
	// 等于把 root shell 交给脚本作者。早期版本里 write 就够了，
	// 于是一个泄漏的 write scope Token（CI / 监控常用）直接等于整机 root。
	// 现在把"能改配置"与"能执行任意 root 命令"拆成两个独立授权位，
	// 默认不授予。
	scopes := make([]string, 0, len(req.Scopes))
	for _, sc := range req.Scopes {
		sc = strings.ToLower(strings.TrimSpace(sc))
		if sc == "" {
			continue
		}
		if sc != "read" && sc != "write" && sc != "shell" {
			writeError(w, http.StatusBadRequest, model.ErrBadRequest)
			return
		}
		scopes = append(scopes, sc)
	}

	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}

	plain := tokenPrefix + randomToken()
	p, _ := principalFrom(r.Context())

	tok := &model.APIToken{
		ID:        store.NewID(),
		Name:      req.Name,
		Prefix:    store.TokenPrefix(plain),
		Token:     plain,
		Scopes:    scopes,
		CreatedBy: p.Username,
		CreatedAt: time.Now(),
	}
	if req.ExpiresInDays > 0 {
		exp := time.Now().AddDate(0, 0, req.ExpiresInDays)
		tok.ExpiresAt = &exp
	}
	if err := s.svc.DB().SaveAPIToken(tok); err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}

	// 明文只在这里出现。响应里带上 token 字段，前端弹窗让用户复制保存。
	tok.Token = plain
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      tok,
		"show_token": true,
	})
}

// handleTokenByID DELETE /api/tokens/{id}
//
// 吊销 = 直接删行。凭据泄漏时必须能立刻失效，不做软删除。
func (s *Server) handleTokenByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tokens/"), "/")
	if id == "" || strings.ContainsAny(id, "/\\") {
		writeError(w, http.StatusBadRequest, model.ErrBadRequest)
		return
	}
	if err := s.svc.DB().RevokeAPIToken(id); err != nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// randomToken 生成 token 的随机部分。
//
// 用 base64url（无 padding）：token 要放进 HTTP 头、URL、shell 变量，
// 特殊字符越少越不容易在传递过程中被转义搞坏。
func randomToken() string {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		// 读不到随机源是致命错误——退化成可预测的 token 比报错危险得多。
		// 这里返回 panic 让进程启动失败，好过发一个弱凭据出去。
		panic("[api] 生成 API Token 失败: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseExpiresInDays 兼容"用秒数传有效期"的旧调用（保留给内部测试用）。
func parseExpiresInDays(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
