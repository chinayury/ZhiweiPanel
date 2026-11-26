package mysql

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// DatabaseInfo 是面板管理范围内（site_*）的数据库概况。
type DatabaseInfo struct {
	Name      string `json:"name"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
	Tables    int64  `json:"tables"`
	SizeBytes int64  `json:"size_bytes"`
}

// ListManagedDatabases 列出面板建的数据库（site_ 前缀）。
//
// 只读 information_schema，不碰任何系统库；结果按名字排序。
// 非 site_ 前缀的库（用户自建、mysql/sys/performance_schema）一律不返回，
// 避免"列出即误操作"。
func (c *Client) ListManagedDatabases(ctx context.Context) ([]DatabaseInfo, error) {
	if c == nil || c.DB == nil {
		return nil, ErrNotInstalled
	}
	stats := map[string]DatabaseInfo{}
	rows, err := c.DB.QueryContext(ctx, `
SELECT TABLE_SCHEMA, COUNT(*), COALESCE(SUM(DATA_LENGTH+INDEX_LENGTH),0)
FROM information_schema.TABLES
GROUP BY TABLE_SCHEMA`)
	if err != nil {
		return nil, fmt.Errorf("读取 information_schema.TABLES 失败: %w", err)
	}
	for rows.Next() {
		var schema string
		var info DatabaseInfo
		if err := rows.Scan(&schema, &info.Tables, &info.SizeBytes); err != nil {
			rows.Close()
			return nil, err
		}
		if strings.HasPrefix(schema, DBNamePrefix) {
			stats[schema] = info
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = c.DB.QueryContext(ctx, `
SELECT SCHEMA_NAME, DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME
FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME`)
	if err != nil {
		return nil, fmt.Errorf("读取 information_schema.SCHEMATA 失败: %w", err)
	}
	defer rows.Close()

	out := make([]DatabaseInfo, 0, len(stats))
	for rows.Next() {
		var name, charset, collation string
		if err := rows.Scan(&name, &charset, &collation); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(name, DBNamePrefix) {
			continue
		}
		info := stats[name]
		info.Name = name
		info.Charset = charset
		info.Collation = collation
		out = append(out, info)
	}
	return out, rows.Err()
}

// ValidateManagedDatabaseName 校验数据库名属于面板管理范围。
//
// 这是所有数据库管理端点的第一道闸：即使调用方传了 mysql/sys，
// 也会在拼 SQL 之前被拒。名字只允许 site_ + 字母数字下划线。
func ValidateManagedDatabaseName(name string) error {
	if !strings.HasPrefix(name, DBNamePrefix) {
		return fmt.Errorf("数据库 %q 不在面板管理范围（必须以 %s 开头）", name, DBNamePrefix)
	}
	if len(name) > MaxNameLen {
		return fmt.Errorf("数据库名 %q 过长", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return fmt.Errorf("数据库名 %q 含非法字符", name)
		}
	}
	return nil
}

// OptimizeDatabase 对库内所有基础表执行 OPTIMIZE TABLE。
//
// 可能锁表；API 层会二次确认，并给足 timeout。
func (c *Client) OptimizeDatabase(ctx context.Context, name string) (int, error) {
	if c == nil || c.DB == nil {
		return 0, ErrNotInstalled
	}
	if err := ValidateManagedDatabaseName(name); err != nil {
		return 0, err
	}
	rows, err := c.DB.QueryContext(ctx, `
SELECT TABLE_NAME FROM information_schema.TABLES
WHERE TABLE_SCHEMA=? AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME`, name)
	if err != nil {
		return 0, err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return 0, err
		}
		tables = append(tables, t)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, t := range tables {
		// OPTIMIZE TABLE 会返回结果集（Table/Op/Msg_type/Msg_text）。
		// 用 Exec 会被驱动当成"未读结果"报 commands out of sync；
		// 这里用 Query 并排空结果。
		rows, err := c.DB.QueryContext(ctx, "OPTIMIZE TABLE "+quoteIdent(name)+"."+quoteIdent(t))
		if err != nil {
			return 0, fmt.Errorf("OPTIMIZE %s 失败: %w", t, err)
		}
		for rows.Next() {
		}
		cerr := rows.Err()
		_ = rows.Close()
		if cerr != nil {
			return 0, fmt.Errorf("OPTIMIZE %s 失败: %w", t, cerr)
		}
	}
	return len(tables), nil
}

// ImportSQL 把 SQL 脚本流式导入指定数据库。
//
// 走系统 mysql 客户端而不是 database/sql：
// 大文件/多语句/存储过程里的 DELIMITER 用驱动逐条执行既慢又容易出错。
// 凭据从 mysql-root.cnf 读（socket 登录），不落命令行明文。
func ImportSQL(ctx context.Context, cnfPath, dbName string, src io.Reader) error {
	if err := ValidateManagedDatabaseName(dbName); err != nil {
		return err
	}
	mysqlBin, err := exec.LookPath("mysql")
	if err != nil {
		return fmt.Errorf("找不到 mysql 客户端命令（宿主机安装 MariaDB/MySQL 客户端后可用）: %w", err)
	}
	cmd := exec.CommandContext(ctx, mysqlBin,
		"--defaults-extra-file="+cnfPath,
		"--database="+dbName,
		"--default-character-set=utf8mb4",
	)
	cmd.Stdin = src
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 600 {
			msg = msg[:600] + "…"
		}
		return fmt.Errorf("SQL 导入失败: %w（%s）", err, msg)
	}
	return nil
}
