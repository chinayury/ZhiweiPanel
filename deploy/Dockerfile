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
ARG VERSION=0.1.3
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

EXPOSE 8888 80 443

# 健康检查
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD curl -fsS http://127.0.0.1:8888/api/auth/me || exit 1

ENTRYPOINT ["/usr/local/bin/zhiweipanel"]
CMD ["-port", "8888", "-runtime", "host", "-data", "/opt/zhiweipanel/data", "-conf", "/etc/nginx/conf.d"]
