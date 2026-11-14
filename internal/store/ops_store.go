package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// ---------------------------------------------------------------------------
// API Token 存储
//
// 明文 token 只在创建时存在一次，库里只留 SHA-256 摘要。
// 摘要不可逆 ⇒ 库文件泄露不会直接变成可用凭据。
// ---------------------------------------------------------------------------

// HashToken 计算 token 的存储摘要。
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// TokenPrefix 取明文 token 的展示前缀。
func TokenPrefix(tok string) string {
	if len(tok) <= 8 {
		return tok
	}
	return tok[:8]
}

// SaveAPIToken 新增或更新一个 token（按 ID 冲突更新）。
//
// 调用方负责保证 t.Token 只在新建时非空；
// 更新既有 token 时应走 RevokeAPIToken 而不是复用此函数。
func (d *DB) SaveAPIToken(t *model.APIToken) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	_, err := d.db.Exec(`
INSERT INTO api_tokens (id,name,prefix,token_hash,scopes,created_by,created_at,last_used_at,expires_at,disabled)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, prefix=excluded.prefix, scopes=excluded.scopes,
  last_used_at=excluded.last_used_at, expires_at=excluded.expires_at,
  disabled=excluded.disabled`,
		t.ID, t.Name, t.Prefix, HashToken(t.Token), strings.Join(t.Scopes, ","),
		t.CreatedBy, t.CreatedAt, t.LastUsedAt, t.ExpiresAt, boolToInt(t.Disabled))
	return err
}

// GetAPITokenByHash 按明文 token 查出对应的记录。
//
// 未命中返回 model.ErrUnauthorized（而不是 ErrNotFound）——
// 认证失败路径上区分"凭据不存在"和"其它错误"没有意义，
// 统一成未授权可以避免给探测者区分信息。
func (d *DB) GetAPITokenByHash(tok string) (*model.APIToken, error) {
	var t model.APIToken
	var scopes string
	var disabled int
	err := d.db.QueryRow(`
SELECT id,name,prefix,scopes,created_by,created_at,last_used_at,expires_at,disabled
FROM api_tokens WHERE token_hash=?`, HashToken(tok)).
		Scan(&t.ID, &t.Name, &t.Prefix, &scopes, &t.CreatedBy, &t.CreatedAt,
			&t.LastUsedAt, &t.ExpiresAt, &disabled)
	if err == sql.ErrNoRows {
		return nil, model.ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	t.Scopes = splitList(scopes)
	t.Disabled = disabled == 1
	// Token 明文永远不返回
	t.Token = ""
	return &t, nil
}

// ListAPITokens 列出所有 token（不含明文）。
func (d *DB) ListAPITokens() ([]*model.APIToken, error) {
	rows, err := d.db.Query(`
SELECT id,name,prefix,scopes,created_by,created_at,last_used_at,expires_at,disabled
FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.APIToken
	for rows.Next() {
		var t model.APIToken
		var scopes string
		var disabled int
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &scopes, &t.CreatedBy, &t.CreatedAt,
			&t.LastUsedAt, &t.ExpiresAt, &disabled); err != nil {
			return nil, err
		}
		t.Scopes = splitList(scopes)
		t.Disabled = disabled == 1
		t.Token = ""
		out = append(out, &t)
	}
	return out, rows.Err()
}

// TouchAPIToken 更新 token 的最后使用时间。
//
// 失败不致命——只是"最后使用时间"这个运维辅助信息丢了，
// 不该因为它让整个认证失败。
func (d *DB) TouchAPIToken(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = d.db.Exec(`UPDATE api_tokens SET last_used_at=? WHERE id=?`, time.Now(), id)
}

// RevokeAPIToken 吊销 token（直接删行）。
func (d *DB) RevokeAPIToken(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM api_tokens WHERE id=?`, id)
	return err
}

// ---------------------------------------------------------------------------
// 操作审计存储
// ---------------------------------------------------------------------------

// SaveAuditLog 写入一条审计记录。
func (d *DB) SaveAuditLog(l *model.AuditLog) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if l.ID == "" {
		l.ID = newID()
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	_, err := d.db.Exec(`
INSERT INTO audit_logs (id,username,action,target,detail,method,path,status,ip,ua,via_token,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.Username, l.Action, l.Target, l.Detail, l.Method, l.Path,
		l.Status, l.IP, l.UA, boolToInt(l.ViaToken), l.CreatedAt)
	return err
}

// ListAuditLogs 按时间倒序列出审计记录。
//
// limit<=0 时给一个保守默认值，避免前端传 0 拉回全表。
func (d *DB) ListAuditLogs(limit int, action string) ([]*model.AuditLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT id,username,action,target,detail,method,path,status,ip,ua,via_token,created_at FROM audit_logs`
	args := []any{}
	if action != "" {
		q += ` WHERE action LIKE ?`
		args = append(args, action+"%")
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.AuditLog
	for rows.Next() {
		var l model.AuditLog
		var viaToken int
		if err := rows.Scan(&l.ID, &l.Username, &l.Action, &l.Target, &l.Detail,
			&l.Method, &l.Path, &l.Status, &l.IP, &l.UA, &viaToken, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.ViaToken = viaToken == 1
		out = append(out, &l)
	}
	return out, rows.Err()
}

// CountAuditLogs 统计审计记录总数。
func (d *DB) CountAuditLogs() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&n)
	return n, err
}

// TrimAuditLogs 只保留最近 keep 条审计记录。
//
// 审计表会无限增长，长期跑着的面板必须有个上限。
// keep<=0 表示不清理。
func (d *DB) TrimAuditLogs(keep int) error {
	if keep <= 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&n); err != nil {
		return err
	}
	if n <= keep {
		return nil
	}
	// 删掉超出 keep 的最旧记录
	_, err := d.db.Exec(`
DELETE FROM audit_logs WHERE id IN (
  SELECT id FROM audit_logs ORDER BY created_at DESC LIMIT -1 OFFSET ?
)`, keep)
	return err
}
