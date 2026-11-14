// 运维中心：API Token / 操作审计 / nginx 配置历史
// v0.2.2 —— 借鉴 1Panel 的操作日志与配置快照、cenvero-fleet 的审计 journal
import { api, formatDateTime, timeAgo, formatBytes } from '../api.js';
import {
  h, clear, button, tag, table, loading, errorState, emptyState, iconButton,
  confirmDialog, toast, modal, field, textInput, selectInput, grid,
  sectionTitle, codeBox, checkboxField,
} from '../components.js';

const state = {
  tokens: [],
  logs: [],
  logTotal: 0,
  history: [],
};

// API Token 权限位的中文名与配色。
//
// 后端只认三个值：read / write / shell（见 internal/api/tokens.go）。
// 早期前端只渲染 read / write 两档，遇到 shell 一律显示成"只读"——
// 界面上完全看不出这个 Token 手里握着 root 执行能力。
const SCOPE_LABELS = {
  read: '只读',
  write: '可写',
  shell: 'Shell',
};
const SCOPE_COLORS = {
  read: 'info',
  write: 'warning',
  shell: 'danger',
};

const TABS = [
  { key: 'tokens', label: 'API Token' },
  { key: 'audit', label: '操作审计' },
  { key: 'history', label: '配置历史' },
];

// currentTab 从 hash 里取当前标签页。
//
// 用 hash 而不是组件内 state：这样刷新页面、切标签都走正常的路由，
// 不用自己去 DOM 里找容器重绘（那套 hack 在嵌套路由下极易失效）。
function currentTab() {
  const m = (location.hash || '').match(/^#\/ops\/([a-z]+)/);
  const key = m ? m[1] : 'tokens';
  return TABS.some((t) => t.key === key) ? key : 'tokens';
}

export async function render(container) {
  const tab = currentTab();
  state.tab = tab;

  clear(container);
  container.appendChild(loading('正在加载运维数据…'));

  try {
    await load(tab);
  } catch (err) {
    clear(container).appendChild(errorState(`加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  clear(container);
  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '运维中心'),
      h('div', { class: 'page-sub' },
        'API Token 让脚本安全调用面板 API · 操作审计记录所有写操作 · 配置历史支持一键回滚'),
    ]),
    h('div', { class: 'page-actions' }, [
      button('刷新', () => render(container), 'default', 'refresh'),
    ]),
  ]));

  container.appendChild(tabBar(tab));

  const body = h('div', { class: 'card' }, h('div', { class: 'card-body' }));
  container.appendChild(body);
  draw(body, tab);
}

function tabBar(active) {
  const wrap = h('div', { class: 'tabs' });
  for (const t of TABS) {
    wrap.appendChild(h('button', {
      class: `tab ${active === t.key ? 'is-active' : ''}`,
      type: 'button',
      onclick: () => { location.hash = `#/ops/${t.key}`; },
    }, t.label));
  }
  return wrap;
}

async function load(tab) {
  if (tab === 'tokens') {
    const r = await api.tokens.list();
    state.tokens = r.tokens || [];
  } else if (tab === 'audit') {
    const r = await api.audit.list(200);
    state.logs = r.logs || [];
    state.logTotal = r.total || 0;
  } else {
    const r = await api.system.configHistory.list();
    state.history = r.history || [];
  }
}

function draw(box, tab) {
  clear(box);
  if (tab === 'tokens') drawTokens(box);
  else if (tab === 'audit') drawAudit(box);
  else drawHistory(box);
}

// ---------------------------------------------------------------------------
// API Token
// ---------------------------------------------------------------------------

function drawTokens(box) {
  box.appendChild(sectionTitle('API Token',
    '用于 CI、监控、备份脚本等自动化调用面板 API。明文只在创建时显示一次，请立即保存。'));

  box.appendChild(h('div', { class: 'inline-actions' }, [
    button('创建 Token', showCreateToken, 'primary', 'plus'),
  ]));

  if (!state.tokens.length) {
    box.appendChild(emptyState('还没有 API Token', button('创建 Token', showCreateToken, 'primary', 'plus'), 'settings'));
    return;
  }

  const cols = [
    {
      label: '名称',
      render: (t) => h('div', {}, [
        h('div', { class: 'site-name-text' }, t.name),
        h('div', { class: 'mono-text muted' }, `${t.prefix}••••••••`),
      ]),
    },
    {
      label: '权限',
      render: (t) => {
        if (!t.scopes || !t.scopes.length) return tag('全部权限', 'primary');
        return h('div', { class: 'inline-actions' },
          t.scopes.map((s) => tag(SCOPE_LABELS[s] || s, SCOPE_COLORS[s] || 'default')));
      },
    },
    {
      label: '创建者',
      render: (t) => h('span', { class: 'muted' }, t.created_by || '-'),
    },
    {
      label: '最后使用',
      render: (t) => h('span', { class: 'muted' }, t.last_used_at ? timeAgo(t.last_used_at) : '从未使用'),
    },
    {
      label: '过期时间',
      render: (t) => (t.expires_at
        ? h('span', { class: 'muted' }, formatDateTime(t.expires_at))
        : tag('永不过期', 'success')),
    },
    {
      label: '操作',
      render: (t) => h('div', { class: 'cell-actions' }, [
        iconButton('trash', '吊销 Token', () => revokeToken(t)),
      ]),
    },
  ];
  box.appendChild(table(cols, state.tokens));
}

function showCreateToken() {
  let name = '';
  let expires = '0';
  // 三个授权位独立保存，最后统一派生成 scopes 数组。
  // 早期版本用"勾选 write 就把 scopes 整个覆盖成 ['read','write']"，
  // 于是先勾 write 再点一下 read 就会把 write 悄悄丢掉——
  // 授权位必须互相独立，不能因为动了另一个就把这个抹掉。
  let wantRead = true;
  let wantWrite = false;
  let wantShell = false;
  const scopes = [];
  const syncScopes = () => {
    scopes.length = 0;
    if (wantRead || wantWrite || wantShell) scopes.push('read');
    // shell 任务要能创建/修改计划任务本身，所以它蕴含 write。
    if (wantWrite || wantShell) scopes.push('write');
    if (wantShell) scopes.push('shell');
  };
  syncScopes();

  const nameInput = textInput('', (v) => { name = v; }, { placeholder: '例如：CI 部署脚本' });
  const scopeRead = checkboxField('只读（read）', true, (v) => {
    wantRead = v;
    syncScopes();
  });
  const scopeWrite = checkboxField('可写（write）', false, (v) => {
    wantWrite = v;
    syncScopes();
  });
  // shell 是**独立**的一档，后端不会从 write 推导它。
  //
  // 面板以 root 运行，"Shell 脚本"类型的计划任务等于把 root shell 交出去。
  // 所以早期版本"write 就够了"是危险的：一个泄漏的 write Token
  // （CI / 监控最常用）直接等于整机 root。
  // 现在只有显式勾选 shell 才会带上这个 scope。
  const scopeShell = checkboxField('可执行 Shell 计划任务（shell）', false, (v) => {
    wantShell = v;
    syncScopes();
  });
  const expiresSelect = selectInput('0', [
    { value: '0', label: '永不过期' },
    { value: '30', label: '30 天' },
    { value: '90', label: '90 天' },
    { value: '365', label: '1 年' },
  ], (v) => { expires = v; });

  const content = h('div', {}, [
    field('名称', nameInput, '用于识别用途，不会出现在凭据里'),
    field('权限', h('div', {}, [scopeRead, scopeWrite, scopeShell]),
      '只读 Token 只能调 GET 接口；可写 Token 可以创建/修改/删除；' +
      'shell 是独立授权位，等于交出 root 执行能力，默认不授予'),
    field('有效期', expiresSelect),
  ]);

  const m = modal({
    title: '创建 API Token',
    content,
    width: '520px',
    footer: [
      button('取消', () => m.close(), 'default'),
      button('创建', async () => {
        if (!name.trim()) { toast('请填写名称', 'error'); return; }
        try {
          const r = await api.tokens.create({
            name: name.trim(),
            scopes,
            expires_in_days: Number(expires) || 0,
          });
          m.close();
          showTokenSecret(r.token);
          await reload();
        } catch (e) {
          toast(e.message || '创建失败', 'error');
        }
      }, 'primary'),
    ],
  });
}

// showTokenSecret 展示只出现一次的明文 token。
function showTokenSecret(tok) {
  if (!tok || !tok.token) return;
  modal({
    title: '保存你的 Token',
    width: '620px',
    content: h('div', {}, [
      h('div', { class: 'warn-box' },
        '这是唯一一次显示明文。关闭后无法再次查看，丢失只能重新创建。'),
      codeBox(tok.token, { maxHeight: '160px' }),
      h('div', { class: 'field-hint' },
        '用法：curl -H "Authorization: Bearer <token>" http://<面板地址>:8888/api/sites'),
    ]),
  });
}

async function revokeToken(t) {
  const ok = await confirmDialog({
    title: '吊销 Token',
    message: `确定吊销「${t.name}」？使用该 Token 的脚本会立即失效。`,
    okText: '吊销',
    danger: true,
  });
  if (!ok) return;
  try {
    await api.tokens.remove(t.id);
    toast('已吊销', 'success');
    await reload();
  } catch (e) {
    toast(e.message || '吊销失败', 'error');
  }
}

// reload 重新拉当前标签页数据并重绘。
//
// 不直接操作 DOM：走 hash 路由让 app.js 统一处理，
// 这样滚动位置、脏数据提示、导航高亮都不会错乱。
function reload() {
  location.hash = `#/ops/${currentTab()}`;
  // hash 没变时 hashchange 不会触发，手动触发一次路由。
  window.dispatchEvent(new HashChangeEvent('hashchange'));
}

// ---------------------------------------------------------------------------
// 操作审计
// ---------------------------------------------------------------------------

function drawAudit(box) {
  box.appendChild(sectionTitle('操作审计',
    `记录所有写操作与登录事件，共 ${state.logTotal} 条（表内最多保留 5000 条，超出自动清理最旧的）。`));

  if (!state.logs.length) {
    box.appendChild(emptyState('暂无审计记录', null, 'logs'));
    return;
  }

  const cols = [
    {
      label: '时间',
      render: (l) => h('span', { class: 'muted' }, formatDateTime(l.created_at)),
    },
    {
      label: '操作者',
      render: (l) => h('div', { class: 'inline-actions' }, [
        h('span', {}, l.username || '(未知)'),
        l.via_token ? tag('Token', 'primary') : null,
      ]),
    },
    {
      label: '动作',
      render: (l) => h('span', { class: 'mono-text' }, l.action || '-'),
    },
    {
      label: '目标',
      render: (l) => h('span', { class: 'muted' }, l.target || l.detail || '-'),
    },
    {
      label: '来源 IP',
      render: (l) => h('span', { class: 'muted' }, l.ip || '-'),
    },
    {
      label: '结果',
      render: (l) => (l.status >= 400
        ? tag(`${l.status} 失败`, 'danger')
        : tag(`${l.status} 成功`, 'success')),
    },
  ];
  box.appendChild(table(cols, state.logs));
}

// ---------------------------------------------------------------------------
// 配置历史
// ---------------------------------------------------------------------------

function drawHistory(box) {
  box.appendChild(sectionTitle('nginx 配置历史',
    '每次配置下发成功后自动留一份快照。配置被改坏时可回滚到任意一份。'
    + '回滚只恢复 nginx 配置文件，不回滚数据库中的站点定义。'));

  if (!state.history.length) {
    box.appendChild(emptyState('还没有配置快照。新建或修改站点后会自动生成。', null, 'backup'));
    return;
  }

  const cols = [
    {
      label: '时间',
      render: (e) => h('div', {}, [
        h('div', { class: 'site-name-text' }, formatDateTime(e.created_at)),
        h('div', { class: 'muted' }, timeAgo(e.created_at)),
      ]),
    },
    {
      label: '原因',
      render: (e) => tag(e.reason || 'config.apply',
        e.reason && e.reason.indexOf('restore') >= 0 ? 'orange' : 'blue'),
    },
    {
      label: '文件数',
      render: (e) => h('span', {}, `${(e.files || []).length} 个`),
    },
    {
      label: '大小',
      render: (e) => h('span', { class: 'muted' }, formatBytes(e.bytes || 0)),
    },
    {
      label: '操作',
      render: (e) => h('div', { class: 'cell-actions' }, [
        iconButton('eye', '查看内容', () => showSnapshot(e)),
        iconButton('search', '与当前配置对比', () => showDiff(e)),
        iconButton('refresh', '回滚到这一版', () => restoreSnapshot(e)),
      ]),
    },
  ];
  box.appendChild(table(cols, state.history));
}

async function showSnapshot(entry) {
  try {
    const r = await api.system.configHistory.get(entry.id);
    const contents = r.contents || {};
    const names = Object.keys(contents);
    const body = h('div', {}, [
      h('div', { class: 'field-hint' }, `快照 ${entry.id} · ${names.length} 个文件`),
    ]);
    if (!names.length) {
      body.appendChild(emptyState('该快照没有文件内容', null, 'backup'));
    } else {
      for (const n of names) {
        body.appendChild(h('div', {}, [
          h('div', { class: 'site-name-text' }, n),
          codeBox(contents[n], { maxHeight: '220px' }),
        ]));
      }
    }
    modal({ title: '配置快照内容', content: body, width: '780px' });
  } catch (e) {
    toast(e.message || '读取快照失败', 'error');
  }
}

async function showDiff(entry) {
  try {
    const r = await api.system.configHistory.diff(entry.id);
    const diffs = r.diffs || [];
    const body = h('div', {});
    if (!diffs.length) {
      body.appendChild(emptyState('没有可比对的配置', null, 'backup'));
    } else {
      for (const d of diffs) {
        const lines = d.lines || [];
        const changed = lines.filter((l) => l.op !== ' ');
        body.appendChild(h('div', {}, [
          h('div', { class: 'inline-actions' }, [
            h('span', { class: 'site-name-text' }, d.name),
            d.identical ? tag('无差异', 'success') : null,
            d.added ? tag(`+${d.added}`, 'success') : null,
            d.removed ? tag(`-${d.removed}`, 'danger') : null,
            d.new_missing ? tag('当前已删除', 'warning') : null,
            d.old_missing ? tag('快照后新建', 'info') : null,
          ]),
          // 只展示有变化的部分，避免几百行相同内容把界面撑爆
          changed.length
            ? codeBox(changed.map((l) => (l.op === '+' ? `+ ${l.text}` : l.op === '-' ? `- ${l.text}` : `  ${l.text}`)).join('\n'),
                      { maxHeight: '260px' })
            : h('div', { class: 'muted' }, '（无变化行）'),
        ]));
      }
    }
    modal({ title: `与当前配置对比（快照 ${entry.id}）`, content: body, width: '780px' });
  } catch (e) {
    toast(e.message || '对比失败', 'error');
  }
}

async function restoreSnapshot(entry) {
  const ok = await confirmDialog({
    title: '回滚配置',
    message: `将 nginx 配置回滚到 ${formatDateTime(entry.created_at)} 的快照？`
      + '\n\n注意：只恢复 nginx 配置文件，数据库中的站点定义不会回滚；'
      + '当前多出来的配置文件也不会被删除。',
    okText: '回滚并重载',
    danger: true,
  });
  if (!ok) return;
  try {
    const r = await api.system.configHistory.restore(entry.id);
    toast(`已回滚 ${(r.restored || []).length} 个配置文件`, 'success');
    await reload();
  } catch (e) {
    toast(e.message || '回滚失败', 'error');
  }
}
