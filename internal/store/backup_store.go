package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/zhiwei/panel/internal/model"
)

// SaveBackup 写一条备份记录。
func (d *DB) SaveBackup(b *model.Backup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now()
	}
	enc := 0
	if b.Encrypted {
		enc = 1
	}
	_, err := d.db.Exec(`
INSERT INTO backups (id,kind,target,target_name,size,path,encrypted,note,created_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
		b.ID, string(b.Kind), b.Target, b.TargetName, b.Size, b.Path, enc, b.Note, b.CreatedAt)
	return err
}

// GetBackup 按 ID 读。
func (d *DB) GetBackup(id string) (*model.Backup, error) {
	row := d.db.QueryRow(`
SELECT id,kind,target,target_name,size,path,encrypted,note,created_at
FROM backups WHERE id=?`, id)
	return scanBackup(row)
}

// ListBackups 列出备份。kind="" 列出全部；target="" 列出全部对象。
//
// limit=0 用默认 100；limit<0 表示**不限量**（SQLite 的 LIMIT -1 语义），
// 用于保留期裁剪这类必须看全量的场景——早期这里传 0，
// 于是"找出所有超出保留份数的备份"最多只能看到 100 条。
func (d *DB) ListBackups(kind, target string, limit int) ([]*model.Backup, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 0 {
		limit = -1
	}
	var (
		rows *sql.Rows
		err  error
	)
	if kind != "" && target != "" {
		rows, err = d.db.Query(`
SELECT id,kind,target,target_name,size,path,encrypted,note,created_at
FROM backups WHERE kind=? AND target=? ORDER BY created_at DESC LIMIT ?`,
			kind, target, limit)
	} else if kind != "" {
		rows, err = d.db.Query(`
SELECT id,kind,target,target_name,size,path,encrypted,note,created_at
FROM backups WHERE kind=? ORDER BY created_at DESC LIMIT ?`, kind, limit)
	} else if target != "" {
		rows, err = d.db.Query(`
SELECT id,kind,target,target_name,size,path,encrypted,note,created_at
FROM backups WHERE target=? ORDER BY created_at DESC LIMIT ?`, target, limit)
	} else {
		rows, err = d.db.Query(`
SELECT id,kind,target,target_name,size,path,encrypted,note,created_at
FROM backups ORDER BY created_at DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// DeleteBackup 删除（同时由 caller 删掉磁盘文件）。
func (d *DB) DeleteBackup(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM backups WHERE id=?`, id)
	return err
}

// TrimBackups 保留某对象最近 keepN 份。
//
// 备份/计划任务里 payload.retain=3 会调这个，超出按 created_at 删旧。
func (d *DB) TrimBackups(kind, target string, keepN int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`
DELETE FROM backups
WHERE kind=? AND target=? AND id NOT IN (
  SELECT id FROM backups WHERE kind=? AND target=? ORDER BY created_at DESC LIMIT ?
)`, kind, target, kind, target, keepN)
	return err
}

func scanBackup(row interface {
	Scan(...any) error
}) (*model.Backup, error) {
	var b model.Backup
	var kind, createdAt string
	var encrypted int
	var created sql.NullTime
	err := row.Scan(&b.ID, &kind, &b.Target, &b.TargetName, &b.Size, &b.Path, &encrypted, &b.Note, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrNotFound
		}
		return nil, err
	}
	b.Kind = model.BackupKind(strings.TrimSpace(kind))
	b.Encrypted = encrypted != 0
	if created.Valid {
		b.CreatedAt = created.Time
	} else if createdAt != "" {
		if t, perr := time.Parse(time.RFC3339, createdAt); perr == nil {
			b.CreatedAt = t
		}
	}
	return &b, nil
}
