// ZhiweiPanel 可复用 UI 原语
// 全部使用工厂函数返回原生 DOM 元素，无框架、无构建

// ---------------------------------------------------------------------------
// DOM 构造器
// ---------------------------------------------------------------------------

// 创建元素：h('div', {class:'x', onclick:fn}, child1, child2)
//
// 刻意不提供 'html' 属性（innerHTML）：那会把任意字符串当 HTML 解析，
// 是整套前端唯一的 XSS 开口。全部用户可控数据走 text / textContent /
// createTextNode 插入，由 appendAll 统一处理。
export function h(tag, props = null, ...children) {
  const el = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v === null || v === undefined || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else if (k === 'value') el._value = v; // 延后到子节点挂载后再赋值
      else if (k === 'checked' || k === 'disabled' || k === 'selected') el[k] = !!v;
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v);
      else el.setAttribute(k, v);
    }
  }
  appendAll(el, children);
  if (el._value !== undefined) el.value = el._value;
  return el;
}

// 批量挂载子节点（支持字符串、数组、DOM 节点、null）
export function appendAll(el, children) {
  for (const c of children.flat(4)) {
    if (c === null || c === undefined || c === false) continue;
    el.appendChild(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

// 清空容器
// el 为 null 时只告警不抛：容器缺失是上层装配问题，
// 不该让一个 UI 助手把整条路由炸成白屏。
export function clear(el) {
  if (!el) {
    console.warn('[zhiweipanel] clear() 收到空容器，已跳过');
    return el;
  }
  while (el.firstChild) el.removeChild(el.firstChild);
  return el;
}

// 行内唯一 id（行编辑器用来保持数组项身份）
let ridCounter = 0;
export function nextRid() {
  return ++ridCounter;
}

// ---------------------------------------------------------------------------
// 图标（内联 SVG，不依赖任何外部资源）
// ---------------------------------------------------------------------------
const ICONS = {
  dashboard: 'M3 3h7v7H3zM14 3h7v5h-7zM14 12h7v9h-7zM3 14h7v7H3z',
  sites: 'M12 2a10 10 0 100 20 10 10 0 000-20zm0 2c1.5 0 3.2 2.1 3.8 5H8.2C8.8 6.1 10.5 4 12 4zM4.3 11h3.4c-.1 1.3-.1 2.7 0 4H4.3a8 8 0 010-4zm0 6h3.7c.6 1.9 1.5 3.3 2.4 3.8A8 8 0 014.3 17zM12 20c-1.5 0-3.2-2.1-3.8-5h7.6c-.6 2.9-2.3 5-3.8 5zm3.1-2h3.6a8 8 0 01-4.1 3.8c.9-.5 1.8-1.9 2.4-3.8zm3.7-2h-3.4c.1-1.3.1-2.7 0-4h3.4a8 8 0 010 4z',
  cert: 'M12 1l9 4v6c0 5.5-3.8 10.6-9 12-5.2-1.4-9-6.5-9-12V5l9-4zm-1 14l6-6-1.4-1.4L11 12.2 8.4 9.6 7 11l4 4z',
  cron: 'M12 2a10 10 0 100 20 10 10 0 000-20zm0 2v8l5.5 3.3 1-1.7L13 10.4V4h-1z',
  backup: 'M19 12v7H5v-7H3v7c0 1.1.9 2 2 2h14a2 2 0 002-2v-7h-2zm-7-.7l4.6-4.6L13 8.3V16h2V8.3l3.6-3.6L18 5.3l-6 6z',
  logs: 'M4 3h16v2H4zm0 5h16v2H4zm0 5h16v2H4zm0 5h10v2H4z',
  alerts: 'M12 2a7 7 0 00-7 7v4.6L3.3 17a1 1 0 00.9 1.5h15.6a1 1 0 00.9-1.5L19 13.6V9a7 7 0 00-7-7zm0 20a3.2 3.2 0 002.9-2H9.1A3.2 3.2 0 0012 22z',
  settings: 'M12 8a4 4 0 100 8 4 4 0 000-8zm0 6a2 2 0 110-4 2 2 0 010 4zm9-2l2 1.6-2 3.4-2.4-.9a7.9 7.9 0 01-1.7 1l-.4 2.5H13l-.4-2.5a8 8 0 01-1.7-1l-2.4.9-2-3.4L8.6 12l-2-1.6 2-3.4 2.4.9a8 8 0 011.7-1L15 4h3.6l.4 2.5a8 8 0 011.7 1l2.4-.9 2 3.4L21 12z',
  plus: 'M11 5h2v6h6v2h-6v6h-2v-6H5v-2h6V5z',
  trash: 'M6 7h12v13a2 2 0 01-2 2H8a2 2 0 01-2-2V7zm3-4h6l1 2h4v2H4V5h4l1-2z',
  edit: 'M3 17.2V21h3.8L17.8 10 14 6.2 3 17.2zM20.7 7.1a1 1 0 000-1.4l-2.4-2.4a1 1 0 00-1.4 0l-1.8 1.8L18.9 8.9l1.8-1.8z',
  refresh: 'M17.6 6.4A8 8 0 104 12h2a6 6 0 115.6 7.8L14 16.5h7V9.4l-2.5 2.5A8 8 0 0017.6 6.4z',
  eye: 'M12 5C5.6 5 1 12 1 12s4.6 7 11 7 11-7 11-7-4.6-7-11-7zm0 11a4 4 0 110-8 4 4 0 010 8z',
  save: 'M5 3h11l3 3v15H5V3zm3 2v5h7V5H8zm5 8a3 3 0 100 6 3 3 0 000-6z',
  logout: 'M10 3H5a2 2 0 00-2 2v14a2 2 0 002 2h5v-2H5V5h5V3zm6.2 3.2L14.8 7.6 17.2 10H9v2h8.2l-2.4 2.4 1.4 1.4L21 12l-4.8-4.8z',
  search: 'M10 2a8 8 0 105.3 14l5.4 5.4 1.4-1.4-5.4-5.4A8 8 0 0010 2zm0 2a6 6 0 110 12 6 6 0 010-12z',
  check: 'M9 16.2L4.8 12l-1.4 1.4L9 19 21 7l-1.4-1.4L9 16.2z',
  // 主题图标：太阳（深色下显示，点它切浅色）/ 月亮（浅色下显示，点它切深色）
  sun: 'M12 7a5 5 0 110 10 5 5 0 010-10zm0-6h0l1 2.6V1.5h-2V3.6L12 1zM12 23a1 1 0 01-1-1v-2.1l-1 2.6 1-.5 1 .5-1-2.6V22a1 1 0 01-1 1zM1 12l2.6-1H1.5v2h2.1L1 12zm22 0l-2.6 1h2.1v-2h-2.1L23 12zM4.2 5.6l1.4 1.4-1.8 1.8-1.4-1.4 1.8-1.8zm14.2 14.2l-1.4-1.4 1.8-1.8 1.4 1.4-1.8 1.8zM19.8 5.6l-1.4 1.4-1.8-1.8 1.4-1.4 1.8 1.8zM5.6 19.8l1.4-1.4 1.8 1.8-1.4 1.4-1.8-1.8z',
  moon: 'M13.5 2A9 9 0 1022 12.5 7.5 7.5 0 0113.5 2z',
  close: 'M18.3 5.7L12 12l6.3 6.3-1.4 1.4L10.6 13.4 4.3 19.7 2.9 18.3 9.2 12 2.9 5.7 4.3 4.3l6.3 6.3 6.3-6.3z',
  warning: 'M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z',
  info: 'M12 2a10 10 0 100 20 10 10 0 000-20zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z',
  power: 'M13 3h-2v10h2V3zm4.8 2.2l-1.4 1.4A7 7 0 1112 5V3a9 9 0 109.8 2.2z',
  reload: 'M12 5V1L7 6l5 5V7a6 6 0 11-6 6H4a8 8 0 108-8z',
  upload: 'M9 16h6v-6h4l-7-7-7 7h4v6zm-4 2h14v2H5v-2z',
  download: 'M11 3h2v9h4l-5 6-5-6h4V3zM5 19h14v2H5v-2z',
  key: 'M14 2a8 8 0 00-7.7 10.2L2 16.5V22h5.5l1.3-1.3v-2.2h2.2l1.6-1.6A8 8 0 1014 2zm3 6.5a1.8 1.8 0 110-3.6 1.8 1.8 0 010 3.6z',
  disk: 'M12 2a10 10 0 100 20 10 10 0 000-20zm0 3a7 7 0 110 14 7 7 0 010-14zm0 3a4 4 0 100 8 4 4 0 000-8z',
  clock: 'M12 2a10 10 0 100 20 10 10 0 000-20zm1 10V6h-2v7h6v-2h-4z',
  store: 'M5 3h14l1 1v3H4V4l1-1zM4 9h16l-1 12H5L4 9zm4 4h2v6H8v-6zm4 0h2v6h-2v-6z',
};

export function icon(name, size = 16) {
  const d = ICONS[name] || ICONS.info;
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('width', size);
  svg.setAttribute('height', size);
  svg.setAttribute('fill', 'currentColor');
  svg.setAttribute('aria-hidden', 'true');
  svg.classList.add('icon');
  const p = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  p.setAttribute('d', d);
  svg.appendChild(p);
  return svg;
}

// 带图标的按钮
export function iconButton(name, title, onClick, kind = '') {
  return h('button', {
    class: `btn btn-icon ${kind}`.trim(),
    type: 'button',
    title,
    onclick: onClick,
  }, icon(name));
}

// ---------------------------------------------------------------------------
// 基础控件
// ---------------------------------------------------------------------------

// 开关
export function switchControl(checked, onChange, disabled = false) {
  const el = h('label', { class: `switch ${disabled ? 'is-disabled' : ''}`.trim() });
  const input = h('input', {
    type: 'checkbox',
    checked: !!checked,
    disabled,
    onchange: (e) => onChange(e.target.checked, e),
  });
  el.appendChild(input);
  el.appendChild(h('span', { class: 'switch-slider' }));
  return el;
}

// 标签
export function tag(text, type = 'default') {
  return h('span', { class: `tag tag-${type}` }, text);
}

// 按钮
export function button(text, onClick, kind = 'default', iconName = null) {
  return h('button', {
    class: `btn btn-${kind}`,
    type: 'button',
    onclick: onClick,
  }, iconName ? icon(iconName) : null, h('span', {}, text));
}

// 表单字段容器
export function field(label, control, hint = null, wide = false) {
  return h('div', { class: `field ${wide ? 'field-wide' : ''}`.trim() }, [
    h('label', { class: 'field-label' }, label),
    control,
    hint ? h('div', { class: 'field-hint' }, hint) : null,
  ]);
}

// 文本输入
export function textInput(value, onInput, opts = {}) {
  return h('input', {
    class: 'input',
    type: opts.type || 'text',
    value: value === undefined || value === null ? '' : value,
    placeholder: opts.placeholder || '',
    disabled: opts.disabled,
    oninput: (e) => onInput && onInput(e.target.value, e),
  });
}

// 数字输入
export function numberInput(value, onInput, opts = {}) {
  return h('input', {
    class: 'input',
    type: 'number',
    value: value === undefined || value === null ? '' : value,
    placeholder: opts.placeholder || '',
    min: opts.min,
    max: opts.max,
    step: opts.step === undefined ? 1 : opts.step,
    disabled: opts.disabled,
    oninput: (e) => onInput && onInput(e.target.value === '' ? 0 : Number(e.target.value), e),
  });
}

// 下拉选择
export function selectInput(value, options, onChange, opts = {}) {
  const sel = h('select', {
    class: 'input',
    disabled: opts.disabled,
    onchange: (e) => onChange && onChange(e.target.value, e),
  });
  for (const o of options) {
    const opt = typeof o === 'object' ? o : { value: o, label: o };
    sel.appendChild(h('option', { value: opt.value, selected: String(opt.value) === String(value) }, opt.label));
  }
  sel.value = value === undefined || value === null ? '' : value;
  return sel;
}

// 多选（多个 checkbox 平铺）
export function checkboxGroup(values, options, onChange) {
  const box = h('div', { class: 'check-group' });
  for (const o of options) {
    const opt = typeof o === 'object' ? o : { value: o, label: o };
    const input = h('input', {
      type: 'checkbox',
      value: opt.value,
      checked: (values || []).includes(opt.value),
      onchange: (e) => {
        const next = Array.from(box.querySelectorAll('input:checked')).map((i) => i.value);
        onChange(next);
      },
    });
    box.appendChild(h('label', { class: 'check-item' }, input, h('span', {}, opt.label)));
  }
  return box;
}

// 开关型字段（带说明的开关行）
export function switchField(label, checked, onChange, hint = null) {
  return h('div', { class: 'switch-field' }, [
    h('div', { class: 'switch-field-text' }, [
      h('div', { class: 'switch-field-label' }, label),
      hint ? h('div', { class: 'field-hint' }, hint) : null,
    ]),
    switchControl(checked, onChange),
  ]);
}

// 文本域
export function textArea(value, onInput, opts = {}) {
  const ta = h('textarea', {
    class: `input textarea ${opts.mono ? 'mono' : ''}`.trim(),
    rows: opts.rows || 6,
    placeholder: opts.placeholder || '',
    spellcheck: 'false',
    oninput: (e) => onInput && onInput(e.target.value, e),
  });
  ta.value = value || '';
  return ta;
}

// 复选框（带文字）
export function checkboxField(label, checked, onChange) {
  const input = h('input', {
    type: 'checkbox',
    checked: !!checked,
    onchange: (e) => onChange(e.target.checked, e),
  });
  return h('label', { class: 'check-item check-item-block' }, input, h('span', {}, label));
}

// ---------------------------------------------------------------------------
// 卡片 / 区块
// ---------------------------------------------------------------------------
export function card(title, ...content) {
  return h('div', { class: 'card' }, [
    title ? h('div', { class: 'card-title' }, [
      h('span', {}, title),
      h('span', { class: 'card-title-extra' }),
    ]) : null,
    h('div', { class: 'card-body' }, content),
  ]);
}

// 带右上角操作区的卡片
export function cardWithActions(title, actions, ...content) {
  return h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, title),
      h('div', { class: 'card-title-extra' }, actions),
    ]),
    h('div', { class: 'card-body' }, content),
  ]);
}

// 统计卡片
export function statCard(label, value, sub, iconName = 'info', tone = '') {
  return h('div', { class: `stat-card ${tone}`.trim() }, [
    h('div', { class: 'stat-icon' }, icon(iconName, 20)),
    h('div', { class: 'stat-main' }, [
      h('div', { class: 'stat-value' }, value),
      h('div', { class: 'stat-label' }, label),
      sub ? h('div', { class: 'stat-sub' }, sub) : null,
    ]),
  ]);
}

// 三种状态：加载中 / 空态 / 错误态
export function loading(text = '加载中…') {
  return h('div', { class: 'state-box' }, [
    h('div', { class: 'spinner' }),
    h('div', { class: 'state-text' }, text),
  ]);
}

export function emptyState(text = '暂无数据', actionNode = null, iconName = 'info') {
  return h('div', { class: 'state-box state-empty' }, [
    h('div', { class: 'state-icon' }, icon(iconName, 32)),
    h('div', { class: 'state-text' }, text),
    actionNode,
  ]);
}

export function errorState(text = '加载失败', retry = null) {
  return h('div', { class: 'state-box state-error' }, [
    h('div', { class: 'state-icon' }, icon('warning', 32)),
    h('div', { class: 'state-text' }, text),
    retry ? button('重试', retry, 'default', 'refresh') : null,
  ]);
}

// 页面标题栏
export function pageHeader(title, ...actions) {
  return h('div', { class: 'page-header' }, [
    h('h1', { class: 'page-title' }, title),
    h('div', { class: 'page-actions' }, actions),
  ]);
}

// ---------------------------------------------------------------------------
// 表格
// ---------------------------------------------------------------------------
// columns: [{key, label, width, align, render(row, index)}]
export function table(columns, rows, emptyNode = null) {
  if (!rows || rows.length === 0) {
    return emptyNode || emptyState('暂无数据');
  }
  const thead = h('thead', {}, h('tr', {}, columns.map((c) =>
    h('th', { style: c.width ? { width: c.width } : null, class: c.align ? `ta-${c.align}` : '' }, c.label)
  )));
  const tbody = h('tbody', {}, rows.map((row, i) =>
    h('tr', {}, columns.map((c) =>
      h('td', { class: c.align ? `ta-${c.align}` : '' }, c.render ? c.render(row, i) : row[c.key])
    ))
  ));
  return h('div', { class: 'table-wrap' }, h('table', { class: 'table' }, thead, tbody));
}

// ---------------------------------------------------------------------------
// 模态框 / 确认框 / Toast
// ---------------------------------------------------------------------------
let modalStack = [];

// 打开模态框：modal({title, content, width, footer, onClose})
export function modal({ title, content, width = '560px', footer = null, onClose = null, closable = true }) {
  const overlay = h('div', { class: 'modal-overlay' });
  const box = h('div', { class: 'modal', style: { maxWidth: width } });

  const close = () => {
    if (!overlay.parentNode) return;
    overlay.remove();
    modalStack = modalStack.filter((m) => m !== close);
    if (onClose) onClose();
  };

  const head = h('div', { class: 'modal-head' }, [
    h('div', { class: 'modal-title' }, title || ''),
    closable ? iconButton('close', '关闭', close) : null,
  ]);
  const body = h('div', { class: 'modal-body' });
  appendAll(body, [content]);

  box.appendChild(head);
  box.appendChild(body);
  if (footer) box.appendChild(h('div', { class: 'modal-foot' }, footer));
  overlay.appendChild(box);
  overlay.addEventListener('mousedown', (e) => {
    if (e.target === overlay && closable) close();
  });

  document.body.appendChild(overlay);
  modalStack.push(close);
  return { close, body, box, overlay };
}

// ESC 关闭最上层模态框
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && modalStack.length) {
    const top = modalStack[modalStack.length - 1];
    top();
  }
});

// 确认对话框 → Promise<boolean>
export function confirmDialog({ title = '确认操作', message = '', okText = '确定', cancelText = '取消', danger = false }) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (v) => {
      if (done) return;
      done = true;
      resolve(v);
      m.close();
    };
    const m = modal({
      title,
      width: '420px',
      content: h('div', { class: 'confirm-body' }, [
        icon(danger ? 'warning' : 'info', 24),
        h('div', {}, message),
      ]),
      footer: [
        button(cancelText, () => finish(false), 'default'),
        button(okText, () => finish(true), danger ? 'danger' : 'primary'),
      ],
      onClose: () => finish(false),
    });
  });
}

// 提示框（只读模态框）
export function alertDialog(title, content, width = '520px') {
  // m 必须在闭包里可见：footer 里的按钮回调是在点击时才执行的，
  // 那时 modal 已经返回，m 已赋值。
  const m = modal({
    title,
    width,
    content,
    footer: [button('关闭', () => m.close(), 'primary')],
  });
  return m;
}

// Toast 提示
let toastBox = null;
export function toast(message, type = 'info', duration = 2600) {
  if (!toastBox) {
    toastBox = h('div', { class: 'toast-box' });
    document.body.appendChild(toastBox);
  }
  const t = h('div', { class: `toast toast-${type}` }, [
    icon(type === 'error' ? 'warning' : type === 'success' ? 'check' : 'info', 16),
    h('span', {}, message),
  ]);
  toastBox.appendChild(t);
  setTimeout(() => {
    t.classList.add('toast-out');
    setTimeout(() => t.remove(), 220);
  }, duration);
  return t;
}

// ---------------------------------------------------------------------------
// 数组行编辑器（重定向 / 代理 / 告警规则 / 子目录 / 渠道 统一复用）
// ---------------------------------------------------------------------------
// columns: [{key, label, type, options, width, placeholder, min, step, flex}]
//   type: text | number | select | checkbox | textarea | static
// items: 直接操作目标数组，字段变化即写回（保持数组引用，实时同步到站点对象）
export function rowEditor({ items, columns, onChange, addText = '添加一行', emptyText = '暂无规则' }) {
  const wrap = h('div', { class: 'row-editor' });
  const tableBox = h('div', { class: 'row-editor-body' });
  const foot = h('div', { class: 'row-editor-foot' });
  wrap.appendChild(tableBox);
  wrap.appendChild(foot);

  const notify = () => {
    if (onChange) onChange(items);
  };

  const renderRows = () => {
    clear(tableBox);
    if (!items.length) {
      tableBox.appendChild(emptyState(emptyText, null, 'edit'));
      return;
    }
    tableBox.appendChild(
      table(
        [
          {
            label: '#',
            width: '44px',
            render: (_r, i) => h('span', { class: 'row-index' }, String(i + 1)),
          },
          ...columns.map((c) => ({
            label: c.label,
            width: c.width,
            align: c.align,
            render: (row) => cellControl(c, row, renderRows, notify),
          })),
          {
            label: '',
            width: '56px',
            render: (row) => iconButton('trash', '删除该行', () => {
              const idx = items.indexOf(row);
              if (idx >= 0) items.splice(idx, 1);
              renderRows();
              notify();
            }, 'danger-ghost'),
          },
        ],
        items
      )
    );
  };

  foot.appendChild(
    button(addText, () => {
      const blank = { _rid: nextRid() };
      for (const c of columns) {
        if (!(c.key in blank)) {
          blank[c.key] = c.type === 'checkbox' ? false : c.type === 'number' ? 0 : '';
        }
      }
      items.push(blank);
      renderRows();
      notify();
    }, 'default', 'plus')
  );

  renderRows();
  return wrap;
}

// 行编辑器单元格控件
function cellControl(col, row, rerender, notify) {
  const commit = (v) => {
    row[col.key] = v;
    notify();
  };
  if (col.type === 'checkbox') {
    return switchControl(row[col.key], commit);
  }
  if (col.type === 'number') {
    return numberInput(row[col.key], commit, { min: col.min, step: col.step === undefined ? 1 : col.step, placeholder: col.placeholder });
  }
  if (col.type === 'select') {
    return selectInput(row[col.key], col.options, commit);
  }
  if (col.type === 'textarea') {
    return textArea(row[col.key], commit, { rows: col.rows || 3, placeholder: col.placeholder, mono: true });
  }
  if (col.type === 'static') {
    return h('span', { class: 'cell-static' }, row[col.key]);
  }
  return textInput(row[col.key], commit, { placeholder: col.placeholder });
}

// 字符串数组编辑器（域名列表 / 允许 IP / 扩展名 / UA 关键字 / 渠道目标）
// mode: 'lines'（一行一个）| 'comma'（逗号分隔）
export function stringListEditor(values, onChange, opts = {}) {
  const ta = h('textarea', {
    class: 'input textarea mono',
    rows: opts.rows || 4,
    spellcheck: 'false',
    placeholder: opts.placeholder || '',
  });
  ta.value = (values || []).join('\n');
  ta.addEventListener('input', () => {
    const list = ta.value.split('\n').map((s) => s.trim()).filter(Boolean);
    onChange(list);
  });
  return h('div', {}, [
    ta,
    h('div', { class: 'field-hint' }, opts.hint || '一行一项，保存时自动去空与去重'),
  ]);
}

// 小型行内标签选择器（多选 pill 形式，用于协议、扩展名等）
export function pillMultiSelect(values, options, onChange) {
  const box = h('div', { class: 'pill-group' });
  const cur = new Set(values || []);
  for (const o of options) {
    const opt = typeof o === 'object' ? o : { value: o, label: o };
    const el = h('button', {
      class: `pill ${cur.has(opt.value) ? 'is-active' : ''}`.trim(),
      type: 'button',
      onclick: () => {
        if (cur.has(opt.value)) cur.delete(opt.value);
        else cur.add(opt.value);
        el.classList.toggle('is-active');
        onChange(Array.from(cur));
      },
    }, opt.label);
    box.appendChild(el);
  }
  return box;
}

// 表单栅格容器
export function grid(cols = 2, ...children) {
  return h('div', { class: `grid grid-${cols}` }, children);
}

// 区块小标题（带竖线）
export function sectionTitle(text, hint = null) {
  return h('div', { class: 'section-title' }, [
    h('span', { class: 'section-bar' }),
    h('span', {}, text),
    hint ? h('span', { class: 'field-hint' }, hint) : null,
  ]);
}

// 只读代码框（nginx 配置预览 / 主配置查看）
export function codeBox(text, { maxHeight = '460px' } = {}) {
  return h('pre', { class: 'code-box', style: { maxHeight } }, h('code', {}, text || '# 配置为空'));
}

// 证书剩余天数分级（>30 绿 / 8-30 黄 / <8 红）
export function certDaysBadge(days) {
  if (days === null || days === undefined) return tag('未知', 'default');
  if (days < 0) return tag(`已过期 ${Math.abs(days)} 天`, 'danger');
  if (days < 8) return tag(`${days} 天`, 'danger');
  if (days <= 30) return tag(`${days} 天`, 'warning');
  return tag(`${days} 天`, 'success');
}

// 进度条（有效期）
export function progressBar(percent, tone = '') {
  const p = Math.max(0, Math.min(100, Math.round(percent)));
  return h('div', { class: 'progress' }, h('div', { class: `progress-bar ${tone}`.trim(), style: { width: p + '%' } }));
}

// 内联 SVG 折线 + 面积图（仪表盘流量趋势，无图表库）
// data: [{label, value}]，返回 {el, max}
export function lineChart(data, opts = {}) {
  const w = opts.width || 760;
  const hgt = opts.height || 220;
  const padL = 48;
  const padR = 16;
  const padT = 16;
  const padB = 30;
  const values = data.map((d) => Number(d.value) || 0);
  const maxV = Math.max(1, ...values);
  const innerW = w - padL - padR;
  const innerH = hgt - padT - padB;
  const stepX = data.length > 1 ? innerW / (data.length - 1) : 0;

  const xAt = (i) => padL + (data.length > 1 ? i * stepX : innerW / 2);
  const yAt = (v) => padT + innerH - (v / maxV) * innerH;

  const svgNS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(svgNS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${w} ${hgt}`);
  svg.setAttribute('class', 'chart-svg');
  svg.setAttribute('preserveAspectRatio', 'none');

  const mk = (tag, attrs, text) => {
    const el = document.createElementNS(svgNS, tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    if (text !== undefined) el.textContent = text;
    return el;
  };

  // 横向网格线与 Y 轴刻度
  const ticks = 4;
  for (let i = 0; i <= ticks; i++) {
    const y = padT + (innerH / ticks) * i;
    svg.appendChild(mk('line', { x1: padL, y1: y, x2: w - padR, y2: y, class: 'chart-grid' }));
    svg.appendChild(mk('text', { x: padL - 8, y: y + 4, class: 'chart-axis-label', 'text-anchor': 'end' },
      opts.fmtY ? opts.fmtY(maxV - (maxV / ticks) * i) : Math.round(maxV - (maxV / ticks) * i)));
  }

  if (!data.length) return h('div', { class: 'chart-empty' }, '暂无统计数据');

  // 面积填充
  const areaPts = [`${padL},${padT + innerH}`, ...data.map((d, i) => `${xAt(i)},${yAt(Number(d.value) || 0)}`), `${xAt(data.length - 1)},${padT + innerH}`];
  svg.appendChild(mk('polygon', { points: areaPts.join(' '), class: 'chart-area' }));

  // 折线
  const linePts = data.map((d, i) => `${xAt(i)},${yAt(Number(d.value) || 0)}`).join(' ');
  svg.appendChild(mk('polyline', { points: linePts, class: 'chart-line' }));

  // 数据点 + X 轴标签
  data.forEach((d, i) => {
    svg.appendChild(mk('circle', { cx: xAt(i), cy: yAt(Number(d.value) || 0), r: 3.5, class: 'chart-dot' }));
    svg.appendChild(mk('title', {}, `${d.label}：${opts.fmtTip ? opts.fmtTip(d.value) : d.value}`));
    svg.appendChild(mk('text', { x: xAt(i), y: hgt - 8, class: 'chart-axis-label', 'text-anchor': 'middle' }, d.label));
  });

  return h('div', { class: 'chart-wrap' }, svg);
}

// 内联 SVG 柱状图（状态码分布）
export function barChart(data, opts = {}) {
  const w = opts.width || 460;
  const hgt = opts.height || 180;
  const padL = 36, padR = 12, padT = 14, padB = 26;
  const innerW = w - padL - padR;
  const innerH = hgt - padT - padB;
  const maxV = Math.max(1, ...data.map((d) => Number(d.value) || 0));
  const bw = innerW / Math.max(1, data.length);
  const svgNS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(svgNS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${w} ${hgt}`);
  svg.setAttribute('class', 'chart-svg');
  const mk = (tag, attrs, text) => {
    const el = document.createElementNS(svgNS, tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    if (text !== undefined) el.textContent = text;
    return el;
  };
  svg.appendChild(mk('line', { x1: padL, y1: padT + innerH, x2: w - padR, y2: padT + innerH, class: 'chart-grid' }));
  svg.appendChild(mk('text', { x: padL - 6, y: padT + 4, class: 'chart-axis-label', 'text-anchor': 'end' }, String(maxV)));
  svg.appendChild(mk('text', { x: padL - 6, y: padT + innerH + 4, class: 'chart-axis-label', 'text-anchor': 'end' }, '0'));
  data.forEach((d, i) => {
    const v = Number(d.value) || 0;
    const bh = maxV ? (v / maxV) * innerH : 0;
    const x = padL + i * bw + bw * 0.2;
    const bwidth = bw * 0.6;
    svg.appendChild(mk('rect', {
      x, y: padT + innerH - bh, width: bwidth, height: Math.max(1, bh),
      rx: 3, class: `chart-bar ${d.tone || ''}`.trim(),
    }));
    svg.appendChild(mk('title', {}, `${d.label}：${v}`));
    svg.appendChild(mk('text', { x: x + bwidth / 2, y: hgt - 8, class: 'chart-axis-label', 'text-anchor': 'middle' }, d.label));
  });
  return h('div', { class: 'chart-wrap' }, svg);
}

// 标签页容器
// tabs: [{key, label, render: () => Node}]
export function tabPanel(tabs, opts = {}) {
  let active = opts.active || (tabs[0] && tabs[0].key);
  const nav = h('div', { class: 'tabs' });
  const panel = h('div', { class: 'tab-panel' });
  const extra = h('div', { class: 'tabs-extra' });

  const show = (key) => {
    active = key;
    Array.from(nav.children).forEach((el) => {
      el.classList.toggle('is-active', el.dataset.key === key);
    });
    clear(panel);
    const tab = tabs.find((t) => t.key === key) || tabs[0];
    panel.appendChild(tab.render());
    if (opts.onChange) opts.onChange(key, tab);
  };

  for (const t of tabs) {
    nav.appendChild(h('button', {
      class: `tab ${t.key === active ? 'is-active' : ''}`.trim(),
      type: 'button',
      dataset: { key: t.key },
      onclick: () => show(t.key),
    }, t.label));
  }

  if (opts.actions) appendAll(extra, opts.actions);

  const wrap = h('div', { class: 'tab-layout' }, [nav, extra, panel]);
  show(active);
  wrap._show = show;
  return wrap;
}

// 分页控件
export function pager({ page, pages, onChange }) {
  if (pages <= 1) return null;
  return h('div', { class: 'pager' }, [
    h('button', { class: 'btn btn-default', type: 'button', disabled: page <= 1, onclick: () => onChange(page - 1) }, '上一页'),
    h('span', { class: 'pager-text' }, `第 ${page} / ${pages} 页`),
    h('button', { class: 'btn btn-default', type: 'button', disabled: page >= pages, onclick: () => onChange(page + 1) }, '下一页'),
  ]);
}