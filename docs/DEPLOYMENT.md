# 智维面板 ZhiweiPanel 环境部署教程

适用版本：**v0.1.1**（版本号权威源：`internal/version/version.go`）。

本文只讲**安装与运维**。功能怎么用见 [USAGE.md](USAGE.md)，项目概览见 [README.md](../README.md)。

---

## 一、环境要求

### 1.1 最低配置

| 项 | 要求 |
|----|------|
| 操作系统 | Linux（x86_64 / arm64）。生产部署只支持 Linux；开发与测试可在 Windows / macOS 上跑 |
| Go（仅构建时需要） | 1.26.10（`go.mod` 里 `go 1.26.10`，Go 会自动切换到对应 toolchain） |
| 面板二进制 | 静态编译（`CGO_ENABLED=0`），不依赖 Go 运行时 |
| 内存 | 1 GB 起步 |
| 磁盘 | 数据目录建议预留 ≥ 5 GB（备份、日志、证书都落在这里） |

### 1.2 软件依赖

| 依赖 | 必需性 | 说明 |
|------|--------|------|
| nginx | **必需** | 面板要执行 `nginx -t` 与 reload；没有 nginx 无法下发任何站点配置 |
| PHP-FPM | PHP 站点必需 | 版本由面板在「系统状态」里探测，站点可按版本选择 |
| MariaDB / MySQL | 可选 | 只有要用「联动建库」时才需要；未安装时该功能自动降级并明确报错 |
| systemd | 裸机部署必需 | 开机自启与进程守护 |
| Docker + Docker Compose | Docker 部署必需 | |

### 1.3 端口

| 端口 | 用途 | 谁在用 |
|------|------|--------|
| 8888 | 面板 HTTP（默认） | 面板自身 |
| 8887 | 面板 HTTP→HTTPS 301 重定向 | 面板自身，**仅在 `-tls` 开启时监听** |
| 80 | 站点 HTTP + ACME HTTP-01 挑战 | nginx |
| 443 | 站点 HTTPS | nginx（站点启用 SSL 后） |

> 站点对外端口可以逐站点配置（站点的「对外监听端口」字段），也可以用面板的 `-public-port` 全局统一。**同一端口只能给一个站点**，否则 nginx 监听冲突。

---

## 二、裸机部署（systemd）

### 2.1 一键安装

仓库自带 `deploy/install.sh`，面向裸机 Linux + systemd：

```bash
BIN_URL=https://your-host/zhiweipanel-0.1.1-linux-amd64 \
PANEL_PASS='CHANGE_ME_at_least_8_chars' \
bash deploy/install.sh
```

支持的环境变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `BIN_URL` | **必填** | 二进制下载地址，如 `https://example.com/zhiweipanel-0.1.1-linux-amd64` |
| `PANEL_PASS` | 交互式输入 | 管理员初始密码，**至少 8 位**。留空则脚本交互式读取（不落 shell 历史） |
| `PANEL_USER` | `admin` | 管理员用户名 |
| `PANEL_PORT` | `8888` | 面板端口 |
| `INSTALL_DIR` | `/opt/zhiweipanel` | 安装目录 |
| `INSTALL_PHP` | `1` | 是否安装 PHP-FPM；`0` 跳过 |
| `PHP_VERSION` | `8.1` | PHP 版本（Debian 11 自带版本；其它发行版需自行换源） |
| `INSTALL_MYSQL` | `1` | 是否安装 MariaDB；`0` 跳过 |
| `MYSQL_ROOT_PASS` | 空则自动生成 | MariaDB root 密码。留空则生成 24 位随机 hex 写入 `${INSTALL_DIR}/data/mysql-root.cnf`（权限 0600） |

脚本执行顺序：

1. 环境检查（必须 Linux、必须 root、识别 apt/dnf/yum）；
2. 安装 nginx（缺失时）、PHP-FPM（`INSTALL_PHP=1`）、MariaDB（`INSTALL_MYSQL=1`），并 `systemctl enable --now` 拉起；
3. **移除发行版默认站点**（`/etc/nginx/sites-enabled/default`、`/etc/nginx/conf.d/default.conf`）：二者都在 `:80` 声明 `default_server`，与面板下发的 catch-all 默认站冲突（`nginx -t` 报 `a duplicate default server`，配置下发永远不生效——且安装期自检发现不了，冲突要等面板首次渲染才爆发）。先备份到 `/etc/nginx/zw-removed-vhosts/`（**不能留在原目录**：`include sites-enabled/*` 不带扩展名过滤，备份文件会被一起 include，冲突照旧）再移除，移除后跑 `nginx -t`，不通过就整体回滚并退出；无默认站点则跳过（幂等）；
4. 创建目录结构：`data/certs`、`data/backups`、`data/logs`、`nginx/conf.d`；
5. **幂等地**向 `/etc/nginx/nginx.conf` 的 `http { }` 块内注入 `include <INSTALL_DIR>/nginx/conf.d/*.conf;`——注入前备份为 `nginx.conf.zw-backup`，注入后跑 `nginx -t`，不通过就自动回滚并退出；
6. 下载二进制到 `$INSTALL_DIR/zhiweipanel` 并 `chmod +x`；
7. 写 `/etc/systemd/system/zhiweipanel.service`（含 `-admin` / `-admin-pass`），`daemon-reload` + `enable` + `restart`；
8. 放行防火墙（firewalld 或 ufw，二者都装了才处理）；
9. 打印安装结果与常用命令。

> **安全提醒**：`install.sh` 把 `-admin-pass` 明文写进了 systemd unit 文件。装完请立刻 `chmod 600 /etc/systemd/system/zhiweipanel.service`，并到面板里改掉初始密码（设置 → 修改密码）。改完可以把 unit 里的 `-admin-pass` 行删掉再 `systemctl daemon-reload`——该参数只在首次创建账户时生效，删掉不影响已存在的账户。

### 2.2 手动部署

不想用脚本就手工来，三步：

```bash
# 1. 目录 + 二进制
sudo mkdir -p /opt/zhiweipanel/data /opt/zhiweipanel/nginx/conf.d
sudo cp zhiweipanel /opt/zhiweipanel/
sudo chmod +x /opt/zhiweipanel/zhiweipanel

# 2. 把 include 注入 nginx 主配置的 http {} 块内（必须在 http 块内，放错位置 nginx -t 会报 emerg）
sudo cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.zw-backup
sudo sed -i '/^[[:space:]]*http[[:space:]]*{/a\    include /opt/zhiweipanel/nginx/conf.d/*.conf;' /etc/nginx/nginx.conf
sudo nginx -t

# 3. 启动（先手工跑一次确认能起来）
sudo /opt/zhiweipanel/zhiweipanel \
  -port 8888 \
  -data /opt/zhiweipanel/data \
  -conf /opt/zhiweipanel/nginx/conf.d \
  -nginx /usr/sbin/nginx \
  -admin admin -admin-pass 'CHANGE_ME_at_least_8_chars'
```

面板启动时会自动创建这些目录：`data`、`conf`、`site-conf`（默认等于 `conf`）、`cert-dir`（默认 `data/certs`）、`backup-dir`（默认 `data/backups`）。

### 2.3 systemd 服务单元

仓库里的 `deploy/zhiweipanel.service` 可直接使用：

> **初始管理员口令的环境变量名是 `ZW_ADMIN_PASS`**（旧版为 `SP_ADMIN_PASS`，改名时同步改了）。
> 把口令写进 `/etc/zhiweipanel/zhiweipanel.env`（权限 0600）：
>
> ```bash
> sudo mkdir -p /etc/zhiweipanel
> sudo sh -c 'printf "ZW_ADMIN_PASS=%s\n" "你的强口令" > /etc/zhiweipanel/zhiweipanel.env'
> sudo chmod 600 /etc/zhiweipanel/zhiweipanel.env
> ```
>
> 该文件不存在时 systemd 会跳过（`-` 前缀），面板仍能启动，但**首次登录不可用**
> （没有管理员账户）——此时需在面板里重新设置，或补上这个文件后重启。

```ini
[Unit]
Description=ZhiweiPanel - 轻量建站面板
Documentation=https://github.com/zhiwei/panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# nginx 需 root 权限执行 nginx -t 与 reload
User=root
WorkingDirectory=/opt/zhiweipanel

ExecStart=/opt/zhiweipanel/zhiweipanel \
  -port 8888 \
  -runtime host \
  -data /opt/zhiweipanel/data \
  -conf /opt/zhiweipanel/nginx/conf.d \
  -site-conf /opt/zhiweipanel/nginx/conf.d \
  -nginx /usr/sbin/nginx

Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=20

StandardOutput=journal
StandardError=journal
SyslogIdentifier=zhiweipanel

NoNewPrivileges=false
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

安装并设开机自启：

```bash
sudo cp deploy/zhiweipanel.service /etc/systemd/system/zhiweipanel.service
sudo systemctl daemon-reload
sudo systemctl enable zhiweipanel      # 开机自启
sudo systemctl start zhiweipanel
```

> `-admin-pass` 没有写进上面的单元文件（`install.sh` 生成的版本才有）。这样启动时面板发现账户不存在会打印警告「未指定 -admin-pass 且管理员账户不存在，首次登录将不可用」。两种做法二选一：**要么**把 `-admin-pass` 写进 unit（记得 `chmod 600`），**要么**启动一次带 `-admin-pass` 把账户建出来，之后再删掉该参数。

### 2.4 服务管理

```bash
systemctl status zhiweipanel       # 查看状态
systemctl restart zhiweipanel      # 重启
systemctl stop zhiweipanel         # 停止
journalctl -u zhiweipanel -f       # 实时日志
journalctl -u zhiweipanel -n 100   # 最近 100 行
nginx -t                         # 配置检测
```

面板自身日志前缀是 `[zhiweipanel]`，例如 `初始配置下发失败`、`生成 catch-all dummy 证书失败`、`MySQL 凭据文件 ... 不存在，子系统未启用`。

---

## 三、Docker 部署

### 3.1 一键起

```bash
cd deploy
docker compose up -d --build
docker compose logs -f
```

`deploy/docker-compose.yml` 的关键点：

| 项 | 值 |
|----|----|
| 镜像 | `zhiweipanel:${VERSION:-0.1.0}`，构建参数 `VERSION` 透传到 Dockerfile 的 `-ldflags "-X main.Version=..."` |
| 端口 | `${PANEL_PORT:-8888}:8888`、`${HTTP_PORT:-80}:80`、`${HTTPS_PORT:-443}:443` |
| 容器用户 | `root`（需要执行 `nginx -t` 与 reload） |
| 卷 | `site-data` → `/opt/zhiweipanel/data`；`nginx-conf` → `/etc/nginx/conf.d`；`wwwroot` → `/www/wwwroot` |
| 健康检查 | `curl -fsS http://127.0.0.1:8888/api/auth/me`，间隔 30s，超时 5s，重试 3 次 |
| 重启策略 | `unless-stopped` |

改端口直接改环境变量，不用改文件：

```bash
PANEL_PORT=9999 HTTP_PORT=8080 HTTPS_PORT=8443 docker compose up -d
```

### 3.2 两种运行时别搞混

| 形态 | `-runtime` | 说明 |
|------|-----------|------|
| 面板在容器里管容器里的 nginx | `host`（compose 默认） | nginx 也在同一个容器内，面板直接操作 `/etc/nginx/conf.d` 并执行 `/usr/sbin/nginx` |
| 面板在宿主机上管独立 nginx 容器 | `docker` | 面板通过 `docker exec` 操作名为 `-container` 指定的容器；`-host-conf` 指向宿主机上被挂进容器的 `conf.d` 路径 |

第二种形态的典型用法（面板与 nginx 分容器）：

```bash
zhiweipanel \
  -runtime docker \
  -container nginx \
  -host-conf /srv/nginx/conf.d \
  -site-conf /srv/nginx/conf.d \
  -data /opt/zhiweipanel/data
```

### 3.3 Docker 模式的注意点

- 镜像基于 `alpine:3.20`，内置 nginx + ca-certificates + tzdata + curl，时区固定为 `Asia/Shanghai`。
- **入口脚本会先拉起容器内的 nginx，再启动面板**（`deploy/docker-entrypoint.sh`）。面板只负责「下发配置 → `nginx -t` → reload」，从不 start nginx；容器里也没有 systemd，没有这个入口镜像内就没有 nginx 进程，首屏配置下发会直接报 `invalid PID number "" in "/run/nginx/nginx.pid"`。
- 镜像内置了 `deploy/nginx.conf`：面板的 `conf.d` include 被放在 `http {}` **内**。alpine 默认把它放在 root context，而面板下发的配置里既有 http 级指令（`log_format` / `limit_req_zone`）也有 server 块，直接挂载会报 `"log_format" directive is not allowed here`。同时把 pid 路径显式固定为 `/run/nginx/nginx.pid`。**不要**把 `nginx.conf` 放进 `nginx-conf` 卷——该卷挂载在 `/etc/nginx/conf.d`，不会覆盖主配置。
- 发行版自带的 `http.d/default.conf`（`:80` 上的 `default_server`）已在镜像构建阶段删除，否则会与面板下发的 catch-all 冲突（重复定义 default server）。
- 容器内 nginx 以 `nginx` 用户运行，需要写日志/站点目录的卷已经 `chown` 过；你自己加卷时要保证权限对得上。
- 镜像里的 nginx **不自带 PHP-FPM**。需要 PHP 站点时，请单独起一个 php-fpm 容器并把 socket 挂进面板容器，再在站点的「PHP-FPM Socket」里填绝对路径。
- 宿主机上的 nginx 若已在监听 80/443，容器的端口映射会冲突。要么改映射端口（`HTTP_PORT`/`HTTPS_PORT`），要么让宿主机 nginx 把 80/443 反代到面板容器。
- **端口映射绕得开 UFW**：Docker 发布端口不会经过 ufw 规则，映射 `8888`/`80`/`443` 等于直接对公网开放。不需要公网访问时，把 `PANEL_PORT` 之外那两个映射去掉或改用内网地址绑定（`127.0.0.1:80:80`）。

---

## 四、命令行参数

全部参数定义在 `main.go`，`-version` 可直接打印版本号。

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `-port` | int | `8888` | 面板监听端口 |
| `-http-port` | int | `8887` | HTTP→HTTPS 301 重定向端口，`0` 禁用；**仅 `-tls` 开启时生效** |
| `-tls` | bool | `false` | 启用面板 HTTPS（自签证书自动生成到 `<data>/panel.crt`） |
| `-tls-cert` | string | 空 | TLS 证书路径，留空用 `<data>/panel.crt` |
| `-tls-key` | string | 空 | TLS 私钥路径，留空用 `<data>/panel.key` |
| `-tls-hosts` | string | `localhost,127.0.0.1` | 面板证书 SAN 列表（逗号分隔） |
| `-public-port` | int | `0` | 全局对外 HTTP 端口；`0` = 各站点用自己的 `listen_ports` |
| `-disable-ipv6` | bool | `false` | 关闭 `listen ... [::]:`，老机器无 IPv6 时用 |
| `-data` | string | `/opt/zhiweipanel/data` | 数据目录 |
| `-conf` | string | `/opt/zhiweipanel/nginx/conf.d` | nginx `conf.d` 目录 |
| `-site-conf` | string | 空 | 站点 vhost 目录，留空同 `-conf` |
| `-cert-dir` | string | 空 | 证书目录，留空用 `<data>/certs` |
| `-backup-dir` | string | 空 | 备份目录，留空用 `<data>/backups`。**注意**：当前该参数只创建目录，备份文件实际落在 `<data>/backup/{site,database}/` |
| `-runtime` | string | `host` | `host` 或 `docker` |
| `-nginx` | string | `nginx` | nginx 可执行文件 |
| `-nginx-unit` | string | 空（= `nginx`） | nginx 的 systemd 服务名。nginx 由自定义单元托管时必须填（如 `aa_nginx`、`openresty`），否则状态页显示"未运行"，`systemctl reload` 也会打错单元（此时面板会退化为 `nginx -s reload` 信号重载，仍能生效） |
| `-container` | string | `nginx` | docker 模式下的 nginx 容器名 |
| `-host-conf` | string | `/opt/zhiweipanel/nginx/conf.d` | docker 模式宿主机挂载的 `conf.d` |
| `-admin` | string | `admin` | 初始管理员用户名 |
| `-admin-pass` | string | 空 | 初始管理员密码，**仅首次启动生效** |
| `-version` | bool | `false` | 打印版本号后退出 |
| `-trusted-proxy` | string | 空 | 受信反向代理网段（CIDR / 单 IP，逗号分隔）。直连方不在其中时一律用直连地址；留空 = 不信任任何代理 |

推荐的最小生产参数组合：

```bash
/opt/zhiweipanel/zhiweipanel \
  -port 8888 \
  -runtime host \
  -data /opt/zhiweipanel/data \
  -conf /opt/zhiweipanel/nginx/conf.d \
  -nginx /usr/sbin/nginx
```

### 4.1 面板 HTTPS

```bash
/opt/zhiweipanel/zhiweipanel -tls -port 8888 -http-port 8887 \
  -tls-hosts panel.example.com
```

- `-tls` 开启后，8888 提供 HTTPS（最低 TLS 1.2），8887 提供 301 跳转到 HTTPS；
- 证书不存在时自动生成自签证书到 `<data>/panel.crt` / `<data>/panel.key`，SAN 取 `-tls-hosts`；
- 未开启 `-tls` 时面板是**明文 HTTP**，登录口令会裸奔在网络里。生产环境请务必开启 `-tls`，或在前面放一层 Nginx 反代并配好 TLS。

### 4.2 面板挂在反向代理后面

只有当直连方（`RemoteAddr`）落在 `-trusted-proxy` 指定的网段里时，面板才采信 `X-Forwarded-For`；否则一律用直连地址。

```bash
# 面板前面有一台 Nginx 在 127.0.0.1:8888 上反代
/opt/zhiweipanel/zhiweipanel -trusted-proxy 127.0.0.1/32
# 多级代理时把每一跳的网段都列上
/opt/zhiweipanel/zhiweipanel -trusted-proxy 10.0.0.0/8,192.168.0.0/16
```

不配 `-trusted-proxy`（默认）时，客户端可以随便伪造 `X-Forwarded-For`，登录限流的计数键会被绕过——所以**除非确实有反代，否则不要配**。

---

## 五、目录结构

### 5.1 安装目录

```
/opt/zhiweipanel/
├── zhiweipanel                     # 面板二进制
├── data/                         # 数据目录（-data）
│   ├── zhiweipanel.db              # SQLite 主库
│   ├── certs/                    # 证书与私钥
│   │   ├── <证书ID>/fullchain.pem
│   │   ├── <证书ID>/privkey.pem
│   │   └── _accounts/<账户ID>_key.pem   # ACME 账户私钥
│   ├── backups/                  # -backup-dir 指定的目录（当前只创建，备份文件不落这里）
│   ├── backup/                   # 备份文件实际落盘位置
│   │   ├── site/<id>.tar.gz       #   站点备份
│   │   └── database/<id>.sql.gz   #   数据库备份
│   ├── logs/                     # 面板自身日志目录
│   ├── htpasswd/                 # 站点访问认证口令文件，文件名 = 站点 ID
│   ├── ssl/                      # catch-all default_server 用的 dummy 自签证书
│   │   ├── dummy.crt
│   │   └── dummy.key
│   ├── panel.crt / panel.key     # -tls 开启时的面板自签证书
│   ├── mysql-root.cnf            # MariaDB root 凭据（install.sh 生成，0600）
│   └── conf_history/             # nginx 配置历史快照（保留最近 30 份）
└── nginx/
    └── conf.d/                   # nginx 配置目录（-conf / -site-conf）
        ├── 00-zhiweipanel-http.conf        # http 级配置：log_format / limit_req_zone
        ├── 01-zhiweipanel-default-server.conf  # catch-all default_server
        └── 10-site-<站点ID>.conf          # 单站点 server 块
```

### 5.2 nginx 配置的加载顺序

nginx 按**文件名升序**加载 `conf.d/*`，前缀就是用来控制顺序的：

```
00-zhiweipanel-http.conf          ← 先定义 log_format / limit_req_zone
01-zhiweipanel-default-server.conf ← catch-all，防跨站隔离
10-site-<站点ID>.conf            ← 站点定义，引用上面那些
```

把 `00-` 放后面会直接导致 `nginx -t` 报 `unknown log format`——这是最常见的部署事故之一。

只有 `10-site-` 前缀的文件归面板管。删站时面板只清理这个前缀的孤儿配置，你手写在 `conf.d` 里的其它 `.conf` 不会被碰。

### 5.3 站点目录

站点根目录默认是 `/www/wwwroot/<主域名>`（面板创建站点时自动创建，并放入 `logs/` 子目录）。日志与防篡改基线都相对这个目录：

```
/www/wwwroot/example.com/
├── logs/                        # access.log / error.log
├── public/                      # 启用 PHP 时自动创建（run_path 场景）
└── .well-known/acme-challenge/  # ACME HTTP-01 挑战文件（申请证书时临时写入）
```

---

## 六、升级与回滚

### 6.1 升级

```bash
# 1. 停面板（nginx 保持运行，不影响现有站点）
sudo systemctl stop zhiweipanel

# 2. 备份二进制与数据
sudo cp /opt/zhiweipanel/zhiweipanel /opt/zhiweipanel/zhiweipanel.bak-$(date +%Y%m%d-%H%M%S)
sudo tar czf /opt/zhiweipanel/data-backup-$(date +%Y%m%d).tar.gz -C /opt/zhiweipanel data

# 3. 换二进制
sudo cp zhiweipanel-new /opt/zhiweipanel/zhiweipanel
sudo chmod +x /opt/zhiweipanel/zhiweipanel

# 4. 起面板（首次启动会下发一次 nginx 配置）
sudo systemctl start zhiweipanel
sudo journalctl -u zhiweipanel -n 50
```

### 6.2 版本号对齐

升级二进制时不需要手动改版本号，但**要确认版本号是同一个权威源**：

```bash
/opt/zhiweipanel/zhiweipanel -version        # 期望输出：ZhiweiPanel 0.1.0
```

版本号唯一来源是 `internal/version/version.go` 的 `const Version = "0.1.1"`。`internal/version/version_test.go` 是常驻门禁，会校验 Dockerfile、compose、install.sh、README、CHANGELOG 里的版本锚点与权威源一致——**改版本号只改那一处**，然后跑 `go test ./internal/version/`。

> ⚠ **`-version` 不能唯一标识一个二进制。**
>
> 本项目在 2026-10-28 做过**版本线重置**（当前代码以 `v0.1.0` 发布，功能集来自原 `v0.2.2`）。
> 后果是：早于重置的 `0.1.x` 构建与当前的 `v0.1.0` **都自报 `ZhiweiPanel 0.1.0`**。
> 线上实测清理旧二进制时，同一台机器上有 **4 个不同 md5 的二进制自报同一个版本号**。
>
> 核对与回滚请用 **md5**（或 `go version -m` 看构建信息），不要只看 `-version`：

```bash
md5sum /opt/zhiweipanel/zhiweipanel                 # 部署前后对比用这个
go version -m /opt/zhiweipanel/zhiweipanel | head -3   # 看真实构建工具链
```

建议在部署脚本里固化三道校验：**上传后 md5 比对 → 安装后 md5 比对 → 启动后 `-version` 抽样**。

### 6.3 回滚

```bash
sudo systemctl stop zhiweipanel
sudo cp /opt/zhiweipanel/zhiweipanel.bak-20260101-120000 /opt/zhiweipanel/zhiweipanel
sudo chmod +x /opt/zhiweipanel/zhiweipanel
sudo systemctl start zhiweipanel
```

二进制回滚不涉及数据库结构变更：SQLite 的建表语句全部是 `CREATE TABLE IF NOT EXISTS`，新版本加的列用 `ALTER TABLE` 幂等补齐。**回滚二进制不会回滚数据**——如果新版本写了新数据，回滚后旧版读不了那些字段。重要操作前先按 6.1 的步骤打包 `data/`。

**回滚件命名建议**：用 `zhiweipanel.v<版本>.bak`（如 `zhiweipanel.v0.2.2.bak`）并**只保留 1~2 个**。
v0.2.x 开发期留下了十几个 `zhiweipanel.bak.<标签>.<时间戳>` 中间态快照（单台机约 256 MB），
它们都被正式版本取代了，只会在出故障时增加误选回滚目标的风险——建议定期清理。

### 6.4 nginx 配置回滚

面板在「运维中心 → 配置历史」里保留最近 30 份 nginx 配置快照，可以查看原始内容、与当前配置逐行 diff、一键回滚。

> **只恢复 nginx 配置文件，不回滚数据库。**面板里的站点定义仍然是「当前」那一份，恢复的是「当时生效在 nginx 上的配置」。要连站点定义一起回退，请配合站点备份。

---

## 七、卸载

```bash
# 1. 停服务并取消开机自启
sudo systemctl disable --now zhiweipanel

# 2. 清理面板注入的 nginx include
sudo sed -i '\|include /opt/zhiweipanel/nginx/conf.d/\*.conf;|d' /etc/nginx/nginx.conf
sudo nginx -t && sudo systemctl reload nginx

# 3. 删 systemd unit
sudo rm -f /etc/systemd/system/zhiweipanel.service
sudo systemctl daemon-reload

# 4. 删安装目录（谨慎：含全部站点配置、证书、备份、数据库）
sudo rm -rf /opt/zhiweipanel

# 5. 站点目录与数据库另算
#    站点根目录（/www/wwwroot/...）需要单独删
#    MySQL 里的库与用户需要单独 drop
```

`/etc/nginx/nginx.conf.zw-backup` 是 `install.sh` 注入前留的备份，确认新配置没问题后可以删。

---

## 八、常见问题排障

### 8.1 面板起不来

```bash
journalctl -u zhiweipanel -n 100
```

| 日志关键字 | 原因 | 处理 |
|-----------|------|------|
| `打开数据库失败` | 数据目录不可写或磁盘满 | `df -h`、`chown -R root:root /opt/zhiweipanel/data` |
| `准备面板证书失败` | `-tls` 开启但数据目录不可写 | 同上；或先去掉 `-tls` 验证 |
| `生成 catch-all dummy 证书失败` | 同上 | 同上 |
| `未指定 -admin-pass 且管理员账户不存在` | 首次启动没给初始密码 | 带 `-admin-pass` 重启一次，或到设置里创建账户 |
| `MySQL 凭据文件 ... 不存在，子系统未启用` | 没装 MySQL 或没跑 install.sh | 只影响建库功能，不影响其它功能 |
| 端口被占用 | 8888 已被占 | `ss -lntp \| grep 8888`，换 `-port` |

面板默认监听 `0.0.0.0`。只在本机访问的话，前面放 Nginx 反代到 `127.0.0.1:8888` 更稳妥。

### 8.2 `nginx -t` 报 `unknown log format`

**原因**：`log_format` 定义在 `00-zhiweipanel-http.conf` 里，站点配置 `10-site-*.conf` 引用它。nginx 按文件名升序加载，`00-` 必须存在且排在前面。

```bash
ls -l /opt/zhiweipanel/nginx/conf.d/
# 应该看到 00-zhiweipanel-http.conf 在 10-site-*.conf 之前
sudo grep -n 'include' /etc/nginx/nginx.conf
# include 必须落在 http { } 块内
```

处理：

1. 确认 `00-zhiweipanel-http.conf` 存在（面板下发配置后才会生成）；
2. 确认 nginx 主配置里的 `include` 在 `http {}` 块**内部**，不在 `events {}` 或文件末尾；
3. 在面板「设置 → nginx 主配置 → 测试配置」跑一次检测。

### 8.3 站点返回 403

按这个顺序查：

```bash
# 1. 站点根目录存在且 nginx 读得到
ls -ld /www/wwwroot/example.com
sudo -u nginx ls /www/wwwroot/example.com      # 或 www 用户

# 2. nginx 错误日志
sudo tail -50 /var/log/nginx/error.log
#   "Permission denied" → 目录权限
#   "directory index of ... is forbidden" → 缺 index 文件，改默认文档或放 index.html

# 3. 站点配置里是不是命中了拒绝规则
grep -nE 'deny |allow |limit_except' /opt/zhiweipanel/nginx/conf.d/10-site-*.conf
```

面板里的「访问限制」黑名单、UA 黑名单、禁止访问的路径/后缀都会渲染成 `deny`，403 多半来自这里。

### 8.4 站点返回 502（PHP）

502 = nginx 连上了但 PHP-FPM 没响应。

```bash
# 1. PHP-FPM 在跑吗
systemctl status php8.2-fpm
ls -l /run/php/*.sock

# 2. nginx 配置里的 socket 路径对不对
grep -n 'fastcgi_pass' /opt/zhiweipanel/nginx/conf.d/10-site-*.conf

# 3. PHP-FPM 自己的日志
sudo tail -50 /var/log/php8.2-fpm.log
```

面板在站点「PHP → PHP-FPM Socket」留空时会按版本自动探测 socket。手动填错路径是最常见的原因。

另外注意 PHP 站点的运行目录：站点根目录是 `/www/wwwroot/app`、运行目录填 `public` 时，`SCRIPT_FILENAME` 拼出来是 `$document_root/public/$script_name`——Laravel / ThinkPHP 这类框架必须这么配，否则 404 或 502。

### 8.5 证书续签失败

HTTP-01 验证的完整链路是：

```
面板把挑战文件写到 <站点根目录>/.well-known/acme-challenge/<token>
  → nginx 的 location ^~ /.well-known/acme-challenge/ 提供该文件
  → Let's Encrypt 回源 80 端口取文件
```

四个必查点：

```bash
# 1. 80 端口通不通（安全组 + 防火墙 + nginx 都要放行）
curl -I http://example.com/.well-known/acme-challenge/test

# 2. nginx 是否输出了挑战 location
grep -n 'acme-challenge' /opt/zhiweipanel/nginx/conf.d/10-site-*.conf
#   必须有 location ^~ /.well-known/acme-challenge/ 且带 auth_basic off;

# 3. 站点是否被强制 HTTPS 跳转吃掉了挑战
#   跳转必须写在 location / 内，不能写在 server 级
grep -n 'return 301' /opt/zhiweipanel/nginx/conf.d/10-site-*.conf

# 4. 域名解析是否已生效
dig +short example.com
```

**开启了站点访问认证（Basic Auth）也能续签**：渲染器会在挑战 location 里显式输出 `auth_basic off;`，让挑战请求绕过认证。若你手写过自定义配置片段覆盖了这个 location，续签会失败。

DNS-01 验证则不依赖 80 端口，但要保证凭据可用。内置 4 个提供商：阿里云（`API_KEY` / `SECRET_KEY`）、DNSPod（`API_KEY`，格式 `ID,Token`）、腾讯云（`SECRET_ID` / `SECRET_KEY`）、Cloudflare（`DNS_API_TOKEN`）。

### 8.6 开了 Basic Auth 之后一直 401

面板把 apr1 哈希写进 `<data>/htpasswd/<站点ID>`，nginx 侧输出：

```nginx
auth_basic "example.com";
auth_basic_user_file "/opt/zhiweipanel/data/htpasswd/<站点ID>";
```

排查：

```bash
# 1. 口令文件在不在
ls -l /opt/zhiweipanel/data/htpasswd/
sudo cat /opt/zhiweipanel/data/htpasswd/<站点ID>
#   格式必须是 用户名:$apr1$xxxxxxxx$xxxxxxxxxxxxxxxxxxxx

# 2. nginx 配置里的路径和实际路径一致吗
grep -n 'auth_basic' /opt/zhiweipanel/nginx/conf.d/10-site-*.conf

# 3. nginx worker 读得到吗（master 以 root 解析配置，worker 可能不是 root）
sudo -u nginx cat /opt/zhiweipanel/data/htpasswd/<站点ID>
```

几个容易踩的点：

- **密码只写不读**。保存后接口不再回显，界面上密码框永远是空的。留空 = 不修改，填了新值 = 改密码。想确认当前密码，只能删掉重设。
- 认证口令**不会**在站点详情/列表接口回显（明文与哈希都不返回）。
- `auth_basic_user_file` 指向的文件在 nginx **解析配置时**就会被打开，文件不存在 → `nginx -t` 失败 → 整批配置回滚。面板因此在渲染前先写文件，并且每次下发前自愈补齐被外部删掉的文件。
- 口令文件路径里带空格、引号、分号会导致配置结构被改变，面板会直接报错拒绝启用访问认证（此时改 `-data` 到一个路径干净的目录）。

### 8.7 面板显示「配置检测未通过」但站点都正常

面板自带的纯 Go 校验器（`internal/nginx/validator.go`）能在没有 nginx 的机器上做静态检查，覆盖大括号配平、http 级指令误用、location 嵌套、`expires` 单位、`access_log` 引用未定义的 log_format 等。

它**不能替代真实的 `nginx -t`**。以真实 nginx 的结果为准：

```bash
sudo nginx -t
```

### 8.8 单个站点配置坏了，会不会拖垮全站

不会。配置下发的三道防线 + 站点隔离机制保证：渲染失败的站点配置被整段注释掉（等价于临时停用），其余站点照常下发，面板会在界面上展示被隔离的站点说明。修好该站点的配置后点「重新下发配置」即可恢复。

### 8.9 面板以 root 运行的取舍

面板必须执行 `nginx -t` 与 reload，这两件事需要特权。当前实现让面板整体以 root 运行，这是产品取舍。

如果必须降权，需要外部方案（例如用 sudoers 白名单只放行 `nginx -t` / `nginx -s reload`，再把 `-nginx` 指向一个包装脚本）——面板当前**不提供**内置的降权通道。风险清单见第十节。

---

## 九、防火墙

### 9.1 放行面板端口

面板端口只应该对管理网段开放，不要对全网裸奔。

```bash
# firewalld
firewall-cmd --permanent --add-port=8888/tcp
firewall-cmd --permanent --add-rich-rule='rule family="ipv4" source address="10.0.0.0/8" port protocol="tcp" port="8888" accept'
firewall-cmd --reload

# ufw
ufw allow from 10.0.0.0/8 to any port 8888 proto tcp
```

### 9.2 放行站点端口

```bash
# firewalld
firewall-cmd --permanent --add-service=http --add-service=https
firewall-cmd --reload

# ufw
ufw allow 80/tcp
ufw allow 443/tcp
```

### 9.3 云安全组

云厂商安全组在 OS 防火墙之外，两层都要放行。常见故障是：OS 内 `firewalld` / `ufw` 都是 inactive、服务器本机自连正常，但外部访问不通——那基本就是安全组没开。

站点使用非标端口（比如站点的「对外监听端口」填了 8080）时，安全组和 OS 防火墙都要单独放行该端口。

### 9.4 不要放行的端口

| 端口 | 说明 |
|------|------|
| 3306 | MariaDB/MySQL。面板走 unix socket 连接，不需要对外暴露 |
| 面板 8887 | 只在 `-tls` 开启时用于 301 跳转，一般只对内网开放 |
| PHP-FPM socket | unix socket 或 127.0.0.1，**绝不能**监听 0.0.0.0 |

---

## 十、权限与安全建议

### 10.1 目录与文件权限

| 路径 | 建议权限 | 说明 |
|------|----------|------|
| `/opt/zhiweipanel/data` | `0700` | 含数据库、证书私钥、MySQL root 凭据、站点访问认证哈希 |
| `/opt/zhiweipanel/data/zhiweipanel.db` | `0600` | SQLite 主库，含 bcrypt 口令哈希与 session token |
| `/opt/zhiweipanel/data/mysql-root.cnf` | `0600` | `install.sh` 已这样设置，别改 |
| `/opt/zhiweipanel/data/certs/`、`data/ssl/` | `0700` | 证书私钥 |
| `/opt/zhiweipanel/data/htpasswd/` | `0755` | 口令文件是 `0644`：nginx master 以 root 解析配置要能读，部分发行版的 worker 也按请求读 |
| `/opt/zhiweipanel/data/backups/` | `0700` | 备份内容是站点全量文件 |
| `/etc/systemd/system/zhiweipanel.service` | `0600` | 里面有 `-admin-pass` |
| `/opt/zhiweipanel/zhiweipanel` | `0755` | 可执行 |

```bash
sudo chmod 700 /opt/zhiweipanel/data
sudo chmod 600 /opt/zhiweipanel/data/zhiweipanel.db
sudo chmod 600 /etc/systemd/system/zhiweipanel.service
```

### 10.2 凭据处理

- **初始管理员密码**：`install.sh` 会写进 systemd unit。装完立刻改密码，并把 unit 里的 `-admin-pass` 删掉。
- **数据库密码**：创建站点时一次性明文回显，之后任何读路径都拿不到。丢了只能重置。
- **站点访问认证密码**：只写不读，保存后不回显。
- **API Token**：明文只在创建响应里出现一次，库里只存 SHA-256 摘要。丢了只能重建。
- **证书私钥与 ACME 账户私钥**：以 `0600` 存放在数据目录，依赖数据目录本身的权限兜底。

### 10.3 面板自身的暴露面

- 生产环境**务必**开启 `-tls`，或在前面放一层带 TLS 的反向代理；
- 面板默认监听 `0.0.0.0`，用防火墙或反代把它限制在管理网段；
- 站点启用了 Basic Auth 时，密码走 HTTP 头传输，**必须**配 HTTPS 才安全；
- 登录失败限流是内存态的，5 次 / 1 分钟窗口 / 锁定 15 分钟，分 IP+账号两层。面板重启后计数清零。

### 10.4 root 运行的攻击面

面板以 root 运行意味着任何面板侧的代码执行漏洞都直接等于 root 沦陷。本版针对这条链路的防线：

| 防线 | 作用 |
|------|------|
| 站点 ID 服务端生成 | 杜绝「ID 穿越」把配置写到 `conf.d` 之外 |
| 路径白名单校验 | 站点根目录、子目录绑定目标、防篡改目标全部校验，拒绝 `..` 与系统目录 |
| nginx 配置文本净化 | 域名、路径硬校验 + 其余字段净化降级 |
| 写操作强制 POST | 只读 Token 无法越权写 |
| `shell` 独立 scope | 能改配置 ≠ 能执行任意 root 命令 |
| 客户端 IP 只信受信代理 | 限流与审计的 IP 不可伪造 |
| 审计日志 | 所有写操作与登录事件留痕，可事后追溯 |

### 10.5 建议的巡检项

| 频率 | 检查 |
|------|------|
| 每日 | 证书剩余天数（面板「仪表盘」有临期提醒）、站点 5xx 曲线、告警中心未处理条数 |
| 每周 | `journalctl -u zhiweipanel --since '7 days ago' \| grep -i error`、审计日志里的异常来源 IP、磁盘用量 |
| 每月 | 备份可恢复性抽查（下载一份站点备份并 `tar tzf` 验证）、`govulncheck` 复扫、依赖升级评估 |

面板「运维中心 → 配置历史」可以对比最近几份 nginx 配置快照，确认没有人在面板之外改过配置。
