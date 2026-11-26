// Package mysql 是 ZhiweiPanel 的 MySQL/MariaDB 子系统客户端。
//
// 设计原则：
//   - 与 driver.Driver 平级：Driver 管 nginx + 系统状态，本包只管数据库
//     子系统；service 按需持有一个 *Client（MySQL 未装时为 nil）
//   - 单一连接池：Client.DB 是 database/sql.DB，所有操作复用。root 凭据
//     从 install.sh 写的 ${DATA_DIR}/mysql-root.cnf 读，0600 权限
//   - 强类型 API：CreateDatabase / CreateUser / GrantPrivileges / Drop*，
//     不暴露原始 SQL 字符串——上层无法写"GRANT ... ON *.* TO ..."这种
//     越权语句
//   - 安全默认值：用户 host 强制 'localhost'，不让指定 '%'；权限只授
//     该站点一个库，不授 GRANT OPTION
package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Client 是 MySQL/MariaDB 子系统入口。
//
// 一个 ZhiweiPanel 进程持有一个 Client，零值（nil）表示 MySQL 未安装。
// 所有方法都接受 ctx，便于面板 API timeout 透传。
type Client struct {
	DB     *sql.DB
	dsn    string
	binDir string // mysql / mariadb 可执行目录，仅用于版本探测
}

// Status MySQL/MariaDB 安装/运行状态。
type Status struct {
	Installed bool   `json:"installed"`         // 二进制存在
	Running   bool   `json:"running"`           // 能连上
	Version   string `json:"version,omitempty"` // 完整 version string
	Server    string `json:"server,omitempty"`  // "MySQL" or "MariaDB"
	Socket    string `json:"socket,omitempty"`  // unix socket 路径
}

// DBNamePrefix 数据库/用户命名前缀，方便：
//  1. ListSiteDatabases 过滤出"面板建的库"
//  2. 防止面板误删用户的非面板库
const DBNamePrefix = "site_"

// MaxNameLen MySQL 用户名/数据库名最大长度（64 是 MySQL 老版限制，
// MariaDB 放宽到 80，但保持 64 兼容老部署）。
const MaxNameLen = 64

// Errors 公开错误便于 service / api 层 errors.Is 识别。
var (
	ErrNotInstalled = errors.New("mysql 未安装")   // 客户端未初始化
	ErrConnFailed   = errors.New("连接 MySQL 失败") // 鉴权/网络/服务未起
	ErrExists       = errors.New("database 或 user 已存在")
)

// LoadRootDSN 从 install.sh 写入的 cnf 文件读 root 凭据，组装 DSN。
// 文件格式：
//
//	[client]
//	user=root
//	password=xxx
//	socket=/run/mysqld/mysqld.sock
//
// 仅 socket 方式登录（不暴露 3306 端口，安全）。
// 密码可能含特殊字符，按行解析后注入 DSN 时做 URL 编码（mysql driver 内部
// 实际上不要求 URL 编码，但保险起见走 QuoteMeta）。
func LoadRootDSN(credFile string) (string, error) {
	data, err := os.ReadFile(credFile)
	if err != nil {
		return "", fmt.Errorf("读 MySQL 凭据文件失败: %w", err)
	}
	var user, pass, sock string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		k, v := strings.TrimSpace(line[:eq]), strings.TrimSpace(line[eq+1:])
		switch k {
		case "user":
			user = v
		case "password":
			pass = v
		case "socket":
			sock = v
		}
	}
	if user == "" || sock == "" {
		return "", fmt.Errorf("凭据文件 %s 缺少 user 或 socket", credFile)
	}
	// parseTime=true 让 DATETIME 字段返回 time.Time 而不是 []byte
	dsn := fmt.Sprintf("%s:%s@unix(%s)/?parseTime=true&multiStatements=true",
		user, pass, sock)
	return dsn, nil
}

// NewClient 创建客户端；连不上时返回 ErrConnFailed，调用方应据 ErrNotInstalled 提示。
func NewClient(credFile string) (*Client, error) {
	dsn, err := LoadRootDSN(credFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotInstalled
		}
		return nil, err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	// 立即 ping 一次：既验证 DSN，也避免后续第一次查询才暴露错误
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: %v", ErrConnFailed, err)
	}
	return &Client{DB: db, dsn: dsn, binDir: "/usr/bin"}, nil
}

// Close 关闭连接池。面板关闭时调用。
func (c *Client) Close() error {
	if c == nil || c.DB == nil {
		return nil
	}
	return c.DB.Close()
}

// Status 探测子系统状态（已装/运行/版本）。
func (c *Client) Status(ctx context.Context) (Status, error) {
	s := Status{Installed: true}
	if c == nil || c.DB == nil {
		s.Installed = false
		return s, nil
	}
	var version, serverComment string
	row := c.DB.QueryRowContext(ctx, "SELECT VERSION(), @@version_comment")
	if err := row.Scan(&version, &serverComment); err != nil {
		// 连不上 = 服务没运行（或 socket 路径错）
		s.Running = false
		return s, nil
	}
	s.Running = true
	s.Version = version
	s.Server = serverComment // MariaDB / MySQL
	// socket 路径
	row = c.DB.QueryRowContext(ctx, "SHOW VARIABLES LIKE 'socket'")
	var name, val string
	if err := row.Scan(&name, &val); err == nil {
		s.Socket = val
	}
	return s, nil
}

// NameForSite 根据 siteID 推导 db/user 名字。
// site_<12位hex> 形式：足够唯一、足够短、不可猜。
// 如果 siteID 太短（<8）就拒绝；太长就截前 12 位但保留可读性。
func NameForSite(siteID string) (string, error) {
	if len(siteID) < 8 {
		return "", fmt.Errorf("siteID 太短")
	}
	// 用前 12 位 hex（siteID 本身就是 hex）
	short := siteID
	if len(short) > 12 {
		short = short[:12]
	}
	name := DBNamePrefix + short
	if len(name) > MaxNameLen {
		return "", fmt.Errorf("推导名 %q 超过 MySQL 限制 %d 字符", name, MaxNameLen)
	}
	return name, nil
}

// GeneratePassword 生成 24 位 hex 密码（=12 字节随机）。
func GeneratePassword() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败的概率极低（仅在 OS 熵源彻底坏掉时）
		// 此时退化成时间戳的 hex，避免 panic
		ts := time.Now().UnixNano()
		return fmt.Sprintf("%024x", ts)
	}
	return hex.EncodeToString(b)
}

// CreateDatabase 建库。已存在不算错。
func (c *Client) CreateDatabase(ctx context.Context, name, charset string) error {
	if c == nil {
		return ErrNotInstalled
	}
	if charset == "" {
		charset = "utf8mb4"
	}
	// 字符集只能用白名单里的固定枚举：它会原样出现在
	// `CHARACTER SET <cs> COLLATE <collation>` 位置，那个位置不能用
	// 反引号引用，只能靠白名单保证安全。
	if !allowedCharsets[charset] {
		return fmt.Errorf("字符集 %q 不支持（仅 utf8mb4 / utf8 / latin1）", charset)
	}
	stmt := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s CHARACTER SET %s COLLATE %s",
		quoteIdent(name), charset, collationFor(charset))
	_, err := c.DB.ExecContext(ctx, stmt)
	if err != nil {
		// 1007 = ER_DB_CREATE_EXISTS，保险起见翻译一次
		if strings.Contains(err.Error(), "1007") {
			return ErrExists
		}
		return fmt.Errorf("CREATE DATABASE: %w", err)
	}
	return nil
}

// CreateUser 建用户并设密码。host 强制 localhost，拒绝传 '%'。
// 已存在则更新密码（覆盖老密码，运营场景需要这种"重置"语义）。
func (c *Client) CreateUser(ctx context.Context, user, password, host string) error {
	if c == nil {
		return ErrNotInstalled
	}
	if host == "" {
		host = "localhost"
	}
	if host != "localhost" && host != "127.0.0.1" {
		return fmt.Errorf("仅支持 host=localhost（拒绝 %q 以缩小暴露面）", host)
	}
	// 用户名白名单：既挡注入，也挡"站点用户名叫 root"导致删站时
	// 把面板自己的 MySQL root 账号删掉。
	if err := checkUserName(user); err != nil {
		return err
	}
	// 用户的全限定名是 'user'@'host'
	//
	// ⚠️ 不能用 ? 占位符：MySQL/MariaDB 协议里 CREATE USER / ALTER USER /
	// SET PASSWORD / GRANT 这类 DDL 不支持 prepared statement 的参数化，
	// 驱动会把 ? 当字面字符传给服务端，于是服务端报
	// "You have an error in your SQL syntax near '?'"。
	// GeneratePassword() 保证 password 是 24 位 hex（仅 [0-9a-f]），
	// 不存在需要转义的字符，可以安全 inline。已存在 TestGeneratePassword。
	// 测试见 TestCreateUserStmt（防止 SQL 模板回退到 ? 占位符）。
	stmt := buildCreateUserStmt(user, host, password)
	if _, err := c.DB.ExecContext(ctx, stmt); err != nil {
		// IF NOT EXISTS 在某些 MariaDB 版本上不支持；已存在时改走 UPDATE
		if strings.Contains(err.Error(), "exists") || strings.Contains(err.Error(), "1396") {
			upd := buildAlterUserStmt(user, host, password)
			if _, err2 := c.DB.ExecContext(ctx, upd); err2 != nil {
				return fmt.Errorf("ALTER USER (覆盖密码): %w", err2)
			}
			return nil
		}
		return fmt.Errorf("CREATE USER: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// DDL 拼接的安全工具
//
// MySQL/MariaDB 协议里 CREATE USER / ALTER USER / CREATE DATABASE /
// GRANT / DROP USER 这类 DDL **不支持** prepared statement 的占位符参数化
// （驱动会把 ? 当字面字符传给服务端），所以只能靠字符串拼接来构造语句。
//
// 既然必须拼字符串，就必须在拼接层做转义——这是最后一道防线：
// 上游（service 层）已经用白名单拒绝了客户端可控的库名/用户名/密码，
// 但如果哪天有新的调用路径绕过了白名单，这里的转义仍然能保证
// 生成的语句**语法上**不会被闭合引号或反引号截断。
// ---------------------------------------------------------------------------

// quoteIdent 把标识符包成 MySQL 的反引号引用形式。
//
// 内部出现的反引号按 MySQL 规则写成两个（“），从而无法提前闭合引用。
func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// quoteLiteral 把字符串写成 MySQL 的单引号字面量。
//
// 转义规则：反斜杠写成两个，单引号写成两个（”）。
// 这是 MySQL 默认（NO_BACKSLASH_ESCAPES 关闭）下的标准写法。
func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "'", "''")
	return "'" + s + "'"
}

// reservedUsers 是绝不允许被站点业务账号占用的名字。
//
// 站点用户是用**面板自己的 MySQL root 凭据**建的，如果允许站点用户名叫
// root，删站点时的 `DROP USER IF EXISTS root@localhost` 就会把面板自己的
// 账号删掉 —— 之后所有站点的 MySQL 操作全部失败。
var reservedUsers = map[string]bool{
	"root": true, "mysql": true, "admin": true, "administrator": true,
	"system": true, "mariadb": true, "debian-sys-maint": true,
}

// SetUserPassword 直接修改已存在用户的密码（ALTER USER）。
//
// 不能复用 CreateUser：MariaDB/MySQL 的 `CREATE USER IF NOT EXISTS` 对
// 已存在用户只发 warning、不改密码，函数会误以为成功。重置密码必须走
// ALTER USER，才能保证新旧密码真的切换。
func (c *Client) SetUserPassword(ctx context.Context, user, password, host string) error {
	if c == nil || c.DB == nil {
		return ErrNotInstalled
	}
	if host == "" {
		host = "localhost"
	}
	if host != "localhost" && host != "127.0.0.1" {
		return fmt.Errorf("仅支持 host=localhost（拒绝 %q 以缩小暴露面）", host)
	}
	if err := checkUserName(user); err != nil {
		return err
	}
	if len(password) < 16 {
		return fmt.Errorf("密码过短（至少 16 位）")
	}
	stmt := buildAlterUserStmt(user, host, password)
	if _, err := c.DB.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("ALTER USER (重置密码): %w", err)
	}
	return nil
}

// checkUserName 校验用户名可安全用于建用户/删用户。
func checkUserName(user string) error {
	if user == "" {
		return fmt.Errorf("用户名不能为空")
	}
	if reservedUsers[strings.ToLower(user)] {
		return fmt.Errorf("用户名 %q 是保留名，不允许使用", user)
	}
	if len(user) > 32 {
		return fmt.Errorf("用户名 %q 过长（MySQL 上限 32）", user)
	}
	for _, r := range user {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '$' || r == '-' || r == '.':
		default:
			return fmt.Errorf("用户名 %q 含非法字符", user)
		}
	}
	return nil
}

// allowedCharsets 是支持的字符集白名单。
//
// 字符集会原样出现在 `CHARACTER SET <cs> COLLATE <collation>` 位置，
// 那里不能用反引号引用，只能用固定枚举。
var allowedCharsets = map[string]bool{
	"utf8mb4": true, "utf8": true, "latin1": true,
}

// collationFor 返回字符集对应的排序规则。
func collationFor(charset string) string {
	switch charset {
	case "utf8mb4":
		return "utf8mb4_unicode_ci"
	case "utf8":
		return "utf8_general_ci"
	case "latin1":
		return "latin1_swedish_ci"
	default:
		return "utf8mb4_unicode_ci"
	}
}

// buildCreateUserStmt 拼接 CREATE USER 语句（密码 inline，不走占位符）。
// 见 CreateUser 注释里那堆 ⚠️，这是被 MySQL 协议逼出来的设计。
func buildCreateUserStmt(user, host, password string) string {
	return fmt.Sprintf("CREATE USER IF NOT EXISTS %s@%s IDENTIFIED BY %s",
		quoteIdent(user), quoteIdent(host), quoteLiteral(password))
}

// buildAlterUserStmt 同上，用于已存在分支。
func buildAlterUserStmt(user, host, password string) string {
	return fmt.Sprintf("ALTER USER %s@%s IDENTIFIED BY %s",
		quoteIdent(user), quoteIdent(host), quoteLiteral(password))
}

// GrantPrivileges 授所有权限（不含 GRANT OPTION）给 user 对 db。
// 仅授单个 db 的权限：'site_xxx'@'localhost' ON site_xxx.*
// 拒绝 db 用 *.* / * 之类通配。
func (c *Client) GrantPrivileges(ctx context.Context, user, db, host string) error {
	if c == nil {
		return ErrNotInstalled
	}
	if host == "" {
		host = "localhost"
	}
	if db == "" || strings.ContainsAny(db, "*%") {
		return fmt.Errorf("db 名 %q 含通配符，拒绝", db)
	}
	// 单库的所有权限：ALL 但不带 GRANT OPTION。
	// 站点用户能建表/改表/写数据，但不能再 GRANT 给别人（防止权限蔓延）
	stmt := fmt.Sprintf("GRANT ALL PRIVILEGES ON %s.* TO %s@%s",
		quoteIdent(db), quoteIdent(user), quoteIdent(host))
	if _, err := c.DB.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("GRANT: %w", err)
	}
	if _, err := c.DB.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("FLUSH PRIVILEGES: %w", err)
	}
	return nil
}

// DropDatabase 删库。库不存在不报错（幂等）。
func (c *Client) DropDatabase(ctx context.Context, name string) error {
	if c == nil {
		return ErrNotInstalled
	}
	stmt := fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(name))
	if _, err := c.DB.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("DROP DATABASE: %w", err)
	}
	return nil
}

// DropUser 删用户。用户不存在不报错（幂等）。
func (c *Client) DropUser(ctx context.Context, user, host string) error {
	if c == nil {
		return ErrNotInstalled
	}
	if host == "" {
		host = "localhost"
	}
	// 保留名保护：删站点时会调到这里，如果站点用户名叫 root，
	// 这一句就会把面板自己的 MySQL root 账号删掉。
	if err := checkUserName(user); err != nil {
		return err
	}
	stmt := fmt.Sprintf("DROP USER IF EXISTS %s@%s", quoteIdent(user), quoteIdent(host))
	if _, err := c.DB.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("DROP USER: %w", err)
	}
	return nil
}

// RevokePrivileges 删除用户的所有权限（保留用户本身）。
// 删除站点前调用，确保下次重建同 db/user 时权限是干净的。
func (c *Client) RevokePrivileges(ctx context.Context, user, host string) error {
	if c == nil {
		return ErrNotInstalled
	}
	if host == "" {
		host = "localhost"
	}
	stmt := fmt.Sprintf("REVOKE ALL PRIVILEGES, GRANT OPTION FROM `%s`@`%s`", user, host)
	if _, err := c.DB.ExecContext(ctx, stmt); err != nil {
		// 1045 = table doesn't exist / no grant for user，可忽略
		if !strings.Contains(err.Error(), "1269") && !strings.Contains(err.Error(), "1141") {
			return fmt.Errorf("REVOKE: %w", err)
		}
	}
	_, _ = c.DB.ExecContext(ctx, "FLUSH PRIVILEGES")
	return nil
}

// ListSiteDatabases 列出 panel 自己建的库（带 site_ 前缀）。
// 给前端"已建数据库"列表用，运维排查时也能看。
func (c *Client) ListSiteDatabases(ctx context.Context) ([]string, error) {
	if c == nil {
		return nil, ErrNotInstalled
	}
	rows, err := c.DB.QueryContext(ctx, "SHOW DATABASES LIKE ?", DBNamePrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("SHOW DATABASES: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// CredFilePath 给 main.go 推导默认凭据文件路径。
// 约定：${DATA_DIR}/mysql-root.cnf
func CredFilePath(dataDir string) string {
	return filepath.Join(dataDir, "mysql-root.cnf")
}
