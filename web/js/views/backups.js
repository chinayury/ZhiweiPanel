// 备份列表：站点/数据库备份、手动备份、下载/恢复/删除
// v0.2.0 借鉴宝塔
import { api, formatBytes, formatDateTime } from '../api.js';
import {
  h, clear, button, tag, table, loading, errorState, emptyState, iconButton,
  confirmDialog, toast, modal, field, textInput, selectInput,
} from '../components.js';

const state = { kind: 'all', backups: [], sites: [] };

// viewContainer 记住当前视图的容器，供模块级函数刷新列表。
//
// 不能用 document.getElementById('view')：DOM 里根本没有 id 为 view 的元素
// （视图容器在 app.js 里建的是 id="outlet"），那个调用恒为 null，
// 于是 clear(null) 之后紧接着的 container.appendChild 直接抛
// TypeError: Cannot read properties of null (reading 'appendChild')。
// 结果是"操作成功了但界面不刷新"，控制台还留一条未捕获的 Promise 异常。
let viewContainer = null;

// refresh 重新渲染当前视图。
function refresh() {
  if (viewContainer) render(viewContainer);
}

export async function render(container) {
  viewContainer = container;
  clear(container);
  container.appendChild(loading('正在加载备份…'));

  try {
    const [br, sr, dbres] = await Promise.all([
      api.backups.list({ limit: 200 }),
      api.sites.list().catch(() => ({ sites: [] })),
      fetch('/api/system/mysql', { credentials: 'same-origin' }).then((r) => r.json()).catch(() => null),
    ]);
    state.backups = br.backups || [];
    state.sites = sr.sites || [];
    state.dbs = (dbres?.databases) || []; // v0.2.0 暂未实现 /api/dbs 列表
  } catch (err) {
    clear(container).appendChild(errorState(`备份列表加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  clear(container);
  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '备份'),
      h('div', { class: 'page-sub' }, `共 ${state.backups.length} 份备份 · 总大小 ${totalSize()}`),
    ]),
    h('div', { class: 'page-actions' }, [
      button('备份站点', showSiteBackup, 'default', 'sites'),
      button('备份数据库', showDBBackup, 'primary', 'plus'),
    ]),
  ]));

  const kindSel = selectInput(state.kind, [
    { value: 'all', label: '全部' },
    { value: 'site', label: '仅站点' },
    { value: 'database', label: '仅数据库' },
  ], (v) => { state.kind = v; drawList(); });

  const listBox = h('div', { class: 'card' }, h('div', { class: 'card-body' }));
  container.appendChild(h('div', { class: 'filter-bar' }, [
    h('div', { class: 'filter-item' }, kindSel),
  ]));
  container.appendChild(listBox);
  drawList(listBox);
}

function totalSize() {
  let total = 0;
  for (const b of state.backups) total += (b.size || 0);
  return formatBytes(total);
}

function drawList(box) {
  clear(box);
  const list = state.kind === 'all' ? state.backups : state.backups.filter((b) => b.kind === state.kind);
  if (!list.length) {
    box.appendChild(emptyState('还没有备份，可手动备份或配置计划任务自动备份', null, 'backup'));
    return;
  }
  const cols = [
    {
      label: '类型 / 名称',
      render: (b) => h('div', { class: 'site-cell' }, [
        h('div', { class: 'site-name' }, [
          h('span', { class: 'site-name-text' }, b.target_name || b.target),
          tag(b.kind === 'site' ? '站点' : '数据库', 'info'),
        ]),
        h('div', { class: 'site-domains muted' }, b.id),
      ]),
    },
    { label: '大小', width: '110px', render: (b) => formatBytes(b.size) },
    { label: '时间', width: '170px', render: (b) => formatDateTime(b.created_at) },
    { label: '备注', render: (b) => h('span', { class: 'muted' }, b.note || '-') },
    {
      label: '操作',
      width: '280px',
      align: 'right',
      render: (b) => h('div', { class: 'cell-actions' }, [
        button('下载', () => downloadBackup(b), 'ghost', 'download'),
        b.kind === 'site' ? button('查看 conf', () => restorePreview(b), 'ghost') : null,
        iconButton('trash', '删除', async () => {
          const yes = await confirmDialog({
            title: '删除备份',
            message: `确定删除 ${b.target_name} 的备份吗？该操作不可恢复。`,
            okText: '删除',
            danger: true,
          });
          if (!yes) return;
          await api.backups.remove(b.id);
          toast('已删除', 'success');
          refresh();
        }, 'danger-ghost'),
      ]),
    },
  ];
  box.appendChild(table(cols, list));
}

function downloadBackup(b) {
  window.open(`/api/backups/${encodeURIComponent(b.id)}/download`, '_blank');
}

async function restorePreview(b) {
  try {
    const res = await api.backups.restorePreview(b.id);
    const m = modal({
      title: `备份内容预览 · nginx.conf`,
      width: '760px',
      content: h('div', {}, [
        h('div', { class: 'meta-line muted' },
          `${b.target_name} · 备份 nginx.conf (${res.preview_bytes} 字节)`),
        h('pre', { class: 'code-box', style: { maxHeight: '480px' } }, h('code', {}, res.preview || '')),
        h('div', { class: 'field-hint' }, '这是打包时的 nginx.conf 快照。如需还原，请手动 tar xzf 后用 cp 命令写到 conf.d。'),
      ]),
    });
  } catch (e) {
    toast(e.message || '预览失败', 'error');
  }
}

function showSiteBackup() {
  if (!state.sites.length) {
    toast('还没有站点可备份', 'error');
    return;
  }
  const sel = selectInput('', state.sites.map((s) => ({ value: s.id, label: s.name })), null);
  const note = textInput('', null, { placeholder: '可选备注' });
  const m = modal({
    title: '备份站点',
    width: '520px',
    content: h('div', {}, [
      field('选择站点', sel),
      field('备注', note),
    ]),
    footer: [
      button('取消', () => m.close(), 'default'),
      button('开始备份', async () => {
        if (!sel.value) {
          toast('请选择站点', 'error');
          return;
        }
        const btn = document.activeElement;
        btn.disabled = true; btn.textContent = '备份中…';
        try {
          await api.backups.createSite(sel.value, note.value);
          toast('备份完成', 'success');
          m.close();
          refresh();
        } catch (e) {
          toast(e.message || '备份失败', 'error');
          btn.disabled = false; btn.textContent = '开始备份';
        }
      }, 'primary'),
    ],
  });
}

function showDBBackup() {
  const dbName = textInput('', null, { placeholder: 'site_xxxxxx（数据库名）' });
  const note = textInput('', null, { placeholder: '可选备注' });
  const m = modal({
    title: '备份数据库',
    width: '520px',
    content: h('div', {}, [
      field('数据库名', dbName, '需与 MySQL 中的实际库名一致'),
      field('备注', note),
    ]),
    footer: [
      button('取消', () => m.close(), 'default'),
      button('开始备份', async () => {
        if (!dbName.value) {
          toast('请填写数据库名', 'error');
          return;
        }
        const btn = document.activeElement;
        btn.disabled = true; btn.textContent = '备份中…';
        try {
          await api.backups.createDB(dbName.value, note.value);
          toast('备份完成', 'success');
          m.close();
          refresh();
        } catch (e) {
          toast(e.message || '备份失败', 'error');
          btn.disabled = false; btn.textContent = '开始备份';
        }
      }, 'primary'),
    ],
  });
}