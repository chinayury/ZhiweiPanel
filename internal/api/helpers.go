// Package api 提供 REST 接口与 WebSocket 实时日志。
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/zhiwei/panel/internal/model"
	"github.com/zhiwei/panel/internal/service"
)

// errInternal 对外统一返回的内部错误文案。
// 具体错误（含绝对路径、nginx stderr、SQLite 报错）只进服务端日志，
// 不回显给客户端——那会泄露目录布局与配置结构，给攻击者做路径侦察。
var errInternal = errors.New("内部错误，请查看服务端日志")

// errorResponse 统一错误响应。
type errorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, errorResponse{Error: err.Error(), Code: code})
}

// writeModelError 把领域错误映射为合适的 HTTP 状态码。
//
// 只有已知的领域错误（model.Err*）才把原文返回给客户端；
// 其余一律按"内部错误"处理，原文仅记日志。早期 default 分支直接
// 回传 err.Error()，于是 open /www/...: no such file、nginx -t 的完整
// stderr、SQLite 报错都会出现在响应体里。
func writeModelError(w http.ResponseWriter, err error) {
	if err == nil {
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	if errors.Is(err, model.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, model.ErrConflict) || errors.Is(err, model.ErrDuplicate) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if errors.Is(err, model.ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if errors.Is(err, model.ErrForbidden) {
		writeError(w, http.StatusForbidden, err)
		return
	}
	// nginx 配置校验失败：把 nginx -t 原文原样回显给前端。
	// 这是 P0「自定义 Nginx 配置」的用户反馈通道——用户在站点编辑页
	// 写错了片段，必须能看到是哪一行、哪个指令，而不是一句通用「保存失败」。
	if errors.Is(err, model.ErrNginxConfig) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, model.ErrBadRequest) || errors.Is(err, model.ErrInvalidDomain) ||
		errors.Is(err, model.ErrInvalidPath) || errors.Is(err, model.ErrInvalidValue) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, service.ErrFileTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	// 未知错误：记录原文，对外只给通用文案
	log.Printf("[api] internal error: %v", err)
	writeError(w, http.StatusInternalServerError, errInternal)
}

// requirePost 拒绝非 POST 请求。
//
// 为什么所有有副作用的子动作都强制 POST：
//
// 权限模型把"能不能写"绑在 HTTP 方法上（只读 Token 只允许 GET/HEAD/OPTIONS）。
// 于是只要某个写操作允许用 GET 触发，它就自动退化成"只读操作"——
// 这不是假设，是真实发生过的：早期版本里
//
//	GET /api/certs/{id}/delete     删证书记录
//	GET /api/certs/{id}/revoke     吊销证书
//	GET /api/certs/{id}/renew      触发续签
//	GET /api/certs/{id}/bind       改写站点 SSL 绑定
//	GET /api/sites/{id}/toggle     启停站点
//	GET /api/sites/{id}/reload     全量下发 nginx 配置
//	GET /api/system/config-apply   全量下发 nginx 配置
//	GET /api/alerts/{id}/resolve   关闭告警
//	GET /api/sites/{id}/baseline   改写防篡改基线
//	GET /api/sites/{id}/tamper     写篡改事件 + 触发告警通知
//
// 这 10 个端点全都没有方法校验，于是持有一个 scopes:["read"] 的 Token
// 就能删掉生产站点的证书、把站点停掉、重新下发全量配置、
// 把防篡改基线刷成当前状态、反复触发篡改告警通知——
// "只读"这个安全标签直接失效。
//
// 强制 POST 之后，"GET 一定不改状态"就成了一条不依赖人记的规则。
// 前端本来就已经用 POST 调这些动作（见 web/js/api.js），所以不受影响。
//
// 判定标准（不要靠记性）：**凡是会写库、写文件、发通知的端点，
// 一律不能只靠 GET 触发**。只读 Token 的免检名单只有 GET/HEAD/OPTIONS，
// 所以"GET 可达 + 有副作用"就等于权限提升。
func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return false
	}
	return true
}

// requirePutOrPost 拒绝既非 PUT 也非 POST 的请求。
//
// 用于"读 / 写同路径"的接口（改邮箱）：GET 语义是只读，
// 挂在同一个路径上却允许 GET 触发写操作，会让 CSRF 防护形同虚设
// （跨站 GET 不带 body，但同路径的写操作一旦被改成读 query 参数就会中招）。
func requirePutOrPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, model.ErrBadRequest)
		return false
	}
	return true
}

// requireShellScope 检查调用者是否有权操作"Shell 脚本"类型的计划任务。
//
// 面板以 root 运行，shell 任务等于把 root shell 交出去。
// write scope 只代表"能改面板配置"，不代表"能执行任意系统命令"——
// 这两件事必须是**独立**的授权位。
//
// 早期版本 write 就够了，于是：
//   - 一个泄漏的 write scope Token（CI / 监控最常用）直接等于整机 root；
//   - 一个只想"自动备份站点"的 Token，也能顺手把整台机器删了。
//
// 现在 shell 是独立 scope，默认不授予；管理员会话（浏览器登录）天然全权限。
func requireShellScope(w http.ResponseWriter, r *http.Request, jobType string) bool {
	if jobType != string(model.CronShell) {
		return true
	}
	p, _ := principalFrom(r.Context())
	if p.HasScope("shell") {
		return true
	}
	writeError(w, http.StatusForbidden,
		errors.New("执行 Shell 计划任务需要 shell 权限（API Token 默认不授予）"))
	return false
}

// decodeJSON 解析请求体。
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20))
	return dec.Decode(v)
}

// bindSite 从请求体构造站点对象（用于创建）。
func bindSite(r *http.Request) (*model.Site, error) {
	var s model.Site
	if err := decodeJSON(r, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
