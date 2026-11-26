// 设置：面板基础设置、运行时信息、nginx 主配置查看与测试、修改密码
import { api, formatBytes, formatDateTime } from '../api.js';
import {
  h, clear, icon, button, tag, field, textInput, numberInput, selectInput,
  textArea, loading, errorState, toast, modal, codeBox, sectionTitle, grid,
  progressBar, checkboxField,
} from '../components.js';
import { store } from '../app.js';

// 设置项元数据：键 → 中文标签 / 类型 / 说明
const SETTING_FIELDS = [
  { key: 'default_php_version', label: '默认 PHP 版本', type: 'php', hint: '新建站点时的默认 PHP 版本（向导与 API 都生效）' },
  { key: 'default_site_root', label: '默认站点根目录', type: 'text', hint: '新建站点根目录的默认父路径，如 /www/wwwroot' },
  { key: 'log_retention_days', label: '日志保留天数', type: 'number', hint: '保留项：当前版本仅记录，站点日志保留仍按站点配置' },
  { key: 'timezone', label: '时区', type: 'select', options: ['Asia/Shanghai', 'Asia/Chongqing', 'UTC', 'Asia/Tokyo', 'Asia/Singapore', 'Europe/London', 'America/New_York'], hint: '保留项：当前版本仅记录，界面时间取浏览器时区' },
];

// 面板访问安全：仅浏览器会话可改（后端强制），保存后立即生效。
const PANEL_SECURITY_FIELDS = ['panel_path', 'panel_ip_allowlist', 'panel_port', 'panel_bind'];

export async function render(container) {
  clear(container);
  container.appendChild(loading('正在加载设置…'));

  let settings = {}, status = null, sysInfo = null;
  try {
    const [s, st, inf] = await Promise.all([
      api.settings.get(),
      api.system.status().catch(() => null),
      api.system.info().catch(() => null),
    ]);
    settings = s.settings || {};
    status = st;
    sysInfo = inf || {};
  } catch (err) {
    clear(container).appendChild(errorState(`设置加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  // 编辑副本：点保存才提交
  const draft = { ...settings };
  for (const k of PANEL_SECURITY_FIELDS) {
    if (draft[k] === undefined || draft[k] === null) draft[k] = '';
  }
  const panelInfo = (sysInfo && sysInfo.panel) || {};
  const clientIP = (sysInfo && sysInfo.client_ip) || '';
  // 后端 SystemStatus.PHP 是 []PHPVersion 数组，不是带 versions 字段的对象。
  // 早期读 status.php.versions 恒为 undefined，PHP 下拉只能走写死的兜底列表，
  // 设置页的运行时信息也恒显示"未检测到 PHP"。
  const phpVersions = (status && Array.isArray(status.php))
    ? status.php.map((p) => String(p && p.version)).filter(Boolean)
    : []

  clear(container);
  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '设置'),
      h('div', { class: 'page-sub' }, `面板版本 ${store.version ? 'v' + store.version : '未知'}`),
    ]),
    h('div', { class: 'page-actions' }, [
      button('修改密码', showChangePassword, 'default', 'key'),
      button('保存设置', doSave, 'primary', 'save'),
    ]),
  ]));

  // ---------------- 面板访问安全（v0.9.0） ----------------
  const accessLine = h('div', { class: 'runtime-brief' });
  const pathInp = textInput(draft.panel_path, (v) => { draft.panel_path = v; updateAccessPreview(); }, {
    placeholder: '如 zw-8f3k2a（留空 = 不启用）',
  });
  const ipInp = textArea(draft.panel_ip_allowlist, (v) => { draft.panel_ip_allowlist = v; }, {
    rows: 4, mono: true, placeholder: '10.0.0.0/8\n192.168.1.10',
  });
  const portInp = numberInput(draft.panel_port, (v) => { draft.panel_port = v; updateAccessPreview(); }, {
    min: 1, max: 65535, placeholder: `当前 ${panelInfo.port || '-'}`,
  });
  const bindInp = textInput(draft.panel_bind, (v) => { draft.panel_bind = v; }, {
    placeholder: `当前 ${panelInfo.bind || '0.0.0.0'}`,
  });

  function normalizedPath() {
    return String(pathInp.value || '').trim().replace(/^\/+|\/+$/g, '');
  }
  function currentPort() {
    const n = parseInt(portInp.value, 10);
    return n > 0 ? n : (Number(panelInfo.port) || 0);
  }
  // 按"协议 + 当前主机 + 端口 + 入口路径"拼出可点击地址。
  function buildAccessURL(path, port) {
    let host = location.hostname;
    if (host.includes(':') && !host.startsWith('[')) host = '[' + host + ']';
    const defaultPort = location.protocol === 'https:' ? 443 : 80;
    const p = Number(port) || 0;
    const portPart = p && p !== defaultPort ? ':' + p : '';
    const pth = String(path || '').replace(/^\/+|\/+$/g, '');
    return `${location.protocol}//${host}${portPart}/${pth ? pth + '/' : ''}`;
  }
  function updateAccessPreview() {
    clear(accessLine);
    const active = String(panelInfo.path || '');
    accessLine.appendChild(h('span', {}, '当前生效：'));
    accessLine.appendChild(active
      ? tag(`/${active}/`, 'success')
      : tag('根路径（未启用安全入口）', 'default'));
    const draftPath = normalizedPath();
    if (draftPath !== active) {
      accessLine.appendChild(h('span', { class: 'field-hint' }, `保存后：${buildAccessURL(draftPath, currentPort())}`));
    }
    accessLine.appendChild(h('span', { class: 'field-hint' }, 'API 始终为 /api，安全入口只影响浏览器界面。'));
  }

  const genPathBtn = button('随机生成', () => {
    pathInp.value = randomPanelPath();
    draft.panel_path = pathInp.value;
    updateAccessPreview();
    toast('已生成新的安全入口，保存后生效', 'success');
  }, 'default', 'refresh');
  const copyAccessBtn = button('复制当前地址', () => {
    copyText(buildAccessURL(panelInfo.path || '', panelInfo.port));
  }, 'ghost', 'download');
  const addIPBtn = button('加入当前 IP', () => {
    if (!clientIP) {
      toast('无法获取当前客户端 IP', 'error');
      return;
    }
    const lines = String(ipInp.value || '').split('\n').map((s) => s.trim()).filter(Boolean);
    if (!lines.includes(clientIP)) {
      lines.push(clientIP);
      ipInp.value = lines.join('\n');
      draft.panel_ip_allowlist = ipInp.value;
      toast(`已加入 ${clientIP}`, 'success');
    } else {
      toast('当前 IP 已在白名单中', 'info');
    }
  }, 'default', 'plus');

  const securityGrid = h('div', { class: 'settings-grid' }, [
    field('安全入口路径', h('div', {}, [
      pathInp,
      h('div', { class: 'inline-actions' }, [genPathBtn, copyAccessBtn]),
    ]), '2~32 位字母、数字、下划线或短横线；留空 = 不启用。根路径会返回 404。'),
    field('IP 白名单', ipInp, `一行一个 IP 或 CIDR；留空 = 不限制。当前客户端 IP：${clientIP || '未知'}；回环地址始终放行。`, true),
    field('面板端口', portInp, '重启面板后生效；Docker 部署还需同步 compose 端口映射。'),
    field('绑定地址', bindInp, '空 = 所有网卡；填 127.0.0.1 则只允许本机访问（重启后生效）。'),
  ]);
  container.appendChild(h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, '面板访问安全'),
      h('div', { class: 'card-title-extra' }, [addIPBtn]),
    ]),
    h('div', { class: 'card-body' }, [
      accessLine,
      securityGrid,
      h('div', { class: 'field-hint' }, '安全入口与 IP 白名单保存后立即生效；端口/绑定地址需重启面板。仅浏览器会话可修改这三类设置，API Token 无权限。'),
    ]),
  ]));
  updateAccessPreview();

  // ---------------- 面板设置 ----------------
  const fieldsBox = h('div', { class: 'settings-grid' });
  for (const f of SETTING_FIELDS) {
    let ctrl;
    const value = draft[f.key] !== undefined && draft[f.key] !== null ? draft[f.key] : '';
    if (f.type === 'number') {
      ctrl = numberInput(value, (v) => { draft[f.key] = v; }, { min: 0 });
    } else if (f.type === 'select') {
      ctrl = selectInput(value, f.options, (v) => { draft[f.key] = v; });
    } else if (f.type === 'php') {
      const opts = phpVersions.length
        ? phpVersions.map((v) => ({ value: v, label: `PHP ${v}` }))
        : [{ value: value || '8.2', label: `PHP ${value || '8.2'}` }];
      if (value && !opts.some((o) => o.value === value)) opts.unshift({ value, label: `PHP ${value}` });
      ctrl = selectInput(value, opts, (v) => { draft[f.key] = v; });
    } else {
      ctrl = textInput(value, (v) => { draft[f.key] = v; }, { placeholder: f.type === 'text' ? '留空使用默认' : '' });
    }
    fieldsBox.appendChild(field(f.label, ctrl, f.hint));
  }
  container.appendChild(h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, h('span', {}, '面板设置')),
    h('div', { class: 'card-body' }, fieldsBox),
  ]));

  // ---------------- 运行时信息 ----------------
  const statusBox = h('div', { class: 'card-body' });
  container.appendChild(h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, '运行时信息'),
      h('div', { class: 'card-title-extra' }, button('刷新', () => render(container), 'ghost', 'refresh')),
    ]),
    statusBox,
  ]));
  drawStatus(statusBox, status);

  // ---------------- nginx 主配置 ----------------
  const nginxBox = h('div', { class: 'card-body' }, loading('加载 nginx 主配置…'));
  const nginxCard = h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, 'nginx 主配置'),
      h('div', { class: 'card-title-extra' }, [
        button('查看配置', showNginxConfig, 'default', 'eye'),
        button('测试配置', doNginxTest, 'primary', 'check'),
      ]),
    ]),
    nginxBox,
  ]);
  container.appendChild(nginxCard);

  // 配置测试结果条
  const testBar = h('div', { class: 'test-result' }, [
    icon('info'), h('span', {}, '尚未执行配置测试'),
  ]);

  clear(nginxBox).appendChild(h('div', {}, [
    h('div', { class: 'runtime-brief' }, [
      status && status.nginx ? tag(status.nginx.installed ? '已安装' : '未安装', status.nginx.installed ? 'success' : 'danger') : null,
      status && status.nginx ? tag(status.nginx.running ? '运行中' : '未运行', status.nginx.running ? 'success' : 'warning') : null,
      status && status.nginx && status.nginx.version ? tag(status.nginx.version, 'info') : null,
      testBar,
    ]),
  ]));

  async function showNginxConfig() {
    clear(nginxBox).appendChild(loading('加载 nginx 主配置…'));
    try {
      const conf = await api.system.nginxConfig();
      const text = typeof conf === 'string' ? conf : (conf.nginx_conf || conf.config || JSON.stringify(conf, null, 2));
      clear(nginxBox).appendChild(h('div', {}, [
        h('div', { class: 'field-hint' }, 'nginx 主配置文件内容（只读）'),
        codeBox(text, { maxHeight: '420px' }),
      ]));
    } catch (e) {
      clear(nginxBox).appendChild(errorState(`nginx 配置加载失败：${e.message || e}`, () => showNginxConfig()));
    }
  }

  async function doNginxTest() {
    clear(testBar);
    testBar.className = 'test-result';
    testBar.appendChild(h('span', { class: 'spinner spinner-sm' }));
    testBar.appendChild(h('span', {}, '正在测试配置…'));
    try {
      const res = await api.system.nginxTest();
      clear(testBar);
      testBar.className = `test-result ${res.ok ? 'ok' : 'fail'}`;
      testBar.appendChild(icon(res.ok ? 'check' : 'warning'));
      testBar.appendChild(h('span', {}, res.ok ? `配置检测通过${res.message ? '：' + res.message : ''}` : `配置检测未通过：${res.message || '未知错误'}`));
      toast(res.ok ? 'nginx 配置正常' : 'nginx 配置存在问题', res.ok ? 'success' : 'error');
    } catch (e) {
      clear(testBar);
      testBar.className = 'test-result fail';
      testBar.appendChild(icon('warning'));
      testBar.appendChild(h('span', {}, `测试失败：${e.message || e}`));
    }
  }

  // ---------------- 保存 ----------------
  async function doSave() {
    // 仅提交已知设置键，避免覆盖其他运行时写入的键。
    // 值一律转成字符串：后端 settings 表是 string->string，
    // 传 JSON number 会让整个 map 解码失败，一次保存全 400。
    const payload = {};
    for (const f of SETTING_FIELDS) {
      if (draft[f.key] === undefined || draft[f.key] === null) continue;
      payload[f.key] = String(draft[f.key]);
    }
    for (const k of PANEL_SECURITY_FIELDS) {
      let v = draft[k];
      // 端口留空/0 = 清除设置（回落到启动参数），不能把 "0" 发给后端。
      if (k === 'panel_port' && (!v || Number(v) <= 0)) v = '';
      payload[k] = String(v === undefined || v === null ? '' : v);
    }

    const oldPath = String(settings.panel_path || '').replace(/^\/+|\/+$/g, '');
    const oldPort = Number(settings.panel_port) || 0;
    const oldBind = String(settings.panel_bind || '');

    try {
      const res = await api.settings.put(payload);
      const newPath = res && res.panel_path !== undefined
        ? String(res.panel_path || '')
        : normalizedPath();
      const newPort = Number(payload.panel_port) || 0;

      Object.assign(settings, payload);
      settings.panel_path = newPath;
      if (store.settings) Object.assign(store.settings, payload, { panel_path: newPath });
      draft.panel_path = newPath;

      if (newPath !== oldPath) {
        showAccessChanged(newPath, newPort);
      } else if (newPort !== oldPort || String(payload.panel_bind) !== oldBind) {
        toast('设置已保存；端口/绑定地址需重启面板后生效', 'info', 4200);
      } else {
        toast('设置已保存', 'success');
      }
    } catch (e) {
      toast(e.message || '保存失败', 'error');
    }
  }

  // 安全入口保存后旧地址立即 404，必须把新地址明确交给用户，
  // 并给一个一键跳转，避免"保存成功、自己也进不去"。
  function showAccessChanged(path, savedPort) {
    const url = buildAccessURL(path, panelInfo.port);
    const m = modal({
      title: '安全入口已更新',
      width: '560px',
      content: h('div', {}, [
        h('p', {}, path
          ? '旧入口已立即失效（根路径返回 404）。新的访问地址：'
          : '安全入口已关闭，面板回到根路径访问：'),
        codeBox(url, { maxHeight: '90px' }),
        h('p', { class: 'field-hint' }, 'API 仍为 /api，不受安全入口影响。请更新收藏夹。'),
        (savedPort && Number(savedPort) !== Number(panelInfo.port))
          ? h('p', { class: 'field-hint' }, `端口已保存为 ${savedPort}，需重启面板后生效；上面的地址仍使用当前端口。`)
          : null,
        h('p', { class: 'field-hint' }, '如果忘记入口：SSH 启动时加 -panel-path=<新路径> 可强制覆盖恢复。'),
      ]),
      footer: [
        button('稍后', () => m.close(), 'default'),
        button('复制新地址', () => copyText(url), 'default', 'download'),
        button('打开新入口', () => {
          location.href = url + (location.hash || '#/dashboard');
        }, 'primary', 'check'),
      ],
    });
  }

  // ---------------- 修改密码 ----------------
  function showChangePassword() {
    const oldPwd = textInput('', null, { type: 'password', placeholder: '当前密码' });
    const newPwd = textInput('', null, { type: 'password', placeholder: '新密码（至少 8 位）' });
    const confirmPwd = textInput('', null, { type: 'password', placeholder: '再次输入新密码' });
    const errBox = h('div', { class: 'login-error', style: { display: 'none' } });

    const m = modal({
      title: '修改密码',
      width: '460px',
      content: h('div', {}, [
        errBox,
        field('当前密码', oldPwd),
        field('新密码', newPwd),
        field('确认新密码', confirmPwd),
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('确认修改', async () => {
          errBox.style.display = 'none';
          if (!oldPwd.value) {
            errBox.textContent = '请输入当前密码';
            errBox.style.display = 'block';
            return;
          }
          if (newPwd.value.length < 8) {
            errBox.textContent = '新密码至少 8 位';
            errBox.style.display = 'block';
            return;
          }
          if (newPwd.value !== confirmPwd.value) {
            errBox.textContent = '两次输入的新密码不一致';
            errBox.style.display = 'block';
            return;
          }
          try {
            await api.auth.changePassword(oldPwd.value, newPwd.value);
            toast('密码修改成功', 'success');
            m.close();
          } catch (e) {
            errBox.textContent = e.message || '修改失败';
            errBox.style.display = 'block';
          }
        }, 'primary'),
      ],
    });
  }

  // ---------------- 两步验证（TOTP） ----------------
  const twofaBox = h('div', { class: 'card-body' }, loading('加载两步验证状态…'));
  container.appendChild(h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, h('span', {}, '两步验证（TOTP）')),
    twofaBox,
  ]));

  // 初始加载一次。v0.8.x 这里漏了调用，设置页会永远停在
  // "加载两步验证状态…"，开启/关闭按钮根本不出现。
  draw2FA();

  async function draw2FA() {
    clear(twofaBox);
    let enabled = false;
    let recoveryRemaining = 0;
    try {
      const st = await api.auth.status2FA();
      enabled = !!st.enabled;
      recoveryRemaining = Number(st.recovery_remaining) || 0;
    } catch (e) {
      clear(twofaBox).appendChild(errorState(e.message || '加载失败', () => draw2FA()));
      return;
    }
    clear(twofaBox).appendChild(h('div', {}, [
      h('div', { class: 'runtime-brief' }, [
        enabled ? tag('已启用', 'success') : tag('未启用', 'warning'),
        h('span', { class: 'field-hint' }, enabled
          ? '登录时除密码外还需输入认证器 App 的 6 位动态口令；手机不在身边时也可输入恢复码。'
          : '开启后，登录除密码外还需输入认证器 App（Google Authenticator 等）的 6 位动态口令。'),
      ]),
      h('div', { class: 'inline-actions' }, [
        enabled
          ? button('关闭两步验证', show2FADisable, 'danger', 'lock')
          : button('开启两步验证', show2FASetup, 'primary', 'key'),
      ]),
      // v0.8.0: 恢复码区——启用后才出现。手机丢失/损坏时的自救通道。
      enabled
        ? h('div', { class: 'runtime-brief' }, [
            h('span', {}, `恢复码：剩余 ${recoveryRemaining} 枚未使用`),
            h('span', { class: 'field-hint' }, '登录界面输入恢复码即可替代动态口令，每枚仅能用一次。'),
            button('重新生成恢复码', showRecoveryRegenerate, 'default', 'refresh'),
          ])
        : null,
    ].filter(Boolean)));
  }

  // 开启流程：setup 拿密钥 → 用户扫码/录入 → 提交首码 → enable。
  // 密钥只在 setup 响应里出现一次，关闭弹窗后需重新 setup（后端会覆盖 pending 密钥）。
  async function show2FASetup() {
    let setup = null;
    try {
      setup = await api.auth.setup2FA();
    } catch (e) {
      toast(e.message || '生成密钥失败', 'error');
      return;
    }
    const codeInput = textInput('', null, { placeholder: '6 位动态口令' });
    const errBox = h('div', { class: 'login-error', style: { display: 'none' } });
    const m = modal({
      title: '开启两步验证',
      width: '560px',
      content: h('div', {}, [
        h('div', { class: 'field-hint' }, '第 1 步：用认证器 App（Google Authenticator 等）扫描下面链接，或手动录入密钥：'),
        codeBox(setup.secret, {}),
        h('div', { class: 'field-hint' }, [
          h('span', {}, 'otpauth 链接：'),
          h('a', { href: setup.otpauth, class: 'mono-text' }, setup.otpauth),
        ]),
        h('div', { class: 'field-hint' }, '第 2 步：输入认证器显示的 6 位动态口令完成启用：'),
        field('动态口令', codeInput),
        errBox,
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('确认启用', async () => {
          errBox.style.display = 'none';
          try {
            const res = await api.auth.enable2FA(codeInput.value.trim());
            m.close();
            await draw2FA();
            // v0.8.0: 启用成功即发放恢复码——明文只在本次响应里，
            // 必须当场让用户抄录保存（之后只能重新生成）。
            if (res && Array.isArray(res.recovery_codes) && res.recovery_codes.length) {
              showRecoveryCodes(res.recovery_codes, false);
            } else {
              toast('两步验证已启用，下次登录需要输入动态口令', 'success');
            }
          } catch (e) {
            errBox.textContent = e.message || '启用失败';
            errBox.style.display = 'block';
          }
        }, 'primary'),
      ],
    });
  }

  // 展示只出现一次的恢复码明文（启用后 / 重新生成后共用）。
  // regenerated=true 时强调旧码已全部作废。
  function showRecoveryCodes(codes, regenerated) {
    let acked = false;
    const ack = checkboxField('我已将恢复码抄录/复制到安全的地方保存', false, (v) => { acked = v; });
    const m = modal({
      title: regenerated ? '恢复码已重新生成' : '保存恢复码',
      width: '520px',
      content: h('div', {}, [
        h('div', { class: 'field-hint' }, regenerated
          ? '旧恢复码已全部作废。下面是新的 10 枚恢复码：'
          : '手机丢失或损坏时，登录界面可输入恢复码替代动态口令。每枚仅能用一次，请立即保存：'),
        codeBox(codes.join('\n'), { maxHeight: '200px' }),
        h('div', { class: 'field-hint' }, '恢复码只显示这一次，关闭本窗后无法再次查看（可随时重新生成，但旧码会作废）。'),
        ack,
      ]),
      footer: [
        button('关闭', () => m.close(), 'default'),
        button('我已保存', () => {
          if (!acked) {
            toast('请先勾选确认已保存', 'warning');
            return;
          }
          m.close();
        }, 'primary'),
      ],
    });
  }

  // 重新生成恢复码：POST 是整体替换语义，旧码立即作废——先确认再执行。
  async function showRecoveryRegenerate() {
    const m = modal({
      title: '重新生成恢复码',
      width: '460px',
      content: h('div', {}, [
        h('div', { class: 'field-hint' }, '重新生成会使旧的 10 枚恢复码全部立即作废。确认继续？'),
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('确认重新生成', async () => {
          m.close();
          try {
            const res = await api.auth.recovery2FA();
            if (res && Array.isArray(res.recovery_codes) && res.recovery_codes.length) {
              showRecoveryCodes(res.recovery_codes, true);
              await draw2FA();
            } else {
              toast('重新生成失败：响应异常', 'error');
            }
          } catch (e) {
            toast(e.message || '重新生成失败', 'error');
          }
        }, 'danger'),
      ],
    });
  }

  // 关闭流程：要求出示当前有效口令——能出示动态口令的人才有资格降级防护。
  async function show2FADisable() {
    const codeInput = textInput('', null, { placeholder: '6 位动态口令' });
    const errBox = h('div', { class: 'login-error', style: { display: 'none' } });
    const m = modal({
      title: '关闭两步验证',
      width: '460px',
      content: h('div', {}, [
        h('div', { class: 'field-hint' }, '关闭后登录仅需密码。请输入认证器当前的 6 位动态口令确认操作：'),
        field('动态口令', codeInput),
        errBox,
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('确认关闭', async () => {
          errBox.style.display = 'none';
          try {
            await api.auth.disable2FA(codeInput.value.trim());
            toast('两步验证已关闭', 'success');
            m.close();
            await draw2FA();
          } catch (e) {
            errBox.textContent = e.message || '关闭失败';
            errBox.style.display = 'block';
          }
        }, 'danger'),
      ],
    });
  }

  function drawStatus(box, st) {
    clear(box);
    if (!st) {
      box.appendChild(errorState('运行时信息获取失败'));
      return;
    }
    const nginx = st.nginx || {};
    const docker = st.docker || {};
    const disk = st.disk || {};
    const diskPercent = disk.total ? Math.round((disk.used / disk.total) * 100) : 0;

    // nginx
    const nginxRows = [
      ['是否安装', nginx.installed ? tag('已安装', 'success') : tag('未安装', 'danger')],
      ['运行状态', nginx.running ? tag('运行中', 'success') : tag('未运行', 'warning')],
      ['版本', h('span', { class: 'mono-text' }, nginx.version || '-')],
      ['配置检测', nginx.config_ok === true ? tag('正常', 'success') : nginx.config_ok === false ? tag('有错误', 'danger') : tag('未检测', 'default')],
    ];
    if (nginx.error) nginxRows.push(['错误信息', h('span', { class: 'err-text' }, nginx.error)]);

    // 运行时
    const runtimeRows = [
      ['运行模式', tag(st.runtime === 'docker' ? 'Docker 容器' : '宿主机', st.runtime === 'docker' ? 'info' : 'default')],
      ['Docker 可用', docker.available ? tag('可用', 'success') : tag('不可用', 'default')],
      ['Docker 容器', docker.containers && docker.containers.length
        ? h('div', { class: 'chip-row' }, docker.containers.map((c) => tag(typeof c === 'string' ? c : c.name || c.image || '-', 'default')))
        : h('span', { class: 'muted' }, '-')],
      ['PHP 版本', phpVersions.length
        ? h('div', { class: 'chip-row' }, phpVersions.map((v) => tag(`PHP ${v}`, 'info')))
        : h('span', { class: 'muted' }, '未检测到 PHP')],
    ];

    // 磁盘
    const diskRows = [
      ['总容量', formatBytes(disk.total || 0)],
      ['已用', formatBytes(disk.used || 0)],
      ['可用', formatBytes(disk.free || 0)],
      ['占用率', h('div', { class: 'disk-cell' }, [
        h('span', {}, `${diskPercent}%`),
        progressBar(diskPercent, diskPercent > 85 ? 'bar-red' : diskPercent > 70 ? 'bar-yellow' : 'bar-green'),
      ])],
    ];

    const runtimeCard = (title, rows, hint) => h('div', { class: 'runtime-card' }, [
      h('div', { class: 'runtime-card-title' }, title, hint ? h('span', { class: 'field-hint' }, hint) : null),
      h('div', { class: 'runtime-list' }, rows.map(([k, v]) =>
        h('div', { class: 'runtime-row' }, [h('span', { class: 'runtime-k' }, k), h('span', { class: 'runtime-v' }, v)])
      )),
    ]);

    box.appendChild(h('div', { class: 'grid grid-3' }, [
      runtimeCard('nginx', nginxRows),
      runtimeCard('运行时', runtimeRows),
      runtimeCard('磁盘', diskRows),
    ]));
  }

  // ---------------- 管理员账户（邮箱 + 最近登录） ----------------
  const accountCard = h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, '管理员账户'),
      h('div', { class: 'card-title-extra' }, button('保存邮箱', saveAccountEmail, 'primary', 'save')),
    ]),
    h('div', { class: 'card-body' }, [loading('正在加载…')]),
  ]);
  container.appendChild(accountCard);
  const accountBody = accountCard.querySelector('.card-body');

  // SMTP 卡片：先占位，邮件账户保存后供异常登录通知使用
  const smtpCard = h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, 'SMTP 邮件发送（异常登录通知 + 验证码）'),
      h('div', { class: 'card-title-extra' }, [
        button('发测试邮件', smtpTestSend, 'default', 'mail'),
        button('保存配置', smtpSave, 'primary', 'save'),
      ]),
    ]),
    h('div', { class: 'card-body' }, [loading('正在加载…')]),
  ]);
  container.appendChild(smtpCard);
  const smtpBody = smtpCard.querySelector('.card-body');

  // ---------------- 流量统计维护（v0.9.0） ----------------
  container.appendChild(h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, '流量统计维护'),
      h('div', { class: 'card-title-extra' }, button('清空统计', showResetTraffic, 'danger', 'trash')),
    ]),
    h('div', { class: 'card-body' }, [
      h('div', { class: 'field-hint' }, 'v0.8.1 及以前的采集每 5 分钟重读最近 5 万行并累加，历史请求数/流量会偏高。v0.9.0 起改为按日志偏移增量采集，新数据准确；清空后可从一个干净的起点重新统计。'),
    ]),
  ]));

  function showResetTraffic() {
    const confirmInput = textInput('', null, { placeholder: '输入 RESET' });
    const err = h('div', { class: 'login-error', style: { display: 'none' } });
    const m = modal({
      title: '清空流量统计',
      width: '500px',
      content: h('div', {}, [
        h('p', {}, '将删除所有站点的日汇总、UV 明细、状态码明细与采集 checkpoint。下一轮采集从当前日志末尾开始，不会把旧日志重新算一遍。此操作不可撤销。'),
        field('确认词', confirmInput, '输入 RESET 才会执行'),
        err,
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('确认清空', async () => {
          if (confirmInput.value.trim() !== 'RESET') {
            err.textContent = '请输入 RESET';
            err.style.display = 'block';
            return;
          }
          try {
            await api.system.resetTraffic('RESET');
            toast('流量统计已清空', 'success');
            m.close();
          } catch (e) {
            err.textContent = e.message || '清空失败';
            err.style.display = 'block';
          }
        }, 'danger'),
      ],
    });
  }

  // 加载账户 + SMTP
  let account = null;
  let smtp = null;
  try {
    [account, smtp] = await Promise.all([
      api.account.get(),
      api.smtp.get().catch((e) => ({ _err: e.message })),
    ]);
  } catch (e) {
    // 账户必须加载成功；SMTP 失败不阻塞
    clear(accountBody).appendChild(errorState(`账户加载失败：${e.message || e}`));
    return;
  }
  drawAccountForm(accountBody, account);
  drawSmtpForm(smtpBody, smtp);

  // ---------- 账户邮箱表单 ----------
  function drawAccountForm(box, prof) {
    clear(box);
    const emailInput = textInput(prof.email || '', null, { placeholder: 'admin@example.com' });
    const lastIP = prof.last_login_ip || '-';
    const lastUA = prof.last_login_ua || '-';
    const lastAt = prof.last_login_at ? formatDateTime(prof.last_login_at) : '-';
    const emailStatus = prof.email
      ? (prof.email_verified ? tag('已验证', 'success') : tag('未验证', 'warning'))
      : tag('未绑定', 'default');
    box.appendChild(h('div', { class: 'settings-grid' }, [
      field('用户名', h('span', { class: 'mono-text' }, prof.username || '-')),
      field('角色', h('span', {}, prof.role || '-')),
      field('邮箱', emailInput, '用于异常登录通知、密码重置。需点击"发送验证邮件"完成验证。'),
      field('邮箱状态', emailStatus),
      field('最近登录 IP', h('span', { class: 'mono-text' }, lastIP)),
      field('最近登录时间', h('span', {}, lastAt)),
      field('最近登录 UA', h('span', { class: 'mono-text', style: { wordBreak: 'break-all' } }, lastUA)),
    ]));
    // 验证按钮单独放
    const verifyBtn = button('发送验证邮件', async () => {
      try {
        verifyBtn.disabled = true;
        const r = await api.account.sendEmailVerify();
        toast('验证邮件已发送到 ' + (prof.email || emailInput.value), 'success');
        // 提示用户去收件箱取 6 位验证码，弹窗输入
        promptVerifyCode(r.challenge_id, () => {
          // 验证后刷新状态
          api.account.get().then((p) => { prof = p; drawAccountForm(box, p); });
        });
      } catch (e) {
        toast('发送失败：' + (e.message || e), 'error', 4800);
      } finally {
        verifyBtn.disabled = false;
      }
    }, 'default', 'mail');
    box.appendChild(h('div', { class: 'settings-actions' }, [verifyBtn]));
  }

  function promptVerifyCode(challengeId, onOk) {
    const codeInput = textInput('', null, { placeholder: '6 位数字', maxlength: 6 });
    const errBox = h('div', { class: 'login-error', style: { display: 'none' } });
    const m = modal({
      title: '邮箱验证',
      content: h('div', {}, [
        errBox,
        h('div', { class: 'field-hint', style: { marginBottom: '8px' } }, '请到邮箱查收 6 位验证码，并在下方输入。'),
        field('验证码', codeInput),
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('提交', async () => {
          errBox.style.display = 'none';
          try {
            const r = await api.auth.verifyChallenge(challengeId, codeInput.value.trim());
            if (r && r.ok) {
              toast('邮箱已验证', 'success');
              m.close();
              if (onOk) onOk();
            } else {
              errBox.textContent = '验证码错误';
              errBox.style.display = 'block';
            }
          } catch (e) {
            errBox.textContent = e.message || '验证失败';
            errBox.style.display = 'block';
          }
        }, 'primary'),
      ],
    });
  }

  async function saveAccountEmail() {
    const emailInput = box_emailInput();
    if (!emailInput) return;
    try {
      await api.account.setEmail(emailInput.value.trim());
      toast('邮箱已保存（需点击"发送验证邮件"完成验证）', 'success');
      const p = await api.account.get();
      drawAccountForm(accountBody, p);
    } catch (e) {
      toast(e.message || '保存失败', 'error');
    }
  }
  function box_emailInput() {
    // 取当前 email 输入框的值（页面只有一个）
    const inputs = accountBody.querySelectorAll('input');
    return inputs.length ? inputs[0] : null;
  }

  // ---------- SMTP 表单 ----------
  function drawSmtpForm(box, cfg) {
    clear(box);
    if (cfg && cfg._err) {
      box.appendChild(h('div', { class: 'field-hint' }, 'SMTP 配置加载失败：' + cfg._err));
    }
    const hostInp = textInput(cfg.host || '', null, { placeholder: 'smtp.example.com' });
    const portInp = textInput(cfg.port ? String(cfg.port) : '587', null, { placeholder: '25 / 465 / 587' });
    const userInp = textInput(cfg.user || '', null, { placeholder: 'SMTP 认证用户' });
    const passInp = textInput('', null, { type: 'password', placeholder: cfg.password ? '留空保留原密码' : 'SMTP 密码' });
    const fromInp = textInput(cfg.from || '', null, { placeholder: '[email protected]' });
    const fromNameInp = textInput(cfg.from_name || '', null, { placeholder: '智维面板' });
    const encInp = selectInput(cfg.encryption || 'starttls', [
      { value: 'starttls', label: 'STARTTLS（587）' },
      { value: 'ssl', label: 'SSL / TLS（465）' },
      { value: 'none', label: '明文（25，内网）' },
    ]);
    const statusTag = cfg.configured ? tag('已配置', 'success') : tag('未配置', 'default');
    box.appendChild(h('div', { class: 'settings-grid' }, [
      field('状态', statusTag),
      field('SMTP 服务器', hostInp),
      field('端口', portInp),
      field('认证用户', userInp),
      field('认证密码', passInp),
      field('发件邮箱', fromInp),
      field('发件人显示名', fromNameInp),
      field('加密方式', encInp),
    ]));
    // 暴露给 smtpSave / smtpTestSend
    box._getCfg = () => ({
      host: hostInp.value.trim(),
      port: parseInt(portInp.value, 10) || 0,
      user: userInp.value.trim(),
      password: passInp.value,
      from: fromInp.value.trim(),
      from_name: fromNameInp.value.trim(),
      encryption: encInp.value,
    });
  }

  async function smtpSave() {
    const cfg = smtpBody._getCfg ? smtpBody._getCfg() : null;
    if (!cfg) return;
    try {
      await api.smtp.set(cfg);
      toast('SMTP 配置已保存', 'success');
      // 重新拉一次刷新状态徽章
      const s = await api.smtp.get();
      drawSmtpForm(smtpBody, s);
    } catch (e) {
      toast(e.message || '保存失败', 'error');
    }
  }

  async function smtpTestSend() {
    const to = prompt('输入测试收件邮箱：', '');
    if (!to) return;
    try {
      await api.smtp.test(to);
      toast('测试邮件已发送，请检查 ' + to + ' 收件箱', 'success', 3600);
    } catch (e) {
      toast('发送失败：' + (e.message || e), 'error', 4800);
    }
  }
}

// 生成随机安全入口：前缀 zw- + 10 位无歧义字符（去掉 i/l/o/0/1）。
function randomPanelPath() {
  const abc = 'abcdefghjkmnpqrstuvwxyz23456789';
  const buf = new Uint8Array(10);
  if (window.crypto && crypto.getRandomValues) {
    crypto.getRandomValues(buf);
  } else {
    for (let i = 0; i < buf.length; i++) buf[i] = Math.floor(Math.random() * 256);
  }
  let s = 'zw-';
  for (const b of buf) s += abc[b % abc.length];
  return s;
}

// 复制文本：优先 Clipboard API（HTTPS/localhost），HTTP 内网地址回退 execCommand。
async function copyText(text) {
  if (!text) return;
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      toast('已复制', 'success');
      return;
    }
  } catch (e) { /* 回退到 execCommand */ }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    toast(ok ? '已复制' : '复制失败，请手动选择文本', ok ? 'success' : 'error');
  } catch (e) {
    toast('复制失败，请手动选择文本', 'error');
  }
}