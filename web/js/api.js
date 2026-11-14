// ZhiweiPanel 前端 API 封装
// 统一处理：请求前缀 /api、Cookie 认证、JSON 解析、错误规范化、401 跳转
// 后端由 Go 单二进制提供，Cookie 为 HttpOnly，前端不接触 token 存储

const BASE = '/api';

// 401 回调（由 app.js 注入，用于跳转登录页）
let unauthorizedHandler = null;
export function setUnauthorizedHandler(fn) {
  unauthorizedHandler = fn;
}

// 从 JSON 中尽力提取可读的错误信息
function pickMessage(data, status) {
  if (!data) return `请求失败（HTTP ${status}）`;
  if (typeof data === 'string') return data || `请求失败（HTTP ${status}）`;
  return data.message || data.error || data.msg || `请求失败（HTTP ${status}）`;
}

// 核心请求函数：失败时抛出 {status, message, data}
async function request(method, path, body, options = {}) {
  const init = {
    method,
    // 认证依赖 HttpOnly Cookie，同源自动携带
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  };
  // FormData / Blob / URLSearchParams：浏览器自动设 Content-Type（含 boundary），
  // 不要我们自己覆盖为 application/json。
  const isFormData = typeof FormData !== 'undefined' && body instanceof FormData;
  if (body !== undefined && body !== null && !isFormData) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  } else if (isFormData) {
    init.body = body;
  }

  let res;
  try {
    res = await fetch(BASE + path, init);
  } catch (e) {
    // 网络层失败（后端未启动 / 内网中断）
    throw { status: 0, message: '无法连接面板服务，请确认后端已启动' };
  }

  if (res.status === 204) return null;

  const text = await res.text();
  let data = null;
  if (text) {
    // 按 Content-Type 分流，**不能**靠"能不能 JSON.parse"来猜响应类型。
    //
    // 同一个接口（GET /api/sites/{id}/files/raw?path=）有两种响应形态：
    // 文本文件返回 text/plain 的裸文本，二进制返回 application/json 的
    // {content, encoding:'base64', ...}。早期实现一律先试 JSON.parse，
    // 于是当被打开的文件内容**恰好是合法 JSON** 时
    // （package.json / composer.json / {"a":1} / 123 / "foo"）：
    //   - JSON.parse 成功 → 返回的是对象/数字/字符串，不是约定的 base64 形状；
    //   - 调用方两个分支都不匹配 → 编辑器打开成空白；
    //   - 用户点保存 → 把空内容写回去 → **原文件被清空**。
    // 这是静默数据丢失，package.json 这类文件极其常见。
    const ctype = (res.headers.get('content-type') || '').toLowerCase();
    const looksJSON = ctype.includes('application/json');
    if (looksJSON) {
      try {
        data = JSON.parse(text);
      } catch (e) {
        // 声称是 JSON 却解析不了：当成错误处理，别把半截内容当成功返回
        if (res.ok) return text;
        throw { status: res.status, message: text.slice(0, 400) };
      }
    } else {
      // 明确的非 JSON 响应：原样返回文本。
      if (!res.ok) throw { status: res.status, message: pickMessage(null, res.status) };
      return text;
    }
  }

  if (!res.ok) {
    if (res.status === 401 && unauthorizedHandler) unauthorizedHandler();
    throw { status: res.status, message: pickMessage(data, res.status), data };
  }
  return unwrap(data);
}

// 后端**列表**端点用 `{"sites": [{"site": {...}}, ...]}` 这种"summary 包一层 site"
// 的结构（站点字段嵌在 `site` 键下，today_reqs 等汇总指标与它同层）。
// 前端很多地方直接当扁平数组用（filter/map），每个 site 在 `site` 字段里。
// 在这里集中展平，免得到处写 `s.site || s` 容易漏。
//
// **单对象**响应（POST /api/sites 建库成功的 `{site:..., show_password:true}`、
// PUT /api/sites/{id} applyErr 的 `{site:..., warning:...}`）**不**在这里展平——
// site-create.js 依赖 `created.site.mysql` 弹密码框，展平会把这条路径废掉。
// 想要单对象扁平时由调用方在 `api.xxx` 里自己展。
function unwrap(data) {
  if (!data || typeof data !== 'object') return data;
  // 顶层是 { sites: [...] } / { certs: [...] } / { accounts: [...] } 的，
  // 元素里有 `site` 键的项才展平。元素里没有 site 的原样保留。
  for (const k of ['sites', 'certs', 'accounts']) {
    if (Array.isArray(data[k]) && data[k].length) {
      data[k] = data[k].map((item) => flattenSiteSummary(item));
    }
  }
  return data;
}

export const http = {
  get: (p, o) => request('GET', p, undefined, o),
  post: (p, b, o) => request('POST', p, b ?? {}, o),
  put: (p, b, o) => request('PUT', p, b ?? {}, o),
  del: (p, o) => request('DELETE', p, undefined, o),
};

// ---------------------------------------------------------------------------
// 后端接口映射（严格对应已定稿的 API 契约，不自行发明接口）
// ---------------------------------------------------------------------------

// 把 SiteSummary 摊平：{site:{...}, today_reqs:1} -> {...site, today_reqs:1}
// 汇总指标放在后面展开，确保不会被站点自身的同名字段覆盖（目前没有重名）。
function flattenSiteSummary(x) {
  if (!x || !x.site) return x;
  const { site, ...metrics } = x;
  return { ...site, ...metrics };
}

export const api = {
  // 认证
  auth: {
    login: (username, password) => http.post('/auth/login', { username, password }),
    logout: () => http.post('/auth/logout'),
    me: () => http.get('/auth/me'),
    changePassword: (old_password, new_password) =>
      http.post('/auth/password', { old_password, new_password }),
    // 异常登录第二步：校验 challenge code。
    // 成功由后端直接写 session cookie（通过 Set-Cookie 头），前端不需要拿 token。
    verifyChallenge: (challenge_id, code) =>
      http.post('/auth/verify-challenge', { challenge_id, code }),
    // 忘记密码：两步流程（申请验证码 → 提交验证码 + 新密码）。
    //
    // 这两个端点挂在主 mux 上（未登录也可访问），但仍然走 request() 封装，
    // 这样错误提示与登录流程保持一致。
    // 早期 app.js 直接调 `http.post(...)`，而 http 没有被 import，
    // 于是整个"忘记密码"流程 100% 失败，界面上显示的是
    // "http is not defined" 这种 JS 报错文案。
    passwordResetRequest: (username) =>
      http.post('/auth/password-reset/request', { username }),
    passwordResetConfirm: (challenge_id, username, code, new_password) =>
      http.post('/auth/password-reset/confirm', { challenge_id, username, code, new_password }),
  },

  // 管理员账户 + SMTP
  account: {
    get: () => http.get('/admin/account'),
    setEmail: (email) => http.put('/admin/account/email', { email }),
    sendEmailVerify: () => http.post('/admin/account/email/verify'),
  },
  smtp: {
    get: () => http.get('/admin/smtp'),
    set: (cfg) => http.put('/admin/smtp', cfg),
    test: (to) => http.post('/admin/smtp/test', { to }),
  },

  // 站点
  sites: {
    // 后端 /api/sites 返回的是 SiteSummary：站点字段嵌在 site 键下，
    // 汇总指标（today_reqs / month_bytes / cert_days ...）与它同层。
    // 这里统一摊平成一层，免得每个视图都要写 x.site || x——
    // 漏一处的后果就是 s.id 取到 undefined，进而请求 /api/sites/undefined/...
    list: async () => {
      const res = await http.get('/sites');
      const list = (res && res.sites) || [];
      return { ...res, sites: list.map(flattenSiteSummary) };
    },
    // v0.2.1: 系统运行参数（全局 -public-port / IPv6 开关）
    systemInfo: () => http.get('/system/info'),
    get: (id) => http.get(`/sites/${encodeURIComponent(id)}`),
    create: (site) => http.post('/sites', site),
    update: (id, site) => http.put(`/sites/${encodeURIComponent(id)}`, site),
    remove: (id) => http.del(`/sites/${encodeURIComponent(id)}`),
    toggle: (id, status) => http.post(`/sites/${encodeURIComponent(id)}/toggle`, { status }),
    reload: (id) => http.post(`/sites/${encodeURIComponent(id)}/reload`),
    config: (id) => http.get(`/sites/${encodeURIComponent(id)}/config`),
    logs: (id, type = 'access', limit = 200, page = 1) =>
      http.get(`/sites/${encodeURIComponent(id)}/logs?type=${type}&limit=${limit}&page=${page}`),
    stats: (id, days = 7) => http.get(`/sites/${encodeURIComponent(id)}/stats?days=${days}`),
    // 防篡改基线。必须 POST：
    // 后端 EstablishBaseline 会直接改写 integrity 基线表，
    // GET 会让只读 Token 也能把基线刷成当前状态（等于洗掉篡改痕迹）。
    establishBaseline: (id) => http.post(`/sites/${encodeURIComponent(id)}/baseline`),
    // 篡改检测。同样必须 POST：后端会写 tamper_events 表并触发告警通知。
    checkTamper: (id) => http.post(`/sites/${encodeURIComponent(id)}/tamper`),
    // 真正的 WebSocket 端点是 POST /api/system/logs/tail?site_id=<id>，
    // 返回 {ws_url}。早期写成 /sites/{id}/logs/tail，会落到历史日志分支、
    // 拿不到 ws_url，实时跟踪 100% 失败。
    tailToken: (id) => http.post(`/system/logs/tail?site_id=${encodeURIComponent(id)}`),
    // 文件管理（站点根目录）
    // 读二进制（>2MB 或 NUL 字节）时后端返回 {content:base64, encoding:'base64', mime, size}。
    // 读文本时直接返 raw text。列表统一返 {entries, path}。
    files: {
      list: (id, path = '') => {
        const q = path ? `?path=${encodeURIComponent(path)}` : '';
        return http.get(`/sites/${encodeURIComponent(id)}/files${q}`);
      },
      read: (id, path) => http.get(`/sites/${encodeURIComponent(id)}/files/raw?path=${encodeURIComponent(path)}`),
      // 写用 JSON {content, encoding} 走 base64 路径，避免 textarea/range 限制；
      // 读返回的是 raw text，所以也用 base64 通道双向对称。
      write: (id, path, content) =>
        http.put(`/sites/${encodeURIComponent(id)}/files?path=${encodeURIComponent(path)}`,
                 { content, encoding: 'base64' }),
      remove: (id, path) => http.del(`/sites/${encodeURIComponent(id)}/files?path=${encodeURIComponent(path)}`),
      rename: (id, from, to) => http.post(`/sites/${encodeURIComponent(id)}/files/rename`, { from, to }),
      mkdir: (id, path) => http.post(`/sites/${encodeURIComponent(id)}/files/mkdir`, { path }),
      // 上传：multipart/form-data，path 为目标目录，files 为 File[]（可多文件）
      upload: (id, dir, files, onProgress) => {
        const fd = new FormData();
        if (dir) fd.append('path', dir);
        for (const f of files) fd.append('file', f, f.name);
        return http.post(`/sites/${encodeURIComponent(id)}/files/upload`, fd, { onProgress });
      },
      // 解压：把已经上传到站点内的 zip 解压。
      // zipRel: "site.zip" / "uploads/site.zip"
      // dest:   可选，目标目录（相对站点根）。空 = zip 父目录
      unzip: (id, zipRel, dest = '') =>
        http.post(`/sites/${encodeURIComponent(id)}/files/unzip`, { zip_path: zipRel, dest }),
    },
  },

  // 证书 / ACME
  certs: {
    list: () => http.get('/certs'),
    issue: (payload) => http.post('/certs/issue', payload),
    revoke: (id) => http.post(`/certs/${encodeURIComponent(id)}/revoke`),
    import: (payload) => http.post('/certs/import', payload),
    download: (id) => http.get(`/certs/${encodeURIComponent(id)}/download`),
    accounts: () => http.get('/acme/accounts'),
    createAccount: (payload) => http.post('/acme/accounts', payload),
    // 把证书绑定到站点：第一个参数是证书 ID，第二个是站点 ID
    bind: (certID, siteID) => http.post(`/certs/${encodeURIComponent(certID)}/bind`, { site_id: siteID }),
  },

  // DNS-01 提供商列表（含每个 provider 需要的凭据字段说明）
  dnsProviders: () => http.get('/dns-providers'),
  // alertTypes 返回受支持的站点告警规则类型（后端权威清单）。
  alertTypes: () => http.get('/alert-types'),

  // 告警
  alerts: {
    list: (limit = 100) => http.get(`/alerts?limit=${limit}`),
    resolve: (id) => http.post(`/alerts/${encodeURIComponent(id)}/resolve`),
    test: (channel) => http.post('/alerts/test', { channel }),
  },

  // v0.2.0: 计划任务
  cron: {
    list: () => http.get('/cron/jobs'),
    get: (id) => http.get(`/cron/jobs/${encodeURIComponent(id)}`),
    create: (job) => http.post('/cron/jobs', job),
    update: (id, job) => http.put(`/cron/jobs/${encodeURIComponent(id)}`, job),
    remove: (id) => http.del(`/cron/jobs/${encodeURIComponent(id)}`),
    run: (id) => http.post('/cron/jobs/run', { job_id: id }),
  },

  // v0.2.0: 备份
  backups: {
    list: (params = {}) => {
      const q = new URLSearchParams();
      if (params.kind) q.set('kind', params.kind);
      if (params.target) q.set('target', params.target);
      if (params.limit) q.set('limit', params.limit);
      const qs = q.toString();
      return http.get('/backups' + (qs ? '?' + qs : ''));
    },
    createSite: (siteID, note = '') => http.post('/backups', { kind: 'site', target: siteID, note }),
    createDB: (dbName, note = '') => http.post('/backups', { kind: 'database', target: dbName, note }),
    remove: (id) => http.del(`/backups/${encodeURIComponent(id)}`),
    restorePreview: (id) => http.post(`/backups/${encodeURIComponent(id)}/restore`),
  },

  // v0.2.0: 站点批量操作
  sitesBatch: (action, ids) => http.post('/sites/batch', { action, ids }),

  // 系统设置与运行时
  settings: {
    get: () => http.get('/settings'),
    put: (settings) => http.put('/settings', { settings }),
  },
  system: {
    status: () => http.get('/system/status'),
    // mysql MySQL/MariaDB 子系统状态探测（installed/running/version）。
    // 探测失败 → 返回 null（前端"是否显示建库选项"用这个判断）。
    mysql: () => http.get('/system/mysql').catch(() => null),
    nginxConfig: () => http.get('/system/nginx-config'),
    nginxTest: () => http.post('/system/nginx-test'),
    // v0.2.2: 宿主机运行指标（CPU/内存/磁盘/负载/网络）。
    // 返回 {current, history}，history 是内存环形缓冲里的最近若干条采样。
    metrics: () => http.get('/system/metrics'),
    // v0.2.2: nginx 配置历史快照。
    configHistory: {
      list: () => http.get('/system/config-history'),
      get: (id) => http.get(`/system/config-history/${encodeURIComponent(id)}`),
      diff: (id) => http.get(`/system/config-history/${encodeURIComponent(id)}/diff`),
      restore: (id) => http.post(`/system/config-history/${encodeURIComponent(id)}/restore`),
    },
  },

  // v0.2.2: API Token（机器访问凭据）
  // 创建时明文只在响应里出现一次，前端要弹窗让用户复制保存。
  tokens: {
    list: () => http.get('/tokens'),
    create: (payload) => http.post('/tokens', payload),
    remove: (id) => http.del(`/tokens/${encodeURIComponent(id)}`),
  },

  // v0.2.2: 操作审计日志
  audit: {
    list: (limit = 200, action = '') => {
      const q = new URLSearchParams();
      if (limit) q.set('limit', limit);
      if (action) q.set('action', action);
      const qs = q.toString();
      return http.get('/audit-logs' + (qs ? '?' + qs : ''));
    },
  },
};

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

// 格式化字节数
export function formatBytes(bytes) {
  const n = Number(bytes) || 0;
  if (n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  const v = n / Math.pow(1024, i);
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

// 格式化请求数（千分位）
export function formatNumber(n) {
  const v = Number(n) || 0;
  return v.toLocaleString('zh-CN');
}

// 相对时间（如「3 天前」）
export function timeAgo(iso) {
  if (!iso) return '-';
  const t = new Date(iso);
  if (isNaN(t.getTime())) return String(iso);
  const diff = Date.now() - t.getTime();
  if (diff < 0) return formatDateTime(iso);
  const min = Math.floor(diff / 60000);
  if (min < 1) return '刚刚';
  if (min < 60) return `${min} 分钟前`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h} 小时前`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d} 天前`;
  return formatDateTime(iso);
}

// 日期时间格式化
export function formatDateTime(iso, withSec = true) {
  if (!iso) return '-';
  const t = new Date(iso);
  if (isNaN(t.getTime())) return String(iso);
  const p = (x) => String(x).padStart(2, '0');
  const date = `${t.getFullYear()}-${p(t.getMonth() + 1)}-${p(t.getDate())}`;
  const time = withSec ? `${p(t.getHours())}:${p(t.getMinutes())}:${p(t.getSeconds())}` : `${p(t.getHours())}:${p(t.getMinutes())}`;
  return `${date} ${time}`;
}

// 只取日期部分
export function formatDate(iso) {
  if (!iso) return '-';
  const t = new Date(iso);
  if (isNaN(t.getTime())) return String(iso).slice(0, 10);
  const p = (x) => String(x).padStart(2, '0');
  return `${t.getFullYear()}-${p(t.getMonth() + 1)}-${p(t.getDate())}`;
}

// 计算证书剩余天数
export function certDaysLeft(cert) {
  if (!cert || !cert.not_after) return null;
  const t = new Date(cert.not_after);
  if (isNaN(t.getTime())) return null;
  // Go 的 time.Time 零值会序列化成 "0001-01-01T00:00:00Z"——它是非空字符串，
  // 上面的 !cert.not_after 拦不住，而 new Date 能正常解析出一个巨大的负时间戳。
  // 不拦这一步会算出 -74 万天，界面显示成"已过期 739901 天"。
  // 1970 之前的日期对"证书有效期"没有意义，一律按无证书处理。
  if (t.getTime() <= 0) return null;
  return Math.floor((t.getTime() - Date.now()) / 86400000);
}