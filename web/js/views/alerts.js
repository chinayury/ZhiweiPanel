// 告警中心：按等级与处理状态过滤、标记处理、通知渠道配置与测试
import { api, formatDateTime, timeAgo } from '../api.js';
import {
  h, clear, button, tag, table, loading, errorState, emptyState, toast,
  selectInput, field, textInput, modal, confirmDialog, iconButton, sectionTitle,
} from '../components.js';

// 通知渠道在 settings 表里的键。值是 JSON 字符串（表是 string->string）。
const CHANNELS_KEY = 'alert_channels_json';

const LEVELS = [
  { value: 'all', label: '全部等级' },
  { value: 'critical', label: '严重' },
  { value: 'warning', label: '警告' },
  { value: 'info', label: '提示' },
];
const RESOLVED = [
  { value: 'all', label: '全部状态' },
  { value: 'pending', label: '未处理' },
  { value: 'resolved', label: '已处理' },
];
const CHANNEL_TYPES = [
  { value: 'email', label: '邮件', placeholder: 'ops@example.com' },
  { value: 'webhook', label: 'Webhook', placeholder: 'https://example.com/hook' },
  { value: 'dingtalk', label: '钉钉机器人', placeholder: 'https://oapi.dingtalk.com/robot/send?access_token=xxx' },
  { value: 'wecom', label: '企业微信机器人', placeholder: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx' },
];

const state = { level: 'all', resolved: 'all', channels: [] };

export async function render(container) {
  clear(container);
  container.appendChild(loading('正在加载告警…'));

  let alerts = [];
  try {
    const res = await api.alerts.list(100);
    alerts = res.alerts || [];
  } catch (err) {
    clear(container).appendChild(errorState(`告警列表加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  clear(container);
  const counts = {
    total: alerts.length,
    pending: alerts.filter((a) => !a.resolved).length,
    critical: alerts.filter((a) => !a.resolved && a.level === 'critical').length,
  };

  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '告警'),
      h('div', { class: 'page-sub' }, `共 ${counts.total} 条 · 待处理 ${counts.pending} 条${counts.critical ? ` · 严重 ${counts.critical} 条` : ''}`),
    ]),
    h('div', { class: 'page-actions' }, [
      button('通知渠道', showChannels, 'default', 'alerts'),
      button('全部标记已处理', () => resolveAll(), 'default', 'check'),
      button('刷新', () => render(container), 'default', 'refresh'),
    ]),
  ]));

  // ---- 过滤条 ----
  const levelSel = selectInput(state.level, LEVELS, (v) => { state.level = v; draw(); });
  const resolvedSel = selectInput(state.resolved, RESOLVED, (v) => { state.resolved = v; draw(); });
  container.appendChild(h('div', { class: 'filter-bar' }, [
    h('div', { class: 'filter-item' }, [h('span', { class: 'filter-label' }, '等级'), levelSel]),
    h('div', { class: 'filter-item' }, [h('span', { class: 'filter-label' }, '状态'), resolvedSel]),
  ]));

  const listBox = h('div', { class: 'card' }, h('div', { class: 'card-body' }));
  container.appendChild(listBox);

  function filtered() {
    return alerts.filter((a) => {
      if (state.level !== 'all' && a.level !== state.level) return false;
      if (state.resolved === 'pending' && a.resolved) return false;
      if (state.resolved === 'resolved' && !a.resolved) return false;
      return true;
    });
  }

  function draw() {
    clear(listBox);
    const rows = filtered();
    if (!rows.length) {
      listBox.appendChild(emptyState(
        alerts.length ? '没有符合当前过滤条件的告警' : '暂无告警记录，一切正常',
        null,
        'alerts'
      ));
      return;
    }

    const cols = [
      { label: '等级', width: '90px', render: (a) => tag(levelText(a.level), levelTone(a.level)) },
      { label: '站点', width: '160px', render: (a) => h('a', { href: `#/sites/${encodeURIComponent(a.site_id)}`, class: 'link' }, a.site_name || a.site_id || '-') },
      { label: '类型', width: '130px', render: (a) => tag(a.type || '-', 'default') },
      { label: '内容', render: (a) => h('span', { class: `alert-msg-inline ${a.resolved ? 'is-resolved' : ''}`.trim() }, a.message || '-') },
      { label: '时间', width: '120px', render: (a) => h('span', { class: 'muted', title: formatDateTime(a.created_at) }, timeAgo(a.created_at)) },
      {
        label: '状态',
        width: '90px',
        render: (a) => (a.resolved ? tag('已处理', 'success') : tag('待处理', 'warning')),
      },
      {
        label: '操作',
        width: '120px',
        align: 'right',
        render: (a) => (a.resolved
          ? h('span', { class: 'muted' }, '—')
          : button('标记处理', () => doResolve(a), 'ghost', 'check')),
      },
    ];

    listBox.appendChild(table(cols, rows));
  }

  async function doResolve(a) {
    try {
      await api.alerts.resolve(a.id);
      a.resolved = true;
      toast('告警已标记为处理', 'success');
      draw();
    } catch (e) {
      toast(e.message || '操作失败', 'error');
    }
  }

  async function resolveAll() {
    const pending = alerts.filter((a) => !a.resolved);
    if (!pending.length) {
      toast('没有待处理的告警', 'info');
      return;
    }
    const ok = await confirmDialog({
      title: '批量标记已处理',
      message: `确定将 ${pending.length} 条未处理告警全部标记为已处理吗？`,
      okText: '全部标记',
    });
    if (!ok) return;
    let okCount = 0;
    for (const a of pending) {
      try {
        await api.alerts.resolve(a.id);
        a.resolved = true;
        okCount++;
      } catch (e) {
        // 单条失败不中断其余处理
      }
    }
    toast(`已处理 ${okCount} / ${pending.length} 条告警`, okCount === pending.length ? 'success' : 'error');
    draw();
  }

  // ---- 通知渠道（存于面板设置，随设置页保存）----
  async function showChannels() {
    const body = h('div', {}, loading('加载通知配置…'));
    const m = modal({
      title: '通知渠道配置',
      width: '760px',
      content: body,
      footer: [button('关闭', () => m.close(), 'default')],
    });

    // 通知渠道保存在面板设置中（键 alert_channels_json，值为 JSON 字符串）
    let channels = [];
    try {
      const res = await api.settings.get();
      const raw = res.settings && res.settings[CHANNELS_KEY];
      // 兼容旧格式：历史上曾直接把数组塞进 settings（那必然保存失败）
      if (Array.isArray(raw)) channels = raw;
      else if (typeof raw === 'string' && raw.trim()) {
        try { const parsed = JSON.parse(raw); if (Array.isArray(parsed)) channels = parsed; }
        catch (e) { channels = []; }
      }
    } catch (e) {
      channels = [];
    }

    const listBox = h('div', { class: 'channel-list' });
    const draw = () => {
      clear(listBox);
      if (!channels.length) {
        listBox.appendChild(emptyState('尚未配置通知渠道，告警不会外发', null, 'alerts'));
      }
      channels.forEach((c, i) => {
        const cfg = CHANNEL_TYPES.find((t) => t.value === c.type) || CHANNEL_TYPES[0];
        const typeSel = selectInput(c.type, CHANNEL_TYPES, (v) => { c.type = v; draw(); });
        const target = textInput(c.target || '', (v) => { c.target = v; }, { placeholder: cfg.placeholder });
        listBox.appendChild(h('div', { class: 'channel-row' }, [
          h('div', { class: 'channel-row-head' }, [
            h('span', { class: 'channel-title' }, `渠道 ${i + 1}`),
            h('div', { class: 'cell-actions' }, [
              button('发送测试', () => testChannel(c), 'ghost', 'alerts'),
              iconButton('trash', '删除该渠道', () => {
                channels.splice(i, 1);
                draw();
              }, 'danger-ghost'),
            ]),
          ]),
          h('div', { class: 'channel-row-body' }, [
            h('div', { class: 'channel-field' }, [h('label', {}, '渠道类型'), typeSel]),
            h('div', { class: 'channel-field channel-field-grow' }, [h('label', {}, '接收目标'), target]),
          ]),
        ]));
      });
    };
    draw();

    clear(body).appendChild(h('div', {}, [
      h('div', { class: 'field-hint' }, '支持邮件、Webhook、钉钉机器人、企业微信机器人四种目标'),
      listBox,
      h('div', { class: 'inline-actions' }, [
        button('添加渠道', () => {
          channels.push({ type: 'email', target: '', enabled: true });
          draw();
        }, 'default', 'plus'),
        button('保存渠道', async () => {
          // 后端 settings 是 string->string 的键值表，收不了对象数组。
          // 这里把渠道列表 JSON 序列化后按字符串存，读取处再 parse。
          try {
            const res = await api.settings.get();
            const settings = {};
            for (const [k, v] of Object.entries(res.settings || {})) {
              if (k === CHANNELS_KEY) continue;
              settings[k] = String(v);
            }
            settings[CHANNELS_KEY] = JSON.stringify(channels);
            await api.settings.put(settings);
            toast('通知渠道已保存', 'success');
          } catch (e) {
            toast(e.message || '保存失败', 'error');
          }
        }, 'primary', 'save'),
      ]),
    ]));
  }

  async function testChannel(c) {
    if (!c.target || !c.target.trim()) {
      toast('请先填写接收目标', 'error');
      return;
    }
    try {
      await api.alerts.test({ channel: { type: c.type, target: c.target.trim() } });
      toast('测试通知已发送', 'success');
    } catch (e) {
      toast(e.message || '测试发送失败', 'error');
    }
  }

  function levelText(l) {
    if (l === 'critical') return '严重';
    if (l === 'warning') return '警告';
    return '提示';
  }

  function levelTone(l) {
    if (l === 'critical') return 'danger';
    if (l === 'warning') return 'warning';
    return 'info';
  }

  draw();
}