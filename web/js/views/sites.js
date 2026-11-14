// 网站列表：站点卡片/表格、启停开关、搜索过滤、重载与删除、批量启停
// v0.2.0 加：到期时间列、批量操作
import { api, formatBytes, formatNumber, certDaysLeft, timeAgo, formatDate } from '../api.js';
import {
  h, clear, icon, button, tag, table, switchControl, loading, errorState,
  emptyState, iconButton, confirmDialog, toast, certDaysBadge, textInput, selectInput,
} from '../components.js';
import { ctx } from '../app.js';

// 视图内状态：过滤条件 + 选中的站点
const state = { keyword: '', status: 'all', selected: new Set() };

export async function render(container) {
  clear(container);
  container.appendChild(loading('正在加载站点列表…'));

  let sites = [];
  try {
    const res = await api.sites.list();
    sites = res.sites || [];
  } catch (err) {
    clear(container).appendChild(errorState(`站点列表加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  clear(container);
  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '网站'),
      h('div', { class: 'page-sub' }, `共 ${sites.length} 个站点 · 运行中 ${sites.filter((s) => s.status === 'running').length} 个`),
    ]),
    h('div', { class: 'page-actions' }, [
      button('刷新', () => render(container), 'default', 'refresh'),
      button('新建站点', () => ctx.navigate('#/sites/new'), 'primary', 'plus'),
    ]),
  ]));

  // 搜索 + 状态过滤
  const kwInput = textInput(state.keyword, (v) => { state.keyword = v; drawList(); }, { placeholder: '搜索域名 / 站点名称 / 根目录…' });
  const statusSel = selectInput(state.status, [
    { value: 'all', label: '全部状态' },
    { value: 'running', label: '运行中' },
    { value: 'stopped', label: '已停止' },
  ], (v) => { state.status = v; drawList(); });

  const filterBar = h('div', { class: 'filter-bar' }, [
    h('div', { class: 'filter-search' }, [icon('search'), kwInput]),
    h('div', { class: 'filter-item' }, statusSel),
  ]);

  const listBox = h('div', { class: 'site-list' });
  container.appendChild(filterBar);
  container.appendChild(listBox);

  function filtered() {
    const kw = state.keyword.trim().toLowerCase();
    return sites.filter((s) => {
      if (state.status !== 'all') {
        const st = s.status === 'running' ? 'running' : 'stopped';
        if (st !== state.status) return false;
      }
      if (!kw) return true;
      const hay = [
        s.name || '',
        ...(s.domains || []),
        (s.root && s.root.path) || '',
        (s.sub_dir && s.sub_dir.rules || []).map((r) => r.path).join(' '),
      ].join(' ').toLowerCase();
      return hay.includes(kw);
    });
  }

  function drawList() {
    clear(listBox);
    const rows = filtered();
    if (!rows.length) {
      listBox.appendChild(
        sites.length
          ? emptyState('没有匹配条件的站点', null, 'search')
          : emptyState('还没有任何站点', button('新建站点', () => ctx.navigate('#/sites/new'), 'primary', 'plus'), 'sites')
      );
      return;
    }

    const cols = [
      {
        label: '站点',
        render: (s) => h('div', { class: 'site-cell' }, [
          h('div', { class: 'site-name' }, [
            h('span', { class: 'site-name-text' }, s.name || s.id),
            s.sub_dir && s.sub_dir.enabled ? tag('子目录', 'info') : null,
            listenPortTag(s.listen_ports),
            edateTag(s.edate),
          ]),
          h('div', { class: 'site-domains' }, (s.domains || []).join(', ') || '-'),
        ]),
      },
      {
        label: '主域名',
        render: (s) => h('span', { class: 'mono-text' }, (s.domains || [])[0] || '-'),
      },
      {
        label: '状态',
        width: '110px',
        render: (s) => h('div', { class: 'cell-inline' }, [
          switchControl(s.status === 'running', async (on) => {
            const target = on ? 'running' : 'stopped';
            try {
              await api.sites.toggle(s.id, target);
              s.status = target;
              toast(`${s.name} 已${on ? '启动' : '停止'}`, 'success');
              drawList();
              updateHeader();
            } catch (e) {
              toast(e.message || '启停失败', 'error');
              drawList();
            }
          }),
          h('span', { class: 'cell-inline-text' }, s.status === 'running' ? '运行中' : '已停止'),
        ]),
      },
      {
        label: '根目录',
        render: (s) => h('span', { class: 'mono-text ellipsis', title: (s.root && s.root.path) || '' }, (s.root && s.root.path) || '-'),
      },
      {
        label: '证书',
        width: '110px',
        render: (s) => {
          const days = certDaysLeft(s.cert || null);
          if (days === null) return tag('未启用', 'default');
          return certDaysBadge(days);
        },
      },
      {
        label: '今日请求',
        width: '110px',
        align: 'right',
        render: (s) => h('span', {}, formatNumber(s.today_reqs || 0)),
      },
      {
        label: '今日流量',
        width: '110px',
        align: 'right',
        render: (s) => h('span', {}, formatBytes(s.today_bytes || 0)),
      },
      {
        label: '告警',
        width: '80px',
        align: 'right',
        render: (s) => {
          const n = Number(s.open_alerts) || 0;
          if (!n) return h('span', { class: 'muted' }, '-');
          return h('span', {
            class: 'alert-count',
            onclick: (e) => { e.stopPropagation(); ctx.navigate('#/alerts'); },
          }, String(n));
        },
      },
      {
        label: '更新时间',
        width: '130px',
        render: (s) => h('span', { class: 'muted' }, timeAgo(s.updated_at || s.created_at)),
      },
      {
        label: '选择',
        width: '60px',
        align: 'center',
        render: (s) => {
          const cb = h('input', {
            type: 'checkbox',
            checked: state.selected.has(s.id),
            onchange: (e) => {
              if (e.target.checked) state.selected.add(s.id);
              else state.selected.delete(s.id);
              drawBatchBar();
            },
          });
          return cb;
        },
      },
      {
        label: '操作',
        width: '190px',
        align: 'right',
        render: (s) => h('div', { class: 'cell-actions' }, [
          button('详情', () => ctx.navigate(`#/sites/${encodeURIComponent(s.id)}`), 'ghost'),
          iconButton('reload', '重载 nginx 配置', () => doReload(s), 'ghost'),
          // 先写 sessionStorage 再改 hash：早期顺序相反，靠"hashchange 的
        // await 比 setTimeout 慢"这一隐式假设传参，模块已缓存时顺序可能反转，
        // 预选站点就丢了。
        iconButton('logs', '查看日志', () => {
          sessionStorage.setItem('zhiweipanel.logs.site', s.id);
          location.hash = '#/logs';
        }, 'ghost'),
          iconButton('trash', '删除站点', () => doDelete(s), 'danger-ghost'),
        ]),
      },
    ];

    listBox.appendChild(table(cols, rows));
  }

  function updateHeader() {
    const sub = container.querySelector('.page-sub');
    if (sub) {
      sub.textContent = `共 ${sites.length} 个站点 · 运行中 ${sites.filter((s) => s.status === 'running').length} 个`;
    }
  }

  async function doReload(s) {
    try {
      await api.sites.reload(s.id);
      toast(`${s.name} 的 nginx 配置已重载`, 'success');
    } catch (e) {
      toast(e.message || '重载失败', 'error');
    }
  }

  async function doDelete(s) {
    const ok = await confirmDialog({
      title: '删除站点',
      message: `确定要删除「${s.name || s.id}」吗？该操作会同时移除站点配置，且不可恢复。`,
      okText: '删除',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.sites.remove(s.id);
      toast('站点已删除', 'success');
      const i = sites.findIndex((x) => x.id === s.id);
      if (i >= 0) sites.splice(i, 1);
      state.selected.delete(s.id);
      drawList();
      updateHeader();
      drawBatchBar();
    } catch (e) {
      toast(e.message || '删除失败', 'error');
    }
  }

  // v0.2.0: 到期时间角标。未来 7 天内 = 黄，已过期 = 红。
  function edateTag(edate) {
    if (!edate) return null;
    const t = new Date(edate);
    if (isNaN(t.getTime())) return null;
    const now = new Date();
    const days = Math.floor((t - now) / 86400000);
    if (days < 0) return tag(`已过期 ${-days} 天`, 'danger');
    if (days <= 7) return tag(`${days} 天后到期`, 'warning');
    return tag(`到期 ${formatDate(edate)}`, 'info');
  }

  // v0.2.1: 监听端口角标。非 80 显示"端口:80,8080"提示用户。
  function listenPortTag(ports) {
    if (!Array.isArray(ports) || !ports.length) return null;
    if (ports.length === 1 && ports[0] === 80) return null; // 默认不显
    return tag(`端口:${ports.join(',')}`, 'info');
  }

  // v0.2.0: 批量操作条。多选站点后显示"批量启动/停止"按钮。
  function drawBatchBar() {
    let bar = document.getElementById('sites-batch-bar');
    if (!state.selected.size) {
      if (bar) bar.remove();
      return;
    }
    const ids = Array.from(state.selected);
    if (!bar) {
      bar = h('div', { id: 'sites-batch-bar', class: 'filter-bar sites-batch-bar' });
      container.appendChild(bar);
    }
    clear(bar);
    bar.appendChild(h('div', { class: 'batch-info' }, `已选 ${ids.length} 个站点`));
    bar.appendChild(h('div', { class: 'inline-actions' }, [
      button('批量启动', async () => {
        await batchDo('start', ids);
      }, 'default'),
      button('批量停止', async () => {
        await batchDo('stop', ids);
      }, 'default'),
      button('清空选择', () => {
        state.selected.clear();
        drawList();
        drawBatchBar();
      }, 'ghost'),
    ]));
  }

  async function batchDo(action, ids) {
    try {
      const r = await api.sitesBatch(action, ids);
      toast(`${action} 完成：成功 ${r.ok_count}/${ids.length}`, 'success');
      // 刷新列表 + 清选择
      state.selected.clear();
      render(container);
    } catch (e) {
      toast(e.message || '批量操作失败', 'error');
    }
  }

  drawList();
  drawBatchBar();
}