// Package store 提供基于 SQLite 的持久化。
// 使用 modernc.org/sqlite（纯 Go 实现），因此 CGO_ENABLED=0 也能编译为静态单二进制。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/zhiwei/panel/internal/model"
)

// DB 封装数据库连接。
type DB struct {
	db *sql.DB
	mu sync.RWMutex
}

// Open 打开数据库并完成初始化。
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单写者模型，限制连接数避免锁竞争。
	sdb.SetMaxOpenConns(1)
	sdb.SetMaxIdleConns(1)
	if err := sdb.Ping(); err != nil {
		return nil, err
	}
	d := &DB{db: sdb}
	if err := d.migrate(); err != nil {
		return nil, err
	}
	return d, nil
}

// Close 关闭连接。
func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS sites (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'running',
    remark     TEXT NOT NULL DEFAULT '',
    domains    TEXT NOT NULL DEFAULT '',
    config     TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sites_name ON sites(name);

CREATE TABLE IF NOT EXISTS certs (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    domains     TEXT NOT NULL DEFAULT '',
    not_before  TIMESTAMP,
    not_after   TIMESTAMP,
    issuer      TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    cert_file   TEXT NOT NULL DEFAULT '',
    key_file    TEXT NOT NULL DEFAULT '',
    auto_renew  INTEGER NOT NULL DEFAULT 0,
    acme_account TEXT NOT NULL DEFAULT '',
    dns_provider TEXT NOT NULL DEFAULT '',
    dns_config   TEXT NOT NULL DEFAULT '',
    method       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP,
    updated_at  TIMESTAMP
);

CREATE TABLE IF NOT EXISTS acme_accounts (
    id         TEXT PRIMARY KEY,
    email      TEXT NOT NULL,
    ca         TEXT NOT NULL,
    key_type   TEXT NOT NULL DEFAULT 'ec256',
    key_path   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS alerts (
    id         TEXT PRIMARY KEY,
    site_id    TEXT NOT NULL DEFAULT '',
    site_name  TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL,
    level      TEXT NOT NULL DEFAULT 'warning',
    message    TEXT NOT NULL,
    created_at TIMESTAMP,
    resolved   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_alerts_created ON alerts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_site ON alerts(site_id);

CREATE TABLE IF NOT EXISTS traffic_stats (
    date       TEXT NOT NULL,
    site_id    TEXT NOT NULL,
    requests   INTEGER NOT NULL DEFAULT 0,
    bytes      INTEGER NOT NULL DEFAULT 0,
    status_2xx INTEGER NOT NULL DEFAULT 0,
    status_3xx INTEGER NOT NULL DEFAULT 0,
    status_4xx INTEGER NOT NULL DEFAULT 0,
    status_5xx INTEGER NOT NULL DEFAULT 0,
    unique_ips INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (date, site_id)
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS users (
    username      TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'admin',
    created_at    TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    username   TEXT NOT NULL,
    expires_at TIMESTAMP,
    created_at TIMESTAMP
);

-- 登录验证码/改密验证码/邮箱验证验证码统一存这里。
-- code_hash 用 bcrypt（不存明文）：即使数据库泄漏，攻击者拿到的是不可逆哈希。
-- attempts 防止暴力尝试：>5 次直接 consume 掉。
-- purpose 区分用途，避免"改密验证码"被拿去登录。
CREATE TABLE IF NOT EXISTS login_challenges (
    id          TEXT PRIMARY KEY,
    username    TEXT NOT NULL,
    code_hash   TEXT NOT NULL,
    purpose     TEXT NOT NULL,
    expires_at  TIMESTAMP,
    attempts    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_challenges_user ON login_challenges(username, purpose);

CREATE TABLE IF NOT EXISTS integrity (
    id         TEXT PRIMARY KEY,
    site_id    TEXT NOT NULL,
    path       TEXT NOT NULL,
    hash       TEXT NOT NULL,
    size       INTEGER NOT NULL DEFAULT 0,
    mode       INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP,
    UNIQUE(site_id, path)
);
CREATE INDEX IF NOT EXISTS idx_integrity_site ON integrity(site_id);

CREATE TABLE IF NOT EXISTS tamper_events (
    id         TEXT PRIMARY KEY,
    site_id    TEXT NOT NULL,
    path       TEXT NOT NULL,
    old_hash   TEXT NOT NULL DEFAULT '',
    new_hash   TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL DEFAULT 'modified',
    resolved   INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_tamper_site ON tamper_events(site_id);

-- v0.2.0: 计划任务（cron）。schedule 是 5 段 crontab 表达式；next_run_at 由调度器维护。
CREATE TABLE IF NOT EXISTS cron_jobs (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    schedule    TEXT NOT NULL,
    target      TEXT NOT NULL DEFAULT '',
    payload     TEXT NOT NULL DEFAULT '{}',
    last_run_at TIMESTAMP,
    last_status TEXT NOT NULL DEFAULT '',
    last_msg    TEXT NOT NULL DEFAULT '',
    next_run_at TIMESTAMP,
    created_at  TIMESTAMP,
    updated_at  TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_next ON cron_jobs(next_run_at);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_enabled ON cron_jobs(enabled);

-- v0.2.0: 计划任务执行历史。仅保留最近 50 条/任务，超出滚动删除。
CREATE TABLE IF NOT EXISTS cron_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id     TEXT NOT NULL,
    started_at TIMESTAMP,
    ended_at   TIMESTAMP,
    status     TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cron_runs_job ON cron_runs(job_id, started_at DESC);

-- v0.2.0: 备份记录。path 是绝对路径；size 单位字节。
CREATE TABLE IF NOT EXISTS backups (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    target      TEXT NOT NULL,
    target_name TEXT NOT NULL DEFAULT '',
    size        INTEGER NOT NULL DEFAULT 0,
    path        TEXT NOT NULL DEFAULT '',
    encrypted   INTEGER NOT NULL DEFAULT 0,
    note        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_backups_target ON backups(target, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_backups_created ON backups(created_at DESC);

-- v0.2.2: API Token（机器访问凭据）。只存 SHA-256 摘要，不存明文。
CREATE TABLE IF NOT EXISTS api_tokens (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    prefix      TEXT NOT NULL DEFAULT '',
    token_hash  TEXT NOT NULL,
    scopes      TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP,
    last_used_at TIMESTAMP,
    expires_at  TIMESTAMP,
    disabled    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash);

-- v0.2.2: 操作审计。面板以 root 运行，所有写操作都留痕。
CREATE TABLE IF NOT EXISTS audit_logs (
    id         TEXT PRIMARY KEY,
    username   TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL DEFAULT '',
    target     TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    method     TEXT NOT NULL DEFAULT '',
    path       TEXT NOT NULL DEFAULT '',
    status     INTEGER NOT NULL DEFAULT 0,
    ip         TEXT NOT NULL DEFAULT '',
    ua         TEXT NOT NULL DEFAULT '',
    via_token  INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action);

-- v0.8.0: 两步验证恢复码。手机丢失/损坏时用户唯一能自救登录的通道。
-- code_hash 用 SHA-256（不是 bcrypt）：恢复码是 12 位 base32 的高熵随机串
-- （约 60 bit），字典攻击不可行，无需慢哈希；bcrypt 那样的是给 6 位低熵
-- 验证码（login_challenges）准备的。used_at 非空即已消费（一次性）。
CREATE TABLE IF NOT EXISTS totp_recovery_codes (
    id         TEXT PRIMARY KEY,
    username   TEXT NOT NULL,
    code_hash  TEXT NOT NULL,
    used_at    TIMESTAMP,
    created_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_recovery_user ON totp_recovery_codes(username, used_at);

-- v0.7.0: 宿主机指标持久化（分钟粒度趋势）。
-- 内存环形缓冲只保留最近约 20 分钟的十秒级细节，重启即丢；
-- 本表由后台采集器每分钟落一条降采样数据，让"近 24 小时"曲线跨重启存在。
-- ts 用 Unix 秒并做主键：范围查询免转换，同一分钟重复写入天然覆盖（幂等）。
-- 保留期由调用方定期 PruneMetricsSamples 清理，不靠触发器。
CREATE TABLE IF NOT EXISTS metrics_samples (
    ts          INTEGER PRIMARY KEY,
    cpu         REAL NOT NULL DEFAULT 0,
    mem         REAL NOT NULL DEFAULT 0,
    load1       REAL NOT NULL DEFAULT 0,
    disk        REAL NOT NULL DEFAULT 0,
    net_rx      REAL NOT NULL DEFAULT 0,
    net_tx      REAL NOT NULL DEFAULT 0
);
`
	if _, err := d.db.Exec(schema); err != nil {
		return err
	}
	// 兼容旧库：为 certs 表补 DNS-01 相关列。
	// SQLite 的 ALTER TABLE ADD COLUMN 在列已存在时会报 "duplicate column"，
	// 因此逐条尝试并忽略该错误，其余错误才上报。
	for _, stmt := range []string{
		`ALTER TABLE certs ADD COLUMN dns_provider TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE certs ADD COLUMN dns_config TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE certs ADD COLUMN method TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := d.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}

	// 兼容旧库：users 表补 email + last_login_* 列。
	for _, stmt := range []string{
		`ALTER TABLE users ADD COLUMN email TEXT`,
		`ALTER TABLE users ADD COLUMN email_verified INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN last_login_ip TEXT`,
		`ALTER TABLE users ADD COLUMN last_login_ua TEXT`,
		`ALTER TABLE users ADD COLUMN last_login_at TIMESTAMP`,
	} {
		if _, err := d.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}

	// 兼容旧库：users 表补两步验证（TOTP）列。
	// totp_secret 存 base32 密钥（认证器共享密钥，验证时需要原文，无法哈希）；
	// totp_enabled=0 表示 setup 了但还没验证过首码（未启用）。
	for _, stmt := range []string{
		`ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := d.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}

	// 兼容旧库：sites 表补 edate（到期时间）
	for _, stmt := range []string{
		`ALTER TABLE sites ADD COLUMN edate TIMESTAMP`,
	} {
		if _, err := d.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

// ---------- 站点 ----------

// SaveSite 新增或更新站点。
func (d *DB) SaveSite(s *model.Site) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 时间戳必须先赋值再序列化：否则 config blob 里存的是零值时间，
	// 而 GetSite/ListSites 只读 config，API 就会返回 0001-01-01。
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	s.UpdatedAt = time.Now()

	config, err := json.Marshal(s)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
INSERT INTO sites (id,name,status,remark,domains,config,created_at,updated_at,edate)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, status=excluded.status, remark=excluded.remark,
  domains=excluded.domains, config=excluded.config, updated_at=excluded.updated_at,
  edate=excluded.edate`,
		s.ID, s.Name, string(s.Status), s.Remark, strings.Join(s.Domains, ","),
		string(config), s.CreatedAt, s.UpdatedAt, s.Edate)
	return err
}

// GetSite 按 ID 读取站点。
func (d *DB) GetSite(id string) (*model.Site, error) {
	row := d.db.QueryRow(`SELECT config FROM sites WHERE id=?`, id)
	var cfg string
	if err := row.Scan(&cfg); err != nil {
		if err == sql.ErrNoRows {
			return nil, model.ErrNotFound
		}
		return nil, err
	}
	var s model.Site
	if err := json.Unmarshal([]byte(cfg), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSites 列出全部站点。
//
// config blob 解析失败时**不能**静默 continue：ApplyAllConfig 的孤儿清理
// 会把"读不到的站点"当成"不存在的站点"，顺手删掉它的 nginx 配置，
// 于是该站点从面板消失且网站静默 404，全程无任何日志。
// 因此这里把损坏的 ID 收集起来由调用方决定如何处理，绝不假装它不存在。
func (d *DB) ListSites() ([]*model.Site, error) {
	rows, err := d.db.Query(`SELECT config FROM sites ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Site
	for rows.Next() {
		var cfg string
		if err := rows.Scan(&cfg); err != nil {
			return nil, err
		}
		var s model.Site
		if err := json.Unmarshal([]byte(cfg), &s); err != nil {
			// 至少把 ID 拿出来，让调用方知道是哪个站点坏了
			badID := extractSiteID(cfg)
			log.Printf("[store] 站点配置解析失败，已跳过: id=%s err=%v", badID, err)
			out = append(out, &model.Site{ID: badID, Name: "(配置损坏)"})
			continue
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// extractSiteID 从损坏的 config JSON 里尽力取出 id 字段，用于日志定位。
func extractSiteID(cfg string) string {
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(cfg), &probe); err != nil {
		return "unknown"
	}
	return probe.ID
}

// DeleteSite 删除站点。
func (d *DB) DeleteSite(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM sites WHERE id=?`, id)
	return err
}

// ---------- 证书 ----------

// SaveCert 新增或更新证书。
func (d *DB) SaveCert(c *model.Cert) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.UpdatedAt = time.Now()
	// DNS 凭据序列化后存 TEXT 列。续签时必须用它重建 provider，
	// 否则通配符证书到期后无法自动续期。
	dnsCfg, err := json.Marshal(c.DNSConfig)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`
INSERT INTO certs (id,name,domains,not_before,not_after,issuer,source,cert_file,key_file,auto_renew,acme_account,dns_provider,dns_config,method,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, domains=excluded.domains, not_before=excluded.not_before,
  not_after=excluded.not_after, issuer=excluded.issuer, source=excluded.source,
  cert_file=excluded.cert_file, key_file=excluded.key_file, auto_renew=excluded.auto_renew,
  acme_account=excluded.acme_account, dns_provider=excluded.dns_provider,
  dns_config=excluded.dns_config, method=excluded.method, updated_at=excluded.updated_at`,
		c.ID, c.Name, strings.Join(c.Domains, ","), c.NotBefore, c.NotAfter,
		c.Issuer, c.Source, c.CertFile, c.KeyFile, boolToInt(c.AutoRenew),
		c.ACMEAccount, c.DNSProvider, string(dnsCfg), c.Method, c.CreatedAt, c.UpdatedAt)
	return err
}

// ListCerts 列出全部证书。
func (d *DB) ListCerts() ([]*model.Cert, error) {
	rows, err := d.db.Query(`SELECT id,name,domains,not_before,not_after,issuer,source,cert_file,key_file,auto_renew,acme_account,dns_provider,dns_config,method,created_at,updated_at FROM certs ORDER BY not_after ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Cert
	for rows.Next() {
		var c model.Cert
		var domains, dnsCfg string
		var autoRenew int
		if err := rows.Scan(&c.ID, &c.Name, &domains, &c.NotBefore, &c.NotAfter,
			&c.Issuer, &c.Source, &c.CertFile, &c.KeyFile, &autoRenew,
			&c.ACMEAccount, &c.DNSProvider, &dnsCfg, &c.Method,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Domains = splitList(domains)
		c.AutoRenew = autoRenew != 0
		// DNS 凭据解析失败不阻断列表：退化为空 map，
		// 续签时会明确报"缺少 DNS 配置"而不是静默失败。
		if dnsCfg != "" && dnsCfg != "null" {
			_ = json.Unmarshal([]byte(dnsCfg), &c.DNSConfig)
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// GetCert 按 ID 读取证书。
func (d *DB) GetCert(id string) (*model.Cert, error) {
	certs, err := d.ListCerts()
	if err != nil {
		return nil, err
	}
	for _, c := range certs {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, model.ErrNotFound
}

// DeleteCert 删除证书。
func (d *DB) DeleteCert(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM certs WHERE id=?`, id)
	return err
}

// ---------- ACME 账户 ----------

// SaveACMEAccount 新增 ACME 账户。
func (d *DB) SaveACMEAccount(a *model.ACMEAccount) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := d.db.Exec(`
INSERT INTO acme_accounts (id,email,ca,key_type,key_path,created_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  email=excluded.email, ca=excluded.ca, key_type=excluded.key_type, key_path=excluded.key_path`,
		a.ID, a.Email, a.CA, a.KeyType, a.KeyPath, a.CreatedAt)
	return err
}

// ListACMEAccounts 列出 ACME 账户。
func (d *DB) ListACMEAccounts() ([]*model.ACMEAccount, error) {
	rows, err := d.db.Query(`SELECT id,email,ca,key_type,key_path,created_at FROM acme_accounts ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.ACMEAccount
	for rows.Next() {
		var a model.ACMEAccount
		if err := rows.Scan(&a.ID, &a.Email, &a.CA, &a.KeyType, &a.KeyPath, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// GetACMEAccount 按 ID 读取账户。
func (d *DB) GetACMEAccount(id string) (*model.ACMEAccount, error) {
	accs, err := d.ListACMEAccounts()
	if err != nil {
		return nil, err
	}
	for _, a := range accs {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, model.ErrNotFound
}

// ---------- 告警 ----------

// SaveAlert 保存告警。
func (d *DB) SaveAlert(a *model.Alert) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := d.db.Exec(`
INSERT INTO alerts (id,site_id,site_name,type,level,message,created_at,resolved)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET resolved=excluded.resolved`,
		a.ID, a.SiteID, a.SiteName, a.Type, a.Level, a.Message, a.CreatedAt, boolToInt(a.Resolved))
	return err
}

// ListAlerts 按条件列出告警。
func (d *DB) ListAlerts(limit int, unresolvedOnly bool) ([]*model.Alert, error) {
	q := `SELECT id,site_id,site_name,type,level,message,created_at,resolved FROM alerts`
	args := []any{}
	if unresolvedOnly {
		q += ` WHERE resolved=0`
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Alert
	for rows.Next() {
		var a model.Alert
		var resolved int
		if err := rows.Scan(&a.ID, &a.SiteID, &a.SiteName, &a.Type, &a.Level,
			&a.Message, &a.CreatedAt, &resolved); err != nil {
			return nil, err
		}
		a.Resolved = resolved != 0
		out = append(out, &a)
	}
	return out, rows.Err()
}

// ResolveAlert 标记告警已处理。
func (d *DB) ResolveAlert(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE alerts SET resolved=1 WHERE id=?`, id)
	return err
}

// CountOpenAlertsBySite 统计各站点未处理告警数。
func (d *DB) CountOpenAlertsBySite() (map[string]int, error) {
	rows, err := d.db.Query(`SELECT site_id, COUNT(*) FROM alerts WHERE resolved=0 GROUP BY site_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ---------- 流量统计 ----------

// SaveTrafficStat 累加某日某站点的流量统计。
func (d *DB) SaveTrafficStat(s *model.TrafficStat) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`
INSERT INTO traffic_stats (date,site_id,requests,bytes,status_2xx,status_3xx,status_4xx,status_5xx,unique_ips)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(date,site_id) DO UPDATE SET
  requests=requests+excluded.requests, bytes=bytes+excluded.bytes,
  status_2xx=status_2xx+excluded.status_2xx, status_3xx=status_3xx+excluded.status_3xx,
  status_4xx=status_4xx+excluded.status_4xx, status_5xx=status_5xx+excluded.status_5xx,
  unique_ips=excluded.unique_ips`,
		s.Date, s.SiteID, s.Requests, s.Bytes, s.Status2xx, s.Status3xx,
		s.Status4xx, s.Status5xx, s.UniqueIPs)
	return err
}

// GetTrafficStats 查询站点最近 days 天统计。
func (d *DB) GetTrafficStats(siteID string, days int) ([]*model.TrafficStat, error) {
	since := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	rows, err := d.db.Query(`SELECT date,site_id,requests,bytes,status_2xx,status_3xx,status_4xx,status_5xx,unique_ips
FROM traffic_stats WHERE site_id=? AND date>=? ORDER BY date ASC`, siteID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.TrafficStat
	for rows.Next() {
		var s model.TrafficStat
		if err := rows.Scan(&s.Date, &s.SiteID, &s.Requests, &s.Bytes, &s.Status2xx,
			&s.Status3xx, &s.Status4xx, &s.Status5xx, &s.UniqueIPs); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// GetTrafficSummary 汇总站点今日与本月流量。
func (d *DB) GetTrafficSummary(siteID string) (today, month int64, err error) {
	todayStr := time.Now().Format("2006-01-02")
	monthStr := time.Now().Format("2006-01")

	row := d.db.QueryRow(`SELECT
  COALESCE(SUM(CASE WHEN date=? THEN requests ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN date=? THEN bytes ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN date LIKE ? THEN bytes ELSE 0 END),0)
FROM traffic_stats WHERE site_id=?`, todayStr, todayStr, monthStr+"%", siteID)

	var reqs int64
	if err = row.Scan(&reqs, &today, &month); err != nil {
		return 0, 0, err
	}
	return reqs, month, nil
}

// ---------- 设置 ----------

// SetSetting 写入单个设置。
func (d *DB) SetSetting(key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT INTO settings (key,value) VALUES (?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetSetting 读取单个设置。
func (d *DB) GetSetting(key string) (string, error) {
	var v string
	err := d.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// AllSettings 读取全部设置。
func (d *DB) AllSettings() (map[string]string, error) {
	rows, err := d.db.Query(`SELECT key,value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ---------- 用户与会话 ----------

// SaveUser 保存用户。
func (d *DB) SaveUser(username, hash, role string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT INTO users (username,password_hash,role,created_at)
VALUES (?,?,?,?)
ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash, role=excluded.role`,
		username, hash, role, time.Now())
	return err
}

// GetUser 读取用户。
func (d *DB) GetUser(username string) (hash, role string, err error) {
	err = d.db.QueryRow(`SELECT password_hash,role FROM users WHERE username=?`, username).
		Scan(&hash, &role)
	if err == sql.ErrNoRows {
		return "", "", model.ErrNotFound
	}
	return hash, role, err
}

// SetTOTPSecret 存放两步验证密钥（base32）。
//
// enabled=false 表示「已生成密钥但还没验证过首码」——setup/enable 两步走，
// 只有验证过动态口令才真正启用。覆盖同名用户的旧 pending 密钥。
func (d *DB) SetTOTPSecret(username, secret string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE users SET totp_secret=?, totp_enabled=0 WHERE username=?`,
		secret, username)
	return err
}

// EnableTOTP 验证首码通过后启用两步验证。
func (d *DB) EnableTOTP(username string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`UPDATE users SET totp_enabled=1 WHERE username=? AND totp_secret<>''`,
		username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// DisableTOTP 关闭两步验证（清空密钥）。幂等：未启用时调用也返回成功。
func (d *DB) DisableTOTP(username string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE users SET totp_secret='', totp_enabled=0 WHERE username=?`,
		username)
	return err
}

// GetTOTP 读取两步验证状态。
func (d *DB) GetTOTP(username string) (secret string, enabled bool, err error) {
	var e int
	err = d.db.QueryRow(`SELECT totp_secret, totp_enabled FROM users WHERE username=?`, username).
		Scan(&secret, &e)
	if err == sql.ErrNoRows {
		return "", false, model.ErrNotFound
	}
	if err != nil {
		return "", false, err
	}
	return secret, e == 1, nil
}

// UserProfile 用户的扩展资料（含邮箱、最近登录）。原 GetUser 只返
// hash/role 不变以保持兼容；UI 用本结构展示。
type UserProfile struct {
	Username      string
	Email         string
	EmailVerified bool
	LastLoginIP   string
	LastLoginUA   string
	LastLoginAt   time.Time
	CreatedAt     time.Time
}

// GetUserProfile 取扩展资料。email 为空时 EmailVerified 也视为 false。
//
// 所有可能为 NULL 的字段（last_login_* / created_at）都用 NullString/NullTime
// 接住，nil 值会变成零值。早起只防了 last_login，created_at NULL 同样会让
// Scan 失败返回 "converting NULL to time.Time is unsupported"。
func (d *DB) GetUserProfile(username string) (*UserProfile, error) {
	p := &UserProfile{Username: username}
	var ev int
	var ip, ua sql.NullString
	var loginAt, createdAt sql.NullTime
	err := d.db.QueryRow(`SELECT email, email_verified, last_login_ip, last_login_ua,
		last_login_at, created_at FROM users WHERE username=?`, username).
		Scan(&p.Email, &ev, &ip, &ua, &loginAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if ip.Valid {
		p.LastLoginIP = ip.String
	}
	if ua.Valid {
		p.LastLoginUA = ua.String
	}
	if loginAt.Valid {
		p.LastLoginAt = loginAt.Time
	}
	if createdAt.Valid {
		p.CreatedAt = createdAt.Time
	}
	p.EmailVerified = ev != 0
	return p, nil
}

// UpdateUserEmail 改邮箱，verified 置 0（需新邮件验证）。
func (d *DB) UpdateUserEmail(username, email string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE users SET email=?, email_verified=0 WHERE username=?`, email, username)
	return err
}

// MarkEmailVerified 邮箱验证通过。
func (d *DB) MarkEmailVerified(username string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE users SET email_verified=1 WHERE username=? AND email<>''`, username)
	return err
}

// UpdateLastLogin 记录最近登录的 IP/UA/时间。
//
// 不论异常登录是否触发了验证码，最终通过验证的下发 session 那一刻都
// 算"成功登录"，都该更新 last_login_*。异常判断时比对的是更新前的值。
func (d *DB) UpdateLastLogin(username, ip, ua string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE users SET last_login_ip=?, last_login_ua=?, last_login_at=? WHERE username=?`,
		ip, ua, time.Now(), username)
	return err
}

// ---------- 登录验证码（login_challenges） ----------

// SaveChallenge 创建一条验证码记录。
// id 由调用方生成（store.NewID()，16 字节随机）。
// code 必须是明文，因为后续要算哈希入库。bcrypt 不可逆，校验时也只能拿明文比对。
func (d *DB) SaveChallenge(id, username, codeHash, purpose string, ttl time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 同 purpose 只保留最新一条：避免旧 challenge 留作后门。
	_, _ = d.db.Exec(`DELETE FROM login_challenges WHERE username=? AND purpose=?`, username, purpose)
	_, err := d.db.Exec(`INSERT INTO login_challenges
		(id, username, code_hash, purpose, expires_at, attempts, created_at)
		VALUES (?,?,?,?,?,0,?)`,
		id, username, codeHash, purpose, time.Now().Add(ttl), time.Now())
	return err
}

// Challenge 单条验证码记录。
type Challenge struct {
	ID        string
	Username  string
	CodeHash  string
	Purpose   string
	ExpiresAt time.Time
	Attempts  int
	CreatedAt time.Time
}

// GetChallenge 按 id 查一条 challenge。
// 校验时调用 — 不在 SQL 里直接 WHERE expires_at>? 比较，因为想让调用方
// 区分"未找到"和"已过期"两种错误。
func (d *DB) GetChallenge(id string) (*Challenge, error) {
	c := &Challenge{}
	err := d.db.QueryRow(`SELECT id, username, code_hash, purpose, expires_at, attempts, created_at
		FROM login_challenges WHERE id=?`, id).
		Scan(&c.ID, &c.Username, &c.CodeHash, &c.Purpose, &c.ExpiresAt, &c.Attempts, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// IncChallengeAttempts 校验失败时调用。返回更新后的 attempts。
func (d *DB) IncChallengeAttempts(id string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(`UPDATE login_challenges SET attempts=attempts+1 WHERE id=?`, id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, model.ErrNotFound
	}
	var att int
	if err := d.db.QueryRow(`SELECT attempts FROM login_challenges WHERE id=?`, id).Scan(&att); err != nil {
		return 0, err
	}
	return att, nil
}

// ConsumeChallenge 校验通过或超限后调用：删掉。
// 不重用是核心：验证码是"一次性"凭据。
func (d *DB) ConsumeChallenge(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM login_challenges WHERE id=?`, id)
	return err
}

// CleanExpiredChallenges 清理过期 challenge，runSessionCleaner 里周期调用。
func (d *DB) CleanExpiredChallenges() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM login_challenges WHERE expires_at < ?`, time.Now())
	return err
}

// SaveSession 保存会话。
func (d *DB) SaveSession(token, username string, ttl time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT INTO sessions (token,username,expires_at,created_at)
VALUES (?,?,?,?) ON CONFLICT(token) DO UPDATE SET expires_at=excluded.expires_at`,
		token, username, time.Now().Add(ttl), time.Now())
	return err
}

// GetSession 校验会话。
func (d *DB) GetSession(token string) (string, error) {
	var username string
	var expires time.Time
	err := d.db.QueryRow(`SELECT username,expires_at FROM sessions WHERE token=?`, token).
		Scan(&username, &expires)
	if err == sql.ErrNoRows {
		return "", model.ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	if time.Now().After(expires) {
		d.DeleteSession(token)
		return "", model.ErrUnauthorized
	}
	return username, nil
}

// DeleteSession 删除会话。
func (d *DB) DeleteSession(token string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM sessions WHERE token=?`, token)
	return err
}

// DeleteUserSessionsExcept 删除某用户除 keepToken 外的所有会话，
// 返回删除条数。用于改密后踢掉其它已登录端。
func (d *DB) DeleteUserSessionsExcept(username, keepToken string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.db.Exec(
		`DELETE FROM sessions WHERE username=? AND token<>?`, username, keepToken)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CleanSessions 清理过期会话。
func (d *DB) CleanSessions() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now())
	return err
}

// ---------- 完整性基线（防复改） ----------

// ReplaceIntegrity 整体替换某站点的完整性基线。
func (d *DB) ReplaceIntegrity(siteID string, files map[string]fileHash) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM integrity WHERE site_id=?`, siteID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO integrity (id,site_id,path,hash,size,mode,created_at)
VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for path, fh := range files {
		if _, err := stmt.Exec(newID(), siteID, path, fh.Hash, fh.Size, fh.Mode, time.Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetIntegrity 读取某站点基线。
func (d *DB) GetIntegrity(siteID string) (map[string]fileHash, error) {
	rows, err := d.db.Query(`SELECT path,hash,size,mode FROM integrity WHERE site_id=?`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]fileHash{}
	for rows.Next() {
		var p string
		var fh fileHash
		if err := rows.Scan(&p, &fh.Hash, &fh.Size, &fh.Mode); err != nil {
			return nil, err
		}
		out[p] = fh
	}
	return out, rows.Err()
}

// SaveTamperEvent 记录篡改事件。
func (d *DB) SaveTamperEvent(e *TamperEvent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT INTO tamper_events (id,site_id,path,old_hash,new_hash,kind,resolved,created_at)
VALUES (?,?,?,?,?,?,0,?)`, e.ID, e.SiteID, e.Path, e.OldHash, e.NewHash, e.Kind, e.CreatedAt)
	return err
}

// ListTamperEvents 列出站点篡改事件。
func (d *DB) ListTamperEvents(siteID string, limit int) ([]*TamperEvent, error) {
	rows, err := d.db.Query(`SELECT id,site_id,path,old_hash,new_hash,kind,created_at
FROM tamper_events WHERE site_id=? ORDER BY created_at DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*TamperEvent
	for rows.Next() {
		var e TamperEvent
		if err := rows.Scan(&e.ID, &e.SiteID, &e.Path, &e.OldHash, &e.NewHash, &e.Kind, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// CountTamperEvents 统计站点篡改事件数。
func (d *DB) CountTamperEvents(siteID string) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM tamper_events WHERE site_id=? AND resolved=0`, siteID).Scan(&n)
	return n, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
