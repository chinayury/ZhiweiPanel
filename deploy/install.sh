#!/usr/bin/env bash
#
# ZhiweiPanel 一键安装脚本（裸机 Linux / systemd）
#
# 用法：
#   curl -fsSL zhiweipanel-install.sh | bash
#   或先下载再执行：bash zhiweipanel-install.sh
#
# 环境变量：
#   PANEL_PORT      面板端口，默认 8888
#   PANEL_PASS      管理员初始密码（必填，且不少于 8 位）
#   PANEL_USER      管理员用户名，默认 admin
#   INSTALL_DIR     安装目录，默认 /opt/zhiweipanel
#   INSTALL_PHP     是否安装 PHP-FPM，默认 1（PHP_VERSION 控制版本）
#   PHP_VERSION     PHP 版本，默认 8.1（Debian 11 自带；其他发行版要换源）
#   INSTALL_MYSQL   是否安装 MariaDB，默认 1
#   MYSQL_ROOT_PASS MariaDB root 密码；留空则生成 24 位随机串并写入
#                   ${INSTALL_DIR}/data/mysql-root.cnf（权限 0600）
#   BIN_URL         二进制下载地址（必填，例：
#                   BIN_URL=https://example.com/zhiweipanel-0.7.0-linux-amd64）

set -euo pipefail

PANEL_PORT="${PANEL_PORT:-8888}"
PANEL_USER="${PANEL_USER:-admin}"
INSTALL_DIR="${INSTALL_DIR:-/opt/zhiweipanel}"
PANEL_PASS="${PANEL_PASS:-}"
INSTALL_PHP="${INSTALL_PHP:-1}"
PHP_VERSION="${PHP_VERSION:-8.1}"
INSTALL_MYSQL="${INSTALL_MYSQL:-1}"
MYSQL_ROOT_PASS="${MYSQL_ROOT_PASS:-}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }

# ---------- 0. 环境检查 ----------
info "检查运行环境…"

if [[ "$(uname -s)" != "Linux" ]]; then
    error "本脚本仅支持 Linux，当前系统: $(uname -s)"
    exit 1
fi

if [[ $EUID -ne 0 ]]; then
    error "请以 root 身份运行：sudo bash $0"
    exit 1
fi

# 检测包管理器
PKG=""
if command -v apt-get &>/dev/null; then
    PKG="apt"
elif command -v dnf &>/dev/null; then
    PKG="dnf"
elif command -v yum &>/dev/null; then
    PKG="yum"
else
    warn "未检测到 apt/dnf/yum，跳过依赖安装（请手动确认 nginx 已安装）"
fi

# ---------- 1. 安装 nginx ----------
if ! command -v nginx &>/dev/null; then
    info "未检测到 nginx，正在安装…"
    case "$PKG" in
        apt)
            export DEBIAN_FRONTEND=noninteractive
            apt-get update -qq && apt-get install -y -qq nginx
            ;;
        dnf|yum)
            $PKG install -y -q nginx
            ;;
        *)
            error "请先手动安装 nginx 后重试"
            exit 1
            ;;
    esac
    info "nginx 安装完成"
else
    info "nginx 已安装: $(nginx -v 2>&1)"
fi

NGINX_BIN="$(command -v nginx)"

# ---------- 1.5 安装 PHP-FPM ----------
if [[ "$INSTALL_PHP" == "1" ]]; then
    if ! command -v php-fpm${PHP_VERSION} &>/dev/null && ! command -v php-fpm &>/dev/null; then
        info "未检测到 PHP-FPM，正在安装 PHP ${PHP_VERSION}…"
        case "$PKG" in
            apt)
                export DEBIAN_FRONTEND=noninteractive
                # Debian/Ubuntu 的 phpX.Y-fpm 包名； Sury 源外发行版要换 PPA
                apt-get install -y -qq "php${PHP_VERSION}-fpm" "php${PHP_VERSION}-mysql" "php${PHP_VERSION}-curl" "php${PHP_VERSION}-mbstring" "php${PHP_VERSION}-xml" "php${PHP_VERSION}-zip" "php${PHP_VERSION}-gd" "php${PHP_VERSION}-bcmath" "php${PHP_VERSION}-intl" || {
                    warn "指定 PHP ${PHP_VERSION} 安装失败，回退默认 php-fpm（不绑版本）"
                    apt-get install -y -qq php-fpm php-mysql php-curl php-mbstring php-xml php-zip php-gd php-bcmath php-intl
                }
                # Debian 把 socket 建在 /run/php/，可能有 php-fpm.sock 软链，
                # 也可能有 phpX.Y-fpm.sock 具体名——让面板去探测，不要 hardcode。
                ;;
            dnf|yum)
                $PKG install -y -q "php${PHP_VERSION}-fpm" "php${PHP_VERSION}-mysqlnd" "php${PHP_VERSION}-cli" || {
                    warn "指定 PHP ${PHP_VERSION} 安装失败，回退默认 php-fpm"
                    $PKG install -y -q php-fpm php-mysqlnd php-cli
                }
                ;;
            *)
                warn "未识别的包管理器，跳过 PHP 安装"
                ;;
        esac
        info "PHP 安装完成"
    else
        info "PHP-FPM 已安装: $(php-fpm${PHP_VERSION} -v 2>/dev/null || php-fpm -v 2>/dev/null | head -1)"
    fi
    # 让 systemd 开机自启 + 立即启动（不启动的话面板 PHP 站点 502）
    for unit in "php${PHP_VERSION}-fpm" "php-fpm" "php${PHP_VERSION}-fpm.service"; do
        if systemctl list-unit-files "${unit}.service" 2>/dev/null | grep -q "${unit}.service"; then
            systemctl enable --now "${unit}.service" >/dev/null 2>&1 || true
            info "已启用 ${unit}.service"
            break
        fi
    done
    # 如果以上都没匹配上，最后兜底：找 /run/php 下的 socket
    if ! systemctl is-active --quiet "php${PHP_VERSION}-fpm" 2>/dev/null && ! ls /run/php/*.sock &>/dev/null; then
        warn "PHP-FPM 似乎没起来：/run/php 下无 socket。后续建站时面板会探测并提示"
    fi
else
    info "跳过 PHP 安装（INSTALL_PHP=0）"
fi

# ---------- 1.6 安装 MariaDB / MySQL ----------
if [[ "$INSTALL_MYSQL" == "1" ]]; then
    if ! command -v mariadb &>/dev/null && ! command -v mysql &>/dev/null; then
        info "未检测到 MariaDB/MySQL，正在安装…"
        case "$PKG" in
            apt)
                export DEBIAN_FRONTEND=noninteractive
                # 预设 root 密码，让 debconf 不会卡交互
                echo "mariadb-server mysql-server/root_password password ${MYSQL_ROOT_PASS}" | debconf-set-selections
                echo "mariadb-server mysql-server/root_password_again password ${MYSQL_ROOT_PASS}" | debconf-set-selections
                apt-get install -y -qq mariadb-server
                ;;
            dnf|yum)
                $PKG install -y -q mariadb-server
                ;;
            *)
                warn "未识别的包管理器，跳过 MySQL 安装"
                ;;
        esac
        info "MariaDB 安装完成"
    else
        info "MariaDB/MySQL 已安装: $(mariadb --version 2>/dev/null || mysql --version)"
    fi

    # 启动服务
    for unit in mariadb mysql mysqld; do
        if systemctl list-unit-files "${unit}.service" 2>/dev/null | grep -q "${unit}.service"; then
            systemctl enable --now "${unit}.service" >/dev/null 2>&1 || true
            info "已启用 ${unit}.service"
            break
        fi
    done

    # 第一次启动后等一下初始化
    sleep 2

    # 设置 root 密码（如果留空就生成）
    if [[ -z "$MYSQL_ROOT_PASS" ]]; then
        # openssl rand -hex 12 → 24 hex chars
        MYSQL_ROOT_PASS="$(openssl rand -hex 12)"
    fi
    # 强制 root 用 unix_socket 认证（Debian 默认就是这样）+ 设密码 fallback
    if command -v mariadb &>/dev/null; then
        mariadb -u root <<SQL 2>/dev/null || warn "MariaDB root 初始化失败（可能已存在，跳过）"
-- 用 unix_socket 认证（无需密码）—— 安全
ALTER USER 'root'@'localhost' IDENTIFIED VIA unix_socket OR mysql_native_password USING PASSWORD('${MYSQL_ROOT_PASS}');
FLUSH PRIVILEGES;
SQL
    fi

    # 把 root 密码写到本地凭据文件，权限 0600
    mkdir -p "$INSTALL_DIR/data"
    cat > "$INSTALL_DIR/data/mysql-root.cnf" <<EOF
# ZhiweiPanel MariaDB/MySQL root 凭据
# 由 deploy/install.sh 自动生成；删除本文件 = 失去自动建库能力
# 用途：站点创建时面板用此密码登录 root 执行 CREATE DATABASE / USER / GRANT
[client]
user=root
password=${MYSQL_ROOT_PASS}
socket=/run/mysqld/mysqld.sock
EOF
    chmod 0600 "$INSTALL_DIR/data/mysql-root.cnf"
    info "MariaDB root 凭据已写入 $INSTALL_DIR/data/mysql-root.cnf（权限 0600）"
else
    info "跳过 MySQL 安装（INSTALL_MYSQL=0）"
fi

# ---------- 2. 创建目录与专用 nginx 配置片段 ----------
info "创建目录结构…"
mkdir -p "$INSTALL_DIR/data/certs" \
         "$INSTALL_DIR/data/backups" \
         "$INSTALL_DIR/data/logs" \
         "$INSTALL_DIR/nginx/conf.d"

# 面板生成的配置需要被 nginx 主配置 include。
# 这里做一次幂等的注入：只在未包含时追加，且必须落在 http 块内部。
MARKER="include ${INSTALL_DIR}/nginx/conf.d/*.conf;"
if ! grep -qF "$MARKER" /etc/nginx/nginx.conf 2>/dev/null; then
    info "向 nginx 主配置注入 include 片段…"
    [[ -f /etc/nginx/nginx.conf.zw-backup ]] || cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.zw-backup

    # 用 awk 在第一个 "http {" 之后插入，比 sed 的行号操作更可靠
    awk -v inc="    $MARKER" '
        /^[[:space:]]*http[[:space:]]*\{/ && !done {
            print
            print inc
            done = 1
            next
        }
        { print }
    ' /etc/nginx/nginx.conf.zw-backup > /etc/nginx/nginx.conf

    if ! nginx -t 2>/dev/null; then
        error "注入后配置检测失败，自动回滚"
        cp /etc/nginx/nginx.conf.zw-backup /etc/nginx/nginx.conf
        exit 1
    fi
    info "include 片段注入完成"
else
    info "include 片段已存在，跳过注入"
fi

# 校验主配置未被改坏
if ! nginx -t 2>/dev/null; then
    error "nginx 配置检测失败，已自动回滚主配置"
    [[ -f /etc/nginx/nginx.conf.zw-backup ]] && cp /etc/nginx/nginx.conf.zw-backup /etc/nginx/nginx.conf
    exit 1
fi
info "nginx 主配置已就绪"

# ---------- 3. 下载二进制 ----------
BIN_URL="${BIN_URL:-}"

if [[ -z "$BIN_URL" ]]; then
    error "请通过环境变量指定二进制下载地址，例如："
    echo "    BIN_URL=https://example.com/zhiweipanel-0.7.0-linux-amd64 bash $0"
    exit 1
fi

info "下载面板二进制…"
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)  GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    *) error "不支持的架构: $ARCH"; exit 1 ;;
esac

curl -fsSL -o "$INSTALL_DIR/zhiweipanel" "$BIN_URL" || {
    error "二进制下载失败: $BIN_URL"
    exit 1
}
chmod +x "$INSTALL_DIR/zhiweipanel"
info "二进制安装完成 ($GOARCH)"

# ---------- 4. 初始化管理员 ----------
if [[ -z "$PANEL_PASS" ]]; then
    # 交互式读取，避免密码进入 shell 历史
    read -r -s -p "请设置管理员密码（至少 8 位）: " PANEL_PASS
    echo
fi

if [[ ${#PANEL_PASS} -lt 8 ]]; then
    error "管理员密码至少 8 位"
    exit 1
fi

# ---------- 5. 注册 systemd 服务 ----------
info "注册 systemd 服务…"
cat > /etc/systemd/system/zhiweipanel.service <<EOF
[Unit]
Description=ZhiweiPanel - 轻量建站面板
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${INSTALL_DIR}
ExecStart=${INSTALL_DIR}/zhiweipanel \\
  -port ${PANEL_PORT} \\
  -runtime host \\
  -data ${INSTALL_DIR}/data \\
  -conf ${INSTALL_DIR}/nginx/conf.d \\
  -site-conf ${INSTALL_DIR}/nginx/conf.d \\
  -nginx ${NGINX_BIN} \\
  -admin ${PANEL_USER} \\
  -admin-pass '${PANEL_PASS}'
Restart=always
RestartSec=5
TimeoutStopSec=20
StandardOutput=journal
StandardError=journal
SyslogIdentifier=zhiweipanel

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable zhiweipanel >/dev/null 2>&1
systemctl restart zhiweipanel

sleep 2

if systemctl is-active --quiet zhiweipanel; then
    info "面板已启动"
else
    error "面板启动失败，请查看日志：journalctl -u zhiweipanel -n 50"
    exit 1
fi

# ---------- 6. 放行防火墙 ----------
if command -v firewall-cmd &>/dev/null && systemctl is-active --quiet firewalld; then
    info "配置 firewalld 放行 $PANEL_PORT 端口"
    firewall-cmd --permanent --add-port=${PANEL_PORT}/tcp >/dev/null
    firewall-cmd --reload >/dev/null
elif command -v ufw &>/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
    info "配置 ufw 放行 $PANEL_PORT 端口"
    ufw allow ${PANEL_PORT}/tcp >/dev/null
fi

echo
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}  ZhiweiPanel 安装完成${NC}"
echo -e "${GREEN}========================================${NC}"
echo "  面板地址   : http://<服务器IP>:${PANEL_PORT}"
echo "  用户名     : ${PANEL_USER}"
echo "  密码       : （你刚设置的）"
echo "  安装目录   : ${INSTALL_DIR}"
echo "  数据目录   : ${INSTALL_DIR}/data"
echo
echo "  组件状态   :"
echo "    nginx       : $(systemctl is-active nginx 2>/dev/null || echo inactive) / $(nginx -v 2>&1 | head -1 || echo '?')"
if [[ "$INSTALL_PHP" == "1" ]]; then
    for unit in "php${PHP_VERSION}-fpm" "php-fpm"; do
        if systemctl list-unit-files "${unit}.service" 2>/dev/null | grep -q "${unit}.service"; then
            echo "    PHP-FPM     : $(systemctl is-active "${unit}" 2>/dev/null) / $(php-fpm${PHP_VERSION} -v 2>/dev/null | head -1 || php-fpm -v 2>/dev/null | head -1 || echo '?')"
            break
        fi
    done
fi
if [[ "$INSTALL_MYSQL" == "1" ]]; then
    for unit in mariadb mysql mysqld; do
        if systemctl list-unit-files "${unit}.service" 2>/dev/null | grep -q "${unit}.service"; then
            echo "    MariaDB     : $(systemctl is-active "${unit}" 2>/dev/null) / $(mariadb --version 2>/dev/null || mysql --version 2>/dev/null || echo '?')"
            echo "    root 凭据   : $INSTALL_DIR/data/mysql-root.cnf（0600）"
            break
        fi
    done
fi
echo
echo "  常用命令 :"
echo "    systemctl status zhiweipanel      # 查看状态"
echo "    systemctl restart zhiweipanel     # 重启"
echo "    journalctl -u zhiweipanel -f      # 实时日志"
echo "    nginx -t                        # 配置检测"
echo -e "${GREEN}========================================${NC}"
