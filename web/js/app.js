// ZhiweiPanel 前端入口：全局 store、hash 路由、登录页、主框架布局
// 纯原生 ES Module，视图按需动态加载（无打包器）

import { api, setUnauthorizedHandler } from './api.js';
import {
  h, clear, icon, toast, button, textInput, loading, errorState,
  confirmDialog, field, tag,
} from './components.js';

// ---------------------------------------------------------------------------
// 全局状态（单一 store，跨视图共享）
// ---------------------------------------------------------------------------
export const store = {
  user: null,           // {username, role}
  settings: null,       // 面板设置对象
  version: '',          // 面板版本（顶栏展示）
  dirty: false,         // 当前页面存在未保存修改
  dirtyMessage: '当前页面有未保存的修改，确定离开吗？',
  markDirty: (flag = true) => {
    store.dirty = flag;
    if (flag) window.addEventListener('beforeunload', beforeUnload);
    else window.removeEventListener('beforeunload', beforeUnload);
  },
};

function beforeUnload(e) {
  e.preventDefault();
  e.returnValue = '';
}

// ---------------------------------------------------------------------------
// 主题（浅色 / 深色）
// ---------------------------------------------------------------------------
const THEME_KEY = 'zhiweipanel_theme';

function currentTheme() {
  return document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light';
}

function applyTheme(theme) {
  const t = theme === 'dark' ? 'dark' : 'light';
  document.documentElement.dataset.theme = t;
  try { localStorage.setItem(THEME_KEY, t); } catch (e) { /* 隐私模式下忽略 */ }
  // 同步所有开关按钮的图标与提示。
  // 必须 clear + appendChild：icon() 返回的是 SVG DOM 元素，
  // 直接 innerHTML = 会被字符串化成 "[object SVGSVGElement]" 显示在按钮上。
  const label = t === 'dark' ? '切换到浅色' : '切换到深色';
  for (const btn of document.querySelectorAll('[data-theme-toggle]')) {
    clear(btn).appendChild(icon(t === 'dark' ? 'sun' : 'moon'));
    btn.title = label;
    btn.setAttribute('aria-label', label);
  }
}

function toggleTheme() {
  applyTheme(currentTheme() === 'dark' ? 'light' : 'dark');
}

// 顶栏里的主题开关
function themeToggleButton() {
  return h('button', {
    class: 'btn btn-ghost btn-icon',
    type: 'button',
    'data-theme-toggle': '',
    onclick: toggleTheme,
  }, [icon(currentTheme() === 'dark' ? 'sun' : 'moon')]);
}

// ---------------------------------------------------------------------------
// 视图共享的轻量上下文
// ---------------------------------------------------------------------------
export const ctx = {
  navigate(hash) {
    if (location.hash === hash) handleRoute();
    else location.hash = hash;
  },
};

// ---------------------------------------------------------------------------
// 路由表
// ---------------------------------------------------------------------------
const ROUTES = [
  { pattern: /^\/?$|^\/dashboard$/, key: 'dashboard', title: '仪表盘', icon: 'dashboard', load: () => import('./views/dashboard.js') },
  { pattern: /^\/sites$/, key: 'sites', title: '网站', icon: 'sites', load: () => import('./views/sites.js') },
  { pattern: /^\/sites\/new$/, key: 'site-create', title: '新建站点', icon: 'sites', load: () => import('./views/site-create.js') },
  { pattern: /^\/sites\/([^\/]+)$/, key: 'site-detail', title: '站点详情', icon: 'sites', load: () => import('./views/site-detail.js') },
  { pattern: /^\/certs$/, key: 'certs', title: '证书', icon: 'cert', load: () => import('./views/certs.js') },
  { pattern: /^\/cron$/, key: 'cron', title: '计划任务', icon: 'cron', load: () => import('./views/cron.js') },
  { pattern: /^\/backups$/, key: 'backups', title: '备份', icon: 'backup', load: () => import('./views/backups.js') },
  { pattern: /^\/logs$/, key: 'logs', title: '日志', icon: 'logs', load: () => import('./views/logs.js') },
  { pattern: /^\/alerts$/, key: 'alerts', title: '告警', icon: 'alerts', load: () => import('./views/alerts.js') },
  { pattern: /^\/ops$/, key: 'ops', title: '运维中心', icon: 'settings', load: () => import('./views/ops.js') },
  { pattern: /^\/ops\/([a-z]+)$/, key: 'ops', title: '运维中心', icon: 'settings', load: () => import('./views/ops.js') },
  { pattern: /^\/apps$/, key: 'apps', title: '软件商店', icon: 'store', load: () => import('./views/apps.js') },
  { pattern: /^\/settings$/, key: 'settings', title: '设置', icon: 'settings', load: () => import('./views/settings.js') },
];

// 侧边导航项
const NAV = [
  { hash: '#/dashboard', label: '仪表盘', icon: 'dashboard', match: /^\/?$|^\/dashboard$|^\/sites\// },
  { hash: '#/sites', label: '网站', icon: 'sites', match: /^\/sites$/ },
  { hash: '#/certs', label: '证书', icon: 'cert', match: /^\/certs$/ },
  { hash: '#/cron', label: '计划任务', icon: 'cron', match: /^\/cron$/ },
  { hash: '#/backups', label: '备份', icon: 'backup', match: /^\/backups$/ },
  { hash: '#/logs', label: '日志', icon: 'logs', match: /^\/logs$/ },
  { hash: '#/alerts', label: '告警', icon: 'alerts', match: /^\/alerts$/ },
  { hash: '#/ops', label: '运维中心', icon: 'settings', match: /^\/ops(\/|$)/ },
  { hash: '#/apps', label: '软件商店', icon: 'store', match: /^\/apps$/ },
  { hash: '#/settings', label: '设置', icon: 'settings', match: /^\/settings$/ },
];

function parseHash() {
  const raw = location.hash.replace(/^#/, '') || '/';
  return raw;
}

// ---------------------------------------------------------------------------
// 应用外壳
// ---------------------------------------------------------------------------
let shell = null;
let outlet = null;
let navList = null;

function buildShell() {
  navList = h('nav', { class: 'sidebar-nav' });
  outlet = h('main', { class: 'content', id: 'outlet' });

  const sidebar = h('aside', { class: 'sidebar' }, [
    h('div', { class: 'brand' }, [
      h('span', { class: 'brand-mark' }, 'ZW'),
      h('div', { class: 'brand-text' }, [
        h('div', { class: 'brand-name' }, '智维面板'),
        h('div', { class: 'brand-sub' }, '建站管理控制台'),
      ]),
    ]),
    navList,
    h('div', { class: 'sidebar-foot' }, h('span', {}, '静态前端 · go:embed')),
  ]);

  const userName = h('span', { class: 'topbar-user' }, store.user ? store.user.username : '-');
  const userRole = store.user && store.user.role ? tag(store.user.role) : null;
  const versionLabel = h('span', { class: 'topbar-version' }, store.version ? `v${store.version}` : '');

  const topbar = h('header', { class: 'topbar' }, [
    h('div', { class: 'topbar-left' }, h('span', { class: 'topbar-title' }, '控制台')),
    h('div', { class: 'topbar-right' }, [
      versionLabel,
      h('span', { class: 'topbar-divider' }),
      themeToggleButton(),
      h('span', { class: 'topbar-divider' }),
      userName,
      userRole,
      h('span', { class: 'topbar-divider' }),
      h('button', { class: 'btn btn-ghost', type: 'button', onclick: doLogout }, [icon('logout'), h('span', {}, '退出')]),
    ]),
  ]);

  shell = h('div', { class: 'layout' }, [sidebar, h('div', { class: 'main' }, [topbar, outlet])]);

  // 导航项
  clear(navList);
  for (const item of NAV) {
    navList.appendChild(h('a', {
      class: 'nav-item',
      href: item.hash,
      dataset: { hash: item.hash },
    }, [icon(item.icon), h('span', {}, item.label)]));
  }
  return shell;
}

function markActiveNav(path) {
  if (!navList) return;
  for (const el of navList.children) {
    const item = NAV.find((n) => n.hash === el.dataset.hash);
    el.classList.toggle('is-active', !!(item && item.match.test(path)));
  }
}

// ---------------------------------------------------------------------------
// 路由分发
// ---------------------------------------------------------------------------
let routing = false;

async function handleRoute() {
  if (routing) return;
  routing = true;
  const path = parseHash();

  // 未登录只允许停留在登录页
  if (!store.user) {
    routing = false;
    renderLogin();
    return;
  }

  const route = ROUTES.find((r) => r.pattern.test(path));
  if (!route) {
    ctx.navigate('#/dashboard');
    routing = false;
    return;
  }

  markActiveNav(path);
  const m = path.match(/^\/sites\/([^\/]+)$/);
  const params = { path, id: m ? decodeURIComponent(m[1]) : '' };

  // 离开页面前的未保存确认
  if (store.dirty) {
    store.markDirty(false);
    const ok = await confirmDialog({
      title: '未保存的修改',
      message: store.dirtyMessage,
      okText: '放弃修改并离开',
      danger: true,
    });
    if (!ok) {
      routing = false;
      // 仅回滚 hash，不重绘当前页，避免丢失用户已编辑的内容
      if (location.hash !== `#${path}`) history.replaceState(null, '', `#${path}`);
      return;
    }
  }

  clear(outlet).appendChild(loading());
  try {
    const mod = await route.load();
    clear(outlet);
    outlet.scrollTop = 0;
    await mod.render(outlet, params);
    document.title = `${route.title} · ZhiweiPanel`;
  } catch (err) {
    console.error(err);
    clear(outlet).appendChild(errorState(`页面加载失败：${err.message || err}`, () => handleRoute()));
  } finally {
    routing = false;
  }
}

window.addEventListener('hashchange', handleRoute);

// ---------------------------------------------------------------------------
// 登录页
// ---------------------------------------------------------------------------
function renderLogin() {
  const app = document.getElementById('app');
  clear(app);

  // 状态：'login' / 'verify' / 'reset1' / 'reset2'。
  // reset1 = 输 username 申请验证码；reset2 = 输验证码+新密完成重置。
  const state = { phase: 'login', challengeId: null, resetUsername: '' };

  const errBox = h('div', { class: 'login-error', style: { display: 'none' } });
  const hintBox = h('div', { class: 'login-hint', style: { display: 'none' } });

  const uInput = textInput('', null, { placeholder: '用户名' });
  const pInput = textInput('', null, { type: 'password', placeholder: '密码' });
  // maxlength 16：验证码 6 位、动态口令 6 位、恢复码 12 位+2 短横共用此框。
  const cInput = textInput('', null, { type: 'text', placeholder: '6 位验证码', maxlength: 16 });
  const npInput = textInput('', null, { type: 'password', placeholder: '新密码（至少 8 位）' });

  const submitBtn = h('button', { class: 'btn btn-primary btn-block', type: 'submit' }, '登 录');

  const form = h('form', {
    class: 'login-form',
    onsubmit: (e) => {
      e.preventDefault();
      if (state.phase === 'login') {
        doLogin(state, uInput, pInput, cInput, errBox, hintBox, submitBtn, form);
      } else if (state.phase === 'totp') {
        doVerifyTOTP(state, cInput, errBox, hintBox, submitBtn, form);
      } else if (state.phase === 'verify') {
        doVerifyChallenge(state, cInput, errBox, hintBox, submitBtn, form);
      } else if (state.phase === 'reset1') {
        doResetRequest(state, uInput, errBox, hintBox, submitBtn, form);
      } else if (state.phase === 'reset2') {
        doResetConfirm(state, cInput, npInput, errBox, hintBox, submitBtn, form);
      }
    },
  }, [
    errBox,
    hintBox,
    h('div', { class: 'login-row', id: 'loginRowU' }, field('用户名', uInput)),
    h('div', { class: 'login-row', id: 'loginRowP' }, field('密码', pInput)),
    h('div', { class: 'login-row', id: 'loginRowC', style: { display: 'none' } }, field('验证码', cInput)),
    h('div', { class: 'login-row', id: 'loginRowNP', style: { display: 'none' } }, field('新密码', npInput)),
    submitBtn,
  ]);

  // "返回登录" 链接：verify / reset 阶段都可见
  const backLink = h('a', {
    href: '#',
    class: 'login-back-link',
    style: { display: 'none' },
    onclick: (e) => {
      e.preventDefault();
      resetToLogin(state, form, errBox, hintBox, cInput, pInput, uInput, npInput, submitBtn);
    },
  }, '‹ 返回密码登录');

  // "忘记密码" 链接：仅 login 阶段可见
  const forgotLink = h('a', {
    href: '#',
    class: 'login-back-link',
    style: { display: '' },
    onclick: (e) => {
      e.preventDefault();
      switchToReset1(state, form, errBox, hintBox, uInput, pInput, submitBtn);
    },
  }, '忘记密码？');

  app.appendChild(h('div', { class: 'login-wrap' }, [
    h('div', { class: 'login-card' }, [
      h('div', { class: 'login-brand' }, [
        h('span', { class: 'brand-mark brand-mark-lg' }, 'ZW'),
        h('div', {}, [
          h('div', { class: 'login-title' }, '智维面板'),
          h('div', { class: 'login-sub' }, '自托管建站管理控制台'),
        ]),
      ]),
      form,
      forgotLink,
      backLink,
      h('div', { class: 'login-foot' }, [
        h('span', {}, '请使用管理员账号登录'),
        h('button', {
          class: 'btn btn-ghost btn-icon',
          type: 'button',
          'data-theme-toggle': '',
          onclick: toggleTheme,
        }, [icon(currentTheme() === 'dark' ? 'sun' : 'moon')]),
      ]),
    ]),
  ]));

  setTimeout(() => uInput.focus(), 30);
}

async function doLogin(state, uInput, pInput, cInput, errBox, hintBox, submitBtn, form) {
  errBox.style.display = 'none';
  hintBox.style.display = 'none';
  const username = uInput.value.trim();
  const password = pInput.value;
  if (!username || !password) {
    errBox.textContent = '请输入用户名和密码';
    errBox.style.display = 'block';
    return;
  }
  submitBtn.disabled = true;
  submitBtn.textContent = '登录中…';
  try {
    const res = await api.auth.login(username, password);
    if (res.totp_required && res.login_token) {
      // 两步验证：密码已通过，进入动态口令阶段
      state.phase = 'totp';
      state.loginToken = res.login_token;
      state.username = username;
      form.querySelector('#loginRowU').style.display = 'none';
      form.querySelector('#loginRowP').style.display = 'none';
      form.querySelector('#loginRowC').style.display = '';
      submitBtn.textContent = '验 证';
      hintBox.textContent = '请输入认证器 App 中的 6 位动态口令；手机不在身边时可输入恢复码';
      hintBox.style.display = 'block';
      document.querySelector('.login-back-link').style.display = '';
      setTimeout(() => cInput.focus(), 30);
      submitBtn.disabled = false;
      return;
    }
    if (res.need_verify && res.challenge_id) {
      // 异常登录：进入验证码阶段
      state.phase = 'verify';
      state.challengeId = res.challenge_id;
      form.querySelector('#loginRowU').style.display = 'none';
      form.querySelector('#loginRowP').style.display = 'none';
      form.querySelector('#loginRowC').style.display = '';
      submitBtn.textContent = '验 证';
      hintBox.textContent = `检测到异常登录（${res.reason || 'IP 或设备变化'}），验证码已发送到您绑定的邮箱`;
      hintBox.style.display = 'block';
      document.querySelector('.login-back-link').style.display = '';
      setTimeout(() => cInput.focus(), 30);
      submitBtn.disabled = false;
      return;
    }
    // 正常登录
    store.user = { username: res.username || username, role: res.role || '' };
    await loadSettings();
    const app = document.getElementById('app');
    clear(app).appendChild(buildShell());
    if (!location.hash || location.hash === '#/' || location.hash === '#') location.hash = '#/dashboard';
    else handleRoute();
  } catch (err) {
    errBox.textContent = err.message || '登录失败';
    errBox.style.display = 'block';
    submitBtn.disabled = false;
    submitBtn.textContent = '登 录';
  }
}

async function doVerifyChallenge(state, cInput, errBox, hintBox, submitBtn, form) {
  errBox.style.display = 'none';
  const code = cInput.value.trim();
  if (!/^\d{6}$/.test(code)) {
    errBox.textContent = '请输入 6 位数字验证码';
    errBox.style.display = 'block';
    return;
  }
  submitBtn.disabled = true;
  submitBtn.textContent = '验证中…';
  try {
    const res = await api.auth.verifyChallenge(state.challengeId, code);
    store.user = { username: res.username, role: res.role || '' };
    await loadSettings();
    const app = document.getElementById('app');
    clear(app).appendChild(buildShell());
    if (!location.hash || location.hash === '#/' || location.hash === '#') location.hash = '#/dashboard';
    else handleRoute();
  } catch (err) {
    errBox.textContent = err.message || '验证码错误';
    errBox.style.display = 'block';
    submitBtn.disabled = false;
    submitBtn.textContent = '验 证';
  }
}

// 两步验证第二步：密码已通过（login_token），提交认证器动态口令换 session。
async function doVerifyTOTP(state, cInput, errBox, hintBox, submitBtn, form) {
  errBox.style.display = 'none';
  const code = cInput.value.trim();
  if (!/^\d{6}$/.test(code)) {
    errBox.textContent = '请输入 6 位动态口令';
    errBox.style.display = 'block';
    return;
  }
  submitBtn.disabled = true;
  submitBtn.textContent = '验证中…';
  try {
    const res = await api.auth.totpVerify(state.loginToken, code);
    store.user = { username: res.username || state.username, role: res.role || '' };
    await loadSettings();
    const app = document.getElementById('app');
    clear(app).appendChild(buildShell());
    if (!location.hash || location.hash === '#/' || location.hash === '#') location.hash = '#/dashboard';
    else handleRoute();
  } catch (err) {
    errBox.textContent = err.message || '动态口令错误';
    errBox.style.display = 'block';
    submitBtn.disabled = false;
    submitBtn.textContent = '验 证';
    // 连续错误 5 次后端会作废本次登录凭据，需重新输入密码
  }
}

function resetToLogin(state, form, errBox, hintBox, cInput, pInput, uInput, npInput, submitBtn) {
  state.phase = 'login';
  state.challengeId = null;
  state.loginToken = null;
  state.resetUsername = '';
  form.querySelector('#loginRowU').style.display = '';
  form.querySelector('#loginRowP').style.display = '';
  form.querySelector('#loginRowC').style.display = 'none';
  form.querySelector('#loginRowNP').style.display = 'none';
  submitBtn.textContent = '登 录';
  submitBtn.disabled = false;
  errBox.style.display = 'none';
  hintBox.style.display = 'none';
  cInput.value = '';
  pInput.value = '';
  npInput.value = '';
  const back = document.querySelector('.login-back-link');
  if (back) back.style.display = 'none';
  const forgot = document.querySelectorAll('.login-back-link');
  forgot.forEach((el) => { if (el.textContent.includes('忘记密码')) el.style.display = ''; });
  setTimeout(() => uInput.focus(), 30);
}

// ---------- 忘记密码：第 1 步（输 username 申请验证码）----------
function switchToReset1(state, form, errBox, hintBox, uInput, pInput, submitBtn) {
  state.phase = 'reset1';
  form.querySelector('#loginRowU').style.display = '';
  form.querySelector('#loginRowP').style.display = 'none';
  form.querySelector('#loginRowC').style.display = 'none';
  form.querySelector('#loginRowNP').style.display = 'none';
  submitBtn.textContent = '发 送 验 证 码';
  errBox.style.display = 'none';
  hintBox.style.display = 'none';
  const forgot = document.querySelectorAll('.login-back-link');
  forgot.forEach((el) => { if (el.textContent.includes('忘记密码')) el.style.display = 'none'; });
  document.querySelector('.login-back-link:not([class*=forgot])') || document.querySelectorAll('.login-back-link')[1];
  // 显示"返回登录"
  document.querySelectorAll('.login-back-link').forEach((el) => { if (el.textContent.includes('返回')) el.style.display = ''; });
  setTimeout(() => uInput.focus(), 30);
}

async function doResetRequest(state, uInput, errBox, hintBox, submitBtn, form) {
  errBox.style.display = 'none';
  hintBox.style.display = 'none';
  const username = uInput.value.trim();
  if (!username) {
    errBox.textContent = '请输入用户名';
    errBox.style.display = 'block';
    return;
  }
  submitBtn.disabled = true;
  submitBtn.textContent = '发送中…';
  try {
    const r = await api.auth.passwordResetRequest(username);
    state.resetUsername = username;
    state.challengeId = r.challenge_id || null;
    // 即使 challenge_id 为空（用户不存在/无邮箱/无 SMTP）也走 reset2 阶段，
    // 让用户输入邮件验证码。校验时会报错。
    switchToReset2(state, form, errBox, hintBox, submitBtn);
  } catch (err) {
    errBox.textContent = err.message || '申请失败';
    errBox.style.display = 'block';
    submitBtn.disabled = false;
    submitBtn.textContent = '发 送 验 证 码';
  }
}

function switchToReset2(state, form, errBox, hintBox, submitBtn) {
  state.phase = 'reset2';
  form.querySelector('#loginRowU').style.display = 'none';
  form.querySelector('#loginRowP').style.display = 'none';
  form.querySelector('#loginRowC').style.display = '';
  form.querySelector('#loginRowNP').style.display = '';
  submitBtn.textContent = '重 置 密 码';
  submitBtn.disabled = false;
  errBox.style.display = 'none';
  hintBox.textContent = `已向 ${state.resetUsername} 绑定的邮箱发送验证码（如未收到请联系管理员确认 SMTP/邮箱配置）。请输入 6 位验证码 + 新密码。`;
  hintBox.style.display = 'block';
  setTimeout(() => document.getElementById('loginRowC').querySelector('input').focus(), 30);
}

async function doResetConfirm(state, cInput, npInput, errBox, hintBox, submitBtn, form) {
  errBox.style.display = 'none';
  const code = cInput.value.trim();
  const newPwd = npInput.value;
  if (!/^\d{6}$/.test(code)) {
    errBox.textContent = '请输入 6 位数字验证码';
    errBox.style.display = 'block';
    return;
  }
  if (newPwd.length < 8) {
    errBox.textContent = '新密码至少 8 位';
    errBox.style.display = 'block';
    return;
  }
  submitBtn.disabled = true;
  submitBtn.textContent = '重置中…';
  try {
    await api.auth.passwordResetConfirm(
      state.challengeId || '',
      state.resetUsername,
      code,
      newPwd,
    );
    // 成功：返回登录页
    hintBox.style.display = 'none';
    toast('密码已重置，请用新密码登录', 'success', 3600);
    resetToLogin(state, form, errBox, hintBox, cInput, npInput, document.querySelectorAll('input')[0], npInput, submitBtn);
  } catch (err) {
    errBox.textContent = err.message || '重置失败';
    errBox.style.display = 'block';
    submitBtn.disabled = false;
    submitBtn.textContent = '重 置 密 码';
  }
}

async function doLogout() {
  try {
    await api.auth.logout();
  } catch (e) {
    // 即使后端失败也清空本地状态
  }
  store.user = null;
  store.settings = null;
  store.markDirty(false);
  location.hash = '';
  renderLogin();
  toast('已退出登录', 'success');
}

// 面板设置（含版本号），失败不阻塞登录
async function loadSettings() {
  try {
    const res = await api.settings.get();
    store.settings = res.settings || {};
    store.version = store.settings.version || store.settings.panel_version || '';
  } catch (e) {
    store.settings = {};
  }
}

// ---------------------------------------------------------------------------
// 启动
// ---------------------------------------------------------------------------
setUnauthorizedHandler(() => {
  if (!store.user) return;
  store.user = null;
  store.settings = null;
  store.markDirty(false);
  clear(document.getElementById('app'));
  renderLogin();
  toast('登录已过期，请重新登录', 'error');
});

async function boot() {
  const app = document.getElementById('app');
  // /api/auth/me 现在永远返回 200 + {authenticated: bool}，
  // 不再依赖 401 来判定登录态——避免每次刷新都让浏览器
  // 在控制台弹一条红色 Fetch 警告。
  //
  // 整个 boot 必须有兜底：早期版本这里没有 try/catch，
  // 而 `boot()` 又是裸调用（没有 .catch()），于是后端没启动 /
  // 网络中断 / 反代 502 时，api.js 抛出的
  // {status:0, message:'无法连接面板服务，请确认后端已启动'}
  // 会一路穿透到 unhandledrejection，页面上的 .boot-loading
  // 转圈动画**永远停不下来**，用户看不到任何错误文案。
  // 而"后端没起来"恰恰是最常见的运维故障场景。
  let me;
  try {
    me = await api.auth.me();
  } catch (err) {
    clear(app).appendChild(errorState(
      `无法连接面板服务：${err.message || err}`,
      () => location.reload(),
    ));
    return;
  }
  if (!me || !me.authenticated) {
    renderLogin();
    return;
  }
  store.user = { username: me.username, role: me.role };
  try {
    await loadSettings();
  } catch (err) {
    clear(app).appendChild(errorState(
      `加载面板设置失败：${err.message || err}`,
      () => location.reload(),
    ));
    return;
  }
  clear(app).appendChild(buildShell());
  await handleRoute();
}

// 全局未捕获异常兜底。
//
// 项目里所有未捕获异常在 UI 上原本是完全静默的——只写进浏览器控制台，
// 用户完全看不到。计划任务的"启用"开关这类操作会因此出现
// "开关视觉上翻转了、但后端状态与界面不一致"的错觉。
window.addEventListener('unhandledrejection', (e) => {
  const msg = (e.reason && (e.reason.message || e.reason)) || '未知错误';
  console.error('[unhandledrejection]', e.reason);
  toast(`操作失败：${msg}`, 'error', 5000);
});

// 标签页标题跟随路由
window.addEventListener('hashchange', () => {
  const p = parseHash();
  const r = ROUTES.find((x) => x.pattern.test(p));
  document.title = `${r ? r.title : '控制台'} · ZhiweiPanel`;
});

boot();