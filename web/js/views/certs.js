// 证书管理：列表 / 有效期分级 / ACME 申请 / 导入 / 吊销 / 绑定站点 / 账户管理
import { api, formatDate, formatDateTime, certDaysLeft } from '../api.js';
import {
  h, clear, button, tag, table, loading, errorState, emptyState, iconButton,
  confirmDialog, toast, modal, field, textInput, textArea, selectInput, grid,
  progressBar, certDaysBadge, sectionTitle,
} from '../components.js';
import { ctx } from '../app.js';

const CA_OPTIONS = [
  { value: 'letsencrypt', label: "Let's Encrypt（正式环境）" },
  { value: 'letsencrypt_staging', label: "Let's Encrypt Staging（测试）" },
];
const KEY_TYPES = [
  { value: 'ec256', label: 'EC P-256（推荐）' },
  { value: 'rsa2048', label: 'RSA 2048' },
];
const METHODS = [
  { value: 'http', label: 'HTTP 验证' },
  { value: 'dns', label: 'DNS 验证' },
];

// 视图内缓存
let cache = { certs: [], accounts: [], sites: [] };

export async function render(container) {
  clear(container);
  container.appendChild(loading('正在加载证书…'));

  try {
    const [cr, ar, sr] = await Promise.all([
      api.certs.list(),
      api.certs.accounts().catch(() => ({ accounts: [] })),
      api.sites.list().catch(() => ({ sites: [] })),
    ]);
    cache = { certs: cr.certs || [], accounts: ar.accounts || [], sites: sr.sites || [] };
  } catch (err) {
    clear(container).appendChild(errorState(`证书列表加载失败：${err.message || err}`, () => render(container)));
    return;
  }

  clear(container);
  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('h1', { class: 'page-title' }, '证书'),
      h('div', { class: 'page-sub' }, `共 ${cache.certs.length} 张证书 · ACME 账户 ${cache.accounts.length} 个`),
    ]),
    h('div', { class: 'page-actions' }, [
      button('ACME 账户', showAccounts, 'default', 'key'),
      button('导入证书', showImport, 'default', 'upload'),
      button('申请证书', showIssue, 'primary', 'plus'),
    ]),
  ]));

  const listBox = h('div', { class: 'card' }, h('div', { class: 'card-body' }));
  container.appendChild(listBox);
  drawList(listBox);

  function drawList(box) {
    clear(box);
    if (!cache.certs.length) {
      box.appendChild(emptyState(
        '还没有证书，可通过 ACME 自动申请或导入已有证书',
        h('div', { class: 'inline-actions' }, [
          button('申请证书', showIssue, 'primary', 'plus'),
          button('导入证书', showImport, 'default', 'upload'),
        ]),
        'cert'
      ));
      return;
    }

    const cols = [
      {
        label: '证书名称 / 域名',
        render: (c) => h('div', { class: 'site-cell' }, [
          h('div', { class: 'site-name' }, [
            h('span', { class: 'site-name-text' }, c.name),
            tag(sourceLabel(c.source), c.source === 'acme' ? 'info' : 'default'),
          ]),
          h('div', { class: 'site-domains' }, (c.domains || []).join(', ')),
        ]),
      },
      { label: '签发者', width: '170px', render: (c) => h('span', { class: 'ellipsis', title: c.issuer || '' }, c.issuer || '-') },
      {
        label: '有效期',
        width: '220px',
        render: (c) => {
          const days = certDaysLeft(c);
          const total = 90;
          const pct = days === null ? 0 : Math.max(0, Math.min(100, (days / total) * 100));
          const tone = days !== null && days < 8 ? 'bar-red' : days !== null && days <= 30 ? 'bar-yellow' : 'bar-green';
          return h('div', { class: 'cert-cell' }, [
            h('div', { class: 'cert-cell-line' }, [
              h('span', {}, `${formatDate(c.not_before)} ~ ${formatDate(c.not_after)}`),
              certDaysBadge(days),
            ]),
            progressBar(pct, tone),
          ]);
        },
      },
      {
        label: '自动续签',
        width: '100px',
        align: 'center',
        render: (c) => (c.auto_renew ? tag('已开启', 'success') : tag('未开启', 'default')),
      },
      { label: '绑定站点', width: '140px', render: (c) => boundSites(c) },
      {
        label: '操作',
        width: '340px',
        align: 'right',
        render: (c) => {
          const unmatched = unmatchedMatchingSites(c);
          return h('div', { class: 'cell-actions' }, [
            // 一键绑定：只对"有匹配但未绑定"的 cert 显示，避免每个 cert 都刷一排死按钮
            unmatched > 0
              ? button(`一键绑定 (${unmatched})`, async () => {
                  const yes = await confirmDialog({
                    title: '一键绑定',
                    message: `将证书「${c.name}」按域名交集自动绑到 ${unmatched} 个匹配站点（未开启 SSL 的会自动开启），是否继续？`,
                    okText: '开始绑定',
                  });
                  if (!yes) return;
                  const n = await autoBindCertToMatchingSites(c);
                  if (n > 0) {
                    toast(`已自动绑定到 ${n} 个站点`, 'success', 3500);
                    render(container);
                  }
                }, 'primary')
              : null,
            button('手动绑定', () => showBind(c), 'ghost'),
            iconButton('download', '下载证书', () => doDownload(c), 'ghost'),
            iconButton('trash', '吊销 / 删除', () => doRevoke(c), 'danger-ghost'),
          ]);
        },
      },
    ];

    box.appendChild(table(cols, cache.certs));
  }

  function sourceLabel(src) {
    if (src === 'acme') return 'ACME 签发';
    if (src === 'import') return '手动导入';
    return src || '未知来源';
  }

  function boundSites(cert) {
    const list = cache.sites.filter((s) => s.cert && s.cert.cert_id === cert.id);
    if (!list.length) return tag('未绑定', 'default');
    return h('div', { class: 'cell-inline' }, list.slice(0, 2).map((s) => tag(s.name || s.id, 'info')));
  }

  // 把 cert 的 domains 与 cache.sites 求交集，返回 "站点 id → 匹配上的域名数组" 映射。
  // 一个站点的多个域名跟 cert 多 SAN 重叠时只列一次（一站绑一次即可，多个域名走同一张证书）。
  function matchSitesForCert(cert) {
    const certDoms = new Set((cert.domains || []).map((d) => d.toLowerCase()));
    const out = new Map(); // siteId -> { site, matchedDomains }
    for (const s of cache.sites) {
      const sd = (s.domains || []).map((d) => d.toLowerCase());
      const hits = sd.filter((d) => certDoms.has(d));
      if (hits.length) {
        out.set(s.id, { site: s, matchedDomains: hits });
      }
    }
    return out;
  }

  // 一键绑定 cert 到所有匹配站点。
  // 复用 showBind 内同样的 PUT /api/sites/:id 流程（设 ssl.enabled + 写 cert_file/key_file），
  // 把"勾选站点"这一步换成"按域名交集自动选"。进度通过 toast 持续回报。
  //
  // cert 文件路径必须用 cert.id（hex）做目录，不能用 cert.name：
  // 1. ZhiweiPanel 实际写盘目录是 `/opt/zhiweipanel/data/certs/<id>/`，不是 BT 的
  //   `/www/server/panel/vhost/cert/<name>/`，前者跟着数据库主键稳定。
  // 2. cert.name 在中文命名时（如"part.example.com 等 5 个域名"）含**空格**，
  //   会让 nginx 渲染 `ssl_certificate <path>;` 时把路径切成多个 token，
  //   报 "invalid number of arguments" 整站 nginx -t 失败。
  // 早期 autobind 用 cert.name 导致全站 reload 失败就是这个原因。
  async function autoBindCertToMatchingSites(cert) {
    const matches = matchSitesForCert(cert);
    if (!matches.size) {
      toast(`证书 ${cert.name} 暂无可绑定的匹配站点（站点域名必须包含在证书的 SAN 列表中）`, 'info', 4000);
      return 0;
    }
    const certDir = `/opt/zhiweipanel/data/certs/${cert.id}`;
    let ok = 0;
    for (const [id, { site }] of matches) {
      try {
        const full = await api.sites.get(id);
        full.ssl = {
          ...(full.ssl || {}),
          enabled: true,
          cert_file: `${certDir}/fullchain.pem`,
          key_file: `${certDir}/privkey.pem`,
        };
        full.cert = {
          cert_id: cert.id,
          domains: cert.domains || [],
          not_before: cert.not_before,
          not_after: cert.not_after,
          issuer: cert.issuer,
          source: cert.source,
        };
        await api.sites.update(id, full);
        site.cert = full.cert;
        ok++;
      } catch (e) {
        toast(`绑定 ${site.name || id} 失败：${e.message || e}`, 'error', 4000);
      }
    }
    return ok;
  }

  // 列出未绑定的匹配站点数（用于按钮文案和 disabled 状态）
  function unmatchedMatchingSites(cert) {
    const matches = matchSitesForCert(cert);
    let n = 0;
    for (const [, { site }] of matches) {
      if (!site.cert || site.cert.cert_id !== cert.id) n++;
    }
    return n;
  }

  // ---------------- ACME 账户 ----------------
  function showAccounts() {
    const body = h('div', {}, loading('加载账户…'));
    const m = modal({ title: 'ACME 账户管理', width: '720px', content: body });
    api.certs.accounts()
      .then((res) => {
        const accounts = res.accounts || [];
        clear(body);
        body.appendChild(h('div', {}, [
          accounts.length ? table([
            { label: 'ID', render: (a) => h('span', { class: 'mono-text' }, a.id) },
            { label: '邮箱', render: (a) => a.email },
            { label: 'CA', render: (a) => tag(a.ca === 'letsencrypt_staging' ? 'Staging' : "Let's Encrypt", 'info') },
            { label: '密钥类型', render: (a) => a.key_type },
            { label: '创建时间', render: (a) => h('span', { class: 'muted' }, formatDateTime(a.created_at)) },
          ], accounts) : emptyState('尚未创建 ACME 账户', null, 'key'),
          h('div', { class: 'inline-actions' }, button('创建账户', showCreateAccount, 'primary', 'plus')),
        ]));
      })
      .catch((e) => clear(body).appendChild(errorState(`加载失败：${e.message || e}`)));
  }

  function showCreateAccount() {
    const email = textInput('', null, { placeholder: 'ops@example.com' });
    const ca = selectInput('letsencrypt', CA_OPTIONS, null);
    const keyType = selectInput('ec256', KEY_TYPES, null);
    const m = modal({
      title: '创建 ACME 账户',
      width: '520px',
      content: h('div', {}, [
        field('联系邮箱', email, '用于证书到期与吊销通知'),
        field('证书颁发机构', ca),
        field('账户密钥类型', keyType, '推荐 ec256，兼容性最佳'),
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('创建', async () => {
          if (!email.value.trim()) {
            toast('请填写邮箱', 'error');
            return;
          }
          try {
            await api.certs.createAccount({ email: email.value.trim(), ca: ca.value, key_type: keyType.value });
            toast('ACME 账户已创建', 'success');
            m.close();
            render(container);
          } catch (e) {
            toast(e.message || '创建失败', 'error');
          }
        }, 'primary'),
      ],
    });
  }

  // ---------------- 申请证书 ----------------
  function showIssue() {
    if (!cache.accounts.length) {
      toast('请先创建 ACME 账户', 'error');
      showAccounts();
      return;
    }
    // 域名候选：站点域名 + 手工输入
    const siteDomains = [...new Set(cache.sites.flatMap((s) => s.domains || []))];
    const domainsBox = h('div', { class: 'domain-editor' });
    const selected = new Set();

    const drawDomains = () => {
      clear(domainsBox);
      if (!siteDomains.length) {
        domainsBox.appendChild(h('div', { class: 'field-hint' }, '暂无站点域名，请直接在下方输入要申请的域名'));
      }
      for (const d of siteDomains) {
        domainsBox.appendChild(h('button', {
          class: `pill ${selected.has(d) ? 'is-active' : ''}`.trim(),
          type: 'button',
          onclick: (e) => {
            if (selected.has(d)) selected.delete(d);
            else selected.add(d);
            e.target.classList.toggle('is-active');
          },
        }, d));
      }
    };
    drawDomains();

    const manual = textArea('', null, { rows: 4, placeholder: '手动输入域名，一行一个\nwww.example.com\napi.example.com' });
    const account = selectInput(cache.accounts[0].id, cache.accounts.map((a) => ({
      value: a.id, label: `${a.email}（${a.ca === 'letsencrypt_staging' ? 'Staging' : '生产'}）`,
    })), null);
    const method = selectInput('http', METHODS, null);

    // DNS-01 配置区：选中 DNS 验证时才显示。
    // provider 列表与凭据字段全部来自后端 /api/dns-providers，
    // 后端新增 provider 时前端无需改动。
    const dnsBox = h('div', { style: { display: 'none' } });
    const dnsState = { providers: [], provider: '', fields: [], values: {} };

    function drawDNSBox() {
      clear(dnsBox);
      if (!dnsState.providers.length) {
        dnsBox.appendChild(h('div', { class: 'field-hint' }, '后端未注册任何 DNS 提供商'));
        return;
      }
      const sel = selectInput(
        dnsState.provider,
        dnsState.providers.map((x) => ({ value: x.key, label: x.name })),
        (v) => {
          dnsState.provider = v;
          const spec = dnsState.providers.find((x) => x.key === v);
          dnsState.fields = (spec && spec.fields) || [];
          dnsState.values = {};
          drawDNSBox();
        },
      );
      dnsBox.appendChild(field('DNS 提供商', sel, '凭证仅用于添加解析记录，保存在面板数据库'));
      if (!dnsState.fields.length) {
        dnsBox.appendChild(h('div', { class: 'field-hint' }, '该提供商无需额外凭证'));
        return;
      }
      for (const f of dnsState.fields) {
        dnsBox.appendChild(field(f.label,
          textInput(dnsState.values[f.key] || '', (v) => { dnsState.values[f.key] = v; },
            { placeholder: f.label, type: 'password' })));
      }
    }

    // 弹窗打开时就拉取 provider 列表，避免用户切到 DNS 时才等待
    api.dnsProviders()
      .then((res) => {
        dnsState.providers = (res && res.providers) || [];
        if (dnsState.providers.length) dnsState.provider = dnsState.providers[0].key;
        const spec = dnsState.providers.find((x) => x.key === dnsState.provider);
        dnsState.fields = (spec && spec.fields) || [];
        drawDNSBox();
      })
      .catch((e) => {
        clear(dnsBox);
        dnsBox.appendChild(h('div', { class: 'field-hint' }, `DNS 提供商加载失败：${e.message || e}`));
      });

    method.addEventListener('change', () => {
      dnsBox.style.display = method.value === 'dns' ? '' : 'none';
    });

    // 申请是慢操作（HTTP-01/DNS-01 都要 30-90s），先把 submitBtn 闭包捕获出来
    // 用来在 await 期间 disable + 改文字，否则用户以为 "没反应" 反复点导致
    // LE 速率限制叠加。
    let submitBtn = null;
    const m = modal({
      title: '申请证书',
      width: '660px',
      content: h('div', {}, [
        sectionTitle('选择已有站点域名', '可多选'),
        domainsBox,
        sectionTitle('或手动输入域名'),
        field('域名列表', manual, '一行一个；主域名需包含根域名本身'),
        grid(2,
          field('ACME 账户', account),
          field('验证方式', method, 'HTTP 验证需 80 端口可访问；DNS 验证可签通配符证书'),
        ),
        dnsBox,
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        submitBtn = button('开始申请', async () => {
          const list = [
            ...Array.from(selected),
            ...manual.value.split('\n').map((s) => s.trim()),
          ].filter(Boolean);
          if (!list.length) {
            toast('请至少选择一个域名', 'error');
            return;
          }
          // DNS-01 必须带上 provider 与凭据，否则后端会在 lego 内部才报错
          let dnsProvider = '';
          let dnsConfig = null;
          if (method.value === 'dns') {
            if (!dnsState.provider) {
              toast('请选择 DNS 提供商', 'error');
              return;
            }
            dnsProvider = dnsState.provider;
            dnsConfig = {};
            for (const f of dnsState.fields) {
              const v = (dnsState.values[f.key] || '').trim();
              if (!v) {
                toast(`请填写${f.label}`, 'error');
                return;
              }
              dnsConfig[f.key] = v;
            }
          }
          const originalText = submitBtn ? submitBtn.textContent : '';
          if (submitBtn) {
            submitBtn.disabled = true;
            submitBtn.textContent = '申请中…（可能 1-2 分钟）';
          }
          try {
            const cert = await api.certs.issue({
              domains: list,
              site_id: '',
              account_id: account.value,
              method: method.value,
              dns_provider: dnsProvider,
              dns_config: dnsConfig,
            });
            toast('证书申请成功', 'success');
            m.close();
            // 申请成功后顺手按域名交集自动绑到匹配站点，省得用户再去
            // 手动勾选 5 次。cache.certs 此刻还没拿到新 cert，先塞进 cache 再调用。
            cache.certs.push(cert);
            const n = await autoBindCertToMatchingSites(cert);
            if (n > 0) toast(`已自动绑定到 ${n} 个站点`, 'success', 3500);
            render(container);
          } catch (e) {
            // api.js 在非 2xx 时 throw { status, error, code }，没有 .message。
            // 旧的 `e.message || '申请失败'` 会直接落 '申请失败'，把
            // LE 速率限制 / 域未配置根 / DNS 凭据错等真错误吃掉。
            const msg = (e && (e.error || e.message))
              ? (e.error || e.message)
              : '申请失败（无详细错误，请查看服务端日志）';
            toast(msg, 'error', 6000);
            if (submitBtn) {
              submitBtn.disabled = false;
              if (originalText) submitBtn.textContent = originalText;
            }
          }
        }, 'primary'),
      ],
    });
  }

  // ---------------- 导入证书 ----------------
  function showImport() {
    const name = textInput('', null, { placeholder: 'example.com' });
    const domains = textArea('', null, { rows: 3, placeholder: 'example.com\nwww.example.com' });
    const certPem = textArea('', null, { rows: 6, mono: true, placeholder: '-----BEGIN CERTIFICATE-----' });
    const keyPem = textArea('', null, { rows: 6, mono: true, placeholder: '-----BEGIN PRIVATE KEY-----' });

    const m = modal({
      title: '导入证书',
      width: '720px',
      content: h('div', {}, [
        field('证书名称', name),
        field('绑定域名', domains, '一行一个'),
        field('证书内容（PEM）', certPem, null, true),
        field('私钥内容（PEM）', keyPem, null, true),
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('导入', async () => {
          const doms = domains.value.split('\n').map((s) => s.trim()).filter(Boolean);
          if (!name.value.trim()) {
            toast('请填写证书名称', 'error');
            return;
          }
          if (!doms.length) {
            toast('请至少填写一个域名', 'error');
            return;
          }
          if (!certPem.value.trim() || !keyPem.value.trim()) {
            toast('请填写证书与私钥内容', 'error');
            return;
          }
          try {
            await api.certs.import({
              name: name.value.trim(),
              domains: doms,
              cert_pem: certPem.value,
              key_pem: keyPem.value,
            });
            toast('证书导入成功', 'success');
            m.close();
            render(container);
          } catch (e) {
            toast(e.message || '导入失败', 'error');
          }
        }, 'primary'),
      ],
    });
  }

  // ---------------- 绑定到站点 ----------------
  function showBind(cert) {
    if (!cache.sites.length) {
      toast('还没有站点可绑定', 'error');
      return;
    }
    const picked = new Set();
    const listBox = h('div', { class: 'pick-list' });
    const draw = () => {
      clear(listBox);
      for (const s of cache.sites) {
        const isBound = s.cert && s.cert.cert_id === cert.id;
        const cb = h('input', {
          type: 'checkbox',
          checked: isBound || picked.has(s.id),
          onchange: (e) => {
            if (e.target.checked) picked.add(s.id);
            else picked.delete(s.id);
          },
        });
        listBox.appendChild(h('label', { class: 'pick-item' }, [
          cb,
          h('div', { class: 'pick-body' }, [
            h('div', { class: 'site-name' }, [
              h('span', { class: 'site-name-text' }, s.name || s.id),
              s.ssl && s.ssl.enabled ? tag('SSL 已启用', 'success') : tag('SSL 未启用', 'default'),
              isBound ? tag('当前已绑定', 'primary') : null,
            ]),
            h('div', { class: 'site-domains' }, (s.domains || []).join(', ')),
          ]),
        ]));
      }
    };
    draw();

    const m = modal({
      title: `绑定证书到站点（${cert.name}）`,
      width: '720px',
      content: h('div', {}, [
        h('div', { class: 'field-hint' }, '勾选后会自动把该站点的 SSL 证书路径指向本证书并启用 SSL'),
        listBox,
      ]),
      footer: [
        button('取消', () => m.close(), 'default'),
        button('确认绑定', async () => {
          const ids = Array.from(picked);
          if (!ids.length) {
            toast('请选择要绑定的站点', 'error');
            return;
          }
          // 同 autoBindCertToMatchingSites 的理由：用 cert.id 而不是 name 做目录。
          // cert.name 在中文命名时含空格，nginx 渲染时会被切 token。
          const certDir = `/opt/zhiweipanel/data/certs/${cert.id}`;
          try {
            for (const id of ids) {
              const site = cache.sites.find((s) => s.id === id);
              const full = await api.sites.get(id);
              full.ssl = {
                ...(full.ssl || {}),
                enabled: true,
                cert_file: `${certDir}/fullchain.pem`,
                key_file: `${certDir}/privkey.pem`,
              };
              full.cert = {
                cert_id: cert.id,
                domains: cert.domains || [],
                not_before: cert.not_before,
                not_after: cert.not_after,
                issuer: cert.issuer,
                source: cert.source,
              };
              await api.sites.update(id, full);
              if (site) site.cert = full.cert;
            }
            toast(`已绑定到 ${ids.length} 个站点`, 'success');
            m.close();
            render(container);
          } catch (e) {
            toast(e.message || '绑定失败', 'error');
          }
        }, 'primary'),
      ],
    });
  }

  // ---------------- 下载 / 吊销 ----------------
  async function doDownload(cert) {
    try {
      const res = await api.certs.download(cert.id);
      // 后端可能直接返回 PEM 文本
      if (typeof res === 'string') {
        const m = modal({
          title: `${cert.name} 证书内容`,
          width: '760px',
          content: h('pre', { class: 'code-box', style: { maxHeight: '480px' } }, h('code', {}, res)),
        });
      } else {
        toast('已获取证书内容', 'success');
      }
    } catch (e) {
      toast(e.message || '下载失败', 'error');
    }
  }

  async function doRevoke(cert) {
    const bound = cache.sites.filter((s) => s.cert && s.cert.cert_id === cert.id);
    const ok = await confirmDialog({
      title: '吊销 / 删除证书',
      message: bound.length
        ? `证书「${cert.name}」正被 ${bound.length} 个站点使用（${bound.map((s) => s.name).join('、')}）。吊销后这些站点的 HTTPS 将立即失效，确定继续吗？`
        : `确定要吊销并删除证书「${cert.name}」吗？吊销后无法恢复。`,
      okText: '吊销',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.certs.revoke(cert.id);
      // 吊销后必须同步列表：早期只 toast，已吊销证书仍留在列表里。
      // 这里重渲整个视图，避免依赖外部作用域的 listBox。
      cache.certs = cache.certs.filter((c) => c.id !== cert.id);
      toast('证书已吊销', 'success');
      render(container);
    } catch (e) {
      toast(e.message || '吊销失败', 'error');
    }
  }
}