#!/bin/bash
# ============================================================================
# SitePanel -> ZhiweiPanel 迁移脚本（宿主机 / systemd）
#
# 改名同时改了运行时标识符，且不保留旧名兼容。线上升级必须跑这个，
# 或按 README「改名迁移」一节手工执行。
#
# 三个最容易踩的坑（脚本里都已处理）：
#
#   1. 数据库里存的是**绝对路径**（sites.config 的 ssl.cert_file、
#      certs.cert_file/key_file、acme_accounts.key_path、backups.path）。
#      安装目录一改名，这些路径全部指向不存在的文件 -> 渲染出的站点配置
#      里的 ssl_certificate 加载失败 -> nginx -t 失败 -> 面板配置下发失败
#      （**不阻断启动**，只是打一行日志，于是面板看起来"起来了"）。
#      站点当下还能访问（运行中的 nginx 内存里还是旧配置），但下一次任何
#      外部 nginx reload（certbot 续签、机器重启）就会把全部站点带下去。
#
#   2. 旧的 00-/01- nginx 配置文件必须**先移走**再启动新面板：
#      旧 00- 里的 limit_req_zone zone 名带站点 ID（改名不变）-> 重复定义；
#      旧 01- 带 default_server -> a duplicate default server。
#      两者都会让 nginx -t 失败、整批站点一起停摆。
#
#   3. systemd 单元不能只改路径：ExecStart 的多行续行若首行末尾没有反斜杠，
#      systemd 会把后续每行当独立指令丢弃（Missing '=', ignoring line），
#      面板就只吃到第一个 flag；单元缺 [Install] 则是 static 状态，
#      重启机器不会自启。
#
# 用法：
#   bash deploy/migrate-rename.sh <dry-run|apply>
#
# apply 前请确认：
#   * 新二进制已放到 /tmp/zw_upload/zhiweipanel
#   * 已备份 /opt/sitepanel
#
# 路径可通过环境变量覆盖（便于在沙箱里演练）：
#   OLD_DIR NEW_DIR OLD_UNIT NEW_UNIT OLD_ENV NEW_ENV SRC_BIN
#   NGINX_CONF BACKUP_ROOT SYSTEMCTL
# ============================================================================
set -uo pipefail

MODE="${1:-dry-run}"

OLD_DIR=${OLD_DIR:-/opt/sitepanel}
NEW_DIR=${NEW_DIR:-/opt/zhiweipanel}
OLD_UNIT=${OLD_UNIT:-sitepanel}
NEW_UNIT=${NEW_UNIT:-zhiweipanel}
OLD_ENV=${OLD_ENV:-/etc/sitepanel/sitepanel.env}
NEW_ENV=${NEW_ENV:-/etc/zhiweipanel/zhiweipanel.env}
SRC_BIN=${SRC_BIN:-/tmp/zw_upload/zhiweipanel}
NGINX_CONF=${NGINX_CONF:-/etc/nginx/nginx.conf}
BACKUP_ROOT=${BACKUP_ROOT:-/root/zw-migrate-backup}
SYSTEMCTL=${SYSTEMCTL:-systemctl}
NGINX_BIN=${NGINX_BIN:-/usr/sbin/nginx}
UNIT_DIR=${UNIT_DIR:-/etc/systemd/system}

STAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_DIR="$BACKUP_ROOT/$STAMP"
NEW_BIN="$NEW_DIR/zhiweipanel"
UNIT="$UNIT_DIR/${NEW_UNIT}.service"
OLD_UNIT_FILE="$UNIT_DIR/${OLD_UNIT}.service"
OLD_DB="$OLD_DIR/data/${OLD_UNIT}.db"
NEW_DB="$NEW_DIR/data/${NEW_UNIT}.db"

say()  { echo "[$(date +%H:%M:%S)] $*"; }
step() { echo; echo "===== $* ====="; }
die()  { echo "FATAL: $*" >&2; exit 1; }

if [ "$MODE" != "apply" ]; then
  say "DRY RUN MODE - nothing will be modified"
fi

# run 在 apply 模式执行命令，dry-run 模式只打印。
run() {
  if [ "$MODE" = "apply" ]; then
    eval "$@"
  else
    echo "  [dry-run] $*"
  fi
}

# ---------------------------------------------------------------------------
# rewrite_db_paths：把数据库里的旧绝对路径前缀换成新的。
# 独立成函数（而不是塞进 run + heredoc）——eval 里的 heredoc 很难读也易错。
# 用法：rewrite_db_paths <db路径> <旧前缀> <新前缀>
# ---------------------------------------------------------------------------
rewrite_db_paths() {
  local db=$1 old=$2 new=$3
  python3 - "$db" "$old" "$new" <<'PYEOF'
import sqlite3, sys
db, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
c = sqlite3.connect(db)
n = 0
for sid, cfg in c.execute("select id, config from sites").fetchall():
    if old in cfg:
        c.execute("update sites set config=? where id=?", (cfg.replace(old, new), sid))
        n += 1
for t, col in (("certs", "cert_file"), ("certs", "key_file"),
               ("acme_accounts", "key_path"), ("backups", "path")):
    try:
        rows = c.execute("select id, %s from %s where %s like ?" % (col, t, col),
                         ("%" + old + "%",)).fetchall()
    except sqlite3.Error:
        continue
    for rid, v in rows:
        c.execute("update %s set %s=? where id=?" % (t, col), (v.replace(old, new), rid))
        n += 1
c.commit()
left = 0
for t in [r[0] for r in c.execute(
        "select name from sqlite_master where type='table' and name not like 'sqlite_%'")]:
    for col in [r[1] for r in c.execute("PRAGMA table_info(%s)" % t)]:
        try:
            left += c.execute("select count(*) from %s where cast(%s as text) like ?"
                              % (t, col), ("%" + old + "%",)).fetchone()[0]
        except sqlite3.Error:
            pass
c.close()
print("  rows rewritten: %d, remaining old refs: %d" % (n, left))
sys.exit(1 if left else 0)
PYEOF
}

# ---------------------------------------------------------------------------
# write_unit：按线上真实取值生成单元文件。
# 用法：write_unit <目标单元路径> <旧单元路径> <新目录> <新env路径> <新二进制> <新单元名>
# ---------------------------------------------------------------------------
write_unit() {
  local unit=$1 old_unit_file=$2 new_dir=$3 new_env=$4 new_bin=$5 new_unit=$6
  local tls_hosts nginx_bin admin tls_flag=""
  tls_hosts=$(grep -o '\-tls-hosts=[^ ]*' "$old_unit_file" | head -1)
  nginx_bin=$(grep -o '\-nginx [^ ]*' "$old_unit_file" | head -1 | awk '{print $2}')
  admin=$(grep -o '\-admin [^ ]*' "$old_unit_file" | head -1 | awk '{print $2}')
  [ -n "$tls_hosts" ] || tls_hosts="-tls-hosts=127.0.0.1,localhost"
  [ -n "$nginx_bin" ] || nginx_bin="/usr/sbin/nginx"
  [ -n "$admin" ] || admin="admin"
  grep -q '\-tls\b' "$old_unit_file" && tls_flag="-tls"

  cat > "$unit" <<EOF
[Unit]
Description=ZhiweiPanel - 轻量建站面板
Documentation=https://github.com/zhiwei/panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# nginx 需 root 权限执行 nginx -t 与 reload
User=root
WorkingDirectory=$new_dir

# 初始管理员口令放在 600 权限的 EnvironmentFile 里，**不要**写进本文件：
# systemd 的 unit 文件默认全局可读，写进去等于把管理员口令公开。
EnvironmentFile=$new_env

ExecStart=$new_bin $tls_flag $tls_hosts -port 8888 -runtime host -data $new_dir/data -conf $new_dir/nginx/conf.d -site-conf $new_dir/nginx/conf.d -nginx $nginx_bin -admin $admin -admin-pass \${ZW_ADMIN_PASS}
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=20

# 日志输出到 journald
StandardOutput=journal
StandardError=journal
SyslogIdentifier=$new_unit

# 基础加固
NoNewPrivileges=false
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
  chmod 644 "$unit"
  echo "  unit written: tls='$tls_flag $tls_hosts' nginx=$nginx_bin admin=$admin"
}

# ---------------------------------------------------------------------------
step "0. Preflight"
# ---------------------------------------------------------------------------
[ "$(id -u)" = "0" ] || die "must run as root"
[ -d "$OLD_DIR" ] || die "$OLD_DIR not found (already migrated?)"
[ -f "$SRC_BIN" ] || die "$SRC_BIN not found (upload the new binary first)"
[ -d "$NEW_DIR" ] && die "$NEW_DIR already exists - inspect manually before continuing"

say "unit   : $($SYSTEMCTL list-unit-files 2>/dev/null | grep -E "^${OLD_UNIT}\.service" | awk '{print $1}')"
say "pid    : $(pgrep -f "$OLD_DIR/$OLD_UNIT" | head -1)"
say "nginx  : $($NGINX_BIN -t 2>&1 | tail -1)"
say "sites  : $(ls "$OLD_DIR/nginx/conf.d/" 2>/dev/null | grep -c '^10-site-') site confs"

DB_ROWS_BEFORE=$(python3 - "$OLD_DB" <<'PY' 2>/dev/null || echo "?"
import sqlite3, sys
c = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True)
print(c.execute("select count(*) from sites").fetchone()[0])
PY
)
say "db rows before: $DB_ROWS_BEFORE"

# ---------------------------------------------------------------------------
step "1. Backup"
# ---------------------------------------------------------------------------
if [ "$MODE" = "apply" ]; then
  mkdir -p "$BACKUP_DIR"
  cp -a "$OLD_DIR" "$BACKUP_DIR/sitepanel-dir"
  cp -a "$OLD_UNIT_FILE" "$BACKUP_DIR/" 2>/dev/null || true
  cp -a "$OLD_ENV" "$BACKUP_DIR/sitepanel.env" 2>/dev/null || true
  cp -a "$NGINX_CONF" "$BACKUP_DIR/nginx.conf"
  cp -a "$OLD_DIR/nginx/conf.d" "$BACKUP_DIR/panel-conf.d"
  cp -a "/root/${OLD_UNIT}-admin-pass.txt" "$BACKUP_DIR/admin-pass.txt" 2>/dev/null || true
  chmod 600 "$BACKUP_DIR/sitepanel.env" "$BACKUP_DIR/admin-pass.txt" 2>/dev/null || true
  say "backup -> $BACKUP_DIR"
  du -sh "$BACKUP_DIR" | sed 's/^/  /'
else
  say "would back up install dir / unit / env / nginx.conf / conf.d -> $BACKUP_DIR"
fi

# ---------------------------------------------------------------------------
step "2. Stop panel (nginx keeps serving the sites)"
# ---------------------------------------------------------------------------
run "$SYSTEMCTL stop $OLD_UNIT"
sleep 2
if [ "$MODE" = "apply" ]; then
  $SYSTEMCTL is-active --quiet "$OLD_UNIT" && die "panel still running after stop"
  say "panel stopped"
  say "checkpointing sqlite WAL"
  python3 - "$OLD_DB" <<'PY' || true
import sqlite3, sys
try:
    c = sqlite3.connect(sys.argv[1])
    c.execute("PRAGMA wal_checkpoint(TRUNCATE);")
    c.close()
    print("  wal checkpoint ok")
except Exception as e:
    print("  wal checkpoint skipped:", e)
PY
fi

# ---------------------------------------------------------------------------
step "3. Move install dir; rename db (+wal/shm) and binary"
# ---------------------------------------------------------------------------
run "mv $OLD_DIR $NEW_DIR"
# db + -wal + -shm 必须一起搬 —— 删掉 WAL 会丢已提交的数据。
# -wal/-shm 只在 db 处于 WAL 模式时才存在，不存在时静默跳过。
run "mv $NEW_DIR/data/${OLD_UNIT}.db $NEW_DIR/data/${NEW_UNIT}.db"
run "mv $NEW_DIR/data/${OLD_UNIT}.db-wal $NEW_DIR/data/${NEW_UNIT}.db-wal 2>/dev/null || true"
run "mv $NEW_DIR/data/${OLD_UNIT}.db-shm $NEW_DIR/data/${NEW_UNIT}.db-shm 2>/dev/null || true"
# 旧二进制留作回滚件
run "mv $NEW_DIR/$OLD_UNIT $NEW_DIR/${NEW_UNIT}.v0.1.0.bak"
run "rm -f $NEW_DIR/install.sh"

# ---------------------------------------------------------------------------
step "4. Credentials file: move + rename the env var"
# ---------------------------------------------------------------------------
run "mkdir -p $(dirname "$NEW_ENV")"
run "mv $OLD_ENV $NEW_ENV"
run "sed -i 's/SP_ADMIN_PASS/ZW_ADMIN_PASS/g' $NEW_ENV"
run "sed -i 's/SitePanel 初始管理员口令/智维面板初始管理员口令/' $NEW_ENV"
run "chmod 600 $NEW_ENV"
run "chmod 700 $(dirname "$NEW_ENV")"
run "mv /root/${OLD_UNIT}-admin-pass.txt /root/${NEW_UNIT}-admin-pass.txt 2>/dev/null || true"

# ---------------------------------------------------------------------------
step "5. Rewrite the systemd unit"
# ---------------------------------------------------------------------------
# 整份重写而不是 sed 改路径：ExecStart 续行必须格式正确，
# 且必须带 [Install]，否则单元是 static 状态、重启机器不会自启。
if [ "$MODE" = "apply" ]; then
  write_unit "$UNIT" "$OLD_UNIT_FILE" "$NEW_DIR" "$NEW_ENV" "$NEW_BIN" "$NEW_UNIT"
  $SYSTEMCTL daemon-reload
  $SYSTEMCTL disable "$OLD_UNIT" 2>/dev/null || true
  $SYSTEMCTL enable "$NEW_UNIT"
  rm -f "$OLD_UNIT_FILE"
else
  echo "  [dry-run] rewrite $UNIT (keeping live tls/nginx/admin), daemon-reload, disable old, enable new, rm old unit"
fi

# ---------------------------------------------------------------------------
step "6. Move OLD nginx conf files away (before the new panel starts)"
# ---------------------------------------------------------------------------
run "mv $NEW_DIR/nginx/conf.d/00-${OLD_UNIT}-http.conf $NEW_DIR/nginx/conf.d/00-${OLD_UNIT}-http.conf.migrated.bak"
run "mv $NEW_DIR/nginx/conf.d/01-${OLD_UNIT}-default-server.conf $NEW_DIR/nginx/conf.d/01-${OLD_UNIT}-default-server.conf.migrated.bak"

# ---------------------------------------------------------------------------
step "7. Update nginx.conf include path"
# ---------------------------------------------------------------------------
run "sed -i 's#include $OLD_DIR/nginx/conf.d/\*.conf;#include $NEW_DIR/nginx/conf.d/*.conf;#' $NGINX_CONF"
if [ "$MODE" = "apply" ]; then
  say "nginx -t is EXPECTED to fail right now (old site confs still reference the old log_format); the new panel re-renders them on startup"
fi

# ---------------------------------------------------------------------------
step "8. Fix the ABSOLUTE PATHS stored in the database"
# ---------------------------------------------------------------------------
if [ "$MODE" = "apply" ]; then
  rewrite_db_paths "$NEW_DB" "$OLD_DIR" "$NEW_DIR" || die "old paths remain in the DB"
else
  echo "  [dry-run] rewrite absolute path prefix '$OLD_DIR' -> '$NEW_DIR' in $NEW_DB"
fi

# ---------------------------------------------------------------------------
step "9. Install the new binary + regenerate the panel's own TLS cert"
# ---------------------------------------------------------------------------
run "install -m 0755 $SRC_BIN $NEW_BIN"
run "chown root:root $NEW_BIN"
# 面板自签证书的 CN 带品牌名，且在证书可解析时会被原样复用 -> 重新生成
run "mv $NEW_DIR/data/panel.crt $NEW_DIR/data/panel.crt.stale 2>/dev/null || true"
run "mv $NEW_DIR/data/panel.key $NEW_DIR/data/panel.key.stale 2>/dev/null || true"

# ---------------------------------------------------------------------------
step "10. Start the new panel"
# ---------------------------------------------------------------------------
run "$SYSTEMCTL start $NEW_UNIT"
sleep 6
if [ "$MODE" = "apply" ]; then
  $SYSTEMCTL is-active --quiet "$NEW_UNIT" || die "panel failed to start: $($SYSTEMCTL status $NEW_UNIT --no-pager | tail -8)"
  say "panel active"
  journalctl -u "$NEW_UNIT" --since '-2 min' --no-pager | tail -20
fi

# ---------------------------------------------------------------------------
step "11. Verify"
# ---------------------------------------------------------------------------
if [ "$MODE" = "apply" ]; then
  echo "--- unit ---"
  echo "  active : $($SYSTEMCTL is-active $NEW_UNIT)"
  echo "  enabled: $($SYSTEMCTL is-enabled $NEW_UNIT)"
  echo
  echo "--- nginx -t (must pass) ---"
  $NGINX_BIN -t 2>&1 || die "nginx -t FAILED"
  echo
  echo "--- conf.d ---"
  ls "$NEW_DIR/nginx/conf.d/" | sed 's/^/  /'
  echo
  echo "--- version ---"
  "$NEW_BIN" -version 2>&1 | sed 's/^/  /'
  echo
  echo "--- db sites (before was $DB_ROWS_BEFORE) ---"
  python3 - "$NEW_DB" <<'PY'
import sqlite3, sys
c = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True)
print("  count:", c.execute("select count(*) from sites").fetchone()[0])
for r in c.execute("select name,status from sites"):
    print("  ", r)
PY
  echo
  echo "--- old identifiers gone? ---"
  [ -d "$OLD_DIR" ] && die "$OLD_DIR still exists"
  [ -f "$OLD_UNIT_FILE" ] && die "old unit still exists"
  grep -q sitepanel "$NGINX_CONF" && die "nginx.conf still references sitepanel"
  echo "  clean"
  echo
  echo "--- rollback material ---"
  ls -d "$BACKUP_DIR" 2>/dev/null | sed 's/^/  /'
  ls -la "$NEW_DIR/${NEW_UNIT}.v0.1.0.bak" 2>/dev/null | sed 's/^/  /'
fi

say "MIGRATION $MODE COMPLETE"
