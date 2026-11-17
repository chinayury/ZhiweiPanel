// 软件商店：区分「已安装」与「可安装」的软件，类似宝塔面板的应用商店。
// 只做目录 + 已安装检测；安装动作以命令提示形式给出，不自动执行系统命令。
import { api } from '../api.js';
import {
  h, clear, icon, button, tag, loading, errorState, emptyState,
  card, grid, sectionTitle,
} from '../components.js';

const CATEGORY_LABELS = {
  web: 'Web 服务',
  runtime: '运行时',
  database: '数据库',
  cache: '缓存',
  dev: '开发工具',
};

export async function render(container) {
  clear(container);

  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '软件商店'),
      h('div', { class: 'page-sub' }, '查看已安装与可安装的软件包，安装动作以命令提示形式给出'),
    ]),
    h('div', { class: 'page-actions' }, button('刷新', () => render(container), 'default', 'refresh')),
  ]));

  const body = h('div', {});
  container.appendChild(body);
  body.appendChild(loading('正在检测软件安装状态…'));

  let apps;
  try {
    apps = (await api.apps()).apps || [];
  } catch (err) {
    clear(body).appendChild(errorState(`软件商店加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  const installed = apps.filter((a) => a.status === 'installed');
  const available = apps.filter((a) => a.status !== 'installed');

  clear(body);

  body.appendChild(sectionTitle('已安装'));
  if (!installed.length) {
    body.appendChild(emptyState('未检测到已安装软件', null, 'store'));
  } else {
    body.appendChild(grid(2, ...installed.map((a) => appCard(a))));
  }

  body.appendChild(sectionTitle('可安装'));
  if (!available.length) {
    body.appendChild(emptyState('全部软件均已安装', null, 'store'));
  } else {
    body.appendChild(grid(2, ...available.map((a) => appCard(a))));
  }
}

function appCard(app) {
  const installed = app.status === 'installed';
  const meta = [
    CATEGORY_LABELS[app.category] || app.category,
    installed && app.version ? app.version : null,
  ].filter(Boolean).join(' · ');

  return card(null,
    h('div', { class: 'app-card-head' }, [
      h('div', { class: 'app-name' }, [
        icon('store', 18),
        h('span', {}, app.name),
      ]),
      installed ? tag('已安装', 'success') : tag('可安装', 'default'),
    ]),
    h('div', { class: 'field-hint' }, meta || ''),
    h('div', { class: 'app-desc' }, app.description || ''),
    installed
      ? null
      : h('div', { class: 'app-install' }, [
          h('code', { class: 'app-code' }, app.install_hint || ''),
          h('span', { class: 'field-hint' }, '请在服务器上自行执行（面板不代执行系统安装命令）'),
        ]),
  );
}
