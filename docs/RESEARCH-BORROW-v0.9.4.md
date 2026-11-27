# 保留站点已有首页

nginx 会按站点配置的顺序查找默认文档。如果站点配置了 `index.html home.html`，目录中已有 `home.html`，此时生成 `index.html` 会让访客看到面板欢迎页。建站、编辑站点和重新下发配置现在共用默认文档的过滤规则，创建欢迎页前同时检查常见首页和站点配置的文件名。[nginx 默认文档说明](https://nginx.org/en/docs/http/ngx_http_index_module.html)

| 来源 | 项目取舍 |
| --- | --- |
| [1Panel 创建站点时保留目录内容](https://github.com/1Panel-dev/1Panel/pull/14042) | 采用保留已有内容的做法。当站点明确排除 `index.html` 时跳过欢迎页；需要生成时以独占模式创建，避免覆盖同时写入的同名文件。 |
| [HestiaCP 关于重载前检查 nginx 配置的讨论](https://github.com/hestiacp/hestiacp/issues/5987) | 面板的配置下发已在重载前执行 `nginx -t`，并在失败时恢复配置。 |
| [Infinite OS 的面板操作说明](https://github.com/goinfinite/os/blob/main/SKILL.md) | 现有 [面板操作说明](AGENT.md) 已记录 API 权限、读写端点与配置下发的副作用。 |
