// 站点详情：左侧标签页覆盖站点对象全部 17 组功能配置
// 所有表单直接读写同一个站点对象，保存时整体 PUT /api/sites/:id
import { api, formatDateTime, formatBytes, formatNumber, certDaysLeft } from '../api.js';
import {
  h, clear, icon, button, tag, switchControl, switchField, field, textInput,
  numberInput, selectInput, textArea, checkboxField, loading,
  errorState, emptyState, toast, modal, codeBox, rowEditor,
  stringListEditor, pillMultiSelect, grid, sectionTitle, tabPanel, table,
  certDaysBadge, confirmDialog,
} from '../components.js';
import { ctx, store } from '../app.js';
import { mountFilesView } from './site-files.js';

// 站点对象各分组的默认值（后端返回部分字段时用于补全）
const DEFAULTS = {
  domains: ['example.com'],
  sub_dir: { enabled: false, rules: [{ path: '/blog', root: '/data/blog' }] },
  root: { path: '', user: 'www', group: 'www', symlink: false },
  access: { enabled: false, allow: [], deny: [], auth_enabled: false, auth_user: '', auth_pass: '', allow_methods: [] },
  traffic: { bandwidth_limit: '', monthly_quota_mb: 0, enforce_quota: false, rate_limit_per_ip: 0 },
  rewrite: { enabled: false, rules: '', file: '' },
  index: { files: ['index.php', 'index.html', 'index.htm'] },
  raw_conf: '',
  // v0.4.0: 站点级「自定义 Nginx 配置」。extra_conf 是片段，extra_conf_pos
  // 决定注入位置（inside=server 块内 / outside=server 块外）。raw_conf 保留作
  // 老库兼容：载入时若 extra_conf 为空则把 raw_conf 搬过来（见 mergeDefaults）。
  extra_conf: '',
  extra_conf_pos: 'inside',
  ssl: { enabled: false, cert_file: '', key_file: '', force_https: false, hsts: false, hsts_max_age: 31536000, protocols: ['TLSv1.2', 'TLSv1.3'], ocsp: false, use_http2: true },
  php: { enabled: true, version: '8.2', socket: '', max_execution_time: 30, memory_limit: '256M', disable_functions: [], custom_php_config: '' },
  redirect: { rules: [] },
  proxy: { enabled: false, rules: [] },
  hotlink: { enabled: false, allowed_domains: [], file_extensions: ['jpg', 'png', 'zip'], return_code: 403 },
  lock: { enabled: false, files: [], auto_restore: false, alert_on_tamper: true },
  security: { block_sqli: false, block_xss: false, block_traversal: false, block_ua: false, block_ua_keywords: [], deny_paths: [], deny_extensions: [], server_tokens_off: true, prevent_iframe: false },
  logs: { access_log: true, error_log: true, custom_format: '', retention_days: 30, disable_log: false },
  alert: { enabled: true, rules: [], channels: [] },
  extra: { gzip: true, gzip_type: '', gzip_min_length: 1024, client_max_body_size: '', keepalive_timeout: 0, send_timeout: 0, read_timeout: 0, dns_resolver: '', browser_cache: 0, static_cache_ext: ['jpg', 'png', 'css', 'js'], custom_404: '' },
  edate: null,
  // v0.2.1: 对外监听端口。空数组 = 默认 [80]。全局 -public-port 模式时由后端覆盖。
  listen_ports: [80],
  // cert 分组：仅在服务端确实返回了绑定时才合并（见 mergeDefaults 尾部）。
  // 这里保留完整形状只为文档用途，不是"缺失时的回填值"。
  cert: { cert_id: '', domains: [], not_before: '', not_after: '', issuer: '', source: '' },
};

const HTTP_METHODS = ['GET', 'POST', 'PUT', 'DELETE', 'HEAD', 'OPTIONS', 'PATCH'];
const TLS_PROTOCOLS = ['TLSv1.2', 'TLSv1.3'];

// ALERT_TYPES 站点告警规则类型。
//
// 权威清单在后端（GET /api/alert-types，定义在 internal/model，
// 与 internal/alert 的检查器一一对应），前端启动时拉取，
// 保证下拉框里出现的每一种类型后端都真的能检查。
//
// 这里保留一份兜底常量，接口不可达时下拉框仍要能渲染；
// 但它必须与后端一致——历史上前端硬编码的清单写的是
// status_4xx / cert_expire / file_tamper / response_time，
// 其中四种后端根本不认识：规则保存时不报错，却永远不触发；
// 而后端支持的 nginx_config 前端压根选不到。
const ALERT_TYPES_FALLBACK = [
  { value: 'status_5xx', label: '5xx 错误数' },
  { value: 'traffic_quota', label: '流量超额' },
  { value: 'cert_expiry', label: '证书临期' },
  { value: 'site_down', label: '站点不可访问' },
  { value: 'tamper', label: '文件被篡改' },
  { value: 'nginx_config', label: 'nginx 配置异常' },
];
let ALERT_TYPES = ALERT_TYPES_FALLBACK;

// loadAlertTypes 拉取后端权威的告警类型清单。
// 失败时保留兜底清单（不让整个详情页因为一个枚举接口挂掉）。
export async function loadAlertTypes() {
  try {
    const r = await api.alertTypes();
    if (r && Array.isArray(r.types) && r.types.length) {
      ALERT_TYPES = r.types.map((t) => ({
        value: t.value,
        label: t.label,
        description: t.description || '',
      }));
    }
  } catch (e) {
    // 用兜底清单
  }
}
const ALERT_LEVELS = [
  { value: 'info', label: '提示' },
  { value: 'warning', label: '警告' },
  { value: 'critical', label: '严重' },
];
const CHANNEL_TYPES = [
  { value: 'email', label: '邮件', placeholder: 'ops@example.com' },
  { value: 'webhook', label: 'Webhook', placeholder: 'https://example.com/hook' },
  { value: 'dingtalk', label: '钉钉机器人', placeholder: 'https://oapi.dingtalk.com/robot/send?access_token=xxx' },
  { value: 'wecom', label: '企业微信机器人', placeholder: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx' },
];

// 站点类型 → 默认 client_max_body_size。
// 反代站点更偏向快速响应（上传走代理，8m 够用），PHP 站点要容纳表单/附件上传，
// 静态站点居中。用户仍可在「其他设置」里覆盖。
function defaultBodySize(site) {
  if (site.proxy && site.proxy.enabled) return '8m';
  if (site.php && site.php.enabled) return '32m';
  return '16m';
}

// 深合并默认值，保证嵌套结构完整
function mergeDefaults(site) {
  const out = { ...site };
  for (const [key, def] of Object.entries(DEFAULTS)) {
    // cert 在循环后单独处理：它的时间字段不能填默认空串（见下方注释）。
    if (key === 'cert') continue;
    if (def && typeof def === 'object' && !Array.isArray(def)) {
      out[key] = { ...def, ...(site[key] || {}) };
    } else if (Array.isArray(def)) {
      // 数组类（含 listen_ports）：服务端返回空数组时仍要回填默认，
      // 避免 UI 出现"空输入框"误导用户
      if (!Array.isArray(out[key]) || out[key].length === 0) {
        out[key] = def.slice();
      }
    } else if (out[key] === undefined || out[key] === null) {
      out[key] = def;
    }
  }
  // 数组类分组内部规则数组也补默认值
  if (!Array.isArray(out.redirect.rules)) out.redirect.rules = [];
  if (!Array.isArray(out.proxy.rules)) out.proxy.rules = [];
  if (!Array.isArray(out.alert.rules)) out.alert.rules = [];
  if (!Array.isArray(out.alert.channels)) out.alert.channels = [];
  if (!Array.isArray(out.sub_dir.rules)) out.sub_dir.rules = [];

  // cert 是可选分组，单独处理，不走上面的通用合并。
  //
  // 它的 not_before / not_after 在后端是 time.Time：空字符串无法解码，
  // 整个 PUT 会在 bindSite 阶段直接 400。站点从未绑定过证书时，服务端
  // 不返回 cert 分组——这里必须保持"没有"。曾经用默认对象
  // {not_before:"",not_after:""} 回填，导致这类站点在详情页永远无法
  // 保存：改任何字段都是 400，且与所改字段无关（2026-11-01 线上故障，
  // 站点连续 9 次 PUT 400，根因即此）。
  //
  // 服务端返回了 cert 才合并（真实 RFC3339 时间字符串可正常解码）；
  // 否则置 null——界面各处对 site.cert 均为判空安全
  // （tabSsl 的 site.cert && site.cert.cert_id、certDaysLeft 的判空、
  // 以及"解除绑定"流程本身的 site.cert = null）。
  out.cert = site.cert && typeof site.cert === 'object'
    ? { ...DEFAULTS.cert, ...site.cert }
    : null;

  // 自定义 Nginx 配置迁移：早期 raw_conf 只支持 server 块内固定末尾，
  // 迁移到新的 extra_conf（默认 inside）以便位置可配。搬运后清空 raw_conf，
  // 避免两字段重复渲染（后端在 extra_conf 非空时不再回退 raw_conf）。
  if ((!out.extra_conf || !String(out.extra_conf).trim()) && out.raw_conf && String(out.raw_conf).trim()) {
    out.extra_conf = out.raw_conf;
    out.raw_conf = '';
  }
  if (out.extra_conf_pos !== 'outside' && out.extra_conf_pos !== 'inside') {
    out.extra_conf_pos = 'inside';
  }
  // client_max_body_size 为空时按站点类型给默认值（P1）。
  if (!out.extra.client_max_body_size) {
    out.extra.client_max_body_size = defaultBodySize(out);
  }

  return out;
}

const clone = (o) => JSON.parse(JSON.stringify(o));

// 证书来源中文显示
function sourceText(src) {
  if (src === 'acme') return 'ACME 签发';
  if (src === 'import') return '手动导入';
  return src || '-';
}

export async function render(container, params) {
  clear(container);
  container.appendChild(loading('正在加载站点配置…'));

  // 告警类型清单从后端拉（权威来源，与检查器一一对应）。
  // 失败时用兜底常量，不让详情页因为一个枚举接口挂掉。
  await loadAlertTypes();

  let site, phpVersions = ['8.2', '8.1', '8.0', '7.4', '5.6'];
  try {
    const [siteRes, statusRes] = await Promise.all([
      api.sites.get(params.id),
      api.system.status().catch(() => null),
    ]);
    site = mergeDefaults(siteRes);
    const v = statusRes && statusRes.php && statusRes.php.versions;
    if (Array.isArray(v) && v.length) phpVersions = v.map((x) => String(x));
    if (site.php.version && !phpVersions.includes(site.php.version)) {
      phpVersions = [site.php.version, ...phpVersions];
    }
  } catch (err) {
    clear(container).appendChild(errorState(`站点加载失败：${err.message || err}`, () => ctx.navigate('#/sites')));
    return;
  }

  // 保存快照用于脏检查
  let snapshot = JSON.stringify(site);
  const isDirty = () => JSON.stringify(site) !== snapshot;
  const markDirty = () => store.markDirty(isDirty());

  clear(container);

  // ---------------- 顶部操作条 ----------------
  const statusTag = site.status === 'running' ? tag('运行中', 'success') : tag('已停止', 'default');
  const certDays = certDaysLeft(site.cert);
  const saveBtn = button('保存修改', doSave, 'primary', 'save');

  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('div', { class: 'crumb' }, [
        h('a', { href: '#/sites', class: 'crumb-link' }, '网站'),
        h('span', { class: 'crumb-sep' }, '/'),
        h('span', {}, site.name || site.id),
      ]),
      h('div', { class: 'page-title-row' }, [
        h('h1', { class: 'page-title' }, site.name || site.id),
        statusTag,
        site.ssl.enabled ? tag('SSL', 'success') : null,
        site.php.enabled ? tag(`PHP ${site.php.version}`, 'info') : null,
        site.ssl.enabled && certDays !== null ? certDaysBadge(certDays) : null,
      ]),
      h('div', { class: 'page-sub' }, [
        `域名：${(site.domains || []).join(', ') || '-'}`,
        site.root && site.root.path ? h('span', { class: 'mono-text' }, `　根目录：${site.root.path}`) : null,
        `　创建：${formatDateTime(site.created_at)}`,
      ]),
    ]),
    h('div', { class: 'page-actions' }, [
      button('查看生成的 Nginx 配置', viewConfig, 'default', 'eye'),
      // 删除按钮：从详情页直达，不要让用户回列表找小垃圾桶图标。
      // 详情页通常已经在反复编辑，"删"是同等重要的动作，放主操作区。
      button('删除站点', doDeleteSite, 'danger-ghost', 'trash'),
      h('span', { class: 'save-hint', id: 'dirtyHint' }, ''),
      saveBtn,
    ]),
  ]));

  // ---------------- 标签页 ----------------
  const tabs = [
    { key: 'basic', label: '基本信息', render: tabBasic },
    { key: 'files', label: '文件管理', render: tabFiles },
    { key: 'subdir', label: '子目录绑定', render: tabSubDir },
    { key: 'access', label: '访问限制', render: tabAccess },
    { key: 'traffic', label: '流量限制', render: tabTraffic },
    { key: 'rewrite', label: '伪静态', render: tabRewrite },
    { key: 'index', label: '默认文档', render: tabIndex },
    { key: 'raw', label: '配置文件', render: tabRaw },
    { key: 'ssl', label: 'SSL', render: tabSsl },
    { key: 'php', label: 'PHP', render: tabPhp },
    { key: 'mysql', label: '数据库', render: tabMysql },
    { key: 'redirect', label: '重定向', render: tabRedirect },
    { key: 'proxy', label: '反向代理', render: tabProxy },
    { key: 'hotlink', label: '防盗链', render: tabHotlink },
    { key: 'lock', label: '防复改', render: tabLock },
    { key: 'security', label: '网站安全', render: tabSecurity },
    { key: 'logs', label: '网站日志', render: tabLogs },
    { key: 'alert', label: '网站告警', render: tabAlert },
    { key: 'extra', label: '其他设置', render: tabExtra },
  ];

  const layout = tabPanel(tabs, {
    actions: [h('span', { class: 'tab-count' }, `${tabs.length} 项功能`)],
  });
  container.appendChild(layout);

  // -------------------------------------------------------------------------
  // 各标签页内容
  // -------------------------------------------------------------------------

  // 0. 文件管理：嵌入文件浏览器（每次切到该 tab 都重置到根目录）
  function tabFiles() {
    const wrap = h('div', {});
    // 立即挂载（render 是同步的，不能 setTimeout 0 后再 mount，否则 tab 切换
    // 第一次会闪空）。mountFilesView 自己 clear wrap。
    mountFilesView(wrap, { siteId: site.id, rootPath: site.root && site.root.path });
    return wrap;
  }

  // 1. 基本信息：名称 / 状态 / 域名 / 根目录 / 备注
  function tabBasic() {
    return h('div', {}, [
      grid(2,
        field('站点名称', textInput(site.name, (v) => { site.name = v; markDirty(); }, { placeholder: '用于面板内标识，如：官网' })),
        field('运行状态', h('div', { class: 'inline-switch' }, [
          switchControl(site.status === 'running', (on) => { site.status = on ? 'running' : 'stopped'; markDirty(); }),
          h('span', {}, site.status === 'running' ? '运行中' : '已停止'),
        ]), '状态变更需保存后生效'),
      ),
      sectionTitle('域名'),
      stringListEditor(site.domains, (v) => { site.domains = v; markDirty(); }, { hint: '一行一个域名，第一行为主域名（每行一个，最多 200 字符）', rows: 4 }),
      sectionTitle('根目录与运行身份'),
      grid(3,
        field('网站根目录', textInput(site.root.path, (v) => { site.root.path = v; markDirty(); }, { placeholder: '/www/wwwroot/example.com' })),
        field('运行用户', textInput(site.root.user, (v) => { site.root.user = v; markDirty(); }, { placeholder: 'www' })),
        field('运行用户组', textInput(site.root.group, (v) => { site.root.group = v; markDirty(); }, { placeholder: 'www' })),
      ),
      // v0.2.0: 运行目录（相对 root.path），Laravel/ThinkPHP 用 public/ 风格时必填
      field('运行目录（run_path）',
        textInput(site.root.run_path || '', (v) => { site.root.run_path = v.trim(); markDirty(); },
          { placeholder: '例如：public（留空 = 与根目录相同）' }),
        'PHP SCRIPT_FILENAME 拼成 $document_root/<run_path>/$script_name'),
      switchField('允许符号链接（symlink）', site.root.symlink, (v) => { site.root.symlink = v; markDirty(); }, '关闭后 nginx 会拒绝跟随指向站外的软链接'),
      // v0.2.0: 自定义 404 页面
      field('自定义 404 页面',
        textInput(site.extra.custom_404 || '', (v) => { site.extra.custom_404 = v.trim(); markDirty(); },
          { placeholder: '404.html 或 errors/404.php（留空 = nginx 默认）' }),
        '相对站点根目录的路径，渲染为 error_page 404 指令'),
      // v0.2.1: 对外监听端口
      field('对外监听端口',
        textInput((site.listen_ports || [80]).join(','), (v) => {
          site.listen_ports = v.split(/[,，\s]+/).map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n) && n > 0);
          markDirty();
        }, { placeholder: '80, 8080（逗号分隔）' }),
        '默认 80；可填多个。SSL 自动追加 443。同一端口只能给一个站点。'),
      // v0.2.0: 到期时间
      field('站点到期时间',
        textInput(site.edate || '', (v) => { site.edate = v || null; markDirty(); },
          { placeholder: 'YYYY-MM-DD（留空 = 无到期）' }),
        '列表里未来 7 天内标黄，过期标红'),
      sectionTitle('备注'),
      field('站点备注', textArea(site.remark, (v) => { site.remark = v; markDirty(); }, { rows: 3, placeholder: '内部备注，不影响配置' }), null, true),
    ]);
  }

  // 2. 子目录绑定
  function tabSubDir() {
    return h('div', {}, [
      switchField('启用子目录绑定', site.sub_dir.enabled, (v) => { site.sub_dir.enabled = v; markDirty(); },
        '将站点的某个 URL 路径映射到独立的磁盘目录'),
      sectionTitle('路径映射规则'),
      rowEditor({
        items: site.sub_dir.rules,
        columns: [
          { key: 'path', label: '访问路径', width: '30%', placeholder: '/blog' },
          { key: 'root', label: '磁盘目录', placeholder: '/data/blog' },
        ],
        addText: '添加路径映射',
        emptyText: '暂无路径映射规则',
        onChange: markDirty,
      }),
      h('div', { class: 'field-hint' }, '示例：访问 https://example.com/blog 将读取 /data/blog 下的文件'),
    ]);
  }

  // 3. 访问限制
  function tabAccess() {
    const a = site.access;
    return h('div', {}, [
      switchField('启用访问限制', a.enabled, (v) => { a.enabled = v; markDirty(); }, '按 IP 与访问方式控制谁能访问该站点'),
      sectionTitle('IP 白名单 / 黑名单'),
      grid(2,
        field('允许的 IP / CIDR', stringListEditor(a.allow, (v) => { a.allow = v; markDirty(); }, { hint: '一行一个，留空表示不限制', rows: 4 })),
        field('禁止的 IP / CIDR', stringListEditor(a.deny, (v) => { a.deny = v; markDirty(); }, { hint: '黑名单优先级高于白名单', rows: 4 })),
      ),
      sectionTitle('访问认证'),
      switchField('启用访问认证（Basic Auth）', a.auth_enabled, (v) => { a.auth_enabled = v; markDirty(); }, '开启后未认证访问将弹出浏览器登录框'),
      grid(2,
        field('认证用户名', textInput(a.auth_user, (v) => { a.auth_user = v; markDirty(); }, { placeholder: 'admin' })),
        // 密码是**只写**字段：后端不会回显已设置的密码，
        // 所以这里永远是空的——留空 = 不修改，填了 = 改成新密码。
        field('认证密码', textInput(a.auth_pass, (v) => { a.auth_pass = v; markDirty(); }, { type: 'password', placeholder: '设置访问密码' }),
          '留空表示不修改已设置的密码；密码只写不读，保存后不再回显'),
      ),
      sectionTitle('允许的请求方法'),
      pillMultiSelect(a.allow_methods, HTTP_METHODS, (v) => { a.allow_methods = v; markDirty(); }),
      h('div', { class: 'field-hint' }, '未勾选任何项表示不限制；注意保留 GET / HEAD，否则页面无法访问'),
    ]);
  }

  // 4. 流量限制
  function tabTraffic() {
    const t = site.traffic;
    return h('div', {}, [
      grid(3,
        field('带宽限速', textInput(t.bandwidth_limit, (v) => { t.bandwidth_limit = v; markDirty(); }, { placeholder: '如 512k / 2m，留空不限制' }), 'nginx limit_rate 语法'),
        field('每月流量配额（MB）', numberInput(t.monthly_quota_mb, (v) => { t.monthly_quota_mb = v; markDirty(); }, { min: 0, placeholder: '0 表示不限' })),
        field('单 IP 限速（次/秒）', numberInput(t.rate_limit_per_ip, (v) => { t.rate_limit_per_ip = v; markDirty(); }, { min: 0, placeholder: '0 表示不限' })),
      ),
      switchField('超额后强制执行', t.enforce_quota, (v) => { t.enforce_quota = v; markDirty(); }, '开启后流量达到配额即拦截请求'),
    ]);
  }

  // 5. 伪静态
  function tabRewrite() {
    const rw = site.rewrite;
    return h('div', {}, [
      switchField('启用伪静态规则', rw.enabled, (v) => { rw.enabled = v; markDirty(); }),
      field('规则内容', textArea(rw.rules, (v) => { rw.rules = v; markDirty(); }, { rows: 12, mono: true, placeholder: 'location / {\n    try_files $uri $uri/ /index.php?$query_string;\n}' }), '等价的 nginx location / rewrite 片段', true),
      field('规则文件路径', textInput(rw.file, (v) => { rw.file = v; markDirty(); }, { placeholder: '/www/server/panel/vhost/rewrite/example.com.conf' }), '填写后优先从该文件读取规则'),
    ]);
  }

  // 6. 默认文档
  function tabIndex() {
    return h('div', {}, [
      field('默认文档列表', stringListEditor(site.index.files, (v) => { site.index.files = v; markDirty(); }, { hint: '自上而下匹配，一行一个文件名', rows: 5 })),
      h('div', { class: 'field-hint' }, '常见配置：index.php index.html index.htm'),
    ]);
  }

  // 7. 配置文件（自定义 Nginx 配置，默认折叠）
  function tabRaw() {
    const form = h('div', {}, [
      field('注入位置',
        selectInput(site.extra_conf_pos || 'inside', [
          { value: 'inside', label: 'server 块内（可覆盖面板自带的 IP 白名单/限速）' },
          { value: 'outside', label: 'server 块外（文件末尾，给 upstream / limit_req_zone 等 http 指令）' },
        ], (v) => { site.extra_conf_pos = v; markDirty(); }),
        'inside 插在 ssl_session_timeout 之后、/.well-known 之前；outside 插在 server 块之后'),
      field('自定义 nginx 配置片段',
        textArea(site.extra_conf, (v) => { site.extra_conf = v; markDirty(); },
          { rows: 16, mono: true, placeholder: '例如：\nlocation ^~ /x { return 403; }\n\n# outside 位置可放 http 上下文指令：\nlimit_req_zone $binary_remote_addr zone=api:10m rate=10r/s;' }),
        '内容原样注入；保存前会先做 nginx -t 校验，失败自动回滚并回显原始报错，不会落库', true),
    ]);

    return h('div', {}, [
      h('div', { class: 'warn-box' }, [
        icon('warning'),
        h('span', {}, '此处内容原样注入该站点的 nginx 配置，语法错误会导致 nginx 无法启动。保存时先校验，失败即回滚并显示 nginx 原始报错。'),
      ]),
      h('details', { class: 'collapse' }, [
        h('summary', { class: 'collapse-head' }, [
          sectionTitle('自定义 Nginx 配置'),
          h('span', { class: 'field-hint' }, '点击展开 / 折叠'),
        ]),
        form,
      ]),
      h('div', { class: 'inline-actions' }, [
        button('查看生成的 Nginx 配置', viewConfig, 'default', 'eye'),
        h('span', { class: 'field-hint' }, '预览的是后端已保存的配置，保存后生效'),
      ]),
    ]);
  }

  // 8. SSL
  function tabSsl() {
    const s = site.ssl;
    const certBox = h('div', {});
    const certId = site.cert && site.cert.cert_id;

    // 当前证书区：site.cert 只是内嵌快照，这里额外拉证书库记录，
    // 展示完整状态（自动续签、验证方式、DNS 提供商、文件是否就绪）。
    if (certId) {
      certBox.appendChild(loading('加载证书信息...'));
      api.certs.list()
        .then((res) => {
          const c = (res.certs || []).find((x) => x.id === certId);
          clear(certBox);
          if (!c) {
            certBox.appendChild(h('div', { class: 'field-hint' },
              '证书记录已不存在（可能已被删除），请重新绑定或关闭 SSL'));
            return;
          }
          certBox.appendChild(h('div', { class: 'cert-box' }, [
            h('div', { class: 'cert-box-row' }, [h('span', {}, '证书名称'), h('span', {}, c.name || '-')]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '证书域名'), h('span', {}, (c.domains || []).join(', '))]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '签发者'), h('span', {}, c.issuer || '-')]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '证书来源'), h('span', {}, sourceText(c.source))]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '证书 ID'), h('span', { class: 'mono-text' }, c.id)]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '有效期'), h('span', {}, `${formatDateTime(c.not_before)} ~ ${formatDateTime(c.not_after)}`)]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '剩余天数'), certDaysBadge(certDaysLeft(c))]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '自动续签'), c.auto_renew ? tag('已开启', 'success') : tag('未开启', 'default')]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '验证方式'),
              c.method === 'dns'
                ? tag(`DNS-01（${c.dns_provider || '-'}）`, 'info')
                : tag('HTTP-01', 'default')]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '证书文件'), h('span', { class: 'mono-text ellipsis', title: c.cert_file || '' }, c.cert_file || '-')]),
            h('div', { class: 'cert-box-row' }, [h('span', {}, '私钥文件'), h('span', { class: 'mono-text ellipsis', title: c.key_file || '' }, c.key_file || '-')]),
            h('div', { class: 'inline-actions' }, [
              button('前往证书管理', () => ctx.navigate('#/certs'), 'ghost'),
              button('解除绑定', async () => {
                const ok = await confirmDialog({
                  title: '解除证书绑定',
                  message: '解除后站点将不再使用该证书，HTTPS 可能无法访问。确定继续？',
                  okText: '解除绑定',
                  danger: true,
                });
                if (!ok) return;
                site.cert = null;
                s.enabled = false;
                markDirty();
                await doSave();
                render(container, params);
              }, 'danger-ghost', 'unlink'),
            ]),
          ]));
        })
        .catch((e) => {
          clear(certBox);
          certBox.appendChild(errorState(`证书信息加载失败：${e.message || e}`, () => render(container, params)));
        });
    } else {
      certBox.appendChild(emptyState(
        '当前站点未绑定证书',
        h('div', { class: 'inline-actions' }, [
          button('去申请证书', () => ctx.navigate('#/certs'), 'primary', 'key'),
          button('从证书库选择', showBindCert, 'default', 'cert'),
        ]),
        'cert'
      ));
    }

    // 从证书库选一张已有证书绑定到本站
    async function showBindCert() {
      let certs = [];
      try {
        const res = await api.certs.list();
        certs = res.certs || [];
      } catch (e) {
        toast(e.message || '证书列表加载失败', 'error');
        return;
      }
      if (!certs.length) {
        toast('证书库为空，请先申请或导入证书', 'error');
        return;
      }
      const picker = h('div', { class: 'pick-list' });
      const draw = () => {
        clear(picker);
        for (const c of certs) {
          picker.appendChild(h('label', { class: 'pick-item' }, [
            h('input', {
              type: 'radio', name: 'bind-cert', value: c.id,
              onchange: () => { selected = c.id; },
            }),
            h('div', { class: 'pick-body' }, [
              h('div', { class: 'pick-title' }, [h('span', {}, c.name), certDaysBadge(certDaysLeft(c))]),
              h('div', { class: 'pick-sub' }, (c.domains || []).join(', ')),
            ]),
          ]));
        }
      };
      let selected = certs[0].id;
      draw();

      const m = modal({
        title: '选择要绑定的证书',
        width: '560px',
        content: picker,
        footer: [
          button('取消', () => m.close(), 'default'),
          button('绑定', async () => {
            try {
              await api.certs.bind(selected, site.id);
              toast('证书已绑定', 'success');
              m.close();
              render(container, params);
            } catch (e) {
              toast(e.message || '绑定失败', 'error');
            }
          }, 'primary'),
        ],
      });
    }

    return h('div', {}, [
      switchField('启用 SSL', s.enabled, (v) => { s.enabled = v; markDirty(); }),
      grid(2,
        field('证书文件路径', textInput(s.cert_file, (v) => { s.cert_file = v; markDirty(); }, { placeholder: '/www/server/panel/vhost/cert/example.com/fullchain.pem' })),
        field('私钥文件路径', textInput(s.key_file, (v) => { s.key_file = v; markDirty(); }, { placeholder: '/www/server/panel/vhost/cert/example.com/privkey.pem' })),
      ),
      grid(2,
        switchField('强制 HTTPS 跳转', s.force_https, (v) => { s.force_https = v; markDirty(); }, 'HTTP 访问自动 301 到 HTTPS'),
        switchField('启用 HTTP/2', s.use_http2, (v) => { s.use_http2 = v; markDirty(); }),
      ),
      switchField('启用 HSTS 强制加密传输安全', s.hsts, (v) => { s.hsts = v; markDirty(); }),
      grid(2,
        field('HSTS max-age（秒）', numberInput(s.hsts_max_age, (v) => { s.hsts_max_age = v; markDirty(); }, { min: 0, step: 86400 }), '默认 31536000（1 年）'),
        switchField('启用 OCSP 证书 stapling', s.ocsp, (v) => { s.ocsp = v; markDirty(); }),
      ),
      sectionTitle('允许的 TLS 协议版本'),
      pillMultiSelect(s.protocols, TLS_PROTOCOLS, (v) => { s.protocols = v; markDirty(); }),
      sectionTitle('当前证书'),
      certBox,
    ]);
  }

  // 9. PHP
  function tabPhp() {
    const p = site.php;
    return h('div', {}, [
      switchField('启用 PHP', p.enabled, (v) => { p.enabled = v; markDirty(); }),
      grid(3,
        field('PHP 版本', selectInput(p.version, phpVersions, (v) => { p.version = v; markDirty(); })),
        field('PHP-FPM Socket', textInput(p.socket, (v) => { p.socket = v; markDirty(); }, { placeholder: '/tmp/php-cgi-82.sock' }), '留空则由面板按版本自动匹配'),
        field('最大执行时间（秒）', numberInput(p.max_execution_time, (v) => { p.max_execution_time = v; markDirty(); }, { min: 0 })),
      ),
      grid(2,
        field('内存限制', textInput(p.memory_limit, (v) => { p.memory_limit = v; markDirty(); }, { placeholder: '256M' })),
        field('禁用函数', stringListEditor(p.disable_functions, (v) => { p.disable_functions = v; markDirty(); }, { hint: '一行一个函数名，如 exec', rows: 4 })),
      ),
      field('自定义 php.ini 配置', textArea(p.custom_php_config, (v) => { p.custom_php_config = v; markDirty(); }, { rows: 8, mono: true, placeholder: 'upload_max_filesize = 50M\npost_max_size = 50M' }), null, true),
    ]);
  }

  // MySQL 标签页：展示数据库连接信息。
  // 设计取舍：
  //   - 密码不回显（接口已经脱敏为 ""）：用户创建时拿过一次，重置要走单独流程
  //   - 写字段不开放给用户：site.Mysql.Name/User/Host/Charset 由面板管控
  //     （db 名用 siteID 推导，改了会让"重置"逻辑不一致）
  //   - 删库按钮是"高危"操作：弹二次确认
  function tabMysql() {
    const m = site.mysql;
    if (!m) {
      return h('div', {}, [
        emptyState(
          '该站点未绑定数据库',
          button('从系统设置中启用 MySQL', () => ctx.navigate('#/settings'), 'default', 'plus'),
          'db',
        ),
        h('div', { class: 'field-hint' }, 'MySQL 子系统需在部署时启用（deploy/install.sh 的 INSTALL_MYSQL=1），且当前面板已配置 root 凭据。'),
      ]);
    }
    // 注意：m.password 在 GET 路径上已经是 ""，由 api.go SanitizeForRead 脱敏。
    async function resetDBPassword() {
      const ok = await confirmDialog({
        title: '重置数据库密码',
        message: `将重置 ${m.name} 对应用户的密码，旧密码立即失效。使用该库的应用需要同步更新配置。`,
        okText: '重置密码',
        danger: true,
      });
      if (!ok) return;
      try {
        const res = await api.databases.resetPassword(m.name);
        showDatabasePassword(m.name, res && res.password);
      } catch (e) {
        toast('重置失败：' + (e.message || e), 'error', 5000);
      }
    }
    return h('div', {}, [
      sectionTitle('数据库连接信息'),
      grid(2,
        field('数据库名', textInput(m.name, () => {}, { readonly: true })),
        field('用户', textInput(m.user, () => {}, { readonly: true })),
      ),
      grid(3,
        field('主机', textInput(m.host || 'localhost', () => {}, { readonly: true })),
        field('端口', textInput(String(m.port || 3306), () => {}, { readonly: true })),
        field('字符集', textInput(m.charset || 'utf8mb4', () => {}, { readonly: true })),
      ),
      h('div', { class: 'field-hint' },
        '密码不在此显示：创建时一次性明文已展示在弹窗中。如丢失，请重置。' +
        '删站点会联动 drop database + drop user。'),
      sectionTitle('管理与备份'),
      h('div', { class: 'inline-actions' }, [
        button('打开数据库中心', () => ctx.navigate('#/databases'), 'default', 'database'),
        button('重置密码', resetDBPassword, 'default', 'key'),
        button('导出 .sql.gz', () => {
          window.location.href = api.databases.exportURL(m.name);
          toast('正在生成备份，稍后浏览器会开始下载', 'info', 3200);
        }, 'ghost', 'download'),
      ]),
      sectionTitle('高危操作'),
      button('删除该数据库（不可恢复）', async () => {
        const yes = await confirmDialog({
          title: '确认删除数据库',
          message: `真的要删 ${m.name} 吗？该操作会 drop database 和 drop user，且不可恢复。`,
          okText: '删除',
          danger: true,
        });
        if (!yes) return;
        try {
          // 复用站点级删除：DELETE /api/sites/{id} 会联动删库。
          // 走"删站点"路径而不是单独的"解绑 db"，避免出现"数据库
          // 不在了但站点还引用它"这种半残状态。删除前会做整站确认。
          await doDeleteSite();
        } catch (e) {
          toast('操作失败：' + (e.message || e), 'error');
        }
      }, 'danger'),
    ]);
  }

  // 10. 重定向
  function tabRedirect() {
    return h('div', {}, [
      sectionTitle('重定向规则'),
      rowEditor({
        items: site.redirect.rules,
        columns: [
          { key: 'from', label: '来源', width: '32%', placeholder: '/old-path 或 https://a.com/x' },
          { key: 'to', label: '目标', width: '32%', placeholder: '/new-path' },
          { key: 'status', label: '状态码', width: '140px', type: 'select', options: [301, 302, 303, 307, 308] },
          { key: 'enable', label: '启用', width: '90px', type: 'checkbox' },
        ],
        addText: '添加重定向规则',
        emptyText: '暂无重定向规则',
        onChange: markDirty,
      }),
    ]);
  }

  // 11. 反向代理
  function tabProxy() {
    return h('div', {}, [
      switchField('启用反向代理', site.proxy.enabled, (v) => { site.proxy.enabled = v; markDirty(); }),
      sectionTitle('代理规则'),
      rowEditor({
        items: site.proxy.rules,
        columns: [
          { key: 'path', label: '路径', width: '18%', placeholder: '/api' },
          { key: 'target', label: '目标地址', width: '26%', placeholder: 'http://127.0.0.1:3000' },
          { key: 'websocket', label: 'WS', width: '80px', type: 'checkbox' },
          { key: 'enabled', label: '启用', width: '80px', type: 'checkbox' },
          { key: 'sni', label: 'SNI', width: '80px', type: 'checkbox' },
          { key: 'timeout', label: '超时(s)', width: '110px', type: 'number', min: 0 },
          { key: 'cache_enable', label: '缓存', width: '80px', type: 'checkbox' },
          { key: 'cache_time', label: '缓存(s)', width: '110px', type: 'number', min: 0 },
        ],
        addText: '添加代理规则',
        emptyText: '暂无代理规则',
        onChange: markDirty,
      }),
      h('div', { class: 'field-hint' }, '勾选 WS 表示透传 Upgrade 头；勾选缓存后可设置缓存时长（秒）'),
    ]);
  }

  // 12. 防盗链
  function tabHotlink() {
    const hl = site.hotlink;
    return h('div', {}, [
      switchField('启用防盗链', hl.enabled, (v) => { hl.enabled = v; markDirty(); }, '仅对静态资源生效'),
      grid(2,
        field('允许引用的域名', stringListEditor(hl.allowed_domains, (v) => { hl.allowed_domains = v; markDirty(); }, { hint: '一行一个域名，留空表示不校验', rows: 5 })),
        field('防盗链文件后缀', stringListEditor(hl.file_extensions, (v) => { hl.file_extensions = v; markDirty(); }, { hint: '一行一个后缀，不带点，如 jpg', rows: 5 })),
      ),
      field('触发时返回状态码', selectInput(hl.return_code, [403, 302, 404], (v) => { hl.return_code = Number(v); markDirty(); })),
    ]);
  }

  // 13. 防复改
  function tabLock() {
    const l = site.lock;
    return h('div', {}, [
      switchField('启用防篡改（文件锁定）', l.enabled, (v) => { l.enabled = v; markDirty(); }, '对指定文件做哈希校验，防止被篡改'),
      field('受保护文件列表', stringListEditor(l.files, (v) => { l.files = v; markDirty(); }, { hint: '一行一个文件路径，相对站点根目录，如 index.php', rows: 5 })),
      switchField('检测到篡改后自动还原', l.auto_restore, (v) => { l.auto_restore = v; markDirty(); }),
      switchField('检测到篡改时触发告警', l.alert_on_tamper, (v) => { l.alert_on_tamper = v; markDirty(); }),
      field('基线与检测', h('div', { class: 'inline-actions' }, [
        button('建立防篡改基线', doEstablishBaseline, 'default', 'save'),
        button('立即检测篡改', doCheckTamper, 'default', 'search'),
      ]), '建立基线＝把当前受保护文件的哈希记为"正常状态"；检测＝与基线比对并记录事件、触发告警。基线建立后请先保存修改。'),
    ]);
  }

  // 建立防篡改基线。
  //
  // 后端 EstablishBaseline 会把受保护文件的当前哈希写进 integrity 表，
  // 之后每次检测都以它为基准。所以顺序是：先保存配置（让受保护文件列表生效）
  // → 再建立基线 → 基线之后才能检测出真实的改动。
  async function doEstablishBaseline() {
    if (isDirty()) {
      toast('请先保存修改，让受保护文件列表生效后再建立基线', 'error');
      return;
    }
    if (!site.lock || !site.lock.enabled) {
      toast('请先启用「防篡改（文件锁定）」并保存', 'error');
      return;
    }
    if (!site.lock.files || !site.lock.files.length) {
      toast('受保护文件列表为空，无法建立基线', 'error');
      return;
    }
    const ok = await confirmDialog({
      title: '建立防篡改基线',
      message:
        '将把以下受保护文件的当前哈希记为基线：\n' +
        site.lock.files.join('\n') +
        '\n\n基线之后，任何内容变化都会被判定为篡改。\n' +
        '（请确认这些文件当前是干净的，否则被改过的内容也会被记成基线。）',
      okText: '建立基线',
    });
    if (!ok) return;
    try {
      const res = await api.sites.establishBaseline(site.id);
      toast(`基线已建立：${res.files} 个文件`, 'success');
    } catch (e) {
      toast(e.message || '建立基线失败', 'error');
    }
  }

  // 立即检测篡改。
  //
  // 注意：检测本身会写 tamper_events 事件表，并按站点配置触发告警通知，
  // 所以后端强制 POST（GET 会被 405 拒绝）。
  async function doCheckTamper() {
    if (!site.lock || !site.lock.enabled) {
      toast('该站点未启用防篡改', 'error');
      return;
    }
    try {
      const res = await api.sites.checkTamper(site.id);
      const total = res.total || 0;
      if (!total) {
        toast('检测完成：未发现篡改', 'success');
        return;
      }
      const lines = [];
      if ((res.modified || []).length) lines.push(`被修改：${res.modified.join('、')}`);
      if ((res.added || []).length) lines.push(`新增：${res.added.join('、')}`);
      if ((res.deleted || []).length) lines.push(`被删除：${res.deleted.join('、')}`);
      const m = modal({
        title: `检测到 ${total} 处变化`,
        width: '640px',
        content: h('div', {}, [
          h('div', { class: 'warn-box' }, '受保护文件与基线不一致，请确认是否为预期改动。'),
          codeBox(lines.join('\n'), { maxHeight: '260px' }),
        ]),
        footer: [button('知道了', () => m.close(), 'primary')],
      });
    } catch (e) {
      toast(e.message || '检测失败', 'error');
    }
  }

  // 14. 网站安全
  function tabSecurity() {
    const sc = site.security;
    return h('div', {}, [
      sectionTitle('攻击防护'),
      grid(3,
        h('div', {}, checkboxField('拦截 SQL 注入', sc.block_sqli, (v) => { sc.block_sqli = v; markDirty(); })),
        h('div', {}, checkboxField('拦截 XSS 攻击', sc.block_xss, (v) => { sc.block_xss = v; markDirty(); })),
        h('div', {}, checkboxField('拦截目录穿越', sc.block_traversal, (v) => { sc.block_traversal = v; markDirty(); })),
      ),
      switchField('拦截 User-Agent', sc.block_ua, (v) => { sc.block_ua = v; markDirty(); }),
      field('User-Agent 黑名单关键词', stringListEditor(sc.block_ua_keywords, (v) => { sc.block_ua_keywords = v; markDirty(); }, { hint: '一行一个关键词，命中即拦截', rows: 4 })),
      sectionTitle('路径与后缀拦截'),
      grid(2,
        field('禁止访问的路径', stringListEditor(sc.deny_paths, (v) => { sc.deny_paths = v; markDirty(); }, { hint: '一行一个，如 /config', rows: 4 })),
        field('禁止访问的后缀', stringListEditor(sc.deny_extensions, (v) => { sc.deny_extensions = v; markDirty(); }, { hint: '一行一个后缀，如 sql', rows: 4 })),
      ),
      sectionTitle('响应头与信息隐藏'),
      grid(2,
        h('div', {}, checkboxField('隐藏 nginx 版本号（server_tokens off）', sc.server_tokens_off, (v) => { sc.server_tokens_off = v; markDirty(); })),
        h('div', { class: 'field-hint' }, 'X-Frame-Options / X-Content-Type-Options / Referrer-Policy 三个安全头已默认开启（见生成的 Nginx 配置）'),
      ),
    ]);
  }

  // 15. 网站日志
  function tabLogs() {
    const lg = site.logs;
    return h('div', {}, [
      trafficStatsCard(),
      switchField('开启访问日志', lg.access_log, (v) => { lg.access_log = v; markDirty(); }),
      switchField('开启错误日志', lg.error_log, (v) => { lg.error_log = v; markDirty(); }),
      switchField('完全关闭日志', lg.disable_log, (v) => { lg.disable_log = v; markDirty(); }, '开启后访问日志与错误日志均不再写入'),
      grid(2,
        field('日志保留天数', numberInput(lg.retention_days, (v) => { lg.retention_days = v; markDirty(); }, { min: 1 }), '超期日志由面板定时清理'),
        field('自定义日志格式', textInput(lg.custom_format, (v) => { lg.custom_format = v; markDirty(); }, { placeholder: '留空使用默认组合格式' }), 'nginx log_format 内容'),
      ),
      h('div', { class: 'inline-actions' }, button('前往日志查看', () => { sessionStorage.setItem('zhiweipanel.logs.site', site.id); ctx.navigate('#/logs'); }, 'ghost', 'logs')),
    ]);
  }

  // 近 7 日流量统计卡：v0.9.0 起增量采集，数据可信；精确状态码从本版本开始积累。
  function trafficStatsCard() {
    const box = h('div', { class: 'card-body' }, loading('加载流量统计…'));
    const card = h('div', { class: 'card' }, [
      h('div', { class: 'card-title' }, [
        h('span', {}, '近 7 日流量统计'),
        h('div', { class: 'card-title-extra' }, h('span', { class: 'field-hint' }, '按访问日志增量聚合')),
      ]),
      box,
    ]);
    api.sites.stats(site.id, 7).then((res) => {
      clear(box);
      const rows = (res && res.stats) || [];
      if (!rows.length) {
        box.appendChild(emptyState('暂无统计数据', null, 'logs'));
        return;
      }
      box.appendChild(table([
        { key: 'date', label: '日期', width: '120px' },
        { label: '请求数', align: 'right', render: (r) => formatNumber(r.requests || 0) },
        { label: '独立 IP', align: 'right', render: (r) => formatNumber(r.unique_ips || 0) },
        { label: '流量', align: 'right', render: (r) => formatBytes(r.bytes || 0) },
        { label: '2xx/3xx', align: 'right', render: (r) => `${formatNumber(r.status_2xx || 0)} / ${formatNumber(r.status_3xx || 0)}` },
        { label: '4xx/5xx', align: 'right', render: (r) => `${formatNumber(r.status_4xx || 0)} / ${formatNumber(r.status_5xx || 0)}` },
      ], rows));
      const codes = (res && res.status_codes) || [];
      if (codes.length) {
        box.appendChild(h('div', { class: 'mini-list', style: { marginTop: '10px' } }, [
          h('div', { class: 'field-hint' }, '精确状态码分布'),
          h('div', { class: 'inline-actions' }, codes.map((c) => {
            const code = Number(c.code) || 0;
            const tone = code >= 500 ? 'danger' : code >= 400 ? 'warning' : code >= 300 ? 'info' : 'success';
            return tag(`${code} × ${formatNumber(c.count || 0)}`, tone);
          })),
        ]));
      }
    }).catch((e) => {
      clear(box).appendChild(errorState(`流量统计加载失败：${e.message || e}`, () => {
        card.replaceWith(trafficStatsCard());
      }));
    });
    return card;
  }

  // 16. 网站告警
  function tabAlert() {
    const al = site.alert;
    return h('div', {}, [
      switchField('启用站点告警', al.enabled, (v) => { al.enabled = v; markDirty(); }),
      sectionTitle('告警规则'),
      rowEditor({
        items: al.rules,
        columns: [
          { key: 'type', label: '告警类型', width: '180px', type: 'select', options: ALERT_TYPES },
          { key: 'level', label: '等级', width: '140px', type: 'select', options: ALERT_LEVELS },
          { key: 'value', label: '阈值', width: '120px', type: 'number', min: 0 },
          { key: 'window_minutes', label: '窗口(分钟)', width: '140px', type: 'number', min: 1 },
          { key: 'enabled', label: '启用', width: '90px', type: 'checkbox' },
        ],
        addText: '添加告警规则',
        emptyText: '暂无告警规则',
        onChange: markDirty,
      }),
      h('div', { class: 'field-hint' }, '阈值含义随类型而变：5xx 为窗口内错误数上限，流量配额为 MB，证书临期为天数，文件篡改为文件个数'),
      sectionTitle('通知渠道'),
      rowEditor({
        items: al.channels,
        columns: [
          { key: 'type', label: '渠道类型', width: '180px', type: 'select', options: CHANNEL_TYPES.map((c) => ({ value: c.value, label: c.label })) },
          { key: 'target', label: '接收目标', placeholder: '邮箱 / Webhook 地址' },
          { key: 'enabled', label: '启用', width: '90px', type: 'checkbox' },
        ],
        addText: '添加通知渠道',
        emptyText: '暂无通知渠道，告警将不会外发',
        onChange: markDirty,
      }),
    ]);
  }

  // 17. 其他设置
  function tabExtra() {
    const ex = site.extra;
    return h('div', {}, [
      sectionTitle('压缩'),
      switchField('开启 gzip 压缩', ex.gzip, (v) => { ex.gzip = v; markDirty(); }),
      grid(2,
        field('压缩类型', textInput(ex.gzip_type, (v) => { ex.gzip_type = v; markDirty(); }, { placeholder: '留空使用默认 text/html 等' })),
        field('最小压缩长度（字节）', numberInput(ex.gzip_min_length, (v) => { ex.gzip_min_length = v; markDirty(); }, { min: 0 })),
      ),
      sectionTitle('请求与超时'),
      grid(2,
        field('请求体大小上限', textInput(ex.client_max_body_size, (v) => { ex.client_max_body_size = v; markDirty(); }, { placeholder: '50m' })),
        field('浏览器缓存时长（秒）', numberInput(ex.browser_cache, (v) => { ex.browser_cache = v; markDirty(); }, { min: 0 }), '0 表示不设置'),
      ),
      grid(3,
        field('KeepAlive 超时（秒）', numberInput(ex.keepalive_timeout, (v) => { ex.keepalive_timeout = v; markDirty(); }, { min: 0 })),
        field('发送超时 send_timeout（秒）', numberInput(ex.send_timeout, (v) => { ex.send_timeout = v; markDirty(); }, { min: 0 })),
        field('读取超时 read_timeout（秒）', numberInput(ex.read_timeout, (v) => { ex.read_timeout = v; markDirty(); }, { min: 0 })),
      ),
      field('DNS 解析器', textInput(ex.dns_resolver, (v) => { ex.dns_resolver = v; markDirty(); }, { placeholder: '如 223.5.5.5 119.29.29.29' })),
      sectionTitle('静态资源缓存'),
      field('缓存后缀', stringListEditor(ex.static_cache_ext, (v) => { ex.static_cache_ext = v; markDirty(); }, { hint: '一行一个后缀，不带点', rows: 3 })),
    ]);
  }

  // -------------------------------------------------------------------------
  // 动作
  // -------------------------------------------------------------------------
  async function doSave() {
    if (!isDirty()) {
      toast('没有需要保存的修改', 'info');
      return;
    }
    saveBtn.disabled = true;
    saveBtn.textContent = '保存中…';
    try {
      await api.sites.update(site.id, site);
      snapshot = JSON.stringify(site);
      store.markDirty(false);
      toast('站点配置已保存', 'success');
    } catch (e) {
      // 后端 writeError 返回 {error, code}；nginx 配置校验失败时 error 字段
      // 是 nginx -t 原文（多行）。用弹窗完整展示，而不是被 toast 截断。
      const raw = e && e.data && e.data.error;
      if (raw) {
        const m = modal({
          title: 'nginx 配置校验未通过',
          width: '760px',
          content: h('div', {}, [
            h('div', { class: 'warn-box' },
              '本次修改未落库，磁盘上的配置文件已恢复为保存前内容。以下是 nginx 原始报错：'),
            codeBox(raw, { maxHeight: '320px' }),
          ]),
          footer: [button('关闭', () => m.close(), 'primary')],
        });
      } else {
        toast(e.message || '保存失败', 'error');
      }
    } finally {
      saveBtn.disabled = false;
      clear(saveBtn);
      saveBtn.appendChild(icon('save'));
      saveBtn.appendChild(h('span', {}, '保存修改'));
    }
  }

  // 删除整个站点（含联动 drop database + drop user）。
  //
  // 故意不能"撤销未保存修改"后再删：站点删除是不可逆物理动作（nginx
  // conf 会被立刻移除），先点保存更稳。但有未保存修改时二级确认会提示。
  async function doDeleteSite() {
    const label = site.name || site.id;
    if (isDirty()) {
      toast('存在未保存修改，请先保存或丢弃后再删除', 'info');
      return;
    }
    const ok = await confirmDialog({
      title: '删除站点',
      message:
        `确定要删除「${label}」吗？\n` +
        (site.mysql && site.mysql.name
          ? `该操作会同时移除 Nginx 站点配置、MySQL 数据库 \`${site.mysql.name}\` 与对应用户，且不可恢复。\n`
          : '该操作会同时移除 Nginx 站点配置，且不可恢复。\n') +
        '（仅删除配置，不会动站点根目录文件）',
      okText: '确认删除',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.sites.remove(site.id);
      toast('站点已删除', 'success');
      ctx.navigate('#/sites');
    } catch (e) {
      toast(e.message || '删除失败', 'error');
    }
  }

  // 查看后端渲染的真实 nginx 配置
  async function viewConfig() {
    if (isDirty()) {
      toast('存在未保存修改，预览的是上次保存的配置', 'info');
    }
    const m = modal({
      title: '生成的 Nginx 配置',
      width: '960px',
      content: loading('正在生成配置…'),
    });
    try {
      const res = await api.sites.config(site.id);
      const test = res.nginx_test || {};
      clear(m.body).appendChild(h('div', {}, [
        h('div', { class: `test-result ${test.ok ? 'ok' : 'fail'}` }, [
          icon(test.ok ? 'check' : 'warning'),
          h('span', {}, test.ok ? `配置检测通过${test.message ? '：' + test.message : ''}` : `配置检测未通过：${test.message || '未知错误'}`),
        ]),
        sectionTitle('HTTP server 片段'),
        codeBox(res.http_conf, { maxHeight: '260px' }),
        sectionTitle('站点 server 配置'),
        codeBox(res.site_conf, { maxHeight: '420px' }),
      ]));
    } catch (e) {
      clear(m.body).appendChild(errorState(`配置获取失败：${e.message || e}`));
    }
  }
}

// 数据库改密后的一次性明文展示（离开弹窗即无法再次查看）。
function showDatabasePassword(name, password) {
  const m = modal({
    title: '数据库密码已重置',
    width: '520px',
    content: h('div', {}, [
      h('p', {}, `数据库：${name}`),
      codeBox(password || '(空)', { maxHeight: '80px' }),
      h('div', { class: 'field-hint' }, '明文只显示这一次。请立即更新应用配置或保存到密码管理器。'),
    ]),
    footer: [
      button('复制密码', () => copyText(password || ''), 'default', 'key'),
      button('我已保存', () => m.close(), 'primary', 'check'),
    ],
  });
}

async function copyText(text) {
  if (!text) return;
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      toast('已复制', 'success');
      return;
    }
  } catch (e) { /* 回退 */ }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    toast(ok ? '已复制' : '复制失败，请手动选择', ok ? 'success' : 'error');
  } catch (e) {
    toast('复制失败，请手动选择', 'error');
  }
}