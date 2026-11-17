// 新建站点向导：域名 → 根目录 → PHP → SSL（可跳过先去申请证书）→ 完成
import { api } from '../api.js';
import {
  h, clear, icon, button, tag, field, textInput, textArea, selectInput,
  switchControl, loading, errorState, emptyState, toast, card, grid,
  sectionTitle, iconButton, certDaysBadge,
} from '../components.js';
import { ctx } from '../app.js';

const STEPS = ['域名', '根目录', 'PHP 版本', 'SSL 设置', '确认创建'];

export async function render(container) {
  clear(container);

  // 向导草稿（站点对象的子集，其余交给后端补默认值）
  const draft = {
    name: '',
    domains: [],
    root: { path: '', user: 'www', group: 'www', symlink: false },
    php: { enabled: true, version: '8.2', socket: '', max_execution_time: 30, memory_limit: '256M', disable_functions: [], custom_php_config: '' },
    ssl: { enabled: false, cert_file: '', key_file: '', force_https: true, hsts: false, hsts_max_age: 31536000, protocols: ['TLSv1.2', 'TLSv1.3'], ocsp: false, use_http2: true },
    index: { files: ['index.php', 'index.html', 'index.htm'] },
    raw_conf: '',
    cert_id: '',
    // create_db：用户在确认页勾"同时建数据库"时为 true。
    // payload 里只放占位对象（不传 name/user/password）让后端用 siteID 推导 + 生成。
    create_db: false,
    // v0.2.1: 对外监听端口。空 = 默认 [80]。全局 -public-port 模式下被服务端覆盖。
    listen_ports: [80],
  };

  // 可选数据：PHP 版本、可用证书
  let phpVersions = ['8.2', '8.1', '8.0', '7.4', '5.6'];
  let certs = [];
  try {
    const [st, cr] = await Promise.all([api.system.status().catch(() => null), api.certs.list().catch(() => ({ certs: [] }))]);
    // SystemStatus.PHP 是 []PHPVersion 数组；早期读 st.php.versions
    // 恒为 undefined，PHP 下拉只能走写死的兜底列表。
    if (st && Array.isArray(st.php) && st.php.length) {
      phpVersions = st.php.map((p) => String(p && p.version)).filter(Boolean);
    }
    certs = cr.certs || [];
  } catch (e) {
    // 允许降级为默认版本列表
  }

  let step = 0;
  // 最近一次自动填写的根目录路径。用于区分"自动值"与"手动值"：
  // 自动值在主域名变化时跟随更新，手动值永不回填。
  let autoPath = '';

  container.appendChild(h('div', { class: 'page-header' }, [
    h('div', {}, [
      h('div', { class: 'crumb' }, [h('a', { href: '#/sites', class: 'crumb-link' }, '网站'), h('span', { class: 'crumb-sep' }, '/'), h('span', {}, '新建站点')]),
      h('h1', { class: 'page-title' }, '新建站点'),
      h('div', { class: 'page-sub' }, '按向导完成 5 步即可创建一个可用的站点'),
    ]),
    h('div', { class: 'page-actions' }, button('取消', () => ctx.navigate('#/sites'), 'default')),
  ]));

  const stepper = h('div', { class: 'stepper' });
  const bodyBox = h('div', { class: 'wizard-body' });
  const footBox = h('div', { class: 'wizard-foot' });
  container.appendChild(stepper);
  container.appendChild(bodyBox);
  container.appendChild(footBox);

  function renderStepper() {
    clear(stepper);
    STEPS.forEach((s, i) => {
      stepper.appendChild(h('div', { class: `step ${i === step ? 'is-active' : ''} ${i < step ? 'is-done' : ''}`.trim() }, [
        h('span', { class: 'step-num' }, i < step ? icon('check', 14) : String(i + 1)),
        h('span', { class: 'step-label' }, s),
      ]));
      if (i < STEPS.length - 1) stepper.appendChild(h('div', { class: 'step-line' }));
    });
  }

  function canNext() {
    if (step === 0) return draft.domains.length > 0 && draft.domains.every((d) => d.trim());
    if (step === 1) return !!draft.root.path.trim();
    return true;
  }

  function next() {
    if (!canNext()) {
      toast(step === 0 ? '请至少填写一个域名' : '请填写网站根目录', 'error');
      return;
    }
    // 域名 → 根目录自动填写：离开第 1 步时按主域名填入 /www/wwwroot/<主域名>。
    // 与后端建站兜底（service.go：Root.Path 为空时同样取主域名）保持一致，
    // 通配符 *.foo.com 落盘成 _foo.com。仅当路径为空或仍是上次自动填的值时
    // 覆盖——用户手动改过的路径不回填（回到上一步改域名也不会冲掉手改值）。
    if (step === 0) {
      const auto = autoRootPath();
      if (auto && (!draft.root.path.trim() || draft.root.path === autoPath)) {
        draft.root.path = auto;
        autoPath = auto;
      }
    }
    step = Math.min(STEPS.length - 1, step + 1);
    draw();
  }

  function prev() {
    step = Math.max(0, step - 1);
    draw();
  }

  function draw() {
    renderStepper();
    clear(bodyBox);
    clear(footBox);
    bodyBox.appendChild(h('div', { class: 'card' }, h('div', { class: 'card-body' }, stepContent())));
    footBox.appendChild(
      h('div', { class: 'wizard-actions' }, [
        step > 0 ? button('上一步', prev, 'default') : null,
        step < STEPS.length - 1
          ? button('下一步', next, 'primary')
          : button('创建站点', doCreate, 'primary', 'check'),
      ])
    );
  }

  function stepContent() {
    if (step === 0) return stepDomain();
    if (step === 1) return stepRoot();
    if (step === 2) return stepPhp();
    if (step === 3) return stepSsl();
    return stepConfirm();
  }

  // 步骤 1：域名
  function stepDomain() {
    const list = h('div', { class: 'domain-editor' });
    const drawList = () => {
      clear(list);
      draft.domains.forEach((d, i) => {
        list.appendChild(h('div', { class: 'domain-row' }, [
          tag(i === 0 ? '主域名' : `别名 ${i}`, i === 0 ? 'primary' : 'default'),
          textInput(d, (v) => { draft.domains[i] = v; syncName(); }, { placeholder: 'example.com' }),
          iconButton('trash', '移除该域名', () => {
            if (draft.domains.length <= 1) {
              toast('至少保留一个域名', 'error');
              return;
            }
            draft.domains.splice(i, 1);
            drawList();
            syncName();
          }, 'danger-ghost'),
        ]));
      });
      list.appendChild(button('添加域名', () => {
        draft.domains.push('');
        drawList();
      }, 'default', 'plus'));
    };
    // 只同步站点名称输入框，绝不重绘列表。
    // 早期这里调用 drawList()，而 drawList 会 clear(list) 全量重建所有
    // 域名行——正在输入的框被销毁重建，焦点掉到 body，域名几乎无法连续输入。
    const syncName = () => {
      const first = (draft.domains[0] || '').trim();
      if (!draft.name.trim() || draft.name === prevName) {
        draft.name = first;
        prevName = first;
        nameInput.value = first;
      }
    };
    let prevName = draft.domains[0] || '';
    const nameInput = textInput(draft.name, (v) => { draft.name = v; }, { placeholder: '留空则自动使用主域名' });
    drawList();
    return h('div', {}, [
      sectionTitle('站点名称'),
      field('站点名称', nameInput, '仅用于面板内显示'),
      sectionTitle('绑定域名'),
      list,
      h('div', { class: 'field-hint' }, '主域名将作为默认 server_name；同一站点可绑定多个域名'),
    ]);
  }

  // 步骤 2：根目录
  function stepRoot() {
    // 路径提示随输入实时更新
    const suggestBox = h('div', { class: 'field-hint' }, suggestPath(draft.root.path));
    return h('div', {}, [
      sectionTitle('网站根目录'),
      field('根目录路径', textInput(draft.root.path, (v) => {
        draft.root.path = v;
        suggestBox.textContent = suggestPath(v);
      }, { placeholder: '/www/wwwroot/example.com' }), '必须是服务器上真实存在的目录'),
      suggestBox,
      sectionTitle('运行身份'),
      grid(2,
        field('运行用户', textInput(draft.root.user, (v) => { draft.root.user = v; })),
        field('运行用户组', textInput(draft.root.group, (v) => { draft.root.group = v; })),
      ),
      switchFieldRow('允许符号链接', draft.root.symlink, (v) => { draft.root.symlink = v; }),
      // v0.2.1: 对外监听端口（默认 80）。非标端口需云安全组 + 防火墙同时放行。
      // 若需"主站 80/443 + 内部服务非标端口"，推荐用"反代指向 127.0.0.1:<port>"。
      field('对外监听端口',
        textInput((draft.listen_ports || [80]).join(','), (v) => {
          draft.listen_ports = v.split(/[,，\s]+/).map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n) && n > 0);
        }, { placeholder: '80, 8080（逗号分隔）' }),
        '80 是默认；可填多个。SSL 启用时自动追加 443。同一端口只能给一个站点。'),
      sectionTitle('默认文档'),
      field('默认文档', textInput(draft.index.files.join(' '), (v) => {
        draft.index.files = v.split(/[\s,]+/).filter(Boolean);
      }, { placeholder: 'index.php index.html index.htm' }), '空格分隔，按顺序匹配'),
    ]);
  }

  function suggestPath(root) {
    const d = (draft.domains[0] || '').trim();
    if (root.trim() && root === autoPath) return `已按主域名自动填写，可手动修改：${root.trim()}`;
    if (!root.trim() && d) return `建议路径：/www/wwwroot/${d}`;
    if (!root.trim()) return '例如：/www/wwwroot/example.com';
    return `将挂载到：${root.trim()}`;
  }

  // 主域名 → 默认根目录路径（/www/wwwroot/<主域名>）。
  // 规则与后端建站兜底一致：通配符 *.foo.com → _foo.com（service.go CreateSite）。
  function autoRootPath() {
    const d = (draft.domains[0] || '').trim();
    if (!d) return '';
    return `/www/wwwroot/${d.replace(/^\*\./, '_')}`;
  }

  function switchFieldRow(label, checked, onChange) {
    return h('div', { class: 'switch-field' }, [
      h('div', { class: 'switch-field-text' }, h('div', { class: 'switch-field-label' }, label)),
      switchControl(checked, onChange),
    ]);
  }

  // 步骤 3：PHP
  function stepPhp() {
    return h('div', {}, [
      sectionTitle('是否启用 PHP'),
      h('div', { class: 'switch-field' }, [
        h('div', { class: 'switch-field-text' }, [
          h('div', { class: 'switch-field-label' }, '启用 PHP'),
          h('div', { class: 'field-hint' }, '静态站点请关闭'),
        ]),
        switchControl(draft.php.enabled, (v) => { draft.php.enabled = v; draw(); }),
      ]),
      draft.php.enabled ? grid(2,
        field('PHP 版本', selectInput(draft.php.version, phpVersions, (v) => { draft.php.version = v; })),
        field('内存限制', textInput(draft.php.memory_limit, (v) => { draft.php.memory_limit = v; }, { placeholder: '256M' })),
      ) : emptyState('该站点将按纯静态站点处理（仅默认文档匹配）', null, 'sites'),
    ]);
  }

  // 步骤 4：SSL（可跳过）
  function stepSsl() {
    return h('div', {}, [
      h('div', { class: 'switch-field' }, [
        h('div', { class: 'switch-field-text' }, [
          h('div', { class: 'switch-field-label' }, '启用 SSL'),
          h('div', { class: 'field-hint' }, '若尚未申请证书，可先跳过，创建站点后再去证书页申请并绑定'),
        ]),
        switchControl(draft.ssl.enabled, (v) => { draft.ssl.enabled = v; draw(); }),
      ]),
      draft.ssl.enabled ? h('div', {}, [
        certs.length
          ? field('选择已有证书', selectInput(draft.cert_id, [{ value: '', label: '暂不绑定' }, ...certs.map((c) => ({ value: c.id, label: `${(c.domains || []).join(', ')}（到期 ${(c.not_after || '').slice(0, 10)}）` }))],
            (v) => {
              draft.cert_id = v;
              const c = certs.find((x) => x.id === v);
              if (c) {
                // 同 certs.js 一键绑定的理由：用 cert.id 做目录，cert.name
                // 中文命名时含空格会 nginx 渲染出错。
                draft.ssl.cert_file = `/opt/zhiweipanel/data/certs/${c.id}/fullchain.pem`;
                draft.ssl.key_file = `/opt/zhiweipanel/data/certs/${c.id}/privkey.pem`;
              }
            }))
          : emptyState('还没有可用证书', button('先去申请证书', () => ctx.navigate('#/certs'), 'primary', 'key'), 'cert'),
        h('div', { class: 'inline-actions' }, button('前往申请证书', () => ctx.navigate('#/certs'), 'default', 'key')),
        grid(2,
          switchFieldRow('强制 HTTPS 跳转', draft.ssl.force_https, (v) => { draft.ssl.force_https = v; }),
          switchFieldRow('开启 HTTP/2', draft.ssl.use_http2, (v) => { draft.ssl.use_http2 = v; }),
        ),
      ]) : null,
    ]);
  }

  // 步骤 5：确认
  function stepConfirm() {
    const rows = [
      ['站点名称', draft.name || draft.domains[0]],
      ['域名', draft.domains.join(', ')],
      ['根目录', draft.root.path],
      ['运行身份', `${draft.root.user}:${draft.root.group}`],
      ['默认文档', draft.index.files.join(', ')],
      ['PHP', draft.php.enabled ? draft.php.version : '未启用'],
      ['SSL', draft.ssl.enabled ? (draft.cert_id ? '已绑定证书' : '启用（未绑定证书）') : '未启用'],
    ];
    // MySQL 选项：拉一次 /api/system/mysql 看子系是否就绪。
    // 不就绪时禁用复选框 + 提示用户（前端能做的是"软提醒"，不能替用户装）。
    const dbBox = h('div', { class: 'switch-field' });
    let dbAvailable = true;
    let dbHint = '将自动建库 site_xxxxxxxxxxxx，建同名用户并授该库全权';
    api.system.mysql().then((st) => {
      if (!st || !st.installed || !st.running) {
        dbAvailable = false;
        draft.create_db = false;
        dbBox.replaceChildren(
          h('div', { class: 'switch-field-text' }, [
            h('div', { class: 'switch-field-label' }, '同时创建 MySQL 数据库'),
            h('div', { class: 'field-hint' }, '未检测到 MySQL 子系统：请用 deploy/install.sh 重装时设 INSTALL_MYSQL=1，或在管理后台启用'),
          ]),
          switchControl(false, () => {}, true),
        );
      } else {
        dbBox.replaceChildren(
          h('div', { class: 'switch-field-text' }, [
            h('div', { class: 'switch-field-label' }, '同时创建 MySQL 数据库'),
            h('div', { class: 'field-hint' }, dbHint + '（' + (st.server || 'MySQL') + ' ' + (st.version || '') + '）'),
          ]),
          switchControl(draft.create_db, (v) => { draft.create_db = v; }),
        );
      }
    }).catch(() => {
      // 探测失败保持默认显示
    });
    if (dbAvailable) {
      dbBox.replaceChildren(
        h('div', { class: 'switch-field-text' }, [
          h('div', { class: 'switch-field-label' }, '同时创建 MySQL 数据库'),
          h('div', { class: 'field-hint' }, dbHint + '（探测中…）'),
        ]),
        switchControl(draft.create_db, (v) => { draft.create_db = v; }),
      );
    }
    return h('div', {}, [
      h('div', { class: 'confirm-box' }, [
        h('div', { class: 'confirm-title' }, '请确认站点信息'),
        ...rows.map(([k, v]) => h('div', { class: 'confirm-row' }, [h('span', { class: 'confirm-k' }, k), h('span', { class: 'confirm-v' }, v || '-')])),
      ]),
      sectionTitle('数据库'),
      dbBox,
      h('div', { class: 'field-hint' }, '创建后可在站点详情中继续调整 17 项功能配置'),
    ]);
  }

  // 提交
  async function doCreate() {
    const domains = draft.domains.map((d) => d.trim()).filter(Boolean);
    if (!domains.length) {
      toast('请至少填写一个域名', 'error');
      step = 0;
      draw();
      return;
    }
    if (!draft.root.path.trim()) {
      toast('请填写网站根目录', 'error');
      step = 1;
      draw();
      return;
    }

    const btn = footBox.querySelector('.btn-primary');
    if (btn) btn.disabled = true;

    const payload = {
      name: draft.name.trim() || domains[0],
      status: 'running',
      remark: '',
      domains,
      root: draft.root,
      listen_ports: draft.listen_ports && draft.listen_ports.length ? draft.listen_ports : [80],
      php: draft.php,
      index: draft.index,
      // v0.4.0: 自定义 Nginx 配置统一走 extra_conf（创建时留空，详情页再编辑）
      extra_conf: '',
      extra_conf_pos: 'inside',
      sub_dir: { enabled: false, rules: [] },
      access: { enabled: false, allow: [], deny: [], auth_enabled: false, auth_user: '', auth_pass: '', allow_methods: [] },
      traffic: { bandwidth_limit: '', monthly_quota_mb: 0, enforce_quota: false, rate_limit_per_ip: 0 },
      rewrite: { enabled: false, rules: '', file: '' },
      redirect: { rules: [] },
      proxy: { enabled: false, rules: [] },
      hotlink: { enabled: false, allowed_domains: [], file_extensions: ['jpg', 'png', 'zip'], return_code: 403 },
      lock: { enabled: false, files: [], auto_restore: false, alert_on_tamper: true },
      security: { block_sqli: false, block_xss: false, block_traversal: false, block_ua: false, block_ua_keywords: [], deny_paths: [], deny_extensions: [], server_tokens_off: true, prevent_iframe: false },
      logs: { access_log: true, error_log: true, custom_format: '', retention_days: 30, disable_log: false },
      alert: { enabled: true, rules: [], channels: [] },
      extra: { gzip: true, gzip_type: '', gzip_min_length: 1024, client_max_body_size: (draft.php.enabled ? '32m' : '16m'), keepalive_timeout: 0, send_timeout: 0, read_timeout: 0, dns_resolver: '', browser_cache: 0, static_cache_ext: ['jpg', 'png', 'css', 'js'] },
      ssl: { ...draft.ssl, enabled: draft.ssl.enabled },
    };

    // 若选中了已有证书，后端按 cert_id 绑定
    if (draft.cert_id) payload.cert_id = draft.cert_id;
    // 若勾了建库，传占位 mysql 对象（name/user/password 留空让后端用 siteID 推导 + 随机生成）
    if (draft.create_db) payload.mysql = {};

    try {
      const created = await api.sites.create(payload);
      // 响应里如果 show_password=true，立刻弹"请保存数据库密码"模态框。
      // 这是密码唯一一次明文回显——错过就没了。
      if (created && created.show_password && created.site && created.site.mysql) {
        showPasswordModal(created.site.mysql);
      } else {
        toast('站点创建成功', 'success');
      }
      const id = created && (created.id || (created.site && created.site.id));
      ctx.navigate(id ? `#/sites/${encodeURIComponent(id)}` : '#/sites');
    } catch (e) {
      toast(e.message || '创建失败', 'error');
      const btn2 = footBox.querySelector('.btn-primary');
      if (btn2) btn2.disabled = false;
    }
  }

  // 一次性数据库密码展示模态框。包含"复制到剪贴板"按钮。
  // 这是密码明文唯一一次被前端拿到，必须明显提示用户保存。
  function showPasswordModal(db) {
    const text = `数据库名: ${db.name}\n用户:     ${db.user}\n密码:     ${db.password}\n主机:     ${db.host}:${db.port}\n字符集:   ${db.charset || 'utf8mb4'}`;
    const copyBtn = button('复制到剪贴板', async () => {
      try {
        await navigator.clipboard.writeText(text);
        toast('已复制到剪贴板', 'success');
      } catch (e) {
        toast('复制失败：' + e.message, 'error');
      }
    }, 'primary', 'copy');
    const m = modal({
      title: '请保存数据库密码',
      width: '520px',
      content: h('div', {}, [
        h('div', { class: 'modal-hint' }, '这是密码唯一一次明文显示，关闭后无法再次查看。如丢失请重置密码。'),
        h('pre', { class: 'modal-pre' }, text),
      ]),
      footer: [copyBtn, button('我已保存，关闭', () => m.close(), 'default')],
    });
  }

  draw();
}