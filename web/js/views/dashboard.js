// 仪表盘：全局概览 + 近 7 日流量趋势（内联 SVG 手绘，无图表库）
import { api, formatBytes, formatNumber, certDaysLeft } from '../api.js';
import {
  h, clear, icon, button, tag, loading, errorState, emptyState,
  statCard, lineChart, barChart, progressBar, certDaysBadge,
} from '../components.js';
import { ctx, store } from '../app.js';

export async function render(container) {
  container.appendChild(loading('正在汇总面板数据…'));

  try {
    // 关键数据（站点/告警）失败就让整页进错误态；非关键的
    // 运行时状态与证书列表降级为空值，避免一个接口 500 就把仪表盘打穿。
    const [sitesRes, alertsRes, statusRes, certsRes] = await Promise.all([
      api.sites.list(),
      api.alerts.list(100),
      api.system.status().catch(() => ({})),
      api.certs.list().catch(() => ({ certs: [] })),
    ]);

    const sites = sitesRes.sites || [];
    const alerts = alertsRes.alerts || [];
    const certs = certsRes.certs || [];
    const status = statusRes || {};

    clear(container);

    // ---------------- 顶部指标 ----------------
    const running = sites.filter((s) => s.status === 'running').length;
    const todayReqs = sites.reduce((sum, s) => sum + (Number(s.today_reqs) || 0), 0);
    const todayBytes = sites.reduce((sum, s) => sum + (Number(s.today_bytes) || 0), 0);
    const monthBytes = sites.reduce((sum, s) => sum + (Number(s.month_bytes) || 0), 0);
    const openAlerts = alerts.filter((a) => !a.resolved).length;
    const criticalAlerts = alerts.filter((a) => !a.resolved && a.level === 'critical').length;
    const expiring = certs
      .map((c) => ({ cert: c, days: certDaysLeft(c) }))
      .filter((x) => x.days !== null && x.days <= 30)
      .sort((a, b) => a.days - b.days);

    const disk = status.disk || {};
    const diskPercent = disk.total ? Math.round((disk.used / disk.total) * 100) : 0;

    const statsGrid = h('div', { class: 'grid grid-4 stat-grid' }, [
      statCard('站点总数', formatNumber(sites.length), `运行中 ${running} · 已停止 ${sites.length - running}`, 'sites', 'blue'),
      statCard('今日请求', formatNumber(todayReqs), `今日流量 ${formatBytes(todayBytes)}`, 'clock', 'cyan'),
      statCard('本月流量', formatBytes(monthBytes), '所有站点累计', 'disk', 'purple'),
      statCard('待处理告警', formatNumber(openAlerts), criticalAlerts ? `严重 ${criticalAlerts} 条` : '无严重告警', 'alerts', openAlerts ? 'red' : 'green'),
      statCard('证书临期', formatNumber(expiring.length), expiring.length ? '30 天内到期' : '全部正常', 'cert', expiring.length ? 'orange' : 'green'),
      statCard('磁盘占用', diskPercent ? `${diskPercent}%` : '-', disk.total ? `${formatBytes(disk.used)} / ${formatBytes(disk.total)}` : '未获取到磁盘信息', 'disk', diskPercent > 85 ? 'red' : 'blue'),
    ]);

    container.appendChild(h('div', { class: 'page-header' }, [
      h('div', {}, [
        h('h1', { class: 'page-title' }, '仪表盘'),
        h('div', { class: 'page-sub' }, `运行环境：${status.runtime === 'docker' ? 'Docker' : '宿主机'} · nginx ${status.nginx && status.nginx.running ? '运行中' : '未运行'}`),
      ]),
      h('div', { class: 'page-actions' }, [
        button('刷新', () => render(container), 'default', 'refresh'),
        button('新建站点', () => ctx.navigate('#/sites/new'), 'primary', 'plus'),
      ]),
    ]));

    container.appendChild(statsGrid);

    // ---------------- v0.2.2: 宿主机资源 ----------------
    // 借鉴 1Panel 仪表盘的"资源"区块，但只用 ZhiweiPanel 已有的 statCard/progressBar，
    // 不引图表库。指标接口失败不阻断页面（降级成一张提示卡）。
    const metricsCard = h('div', { class: 'card' }, [
      h('div', { class: 'card-title' }, [
        h('span', {}, '宿主机资源'),
        h('div', { class: 'card-title-extra' }, h('span', { class: 'field-hint' }, 'CPU / 内存 / 磁盘 / 负载 · 每 10 秒采样' )),
      ]),
      h('div', { class: 'card-body' }, h('div', { class: 'state-box' }, '采样中…')),
    ]);
    container.appendChild(metricsCard);

    api.system.metrics()
      .then((r) => drawMetrics(metricsCard, r))
      .catch(() => {
        const b = metricsCard.querySelector('.card-body');
        clear(b).appendChild(emptyState('资源指标获取失败', null, 'info'));
      });

    // ---------------- 流量趋势 + 状态码分布 ----------------
    const trendCard = h('div', { class: 'card' }, [
      h('div', { class: 'card-title' }, [
        h('span', {}, '近 7 日流量趋势'),
        h('div', { class: 'card-title-extra' }, h('span', { class: 'field-hint' }, '按站点访问日志聚合' )),
      ]),
      h('div', { class: 'card-body' }, h('div', { class: 'state-box' }, '统计中…')),
    ]);

    const statusCodeCard = h('div', { class: 'card' }, [
      h('div', { class: 'card-title' }, [
        h('span', {}, '近 7 日响应状态分布'),
        h('div', { class: 'card-title-extra' }, h('span', { class: 'field-hint' }, '2xx / 3xx / 4xx / 5xx')),
      ]),
      h('div', { class: 'card-body' }, h('div', { class: 'state-box' }, '统计中…')),
    ]);

    container.appendChild(h('div', { class: 'grid grid-2' }, [trendCard, statusCodeCard]));

    // 拉取各站点 stats 并按日期聚合
    const targets = sites.slice(0, 30); // 防止站点过多时并发过大
    let dayAgg = null;
    try {
      const statsLists = await Promise.all(
        targets.map((s) => api.sites.stats(s.id, 7).catch(() => ({ stats: [] })))
      );
      dayAgg = new Map();
      for (const { stats } of statsLists) {
        for (const row of stats || []) {
          const cur = dayAgg.get(row.date) || { date: row.date, requests: 0, bytes: 0, status_2xx: 0, status_3xx: 0, status_4xx: 0, status_5xx: 0 };
          cur.requests += Number(row.requests) || 0;
          cur.bytes += Number(row.bytes) || 0;
          cur.status_2xx += Number(row.status_2xx) || 0;
          cur.status_3xx += Number(row.status_3xx) || 0;
          cur.status_4xx += Number(row.status_4xx) || 0;
          cur.status_5xx += Number(row.status_5xx) || 0;
          dayAgg.set(row.date, cur);
        }
      }
    } catch (e) {
      // 统计失败不阻断页面
    }

    const days = buildLastDays(7);
    const rows = days.map((d) => dayAgg ? dayAgg.get(d) || { date: d, requests: 0, bytes: 0, status_2xx: 0, status_3xx: 0, status_4xx: 0, status_5xx: 0 } : null);
    const hasData = rows.some((r) => r && r.requests > 0);

    const trendBody = trendCard.querySelector('.card-body');
    clear(trendBody);
    // 指标切换按钮（流量 / 请求数）
    function metricSwitch(current) {
      return h('div', { class: 'chart-switch' }, [
        button('流量', () => switchTrend('bytes'), current === 'bytes' ? 'primary' : 'default'),
        button('请求数', () => switchTrend('requests'), current === 'requests' ? 'primary' : 'default'),
      ]);
    }

    if (!dayAgg) {
      trendBody.appendChild(errorState('流量统计加载失败'));
    } else if (!hasData) {
      trendBody.appendChild(emptyState('近 7 日暂无访问数据', null, 'logs'));
    } else {
      trendBody.appendChild(lineChart(
        rows.map((r) => ({ label: r.date.slice(5), value: r.bytes })),
        { fmtY: (v) => formatBytes(v), fmtTip: (v) => formatBytes(v), height: 230 }
      ));
      trendBody.appendChild(metricSwitch('bytes'));
    }

    function switchTrend(metric) {
      const box = trendBody;
      clear(box);
      if (!hasData) {
        box.appendChild(emptyState('近 7 日暂无访问数据', null, 'logs'));
        return;
      }
      box.appendChild(lineChart(
        rows.map((r) => ({ label: r.date.slice(5), value: r[metric] })),
        metric === 'bytes'
          ? { fmtY: (v) => formatBytes(v), fmtTip: (v) => formatBytes(v), height: 230 }
          : { fmtY: (v) => formatNumber(v), fmtTip: (v) => formatNumber(v) + ' 次请求', height: 230 }
      ));
      box.appendChild(metricSwitch(metric));
    }

    const codeBody = statusCodeCard.querySelector('.card-body');
    clear(codeBody);
    if (!dayAgg || !hasData) {
      codeBody.appendChild(emptyState('暂无状态码统计', null, 'logs'));
    } else {
      codeBody.appendChild(barChart(
        [
          { label: '2xx', value: sum(rows, 'status_2xx'), tone: 'bar-green' },
          { label: '3xx', value: sum(rows, 'status_3xx'), tone: 'bar-blue' },
          { label: '4xx', value: sum(rows, 'status_4xx'), tone: 'bar-yellow' },
          { label: '5xx', value: sum(rows, 'status_5xx'), tone: 'bar-red' },
        ],
        { height: 200 }
      ));
    }

    // ---------------- 站点状态 + 证书临期 + 最近告警 ----------------
    const siteCard = h('div', { class: 'card' }, [
      h('div', { class: 'card-title' }, [
        h('span', {}, '站点状态'),
        h('div', { class: 'card-title-extra' }, button('全部站点', () => ctx.navigate('#/sites'), 'ghost')),
      ]),
      h('div', { class: 'card-body' }, sites.length ? siteStatusList(sites) : emptyState('还没有站点，先创建一个吧', button('新建站点', () => ctx.navigate('#/sites/new'), 'primary', 'plus'), 'sites')),
    ]);

    const certCard = h('div', { class: 'card' }, [
      h('div', { class: 'card-title' }, [
        h('span', {}, '证书临期提醒'),
        h('div', { class: 'card-title-extra' }, button('证书管理', () => ctx.navigate('#/certs'), 'ghost')),
      ]),
      h('div', { class: 'card-body' }, certs.length ? certList(certs) : emptyState('暂无证书', null, 'cert')),
    ]);

    const recentAlerts = alerts.slice(0, 8);
    const alertCard = h('div', { class: 'card' }, [
      h('div', { class: 'card-title' }, [
        h('span', {}, '最近告警'),
        h('div', { class: 'card-title-extra' }, button('告警中心', () => ctx.navigate('#/alerts'), 'ghost')),
      ]),
      h('div', { class: 'card-body' }, recentAlerts.length ? alertList(recentAlerts) : emptyState('暂无告警记录', null, 'alerts')),
    ]);

    const siteWithCert = h('div', { class: 'grid grid-2' }, [siteCard, certCard]);
    container.appendChild(siteWithCert);
    container.appendChild(alertCard);
  } catch (err) {
    console.error(err);
    clear(container).appendChild(errorState(`仪表盘加载失败：${err.message || err}`, () => render(container)));
  }
}

// 生成最近 n 天的日期字符串（YYYY-MM-DD）
function buildLastDays(n) {
  const out = [];
  for (let i = n - 1; i >= 0; i--) {
    const d = new Date();
    d.setDate(d.getDate() - i);
    const p = (x) => String(x).padStart(2, '0');
    out.push(`${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`);
  }
  return out;
}

function sum(rows, key) {
  return rows.reduce((a, r) => a + (r && r[key] ? Number(r[key]) : 0), 0);
}

function siteStatusList(sites) {
  const box = h('div', { class: 'mini-list' });
  for (const s of sites.slice(0, 10)) {
    box.appendChild(h('div', { class: 'mini-item', onclick: () => ctx.navigate(`#/sites/${encodeURIComponent(s.id)}`) }, [
      h('span', { class: `dot ${s.status === 'running' ? 'dot-green' : 'dot-gray'}` }),
      h('span', { class: 'mini-item-name' }, s.name || (s.domains && s.domains[0]) || s.id),
      h('span', { class: 'mini-item-meta' }, `${formatNumber(s.today_reqs || 0)} 请求`),
      tag(s.status === 'running' ? '运行中' : '已停止', s.status === 'running' ? 'success' : 'default'),
    ]));
  }
  return box;
}

function certList(certs) {
  const box = h('div', { class: 'mini-list' });
  const sorted = certs.map((c) => ({ c, days: certDaysLeft(c) })).sort((a, b) => (a.days ?? 9999) - (b.days ?? 9999));
  for (const { c, days } of sorted.slice(0, 10)) {
    const total = 90; // LE 默认 90 天有效期，用于进度条估算
    const remainPercent = days === null ? 0 : Math.max(0, Math.min(100, (days / total) * 100));
    const tone = days !== null && days < 8 ? 'bar-red' : days !== null && days <= 30 ? 'bar-yellow' : 'bar-green';
    box.appendChild(h('div', { class: 'mini-item mini-item-cert' }, [
      h('div', { class: 'mini-item-main' }, [
        h('span', { class: 'mini-item-name' }, (c.domains && c.domains[0]) || c.name),
        h('span', { class: 'mini-item-meta' }, `到期 ${(c.not_after || '').slice(0, 10)}`),
      ]),
      h('div', { class: 'mini-item-right' }, [
        certDaysBadge(days),
        progressBar(remainPercent, tone),
      ]),
    ]));
  }
  return box;
}

function alertList(alerts) {
  const box = h('div', { class: 'alert-list' });
  for (const a of alerts) {
    box.appendChild(h('div', { class: `alert-item level-${a.level} ${a.resolved ? 'is-resolved' : ''}`.trim(), onclick: () => ctx.navigate(`#/sites/${encodeURIComponent(a.site_id)}`) }, [
      h('span', { class: 'alert-dot' }),
      h('div', { class: 'alert-body' }, [
        h('div', { class: 'alert-msg' }, a.message || a.type),
        h('div', { class: 'alert-meta' }, `${a.site_name || a.site_id} · ${a.type} · ${(a.created_at || '').slice(0, 16).replace('T', ' ')}`),
      ]),
      tag(a.level || 'info', levelTone(a.level)),
      a.resolved ? tag('已处理', 'default') : tag('待处理', 'warning'),
    ]));
  }
  return box;
}

// 告警等级配色（供多个视图共用）
export function levelTone(level) {
  if (level === 'critical') return 'danger';
  if (level === 'warning') return 'warning';
  return 'info';
}

// ---------------------------------------------------------------------------
// v0.2.2: 宿主机资源
// ---------------------------------------------------------------------------

// drawMetrics 渲染宿主机资源卡片。
//
// 指标来自 /api/system/metrics：current 是最新快照，history 是内存环形
// 缓冲里的最近若干条采样（用于画迷你趋势线）。
//
// 全部用已有的 statCard / progressBar / lineChart 组件，
// 不引任何图表库——面板的前端是纯原生 ES Module，加库要改构建链。
function drawMetrics(card, data) {
  const cur = data.current || {};
  const history = data.history || [];
  const body = card.querySelector('.card-body');
  clear(body);

  const cpu = Number(cur.cpu_percent) || 0;
  const mem = Number(cur.mem_percent) || 0;
  const disk = Number(cur.disk_percent) || 0;
  const cpuCount = Number(cur.cpu_count) || 0;

  // 负载归一：1 分钟负载 / 核数。归一后才能跨机器比较。
  const loadPerCore = cpuCount ? (Number(cur.load1) || 0) / cpuCount : 0;
  const loadPct = Math.min(100, Math.round(loadPerCore * 100));

  const tone = (p) => (p >= 90 ? 'bar-red' : p >= 75 ? 'bar-yellow' : 'bar-green');

  const grid = h('div', { class: 'grid grid-4 stat-grid' }, [
    statCard('CPU', `${cpu.toFixed(1)}%`, `${cpuCount} 核 · 负载 ${(Number(cur.load1) || 0).toFixed(2)}`, 'dashboard',
      cpu >= 90 ? 'red' : cpu >= 75 ? 'orange' : 'blue'),
    statCard('内存', `${mem.toFixed(1)}%`,
      `${formatBytes(cur.mem_used || 0)} / ${formatBytes(cur.mem_total || 0)}`, 'info',
      mem >= 90 ? 'red' : mem >= 75 ? 'orange' : 'blue'),
    statCard('磁盘', `${disk.toFixed(1)}%`,
      `${formatBytes(cur.disk_used || 0)} / ${formatBytes(cur.disk_total || 0)}`, 'disk',
      disk >= 90 ? 'red' : disk >= 75 ? 'orange' : 'blue'),
    statCard('网络', formatBytes((Number(cur.net_rx_bps) || 0) + (Number(cur.net_tx_bps) || 0)) + '/s',
      `↓ ${formatBytes(cur.net_rx_bps || 0)}/s ↑ ${formatBytes(cur.net_tx_bps || 0)}/s`, 'clock', 'cyan'),
  ]);
  body.appendChild(grid);

  // 迷你趋势线：CPU 与内存各一条。
  if (history.length >= 2) {
    const chartBox = h('div', { class: 'grid grid-2' }, [
      h('div', {}, [
        h('div', { class: 'field-hint' }, 'CPU 占用率（最近采样）'),
        lineChart(history.map((s) => ({
          label: new Date(s.timestamp).toTimeString().slice(0, 5),
          value: Number(s.cpu_percent) || 0,
        })), { height: 120 }),
      ]),
      h('div', {}, [
        h('div', { class: 'field-hint' }, '内存占用率（最近采样）'),
        lineChart(history.map((s) => ({
          label: new Date(s.timestamp).toTimeString().slice(0, 5),
          value: Number(s.mem_percent) || 0,
        })), { height: 120 }),
      ]),
    ]);
    body.appendChild(chartBox);
  }

  // v0.7.0: 近 24 小时趋势（分钟级持久化，重启不丢）。
  // 与上面的实时迷你曲线互补：实时曲线看毛刺（10 秒级，重启清零），
  // 这里看长趋势（落库采样）。获取失败静默降级——少一块图，不打扰页面。
  const dailyBox = h('div', {}, h('div', { class: 'state-box' }, '统计中…'));
  body.appendChild(dailyBox);
  api.system.metricsHistory(24)
    .then((r) => drawDailyHistory(dailyBox, r))
    .catch(() => clear(dailyBox));

  // 其它指标一行小字
  const uptime = Number(cur.uptime_seconds) || 0;
  const days = Math.floor(uptime / 86400);
  const hours = Math.floor((uptime % 86400) / 3600);
  const mins = Math.floor((uptime % 3600) / 60);
  body.appendChild(h('div', { class: 'meta-line' }, [
    `运行 ${days} 天 ${hours} 小时 ${mins} 分`,
    `· 进程数 ${cur.process_count || 0}`,
    `· Swap ${formatBytes(cur.swap_used || 0)} / ${formatBytes(cur.swap_total || 0)}`,
    `· 磁盘 ${cur.disk_path || '/'}`,
    cur.hostname ? `· ${cur.hostname}` : '',
  ].filter(Boolean).join(' ')));
}

// drawDailyHistory 渲染"近 24 小时"持久化趋势（CPU + 内存）。
//
// 数据是分钟级落库采样（旧 → 新）。必须降采样后再画：lineChart 给每个点
// 渲染一个圆点加一个 X 轴标签，1440 个点就是近 3000 个 SVG 节点，
// 标签全部叠死——压到 ≤90 点既流畅又可读。
function drawDailyHistory(box, data) {
  const history = (data.history || []).filter((s) => Number.isFinite(Number(s.ts)));
  clear(box);
  if (history.length < 2) {
    box.appendChild(h('div', { class: 'field-hint' },
      '近 24 小时趋势为分钟级持久化采样，系统运行一段时间后逐步生成（重启不丢失）'));
    return;
  }
  const maxPts = 90;
  const step = Math.ceil(history.length / maxPts);
  const pts = history.filter((s, i) => i % step === 0 || i === history.length - 1);

  const grid = h('div', { class: 'grid grid-2' }, [
    h('div', {}, [
      h('div', { class: 'field-hint' }, 'CPU 占用率（近 24 小时）'),
      lineChart(pts.map((s) => ({
        label: new Date(s.ts * 1000).toTimeString().slice(0, 5),
        value: Number(s.cpu) || 0,
      })), { height: 120 }),
    ]),
    h('div', {}, [
      h('div', { class: 'field-hint' }, '内存占用率（近 24 小时）'),
      lineChart(pts.map((s) => ({
        label: new Date(s.ts * 1000).toTimeString().slice(0, 5),
        value: Number(s.mem_percent) || 0,
      })), { height: 120 }),
    ]),
  ]);
  box.appendChild(grid);
}