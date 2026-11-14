// 计划任务：列表 / 启停 / 立即执行 / 执行历史
// v0.2.0 借鉴宝塔计划任务
import { api, formatDateTime, timeAgo } from '../api.js';
import {
  h, clear, button, tag, table, loading, errorState, emptyState, iconButton,
  confirmDialog, toast, modal, field, textInput, textArea, selectInput, grid,
  sectionTitle, switchControl,
} from '../components.js';

const TYPE_OPTIONS = [
  { value: 'shell', label: 'Shell 脚本' },
  { value: 'backup_site', label: '备份站点' },
  { value: 'backup_db', label: '备份数据库' },
  { value: 'log_split', label: '切割访问日志' },
  { value: 'release_mem', label: '释放内存' },
  { value: 'access_url', label: '访问 URL' },
];

const SCHEDULE_PRESETS = [
  { value: '0 2 * * *', label: '每天凌晨 2 点' },
  { value: '0 3 * * *', label: '每天凌晨 3 点' },
  { value: '*/30 * * * *', label: '每 30 分钟' },
  { value: '0 * * * *', label: '每小时' },
  { value: '0 0 * * 0', label: '每周日 0 点' },
];

const state = { jobs: [], sites: [], dbs: [] };

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
  container.appendChild(loading('正在加载计划任务…'));

  try {
    const [jr, sr] = await Promise.all([
      api.cron.list(),
      api.sites.list().catch(() => ({ sites: [] })),
    ]);
    state.jobs = jr.jobs || [];
    state.sites = sr.sites || [];
  } catch (err) {
    clear(container).appendChild(errorState(`计划任务加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  clear(container);
  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '计划任务'),
      h('div', { class: 'page-sub' },
        `共 ${state.jobs.length} 个任务 · 启用 ${state.jobs.filter((j) => j.enabled).length} 个`),
    ]),
    h('div', { class: 'page-actions' }, [
      button('添加任务', showAdd, 'primary', 'plus'),
    ]),
  ]));

  const listBox = h('div', { class: 'card' }, h('div', { class: 'card-body' }));
  container.appendChild(listBox);
  drawList(listBox);
}

function drawList(box) {
  clear(box);
  if (!state.jobs.length) {
    box.appendChild(emptyState(
      '还没有计划任务，可定时跑 shell、备份、清理日志等',
      button('添加任务', showAdd, 'primary', 'plus'),
      'cron',
    ));
    return;
  }

  const cols = [
    {
      label: '名称 / 类型',
      render: (j) => h('div', { class: 'site-cell' }, [
        h('div', { class: 'site-name' }, [
          h('span', { class: 'site-name-text' }, j.name),
          tag(typeLabel(j.type), 'info'),
        ]),
        h('div', { class: 'site-domains muted' }, j.schedule),
      ]),
    },
    { label: '下次执行', width: '160px', render: (j) => h('span', {}, j.next_run_at ? formatDateTime(j.next_run_at) : '-') },
    {
      label: '上次执行',
      width: '160px',
      render: (j) => h('div', {}, [
        h('div', {}, j.last_run_at ? formatDateTime(j.last_run_at) : '-'),
        j.last_status ? h('div', { class: 'muted' }, tag(j.last_status, j.last_status === 'ok' ? 'success' : j.last_status === 'failed' ? 'danger' : 'default')) : null,
      ]),
    },
    {
      label: '启用',
      width: '70px',
      align: 'center',
      render: (j) => switchControl(j.enabled, async (v) => {
        try {
          await api.cron.update(j.id, { enabled: v });
          j.enabled = v;
          toast(v ? '已启用' : '已停用', 'success');
          refresh();
        } catch (e) {
          toast(e.message || '更新失败', 'error');
        }
      }),
    },
    {
      label: '操作',
      width: '320px',
      align: 'right',
      render: (j) => h('div', { class: 'cell-actions' }, [
        button('立即执行', async () => {
          await api.cron.run(j.id);
          toast('已加入执行队列，约 30s 内触发', 'success');
        }, 'ghost'),
        button('历史', () => showHistory(j), 'ghost'),
        button('编辑', () => showEdit(j), 'ghost'),
        iconButton('trash', '删除', async () => {
          const yes = await confirmDialog({
            title: '删除计划任务',
            message: `确定要删除任务 "${j.name}" 吗？`,
            okText: '删除',
            danger: true,
          });
          if (!yes) return;
          await api.cron.remove(j.id);
          toast('已删除', 'success');
          refresh();
        }, 'danger-ghost'),
      ]),
    },
  ];

  box.appendChild(table(cols, state.jobs));
}

function typeLabel(t) {
  const o = TYPE_OPTIONS.find((x) => x.value === t);
  return o ? o.label : t;
}

function showAdd() { showEditor(null); }
function showEdit(j) { showEditor(j); }

function showEditor(existing) {
  const isEdit = !!existing;
  const name = textInput(existing?.name || '', null, { placeholder: '每日凌晨数据库备份' });
  const type = selectInput(existing?.type || 'shell', TYPE_OPTIONS, null);
  const schedule = textInput(existing?.schedule || '0 2 * * *', null, { placeholder: 'crontab 表达式' });
  const enabled = switchControl(existing?.enabled !== false, null);

  // target 字段：随 type 变
  const targetRow = h('div', { class: 'field' });
  const payloadRow = h('div', { class: 'field' });
  let target = '';
  let payload = existing?.payload || '';

  const refreshTargetAndPayload = () => {
    clear(targetRow);
    clear(payloadRow);
    target = '';
    payload = '';
    if (type.value === 'backup_site') {
      const opts = state.sites.map((s) => ({ value: s.id, label: s.name }));
      const sel = selectInput(existing?.target || '', opts, (v) => { target = v; });
      targetRow.appendChild(field('目标站点', sel));
      const retain = textInput('3', null, { placeholder: '保留份数' });
      const note = textInput('', null, { placeholder: '可选备注' });
      payloadRow.appendChild(grid(2,
        field('保留份数', retain),
        field('备注', note),
      ));
      const updatePayload = () => {
        payload = JSON.stringify({ retain: parseIntOrZero(retain.value), note: note.value });
      };
      retain.addEventListener('change', updatePayload);
      note.addEventListener('input', updatePayload);
      updatePayload();
    } else if (type.value === 'backup_db') {
      const dbInput = textInput(existing?.target || '', null, { placeholder: '数据库名' });
      targetRow.appendChild(field('数据库名', dbInput, 'site_<12位hex>'));
      const retain = textInput('3', null, { placeholder: '保留份数' });
      const note = textInput('', null, { placeholder: '可选备注' });
      payloadRow.appendChild(grid(2,
        field('保留份数', retain),
        field('备注', note),
      ));
      const update = () => {
        target = dbInput.value;
        payload = JSON.stringify({ retain: parseIntOrZero(retain.value), note: note.value });
      };
      dbInput.addEventListener('input', update);
      retain.addEventListener('change', update);
      note.addEventListener('input', update);
      update();
    } else if (type.value === 'log_split') {
      const opts = state.sites.map((s) => ({ value: s.id, label: s.name }));
      const sel = selectInput(existing?.target || '', opts, (v) => { target = v; });
      targetRow.appendChild(field('目标站点', sel));
      const retain = textInput('30', null, { placeholder: '保留份数' });
      payloadRow.appendChild(field('保留份数', retain, '超出按时间删旧'));
      const update = () => {
        payload = JSON.stringify({ retain: parseIntOrZero(retain.value) });
      };
      retain.addEventListener('change', update);
      update();
    } else if (type.value === 'shell') {
      targetRow.appendChild(h('div', { class: 'field-hint' }, 'shell 任务无需 target'));
      const script = textArea(existing?.payload || '', null, { rows: 8, placeholder: '#!/bin/bash\necho "hello"\nls /tmp' });
      const timeout = textInput('3600', null, { placeholder: '超时秒' });
      payloadRow.appendChild(field('脚本内容', script, '禁用：shutdown / init / mkfs / passwd'));
      payloadRow.appendChild(field('超时（秒）', timeout));
      const update = () => {
        payload = JSON.stringify({ script: script.value, timeout: parseIntOrZero(timeout.value) });
      };
      script.addEventListener('input', update);
      timeout.addEventListener('change', update);
      update();
    } else if (type.value === 'access_url') {
      const url = textInput('', null, { placeholder: 'https://example.com/health' });
      const method = textInput('GET', null, { placeholder: 'GET / HEAD / POST' });
      const timeout = textInput('30', null, { placeholder: '秒' });
      targetRow.appendChild(field('URL', url, '仅允许外网 http/https，禁止内网'));
      payloadRow.appendChild(grid(2,
        field('方法', method),
        field('超时（秒）', timeout),
      ));
      const update = () => {
        payload = JSON.stringify({ url: url.value, method: method.value, timeout: parseIntOrZero(timeout.value) });
      };
      [url, method, timeout].forEach((el) => {
        el.addEventListener('input', update);
        el.addEventListener('change', update);
      });
      update();
    } else if (type.value === 'release_mem') {
      targetRow.appendChild(h('div', { class: 'field-hint' }, '释放内存：sync + drop_caches 3（仅 Linux）'));
    }
  };
  refreshTargetAndPayload();
  type.addEventListener('change', refreshTargetAndPayload);

  // schedule 预设按钮
  const presetBox = h('div', { class: 'inline-actions' },
    SCHEDULE_PRESETS.map((p) => h('button', {
      class: 'btn btn-ghost btn-sm',
      type: 'button',
      onclick: () => { schedule.value = p.value; },
    }, p.label)));

  const m = modal({
    title: isEdit ? `编辑计划任务 · ${existing.name}` : '添加计划任务',
    width: '720px',
    content: h('div', {}, [
      grid(2,
        field('任务名称', name),
        field('任务类型', type),
      ),
      field('Crontab 表达式', schedule, '5 段：分 时 日 月 周'),
      presetBox,
      targetRow,
      payloadRow,
      field('启用', enabled),
    ]),
    footer: [
      button('取消', () => m.close(), 'default'),
      button(isEdit ? '保存' : '创建', async () => {
        try {
          const body = {
            name: name.value,
            type: type.value,
            schedule: schedule.value,
            target: target || '',
            payload,
            enabled: enabled.querySelector('input').checked,
          };
          if (isEdit) {
            await api.cron.update(existing.id, body);
          } else {
            await api.cron.create(body);
          }
          toast(isEdit ? '已保存' : '已创建', 'success');
          m.close();
          refresh();
        } catch (e) {
          toast(e.message || '保存失败', 'error');
        }
      }, 'primary'),
    ],
  });
}

function parseIntOrZero(s) {
  const n = parseInt(s, 10);
  return isNaN(n) ? 0 : n;
}

async function showHistory(j) {
  const body = h('div', {}, loading('加载中…'));
  const m = modal({
    title: `执行历史 · ${j.name}`,
    width: '760px',
    content: body,
  });
  try {
    const res = await api.cron.get(j.id);
    const job = res.job;
    const runs = res.runs || [];
    clear(body);
    body.appendChild(h('div', { class: 'meta-line' }, [
      h('span', { class: 'muted' }, '类型: '), typeLabel(job.type),
      h('span', { class: 'muted' }, '  schedule: '), job.schedule,
    ]));
    if (!runs.length) {
      body.appendChild(emptyState('还没有执行记录', null, 'cron'));
      return;
    }
    const cols = [
      { label: '开始时间', render: (r) => formatDateTime(r.started_at) },
      { label: '耗时', render: (r) => {
        if (!r.ended_at) return '-';
        const ms = new Date(r.ended_at) - new Date(r.started_at);
        return ms < 1000 ? `${ms}ms` : `${(ms/1000).toFixed(1)}s`;
      } },
      { label: '结果', render: (r) => tag(r.status || '-', r.status === 'ok' ? 'success' : r.status === 'failed' ? 'danger' : 'default') },
      { label: '消息', render: (r) => h('span', { class: 'mono-text' }, r.message || '') },
    ];
    body.appendChild(table(cols, runs));
  } catch (e) {
    clear(body).appendChild(errorState(`加载失败：${e.message || e}`));
  }
}