// 数据库中心（v0.9.0）：只管理面板创建的 site_* 库。
// 列表 / 改密 / 导出 / 导入 / 优化 / 连接信息；不做建库删库与表编辑。
import { api, formatBytes, formatNumber } from '../api.js';
import {
  h, clear, icon, button, tag, table, textInput, loading, errorState,
  emptyState, toast, modal, confirmDialog, codeBox, statCard,
} from '../components.js';

export async function render(container) {
  clear(container);
  container.appendChild(loading('加载数据库…'));

  let mysqlStatus = null;
  let databases = [];
  try {
    const [st, list] = await Promise.all([
      api.system.mysql().catch(() => null),
      api.databases.list().catch((e) => {
        // MySQL 未启用时后端返回 503；用系统探测结果给出可操作提示。
        if (e && e.status === 503) return { databases: [] };
        throw e;
      }),
    ]);
    mysqlStatus = st;
    databases = (list && list.databases) || [];
  } catch (err) {
    clear(container).appendChild(errorState(`数据库列表加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  clear(container);
  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '数据库'),
      h('div', { class: 'page-sub' }, '面板创建的 site_* 数据库；库的生命周期跟随站点'),
    ]),
    h('div', { class: 'page-actions' }, [
      button('刷新', () => render(container), 'default', 'refresh'),
    ]),
  ]));

  if (!mysqlStatus || !mysqlStatus.installed) {
    container.appendChild(h('div', { class: 'card' }, [
      h('div', { class: 'card-title' }, h('span', {}, 'MySQL / MariaDB 未启用')),
      h('div', { class: 'card-body' }, [
        emptyState('当前面板没有可用的 MySQL 子系统', null, 'database'),
        h('div', { class: 'field-hint' },
          '部署时需 INSTALL_MYSQL=1（deploy/install.sh），并在数据目录生成 mysql-root.cnf。' +
          '启用后重启面板，数据库页会自动出现。'),
      ]),
    ]));
    return;
  }

  const linked = databases.filter((d) => d.site_id).length;
  const totalBytes = databases.reduce((sum, d) => sum + (Number(d.size_bytes) || 0), 0);
  container.appendChild(h('div', { class: 'grid grid-4 stat-grid' }, [
    statCard('数据库', formatNumber(databases.length), 'site_* 库', 'database', ''),
    statCard('关联站点', formatNumber(linked), `${databases.length - linked} 个未关联`, 'sites', ''),
    statCard('占用空间', formatBytes(totalBytes), '数据 + 索引', 'disk', ''),
  ]));

  const body = h('div', { class: 'card-body' });
  const searchInp = textInput('', () => drawTable(), { placeholder: '搜索库名 / 站点名' });
  container.appendChild(h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, '数据库列表'),
      h('div', { class: 'card-title-extra' }, [
        h('div', { class: 'inline-actions' }, [icon('search'), searchInp]),
      ]),
    ]),
    body,
  ]));

  function drawTable() {
    const q = String(searchInp.value || '').trim().toLowerCase();
    const rows = q
      ? databases.filter((d) => String(d.name || '').toLowerCase().includes(q) || String(d.site_name || '').toLowerCase().includes(q))
      : databases;
    clear(body);
    if (!rows.length) {
      body.appendChild(emptyState(q ? '没有匹配的数据库' : '还没有由面板创建的数据库', null, 'database'));
      return;
    }
    body.appendChild(table([
      {
        label: '数据库名',
        width: '20%',
        render: (d) => h('div', {}, [h('code', {}, d.name), h('div', { class: 'field-hint' }, d.charset || 'utf8mb4')]),
      },
      {
        label: '站点',
        width: '16%',
        render: (d) => d.site_id
          ? h('a', { class: 'crumb-link', href: `#/sites/${encodeURIComponent(d.site_id)}` }, d.site_name || d.site_id)
          : tag('未关联', 'default'),
      },
      { label: '表数', align: 'right', render: (d) => formatNumber(d.tables || 0) },
      { label: '大小', align: 'right', render: (d) => formatBytes(d.size_bytes || 0) },
      { label: '连接', render: (d) => `${d.user || d.name}@${d.host || 'localhost'}:${d.port || 3306}` },
      {
        label: '操作',
        width: '300px',
        render: (d) => h('div', { class: 'inline-actions' }, [
          button('改密', () => resetPassword(d), 'default', 'key'),
          button('导出', () => exportDB(d), 'ghost', 'download'),
          button('导入', () => importDB(d), 'ghost', 'upload'),
          button('优化', () => optimizeDB(d), 'ghost', 'refresh'),
          button('复制连接', () => copyText(`mysql -h ${d.host || 'localhost'} -P ${d.port || 3306} -u ${d.user || d.name} -p ${d.name}`), 'ghost', 'edit'),
        ]),
      },
    ], rows));
  }

  async function resetPassword(d) {
    const ok = await confirmDialog({
      title: '重置数据库密码',
      message: `将重置 ${d.name} 对应用户的密码，旧密码立即失效。请确认所有使用该库的应用都已准备好更新配置。`,
      okText: '重置密码',
      danger: true,
    });
    if (!ok) return;
    try {
      const res = await api.databases.resetPassword(d.name);
      showSecret(d.name, res && res.password);
    } catch (e) {
      toast(e.message || '重置失败', 'error', 5000);
    }
  }

  function showSecret(name, password) {
    const pwdBox = codeBox(password || '(空)', { maxHeight: '80px' });
    const m = modal({
      title: '数据库密码已重置',
      width: '520px',
      content: h('div', {}, [
        h('p', {}, `数据库：${name}`),
        pwdBox,
        h('div', { class: 'field-hint' }, '明文只显示这一次，离开弹窗后无法再次查看。请立即保存到密码管理器或应用配置中。'),
      ]),
      footer: [
        button('复制密码', () => copyText(password || ''), 'default', 'key'),
        button('我已保存', () => m.close(), 'primary', 'check'),
      ],
    });
  }

  function exportDB(d) {
    // 浏览器会话 Cookie 会随导航自动携带；下载由后端生成临时 mysqldump。
    window.location.href = api.databases.exportURL(d.name);
    toast('正在生成备份，稍后浏览器会开始下载', 'info', 3200);
  }

  function importDB(d) {
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = '.sql,.gz';
    input.onchange = async () => {
      const file = input.files && input.files[0];
      if (!file) return;
      if (file.size > 100 * 1024 * 1024) {
        toast('文件超过 100MB 上限', 'error', 4000);
        return;
      }
      const ok = await confirmDialog({
        title: '导入 SQL',
        message: `将把 ${file.name} 导入 ${d.name}。导入会执行文件中的 SQL（可能覆盖同名表），确定继续？`,
        okText: '开始导入',
        danger: true,
      });
      if (!ok) return;
      const fd = new FormData();
      fd.append('file', file);
      toast('正在导入，大文件可能需要几分钟…', 'info', 8000);
      try {
        const res = await api.databases.import(d.name, fd);
        toast(`导入完成（${formatBytes(res && res.bytes ? res.bytes : 0)}）`, 'success', 4000);
        render(container);
      } catch (e) {
        toast(e.message || '导入失败', 'error', 7000);
      }
    };
    input.click();
  }

  async function optimizeDB(d) {
    const ok = await confirmDialog({
      title: '优化数据库',
      message: `将对 ${d.name} 的所有基础表执行 OPTIMIZE TABLE，期间可能短暂锁表。建议在低峰期操作。`,
      okText: '开始优化',
      danger: false,
    });
    if (!ok) return;
    toast('正在优化，请稍候…', 'info', 6000);
    try {
      const res = await api.databases.optimize(d.name);
      toast(`优化完成，共处理 ${formatNumber(res && res.tables ? res.tables : 0)} 张表`, 'success', 4000);
      render(container);
    } catch (e) {
      toast(e.message || '优化失败', 'error', 7000);
    }
  }

  drawTable();
}

// 复制文本：HTTPS/localhost 用 Clipboard API，HTTP 内网回退 execCommand。
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