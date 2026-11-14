#!/bin/sh
# ZhiweiPanel 容器入口。
#
# 面板只负责「下发配置 → nginx -t → reload」，它**从不**启动 nginx；
# 容器里也没有 systemd 可用。少了这个入口脚本，镜像里就不会有 nginx
# 进程，首屏配置下发必然失败：
#
#     nginx: [error] invalid PID number "" in "/run/nginx/nginx.pid"
#
# 入口做三件事：建好运行时目录 → 后台拉起 nginx → exec 面板本体。

set -e

# nginx 以 nginx 用户运行，这些目录必须存在且对它可写
mkdir -p /run/nginx /var/lib/nginx/tmp /var/log/nginx
chown -R nginx:nginx /run/nginx /var/lib/nginx /var/log/nginx 2>/dev/null || true

# 后台拉起 nginx（nginx 自带 daemon，master 进程驻留后台，本进程随即返回）。
# 启动失败不阻断面板：面板仍能起来，用户能在控制台看到 nginx 状态并排查，
# 而不是整个容器卡在启动阶段、连不上面板。
if nginx; then
    :
else
    echo "[entrypoint] nginx 启动失败，面板继续启动；请检查 nginx 配置与 /var/log/nginx/error.log" >&2
fi

# 用 exec 接管 PID 1，保证面板收到正常信号（停止容器时能优雅退出）
exec /usr/local/bin/zhiweipanel "$@"
