# syntax=docker/dockerfile:1
#
# ZhiweiPanel 镜像。
# 内含 nginx + 面板本体，站点目录与配置通过 volume 挂载持久化。

# ---------- 构建阶段 ----------
# go.mod 要求 go >= 1.26.10（GO-2026-6809：crypto/x509 信任被攻陷的中间 CA）。
# 官方 golang 镜像默认 GOTOOLCHAIN=local，不会自动下载工具链，
# 基础镜像必须本身满足 go.mod 的版本要求。
FROM golang:1.26-alpine AS builder

WORKDIR /src

# 先只拷贝依赖清单，最大化利用镜像层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 产出纯静态二进制，可直接放进 scratch/alpine
# -s -w 去除符号表，显著减小体积
ARG VERSION=0.5.0
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.Version=${VERSION}" \
        -o /out/zhiweipanel .

# ---------- 运行阶段 ----------
FROM alpine:3.20

RUN apk add --no-cache nginx ca-certificates tzdata curl \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

# nginx 以非 root 运行，需调整默认监听端口与目录权限
RUN mkdir -p /var/lib/nginx/tmp /var/log/nginx /www/wwwroot /opt/zhiweipanel/data \
    && chown -R nginx:nginx /var/lib/nginx /var/log/nginx /www

COPY --from=builder /out/zhiweipanel /usr/local/bin/zhiweipanel

# alpine 的默认 nginx.conf 把 `include /etc/nginx/conf.d/*.conf` 放在
# **root context**（http 块之外），而面板下发的配置里既有 http 级指令
# （log_format / limit_req_zone）也有 server 块，只能出现在 http {} 内。
# 直接挂载会直接报 "log_format" directive is not allowed here。
# 改用本仓库维护的 nginx.conf（conf.d 挪进 http {}，pid 路径显式固定）。
COPY deploy/nginx.conf /etc/nginx/nginx.conf

# alpine 自带的 /etc/nginx/http.d/default.conf 是 :80 上的 default_server，
# 与面板下发的 catch-all 冲突（重复定义 default server），镜像构建阶段就删掉。
RUN rm -f /etc/nginx/http.d/default.conf

# 入口脚本：先拉起容器内 nginx，再 exec 面板本体。
# 面板只做「下发配置 → nginx -t → reload」，从不 start nginx；
# 容器里也没有 systemd，没有这个脚本镜像内就没有 nginx 进程。
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 8888 80 443

# 健康检查
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD curl -fsS http://127.0.0.1:8888/api/auth/me || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["-port", "8888", "-runtime", "host", "-data", "/opt/zhiweipanel/data", "-conf", "/etc/nginx/conf.d"]
