# 智维面板 ZhiweiPanel

**轻量级自托管建站面板 · v0.5.1**

智维面板（ZhiweiPanel）把"建站面板"这件事收敛到**网站管理本身**：域名、网站目录、SSL、PHP、限流、防盗链、防篡改、日志、告警、计划任务、备份。刻意不做宝塔那套周边（FTP、容器编排、面板市场），把功能面收窄到能一次交付、长期维护得住的规模——软件商店也只做「已安装检测 + 安装命令提示」的只读目录，不自动安装/卸载任何东西。

- **单二进制**：`CGO_ENABLED=0` 静态编译，前端资源用 `go:embed` 内嵌进可执行文件。`scp` 到服务器 `chmod +x` 即可运行，不需要 Go 运行时，也不需要额外部署数据库服务（数据落在 SQLite，纯 Go 驱动，无 cgo）。
- **两种运行时**：宿主机模式（systemd 管 nginx）与 Docker 模式，差异收敛在 `driver.Driver` 一个接口后面，业务逻辑完全复用。
- **以 root 运行**：面板要执行 `nginx -t` 与 reload，这是产品取舍，不是疏忽——降权方案见 [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) 第十节。

---

## 功能清单

### 站点级（站点详情页共 19 个功能页签）

| # | 功能 | 能配什么 | 落地的 nginx 配置 |
|---|------|----------|------------------|
| 1 | 基本信息 | 多域名、根目录、运行用户/组、运行目录 `run_path`、符号链接开关、自定义 404、对外监听端口、站点到期时间、备注 | `server_name` / `root` / `error_page 404` |
| 2 | 文件管理 | 浏览、上传、编辑、新建、重命名、删除、在线解压 zip | — |
| 3 | 子目录绑定 | 把 `/blog` 这类 URL 前缀映射到独立磁盘目录 | `location /blog/ { alias ... }` |
| 4 | 访问限制 | IP 黑白名单、HTTP 方法白名单、**HTTP Basic Auth 访问认证** | `allow` / `deny` / `limit_except` / `auth_basic` |
| 5 | 流量限制 | 单连接限速、单 IP 请求频率、月流量配额、超额强制执行 | `limit_rate` / `limit_req_zone` |
| 6 | 伪静态 | 自定义 rewrite 规则或规则文件路径 | 规则原文注入 |
| 7 | 默认文档 | 自定义 index 优先级 | `index` |
| 8 | 配置文件 | 自定义 nginx 片段，原样注入 server 块 | 原文注入 |
| 9 | SSL | 证书绑定、强制 HTTPS、HSTS、TLS 协议版本、OCSP stapling、HTTP/2 | `ssl_*` / HSTS 头 |
| 10 | PHP | 版本切换、PHP-FPM socket、执行时间、内存限制、禁用函数、自定义 php.ini | `fastcgi_pass` |
| 11 | 数据库 | MySQL/MariaDB 联动建库 + 建用户 + 授权，删站联动 drop | — |
| 12 | 重定向 | 301/302/303/307/308 规则 | `return 301/302/...` |
| 13 | 反向代理 | 多上游、路径前缀、WebSocket、SNI、超时、缓存 | `proxy_pass` |
| 14 | 防盗链 | 来源域名白名单 + 受保护扩展名 + 触发状态码 | `valid_referers` / `$invalid_referer` |
| 15 | 防复改 | 文件哈希基线、篡改检测、自动还原、篡改告警 | SHA-256 基线比对 |
| 16 | 网站安全 | SQLi/XSS/目录穿越拦截、UA 黑名单、敏感路径与扩展名屏蔽、隐藏版本号、禁 iframe | `if` / `location deny` |
| 17 | 网站日志 | 访问/错误日志开关、自定义 log_format、保留天数 | 自定义 `log_format` |
| 18 | 网站告警 | 6 种规则（5xx / 流量配额 / 证书临期 / 站点不可访问 / 文件篡改 / nginx 配置异常）、4 种通知渠道 | — |
| 19 | 其他设置 | gzip、上传大小限制、浏览器缓存、KeepAlive/读写超时、DNS 解析器、静态资源缓存 | `gzip` / `expires` / `client_max_body_size` |

### 平台级

| 功能 | 说明 |
|------|------|
| 证书管理 | Let's Encrypt 签发（HTTP-01 / DNS-01），DNS 提供商内置阿里云 / DNSPod / 腾讯云 / Cloudflare；自动续签、ACME 账户管理、导入外部证书、证书与站点批量绑定 |
| 计划任务 | 6 种任务类型（Shell 脚本 / 备份站点 / 备份数据库 / 切割访问日志 / 释放内存 / 访问 URL），5 段 crontab 表达式，执行历史留档，失败自动通知 |
| 备份 | 站点备份（tar.gz，含渲染好的 nginx conf）与数据库备份（mysqldump + gzip），支持下载、恢复预览、保留份数裁剪 |
| 站点批量操作 | 列表多选后批量启动 / 停止 |
| 站点到期时间 | 未来 7 天内到期标黄、已过期标红 |
| API Token | 机器访问凭据，`read` / `write` / `shell` 三档 scope 隔离，明文只显示一次 |
| 软件商店 | 面板已知软件的只读目录（nginx/PHP/MySQL/Redis/Docker/Node.js/Memcached）：检测安装状态与版本，给出安装命令提示；**不自动安装/卸载** |
| 操作审计 | 记录写操作与登录事件：谁、何时、动了什么、结果码、来源 IP，并区分浏览器会话与机器 Token |
| 系统运行指标 | CPU / 内存 / 磁盘 / 负载 / 网络速率 / 进程数 / 运行时长，纯 Go 读 `/proc`，零新增依赖 |
| 配置历史 | 每次配置下发成功后留快照（保留 30 份），可查看原始内容、与当前配置逐行 diff、一键回滚 |
| 告警中心 | 全局告警列表、批量标记已处理、通知渠道管理与连通性测试 |
| 账户与邮件 | 管理员邮箱绑定与验证、SMTP 配置、忘记密码（邮箱验证码重置）、改密、异常登录二次验证 |

---

## 快速开始

完整教程见 [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)，这里只给最短路径。

### 裸机部署（systemd）

```bash
# 1. 准备环境：nginx / PHP-FPM / MariaDB（install.sh 会自动装）
# 2. 一键安装：二进制地址与管理员密码必须显式给出
BIN_URL=https://your-host/zhiweipanel-0.1.3-linux-amd64 \
PANEL_PASS='CHANGE_ME_at_least_8_chars' \
INSTALL_PHP=1  PHP_VERSION=8.1 \
INSTALL_MYSQL=1 \
bash deploy/install.sh

# 3. 确认面板已起
systemctl status zhiweipanel
journalctl -u zhiweipanel -f
```

`install.sh` 会：安装 nginx / PHP-FPM / MariaDB → 创建目录 → **幂等地**向 nginx 主配置的 `http {}` 块内注入 `include` → 下载二进制 → 注册 systemd 服务并开机自启 → 放行防火墙 → 把 MariaDB root 凭据写入 `data/mysql-root.cnf`（权限 0600）供面板建库使用。

手工部署也可以，只有三步：

```bash
sudo mkdir -p /opt/zhiweipanel/data /opt/zhiweipanel/nginx/conf.d
sudo cp zhiweipanel /opt/zhiweipanel/ && sudo chmod +x /opt/zhiweipanel/zhiweipanel

# 向 /etc/nginx/nginx.conf 的 http { } 块内加入这一行（必须在 http 块内）
#   include /opt/zhiweipanel/nginx/conf.d/*.conf;

sudo /opt/zhiweipanel/zhiweipanel \
  -port 8888 \
  -data /opt/zhiweipanel/data \
  -conf /opt/zhiweipanel/nginx/conf.d \
  -nginx /usr/sbin/nginx \
  -admin admin -admin-pass 'CHANGE_ME_at_least_8_chars'
```

### Docker 部署

```bash
cd deploy
docker compose up -d --build
docker compose logs -f
```

`deploy/docker-compose.yml` 默认映射面板 8888、HTTP 80、HTTPS 443，并把站点数据、nginx 配置目录、站点根目录挂成三个具名卷。

> 注意：compose 传的是 `-runtime=host`，即**面板在容器内管理本容器里的 nginx**。`-runtime=docker` 是另一种形态——面板在宿主机上、通过 `docker exec` 管理一个独立 nginx 容器，见 [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) 第三节。

---

## 命令行参数

全部参数定义在 `main.go` 的 `flag` 声明里，`-version` 可直接打印版本号。

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `-port` | int | `8888` | 面板监听端口。启用 `-tls` 时是 HTTPS 端口，未启用时是 HTTP 端口 |
| `-http-port` | int | `8887` | HTTP→HTTPS 301 重定向端口，`0` 表示禁用。**仅在 `-tls` 开启时才生效**（未启用 TLS 时不会起这个 listener） |
| `-tls` | bool | `false` | 启用 HTTPS。自签证书不存在时自动生成到 `<data>/panel.crt` + `<data>/panel.key` |
| `-tls-cert` | string | 空 | 指定 TLS 证书路径；留空则用 `<data>/panel.crt` |
| `-tls-key` | string | 空 | 指定 TLS 私钥路径；留空则用 `<data>/panel.key` |
| `-tls-hosts` | string | `localhost,127.0.0.1` | 面板自签证书的 SAN 列表，逗号分隔（DNS 域名或 IP） |
| `-public-port` | int | `0` | 全局对外 HTTP 端口。`0` = 各站点用自己配置的 `listen_ports`；非 0 = 所有站点统一用这个端口 |
| `-disable-ipv6` | bool | `false` | 关闭 `listen ... [::]:` 指令。老机器没有 IPv6 时 nginx 会报 `emerg`，用它兜底 |
| `-data` | string | `/opt/zhiweipanel/data` | 数据目录：数据库、证书、备份、访问认证口令文件、日志 |
| `-conf` | string | `/opt/zhiweipanel/nginx/conf.d` | nginx `conf.d` 目录（http 级配置与 catch-all 的落盘位置） |
| `-site-conf` | string | 空 | 站点 vhost 目录，留空则与 `-conf` 相同 |
| `-cert-dir` | string | 空 | 证书目录，留空则用 `<data>/certs` |
| `-backup-dir` | string | 空 | 站点/数据库备份根目录。留空 = `<data>/backup`（历史默认，老部署的备份记录都指向那里）；显式设置后**新的备份**写到 `<backup-dir>/{site,database}/`，已有的旧备份记录与文件不受影响 |
| `-runtime` | string | `host` | 运行时：`host`（systemd 管 nginx）或 `docker`（`docker exec` 管容器 nginx） |
| `-nginx` | string | `nginx` | nginx 可执行文件路径。宿主机模式下建议写绝对路径（如 `/usr/sbin/nginx`） |
| `-nginx-unit` | string | 空（= `nginx`） | nginx 的 systemd 服务名。nginx 由**自定义单元**托管时必须填（如 `aa_nginx`、`openresty`），否则状态页显示"未运行"，且配置下发时的 `systemctl reload` 会打错单元 |
| `-container` | string | `nginx` | docker 模式下的 nginx 容器名 |
| `-host-conf` | string | `/opt/zhiweipanel/nginx/conf.d` | docker 模式下宿主机挂载的 `conf.d` 路径 |
| `-admin` | string | `admin` | 初始管理员用户名 |
| `-admin-pass` | string | 空 | 初始管理员密码，**仅在首次启动创建账户时生效**。留空且账户不存在时面板会打印警告，首次登录不可用 |
| `-version` | bool | `false` | 打印版本号后退出 |
| `-trusted-proxy` | string | 空 | 受信反向代理网段（CIDR 或单 IP，逗号分隔）。只有直连方落在这些网段内才采信 `X-Forwarded-For`，否则一律用直连地址。留空 = 不信任任何代理（默认，最安全） |

---

## ⚠ 从 SitePanel 改名到智维面板（破坏性变更）

本项目已由 **SitePanel** 更名为 **智维面板 / ZhiweiPanel**（Go 模块路径 `github.com/panel/sitepanel` → `github.com/zhiwei/panel`）。

改名**同时改了运行时标识符**，且**不保留旧名兼容**。线上从旧版升级时必须按下表迁移，否则会出现
"数据库读不到"或"nginx 报重复指令"两类故障。

### 需要迁移的运行时标识符

| 项目 | 旧值 | 新值 |
|------|------|------|
| 安装目录 | `/opt/sitepanel` | `/opt/zhiweipanel` |
| 数据库文件 | `<data>/sitepanel.db` | `<data>/zhiweipanel.db` |
| systemd 单元 | `sitepanel.service` | `zhiweipanel.service` |
| 单元 EnvironmentFile | `/etc/sitepanel/sitepanel.env` | `/etc/zhiweipanel/zhiweipanel.env` |
| 单元环境变量名 | `SP_ADMIN_PASS` | `ZW_ADMIN_PASS` |
| 二进制名 | `sitepanel` | `zhiweipanel` |
| http 级 nginx 配置 | `00-sitepanel-http.conf` | `00-zhiweipanel-http.conf` |
| catch-all default_server | `01-sitepanel-default-server.conf` | `01-zhiweipanel-default-server.conf` |
| Docker 镜像 / 容器 | `sitepanel` | `zhiweipanel` |
| nginx `log_format` | `sitepanel_access` | `zhiweipanel_access` |
| nginx 配额锁变量 | `$sp_quota_lock` | `$zw_quota_lock` |
| nginx 主配置备份名 | `nginx.conf.sp-backup` | `nginx.conf.zw-backup` |
| API Token 前缀 | `sp_` | `zw_` |
| 浏览器主题键 | `sitepanel_theme` | `zhiweipanel_theme` |
| 会话 Cookie | `sitepanel_session` | `zhiweipanel_session` |

> **API Token 前缀是显示用的，不影响已发 token**：服务端对整个 token 做 SHA-256 后比对
> （`internal/store/ops_store.go`），前缀只用于日志/界面展示，所以改名后
> **已签发的 token 继续可用**，只有新签发的 token 用 `zw_` 前缀。

### 迁移步骤（宿主机 / systemd）

```bash
# 1. 停面板（此时站点仍由 nginx 独立提供服务，不停 nginx）
sudo systemctl stop sitepanel

# 2. 搬数据目录与二进制
sudo mv /opt/sitepanel /opt/zhiweipanel
sudo mv /opt/zhiweipanel/sitepanel.db /opt/zhiweipanel/zhiweipanel.db
sudo mv /opt/zhiweipanel/sitepanel /opt/zhiweipanel/zhiweipanel

# 3. 搬凭据文件（600 权限，务必保留）
sudo mkdir -p /etc/zhiweipanel
sudo mv /etc/sitepanel/sitepanel.env /etc/zhiweipanel/zhiweipanel.env
sudo systemctl disable sitepanel

# 4. 换 systemd 单元
sudo cp deploy/zhiweipanel.service /etc/systemd/system/zhiweipanel.service
sudo systemctl daemon-reload
sudo systemctl enable zhiweipanel

# 5. 换 nginx include 路径（旧 include 行必须删掉，否则会加载到已废弃的旧目录）
sudo sed -i '\|include /opt/sitepanel/nginx/conf.d/\*.conf;|d' /etc/nginx/nginx.conf
sudo sed -i '/^[[:space:]]*http[[:space:]]*{/a\    include /opt/zhiweipanel/nginx/conf.d/*.conf;' /etc/nginx/nginx.conf

# 6. 移走旧 http 级 / catch-all 配置（文件名变了，不移走会与新文件重复定义）
#    用 mv 不用 rm：保留 .bak 便于回滚。nginx 只加载 *.conf，.bak 不会被加载
sudo mv /opt/zhiweipanel/nginx/conf.d/00-sitepanel-http.conf /opt/zhiweipanel/nginx/conf.d/00-sitepanel-http.conf.migrated.bak
sudo mv /opt/zhiweipanel/nginx/conf.d/01-sitepanel-default-server.conf /opt/zhiweipanel/nginx/conf.d/01-sitepanel-default-server.conf.migrated.bak

# 7. 改数据库里的绝对路径（★ 最容易漏，见下方说明）
sudo systemctl stop zhiweipanel
sudo cp -a /opt/zhiweipanel/data/zhiweipanel.db /root/zhiweipanel.db.bak-$(date +%Y%m%d-%H%M%S)
sudo python3 - <<'PY'
import sqlite3
DB = "/opt/zhiweipanel/data/zhiweipanel.db"
OLD, NEW = "/opt/sitepanel", "/opt/zhiweipanel"
c = sqlite3.connect(DB)
for sid, cfg in c.execute("select id, config from sites").fetchall():
    if OLD in cfg:
        c.execute("update sites set config=? where id=?", (cfg.replace(OLD, NEW), sid))
        print("  sites.config", sid)
for t, col in (("certs","cert_file"),("certs","key_file"),("acme_accounts","key_path"),("backups","path")):
    for rid, v in c.execute("select id, %s from %s where %s like ?" % (col,t,col), ("%"+OLD+"%",)).fetchall():
        c.execute("update %s set %s=? where id=?" % (t,col), (v.replace(OLD,NEW), rid))
        print("  %s.%s %s" % (t, col, rid))
c.commit()
c.close()
PY

# 8. 起新版本（面板启动时会自动重新下发 http 级配置与 catch-all，然后 nginx -t + reload）
sudo systemctl start zhiweipanel
sudo journalctl -u zhiweipanel -f
```

> ### ★ 第 7 步：数据库里存的是**绝对路径**，改名后必须改
>
> 面板把站点配置、证书路径、ACME 账户私钥路径、备份路径**以绝对路径存进数据库**：
> `sites.config`（JSON 里的 `ssl.cert_file` / `ssl.key_file`）、`certs.cert_file`、
> `certs.key_file`、`acme_accounts.key_path`、`backups.path`。
>
> 安装目录一改名，这些路径就全部指向**不存在的文件**。后果：
>
> - 面板重新渲染出的站点配置里 `ssl_certificate` 指向空路径 →
>   `nginx -t` 报 `cannot load certificate "...": No such file or directory`；
> - 面板的配置下发**失败**（且**不阻断启动**，只是打一行错误日志），
>   于是面板看起来"起来了"，盘上却是一份 nginx 读不了的配置；
> - **站点当下还能访问**（运行中的 nginx 内存里还是旧配置），但下一次任何外部
>   nginx reload（certbot 续签、机器重启）就会把全部站点一起带下去。
>
> 证书文件本身是跟着数据目录一起 `mv` 过去的，内容没变，所以**统一替换路径前缀**即可。
> 改完**必须**确认 `nginx -t` 通过、`journalctl | grep 下发` 无失败，再对外宣布完成。

> 第 6 步**最容易被漏**。`00-sitepanel-http.conf` 里定义了每个站点的 `limit_req_zone`
> （zone 名带站点 ID，改名不会变），`01-sitepanel-default-server.conf` 里带 `default_server` 标记——
> 旧文件留在盘上，新版本再写一份同职责的新文件，`nginx -t` 就会报
> **`limit_req_zone` 重复定义**或 **`a duplicate default server`**，整批站点一起停摆。

> 第 6 步与第 8 步之间**不要手工 reload nginx**：此时盘上的 `10-site-*.conf`
> 还引用旧的 `sitepanel_access` 格式，而该格式定义在被移走的旧 http 级配置里，
> 此时 reload 必然失败。等面板起来自己下发完配置再 reload。

> 起新版本后**务必确认启动下发成功**：
> `sudo journalctl -u zhiweipanel | grep 下发`。
> 启动时的配置下发失败**不会**中断面板启动（只是打一行错误日志），
> 于是面板看起来"起来了"、盘上却是一份 nginx 读不了的配置——
> 下一次任何外部 nginx reload（certbot 续签、机器重启）就会把全部站点一起带下去。

### 迁移步骤（Docker）

Docker 侧除了服务名/镜像名，**还必须处理数据库文件名与 nginx 旧配置文件**——
卷名没变（数据还在），但面板按新文件名找库、按新文件名写配置，
不处理的话会得到一个"空库 + 重复指令"的组合。

```bash
cd deploy

# 1. 停旧容器
#    注意：compose 文件里的服务名已改成 zhiweipanel，
#    旧的 sitepanel 容器会变成孤儿容器继续占着 8888/80/443 端口，
#    必须用 --remove-orphans 收掉，或直接 docker rm -f。
docker compose down --remove-orphans
docker rm -f sitepanel 2>/dev/null || true

# 2. 找旧卷名（compose 会加项目名前缀，实际卷名形如 <project>_site-data）
docker volume ls | grep -E '(site-data|nginx-conf)'

# 3. 在旧卷里把数据库与 nginx 配置改名/清掉
#    先用一条命令看卷里有什么：
#    docker run --rm -v <project>_site-data:/d alpine ls -l /d
#    docker run --rm -v <project>_nginx-conf:/c alpine ls -l /c
docker run --rm -v <project>_site-data:/d alpine \
  sh -c 'mv -f /d/sitepanel.db /d/zhiweipanel.db 2>/dev/null || true; ls -l /d'
docker run --rm -v <project>_nginx-conf:/c alpine \
  sh -c 'rm -f /c/00-sitepanel-http.conf /c/01-sitepanel-default-server.conf; ls -l /c'

# 4. 改数据库里的绝对路径（与宿主机迁移第 7 步完全相同，理由见上文 ★）
#    用一次性容器执行 python3 改库；把宿主机迁移第 7 步那段脚本贴进 sh -c 即可
docker compose up -d
docker compose exec zhiweipanel sh -c 'python3 -c "
import sqlite3
DB = \"/opt/zhiweipanel/data/zhiweipanel.db\"
OLD, NEW = \"/opt/sitepanel\", \"/opt/zhiweipanel\"
c = sqlite3.connect(DB)
for sid, cfg in c.execute(\"select id, config from sites\").fetchall():
    if OLD in cfg: c.execute(\"update sites set config=? where id=?\", (cfg.replace(OLD,NEW), sid))
for t, col in ((\"certs\",\"cert_file\"),(\"certs\",\"key_file\"),(\"acme_accounts\",\"key_path\"),(\"backups\",\"path\")):
    for rid, v in c.execute(\"select id, %s from %s where %s like ?\" % (col,t,col), (\"%\"+OLD+\"%\",)).fetchall():
        c.execute(\"update %s set %s=? where id=?\" % (t,col), (v.replace(OLD,NEW), rid))
c.commit(); c.close()
print(\"paths rewritten\")
"'

# 5. 重启面板，让它重新下发配置
docker compose restart zhiweipanel
docker compose logs -f zhiweipanel
```

> 第 4 步（改数据库里的绝对路径）**在 Docker 侧同样不能省**：证书路径存在数据库里，
> 不改的话容器里 nginx 读不到证书 → `nginx -t` 失败 → 容器起不来。
> 详见上文「★ 第 7 步」的说明。

> 第 3 步的 nginx 清理**不能省**：`nginx-conf` 卷里若留着旧的
> `00-sitepanel-http.conf` / `01-sitepanel-default-server.conf`，
> 面板再写一份新名的同职责文件 → `limit_req_zone` 重复定义 +
> `a duplicate default server` → 容器里的 nginx 起不来，面板反复重启。

> 改了环境变量名的（`SP_ADMIN_PASS` → `ZW_ADMIN_PASS`）在 `deploy/zhiweipanel.service` 里，
> 部署前先改好 `/etc/zhiweipanel/zhiweipanel.env` 里的变量名。

> **单元文件要整体重写，不要只改路径**：`ExecStart=` 的多行续行如果第一行末尾
> 没有 `\`，systemd 会把后续每行都当成独立指令丢弃（报 `Missing '=', ignoring line`），
> 面板就会**只吃到第一个 flag**，其余全部走默认值——现象是"面板起来了，但参数不生效"。
> 最稳的做法是按 `deploy/zhiweipanel.service` 模板重写整份单元（保留线上实际的
> `-tls-hosts` / `-nginx` 等真实取值），并确认末尾有 `[Install] WantedBy=multi-user.target`
> ——缺了 `[Install]` 的单元是 `static` 状态，**重启机器不会自启**。

---

## 架构

### 目录结构

```
main.go                     启动入口：命令行参数、go:embed 前端、后台任务编排
internal/
  version/                  版本号唯一权威源（Version 常量）+ 常驻门禁测试
  model/                    领域模型：Site 聚合全部站点配置 + 全字段校验器
  nginx/                    配置渲染引擎、三道防线、纯 Go 配置校验器、catch-all
  service/                  业务编排：写配置 → nginx -t → reload，失败自动回滚
  store/                    SQLite 持久化（modernc.org/sqlite，纯 Go，无 cgo）
  driver/                   运行时抽象
    ├ hostdriver/           systemctl / nginx -s reload
    └ dockerdriver/         docker exec
  api/                      REST + WebSocket、鉴权、scope 校验、审计、限流
  htpasswd/                 apr1 哈希生成（nginx auth_basic 用）
  acme/                     Let's Encrypt（lego v4）+ HTTP-01 webroot + DNS-01 提供商
  cron/                     计划任务调度器 + 6 类任务 handler + 危险命令检查
  backup/                   站点 / 数据库备份与恢复
  logmgr/                   访问日志解析、流量聚合、文件指纹基线
  alert/                    告警引擎 + 通知渠道
  mailer/                   SMTP 邮件发送
  mysql/                    MySQL/MariaDB 建库建用户授权与清理
  confhistory/              nginx 配置快照、diff、回滚
  sysinfo/                  宿主机资源采样（纯 Go 读 /proc）
  tlsgen/                   自签证书生成
  apps/                     软件商店静态目录（安装检测在 service 层）
web/                        前端：原生 ES Module，无 npm、无构建步骤
  js/views/                 13 个页面视图（仪表盘/站点/软件商店/证书/计划任务/备份/日志/告警/运维/设置…）
deploy/                     Dockerfile / docker-compose.yml / zhiweipanel.service / install.sh
docs/                       部署、使用、变更、安全与审计文档
```

### 运行时抽象

`driver.Driver`（`internal/driver/driver.go`）把"怎么管 nginx"的差异收敛到 13 个方法：

```go
type Driver interface {
    Runtime() Runtime
    Paths() Paths
    NginxStatus(ctx) (NginxStatus, error)
    NginxTest(ctx) (bool, string, error)
    Reload(ctx) error
    WriteConf(ctx, name, content) error
    RemoveConf(ctx, name) error
    WriteSiteConf(ctx, siteID, content) error
    RemoveSiteConf(ctx, siteID) error
    ReadConf(ctx, name) (string, error)
    ListSiteConfs(ctx) ([]string, error)
    PHPVersions(ctx) ([]PHPVersion, error)
    SystemStatus(ctx) (SystemStatus, error)
}
```

宿主机实现走 `systemctl` + `nginx -s reload`，Docker 实现走 `docker exec`。配置渲染、存储、业务逻辑完全复用。

### 配置下发流水线

`ApplyAllConfig` 是唯一写入 nginx 的入口，串行执行且失败自动回滚：

```
渲染站点（坏站点隔离）→ 写 10-site-<id>.conf → 写 00-zhiweipanel-http.conf
    → 写 01-zhiweipanel-default-server.conf → nginx -t → reload
                              ↓ 失败
                    回滚所有已写入的配置文件
```

- `nginx -t` 通过前绝不 reload，因此不会出现"配置写坏导致全站 502"。
- **坏站点被隔离而不是拖垮全站**：单个站点渲染失败时，它的配置被整段注释掉，其余站点照常下发；面板记录并在界面上展示被隔离的站点（`Service.ConfigIssues()`）。
- **配置加载顺序**由文件名前缀控制：nginx 按文件名升序加载 `conf.d/*`，`log_format` / `limit_req_zone` 等 http 级指令必须先于站点定义，否则报 `unknown log format`。因此 http 级配置用 `00-`、catch-all 用 `01-`、站点配置用 `10-site-<id>.conf`。

### 渲染三道防线

`nginx.Render` 的顺序不能换（`internal/nginx/render.go`）：

1. **域名硬校验** —— 没有合法域名就没有 `server_name`，站点无法对外服务，属站点级致命错误。
2. **路径硬校验** —— 路径没有"安全降级"语义（替换成别的目录 = 静默 404），同样按致命错误处理。
3. **净化降级** —— 其余字段（IP 名单、UA 屏蔽词、反代目标、重定向……）非法时丢弃或回落，**不**让渲染失败。

第 3 步是关键：如果脏数据能让渲染报错，渲染失败 → 全量下发中止 → **所有**站点的配置都停在旧版本，一个站点的历史脏字段就能把整台面板的其它站点一起卡住。

### 安全设计要点

- **写操作一律强制 POST**。权限模型把"能不能写"绑在 HTTP 方法上（只读 Token 只允许 GET/HEAD/OPTIONS），只要写操作允许用 GET 触发，它就自动退化成只读。所有有副作用的子动作都走 `requirePost`，"GET 一定不改状态"成了一条不依赖人记的规则。
- **API Token scope 三档**：`read` / `write` / `shell`。`shell` 是独立授权位——面板以 root 运行，"Shell 脚本"类型的计划任务等于交出 root shell，一个泄漏的 `write` Token 不应该等于整机 root。
- **客户端 IP 只在受信代理内才采信**。没有 `-trusted-proxy` 时一律用直连地址，登录限流的计数键因此无法被伪造的 `X-Forwarded-For` 绕过。
- **登录失败限流分两层**：IP + 用户名（5 次 / 1 分钟窗口 / 锁定 15 分钟）与纯账号维度（阈值 3 倍），换 IP 暴力破解同一个账号同样有上限。
- **异常登录提示不回显上次登录 IP**。原因文案会原样返回给尚未通过二次验证的客户端，回显等于把管理员上次登录 IP 交出去，双因素会被降级成单因素。
- **站点访问认证口令只写不读**。面板把 apr1 哈希写进 `<data>/htpasswd/<站点ID>`，nginx 侧输出 `auth_basic` + `auth_basic_user_file`；明文口令不入库、不回显、留空表示不修改。
- **MySQL 建库参数由服务端决定**。库名、用户名、密码、字符集、主机全部由面板生成，客户端指定会被拒绝；驱动层对标识符与字面量做转义（MySQL DDL 不支持占位符参数化，只能靠拼接 + 转义）。
- **只读 Token 越权写不了**。scope 校验在鉴权中间件里完成，写方法一律要求 `write`；`shell` 任务额外要求 `shell`。

### 版本号治理

版本号只有一个权威源：`internal/version/version.go` 的 `const Version = "0.1.3"`。`main.go` 的 `var Version = version.Version` 只做转发，构建时可用 `-ldflags "-X main.Version=..."` 覆盖（`deploy/Dockerfile` 就是这么做的）。

`internal/version/version_test.go` 是一道**常驻门禁**，跟着 `go test ./...` 一起跑，检查：

- 全锚点对齐：`Dockerfile` 的 `ARG VERSION`、`docker-compose.yml` 的 `${VERSION:-...}`、`install.sh` 的下载文件名、`README.md` 的版本标注、`CHANGELOG.md` 的版本段，必须与权威源一致；
- 无漂移：代码与部署文件里不允许出现既不是当前版本、也不是已登记历史版本的面板版本号。

改版本号时只改 `internal/version/version.go` 一处，然后跑 `go test ./internal/version/`，门禁会告诉你还有哪些锚点需要同步。

---

## 文档索引

| 文档 | 内容 |
|------|------|
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | 环境要求、裸机 / Docker 部署、参数表、目录结构、升级回滚、卸载、排障、防火墙、权限建议 |
| [docs/USAGE.md](docs/USAGE.md) | 逐个功能的操作说明：在哪配、字段含义、生效条件、注意事项；含 API 速查表 |
| [docs/CHANGELOG.md](docs/CHANGELOG.md) | 版本迭代说明 |
| [docs/BUGFIX-v0.1.0.md](docs/BUGFIX-v0.1.0.md) | 本轮缺陷修复清单 |
| [docs/OPTIMIZATION-v0.1.0.md](docs/OPTIMIZATION-v0.1.0.md) | 本轮功能优化与新增日志 |
| [docs/SECURITY-v0.1.0.md](docs/SECURITY-v0.1.0.md) | 安全审查报告 |
| [docs/BT-COMPARE-v0.2.0.md](docs/BT-COMPARE-v0.2.0.md) | 与宝塔面板的差异分析 |
| [docs/RESEARCH-BORROW-v0.2.2.md](docs/RESEARCH-BORROW-v0.2.2.md) | 同类项目借鉴要点清单 |
| [docs/audit/](docs/audit/) | 4 份审计原始报告（本轮修复的缺陷来源） |

---

## 质量状态

以下为当前仓库状态的实测结果。

| 项 | 结果 |
|----|------|
| `go build ./...` | ✅ 通过（Go 1.26.10） |
| `go vet ./...` | ✅ 零告警 |
| `gofmt -l .` | ✅ 零输出 |
| `go test ./...` | ✅ 全部通过：212 个测试函数（17 个包全绿），0 FAIL |
| `govulncheck ./...` | ✅ `Your code is affected by 0 vulnerabilities`（须用与构建一致的 go1.26.10 工具链编译扫描器，见 [安全报告 §3.7](docs/SECURITY-v0.1.0.md)） |
| 依赖升级 | Go `1.26.10`；`github.com/go-acme/lego/v4` `v4.25.2` → `v4.35.2`（修 GO-2026-5593 Webroot 路径穿越任意文件写）；零新增依赖 |
| 代码规模 | 103 个 `.go` 文件 / 21203 行；15 个前端 ES Module / 6139 行；39 个测试文件 / 212 个测试函数 |
| 线上端到端 | ✅ 35 项对抗用例全过、0 失败（生产机真实 nginx + 真实站点） |

> 测试与扫描在开发机（Windows）上执行。面板本身的生产形态是 Linux，宿主机相关的用例（`/proc` 采样、logrotate 式日志切割）在 Linux 上才有意义。

---

## 已知限制

以下限制是当前实现的客观事实，不是路线图承诺：

1. **面板以 root 运行**。`nginx -t` 与 reload 需要特权。降权需要外部 reload 通道，面板当前不提供。
2. **Shell 计划任务不是沙箱**。危险命令检查（归一化 + 词法切分 + 命令词匹配）只是一道减少误操作的护栏，挡不住有意绕过。真正的授权边界在 API 层的独立 `shell` scope。
3. **通知渠道没有 SSRF 防护**。`webhook` / 钉钉 / 企业微信渠道的 target 由用户直接填写，面板不校验目标地址是否指向内网。
4. **配置回滚只恢复 nginx 配置，不回滚数据库**。站点定义仍是当前那一份；要连站点定义一起回退需配合站点备份。
5. **运行指标历史只在内存里**。内存环形缓冲保留最近 120 次采样（10 秒一次 ≈ 20 分钟），面板重启后曲线从头开始。长期留存需要外部时序库。
6. **审计只记写操作与登录事件**。"谁看了站点配置"不留痕。审计表保留最近 5000 条，超出自动裁剪。
7. **备份单文件上限 100MB**。超出直接报错让用户处理，不做自动分卷。
8. **站点备份不含访问日志**。`logs/` 目录被显式排除（access.log 动辄几十 MB），需要单独备份。
9. **站点恢复只提供预览**。`POST /api/backups/{id}/restore` 返回备份内 `nginx.conf` 的预览内容，数据文件需要用户自行 `tar xzf` 恢复。
10. **同一端口只能给一个站点**。nginx 监听冲突由面板拦截，报错提示换端口。
11. **`-http-port` 只在 `-tls` 开启时生效**。纯 HTTP 模式下不会起 301 重定向 listener。
12. **数据库密码只能重置、不能查看**。创建时一次性明文回显，之后任何读路径都拿不到明文。
13. **告警类型是固定枚举**（`status_5xx` / `traffic_quota` / `cert_expiry` / `site_down` / `tamper` / `nginx_config` / `config_drift`），前端下拉由后端 `GET /api/alert-types` 动态下发，新增类型必须同时改后端检查器。
