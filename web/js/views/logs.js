// 日志查看：历史日志分页 + 实时 tail（WebSocket）
import { api, formatBytes, formatDateTime, timeAgo } from '../api.js';
import {
  h, clear, icon, button, tag, table, loading, errorState, emptyState, toast,
  selectInput, textInput, field, modal, confirmDialog, codeBox, pager, iconButton,
} from '../components.js';

const PAGE_SIZE = 100;

// 视图内状态
const state = { siteId: '', type: 'access', page: 1, ws: null, tailing: false };
const lineBuf = [];
const MAX_LINES = 1000;

export async function render(container) {
  // 视图无卸载钩子，模块级 state 跨渲染存活。若不在这里收尾，
  // 上一次的 WebSocket 会继续往已分离的 DOM 推送，且 state.tailing
  // 仍为 true，再次进入时"开始实时跟踪"被直接拦掉。
  stopTail();
  lineBuf.length = 0;

  clear(container);
  container.appendChild(loading('正在加载站点…'));

  let sites = [];
  try {
    const res = await api.sites.list();
    sites = res.sites || [];
  } catch (err) {
    clear(container).appendChild(errorState(`站点列表加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  if (!sites.length) {
    clear(container).appendChild(h('div', {}, [
      emptyState('还没有站点，无法查看日志', null, 'logs'),
    ]));
    return;
  }

  // 预选站点：优先路由参数，其次 sessionStorage（从站点列表/详情跳转过来）
  const preset = sessionStorage.getItem('zhiweipanel.logs.site');
  sessionStorage.removeItem('zhiweipanel.logs.site');
  state.siteId = sites.some((s) => s.id === preset) ? preset : (sites.some((s) => s.id === state.siteId) ? state.siteId : sites[0].id);
  state.page = 1;

  clear(container);
  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '日志'),
      h('div', { class: 'page-sub' }, '查看站点访问日志与错误日志，支持实时跟踪'),
    ]),
    h('div', { class: 'page-actions' }, button('刷新', () => reload(), 'default', 'refresh')),
  ]));

  // ---- 过滤条 ----
  const siteSel = selectInput(state.siteId, sites.map((s) => ({ value: s.id, label: `${s.name}（${(s.domains || [])[0] || s.id}）` })), (v) => {
    stopTail();
    resetTailUI();
    lineBuf.length = 0;
    state.siteId = v;
    state.page = 1;
    reload();
  });
  const typeSel = selectInput(state.type, [
    { value: 'access', label: '访问日志' },
    { value: 'error', label: '错误日志' },
  ], (v) => {
    stopTail();
    resetTailUI();
    lineBuf.length = 0;
    state.type = v;
    state.page = 1;
    reload();
  });

  container.appendChild(h('div', { class: 'filter-bar' }, [
    h('div', { class: 'filter-item' }, [h('span', { class: 'filter-label' }, '站点'), siteSel]),
    h('div', { class: 'filter-item' }, [h('span', { class: 'filter-label' }, '类型'), typeSel]),
  ]));

  // ---- 实时 tail ----
  const tailBox = h('div', { class: 'tail-box' });
  const tailStatus = h('span', { class: 'tail-status' }, '未连接');
  const startBtn = button('开始实时跟踪', () => startTail(), 'primary', 'logs');
  const stopBtn = button('停止', () => { stopTail(); resetTailUI(); }, 'default', 'close');
  const clearBtn = button('清空显示', () => {
    lineBuf.length = 0;
    drawTail();
  }, 'default', 'trash');

  container.appendChild(h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, '实时跟踪'),
      h('div', { class: 'card-title-extra' }, [tailStatus]),
    ]),
    h('div', { class: 'card-body' }, [
      h('div', { class: 'inline-actions' }, [startBtn, stopBtn, clearBtn]),
      tailBox,
    ]),
  ]));

  // ---- 历史日志 ----
  const histTitle = h('span', {}, '历史日志');
  const histInfo = h('span', { class: 'field-hint' }, '');
  const histBox = h('div', { class: 'card' }, [
    h('div', { class: 'card-title' }, [
      h('span', {}, histTitle),
      h('div', { class: 'card-title-extra' }, histInfo),
    ]),
    h('div', { class: 'card-body' }, loading('加载日志…')),
  ]);
  container.appendChild(histBox);

  function setTailStatus(text, tone = '') {
    clear(tailStatus);
    tailStatus.className = `tail-status ${tone}`;
    tailStatus.appendChild(h('span', { class: 'dot ' + (tone === 'on' ? 'dot-green' : tone === 'err' ? 'dot-red' : 'dot-gray') }));
    tailStatus.appendChild(h('span', {}, text));
  }

  function drawTail() {
    clear(tailBox);
    if (!lineBuf.length) {
      tailBox.appendChild(state.tailing
        ? h('div', { class: 'tail-waiting' }, [h('div', { class: 'spinner' }), '等待日志输出…'])
        : emptyState('点击「开始实时跟踪」后，新日志会实时显示在这里', null, 'logs'));
      return;
    }
    const pre = h('div', { class: 'tail-lines' });
    for (const l of lineBuf) {
      pre.appendChild(tailLineNode(l));
    }
    tailBox.appendChild(pre);
    // 自动滚动到底部
    tailBox.scrollTop = tailBox.scrollHeight;
  }

  function tailLineNode(l) {
    const status = Number(l.status) || 0;
    const tone = status >= 500 ? 'log-5xx' : status >= 400 ? 'log-4xx' : status >= 300 ? 'log-3xx' : 'log-2xx';
    if (state.type === 'error' || l.error) {
      return h('div', { class: 'tail-line' }, [
        h('span', { class: 'tail-time' }, formatDateTime(l.time)),
        h('span', { class: 'tail-msg' }, l.error || l.line || l.message || JSON.stringify(l)),
      ]);
    }
    return h('div', { class: 'tail-line' }, [
      h('span', { class: 'tail-time' }, formatDateTime(l.time)),
      h('span', { class: 'tail-ip' }, l.remote_addr || '-'),
      h('span', { class: 'tail-method' }, l.method || '-'),
      h('span', { class: 'tail-path', title: l.path || '' }, l.path || '-'),
      h('span', { class: `tail-status-code ${tone}` }, l.status || '-'),
      h('span', { class: 'tail-size' }, formatBytes(l.size || 0)),
      h('span', { class: 'tail-time-cost' }, l.req_time ? `${l.req_time}s` : ''),
    ]);
  }

  // 建立 WebSocket 实时跟踪
  async function startTail() {
    if (state.tailing) return;
    startBtn.disabled = true;
    setTailStatus('连接中…');
    try {
      const res = await api.sites.tailToken(state.siteId);
      const wsUrl = res && res.ws_url;
      if (!wsUrl) throw new Error('后端未返回 ws_url');

      const ws = new WebSocket(normalizeWsUrl(wsUrl));
      state.ws = ws;
      let got = 0;

      ws.onopen = () => {
        state.tailing = true;
        setTailStatus('已连接', 'on');
        startBtn.disabled = true;
        drawTail();
      };
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data);
          if (msg.type === 'line' && msg.line) {
            lineBuf.push(msg.line);
            got++;
            if (lineBuf.length > MAX_LINES) lineBuf.shift();
            drawTail();
          }
        } catch (e) {
          // 非 JSON 帧直接按文本行处理
          lineBuf.push({ time: new Date().toISOString(), line: String(ev.data) });
          drawTail();
        }
      };
      ws.onerror = () => setTailStatus('连接出错', 'err');
      ws.onclose = () => {
        state.tailing = false;
        state.ws = null;
        startBtn.disabled = false;
        setTailStatus(got ? `已断开（共接收 ${got} 行）` : '已断开', 'err');
      };
    } catch (e) {
      setTailStatus('连接失败', 'err');
      toast(e.message || '无法建立实时连接', 'error');
      startBtn.disabled = false;
    }
  }

  // 只清理状态与连接，不触碰任何 DOM。
  // render() 一进入就会调用它，而 startBtn / tailStatus 等元素是
  // render 内部稍后才用 const 创建的——在 TDZ 里访问会抛
  // "Cannot access 'startBtn' before initialization"，整个日志页白屏。
  function stopTail() {
    if (state.ws) {
      try {
        state.ws.close();
      } catch (e) {
        // 忽略关闭异常
      }
      state.ws = null;
    }
    state.tailing = false;
  }

  // 在已知 DOM 元素就绪后调用，负责重置按钮与状态文案。
  function resetTailUI() {
    if (startBtn) startBtn.disabled = false;
    setTailStatus('未连接');
  }

  // 相对地址的 ws_url 需要补协议
  function normalizeWsUrl(url) {
    if (/^wss?:\/\//i.test(url)) return url;
    if (/^https?:\/\//i.test(url)) return url.replace(/^http/i, 'ws');
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${location.host}${url.startsWith('/') ? '' : '/'}${url}`;
  }

  async function reload() {
    clear(histBox.querySelector('.card-body')).appendChild(loading('加载日志…'));
    try {
      const res = await api.sites.logs(state.siteId, state.type, PAGE_SIZE, state.page);
      // 后端返回 {records, page, total}；早期猜成 logs/lines/items，
      // 三个键全错，历史日志永远显示"暂无日志记录"。
      const logs = res.records || [];
      const total = Number(res.total) || logs.length;
      const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

      histInfo.textContent = `共 ${total} 条 · 第 ${state.page} / ${pages} 页`;
      const bodyBox = histBox.querySelector('.card-body');
      clear(bodyBox);

      if (!logs.length) {
        bodyBox.appendChild(emptyState(
          state.page > 1 ? '没有更多日志了' : '暂无日志记录',
          state.page > 1 ? button('返回第一页', () => { state.page = 1; reload(); }, 'default') : null,
          'logs'
        ));
        return;
      }

      bodyBox.appendChild(histTable(logs));
      const p = pager({
        page: state.page,
        pages,
        onChange: (p2) => { state.page = p2; reload(); },
      });
      if (p) bodyBox.appendChild(p);
    } catch (e) {
      clear(histBox.querySelector('.card-body')).appendChild(errorState(`日志加载失败：${e.message || e}`, () => reload()));
    }
  }

  function histTable(logs) {
    if (state.type === 'error') {
      // 错误日志后端按 {line: <整段文本>} 返回，不是结构化对象
      return table([
        { label: '内容', render: (l) => h('pre', { class: 'code-box', style: { maxHeight: '420px' } }, l.line || l.message || l.error || '-') },
      ], logs.map((l) => (typeof l === 'string' ? { line: l } : l)));
    }
    return table([
      { label: '时间', width: '160px', render: (l) => h('span', { class: 'mono-text' }, formatDateTime(l.time)) },
      { label: '客户端 IP', width: '130px', render: (l) => h('span', { class: 'mono-text' }, l.remote_addr || '-') },
      { label: '方法', width: '70px', render: (l) => tag(l.method || '-', 'default') },
      { label: '路径', render: (l) => h('span', { class: 'ellipsis', title: l.path || '' }, l.path || '-') },
      { label: '协议', width: '80px', render: (l) => l.protocol || '-' },
      { label: '状态', width: '80px', align: 'center', render: (l) => statusTag(l.status) },
      { label: '大小', width: '90px', align: 'right', render: (l) => formatBytes(l.size || 0) },
      { label: '耗时', width: '80px', align: 'right', render: (l) => (l.req_time ? `${l.req_time}s` : '-') },
      { label: 'UA', width: '160px', render: (l) => h('span', { class: 'ellipsis', title: l.ua || '' }, l.ua || '-') },
    ], logs);
  }

  function statusTag(code) {
    const c = Number(code) || 0;
    if (c >= 500) return tag(c, 'danger');
    if (c >= 400) return tag(c, 'warning');
    if (c >= 300) return tag(c, 'info');
    if (c >= 200) return tag(c, 'success');
    return tag(code || '-', 'default');
  }

  drawTail();
  setTailStatus('未连接');
  reload();
}