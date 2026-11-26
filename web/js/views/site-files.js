// 站点文件管理视图：嵌入 site-detail 的 "文件管理" 标签页。
// 入口：mountFilesView(parent, { siteId, rootPath })
//   - parent: 容器元素
//   - siteId: 站点 ID
//   - rootPath: 站点根目录（来自站点对象）
//
// 设计要点：
//   - 内部维护 cwd 状态；切到该 tab 时由 tab.render() 触发初次 mount
//   - 列表/读/写/删/重命名/建目录全部走 api.sites.files
//   - 文本编辑用 modal 包裹大 textarea；不直接覆盖原列表（避免误操作）
//   - 二进制文件不支持编辑器（直接提示），仅支持删除/重命名

import { api } from '../api.js';
import { h, clear, button, icon, toast, modal, confirmDialog, textArea, textInput, loading, emptyState, errorState, appendAll } from '../components.js';

const TEXT_EXT = new Set([
  'txt', 'md', 'log', 'json', 'xml', 'yml', 'yaml', 'conf', 'ini', 'cfg',
  'html', 'htm', 'css', 'js', 'ts', 'map', 'vue', 'svelte',
  'go', 'py', 'rs', 'c', 'h', 'cpp', 'hpp', 'java', 'rb', 'php', 'sh', 'bash', 'sql',
  'env', 'gitignore', 'dockerfile', 'toml',
]);

// 是否"看起来像"文本：扩展名命中 + 文件大小 < 2MB（避免打开巨大文件）
function looksTextual(name, size) {
  if (size > 2 * 1024 * 1024) return false;
  const dot = name.lastIndexOf('.');
  if (dot < 0) return name.toLowerCase() === 'dockerfile' || name.toLowerCase() === 'makefile';
  return TEXT_EXT.has(name.slice(dot + 1).toLowerCase());
}

// 规范化路径：空 -> '.', 其它去掉前导 '/'
function norm(p) {
  if (!p) return '.';
  return p.replace(/^\/+/, '');
}

// 拼接子路径
function joinPath(parent, child) {
  parent = norm(parent);
  if (parent === '.') return child;
  return parent + '/' + child;
}

// 相对当前 cwd 解析目标路径（不允许 .. 跳出）
function resolveChild(cwd, name) {
  if (name === '..') {
    if (cwd === '' || cwd === '.') return '';
    const i = cwd.lastIndexOf('/');
    return i < 0 ? '' : cwd.slice(0, i);
  }
  return joinPath(cwd, name);
}

function formatSize(n) {
  if (n == null) return '-';
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
}

function formatMTime(iso) {
  if (!iso) return '-';
  try {
    const d = new Date(iso);
    const y = d.getFullYear();
    const M = String(d.getMonth() + 1).padStart(2, '0');
    const D = String(d.getDate()).padStart(2, '0');
    const h = String(d.getHours()).padStart(2, '0');
    const m = String(d.getMinutes()).padStart(2, '0');
    return `${y}-${M}-${D} ${h}:${m}`;
  } catch {
    return iso;
  }
}

// 从 base64 字符串安全解码为 UTF-8 文本
function b64ToText(b64) {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new TextDecoder('utf-8', { fatal: false }).decode(bytes);
}

function textToB64(s) {
  const bytes = new TextEncoder().encode(s);
  let bin = '';
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin);
}

export function mountFilesView(parent, ctx) {
  const { siteId, rootPath } = ctx;
  if (!rootPath) {
    clear(parent).appendChild(emptyState('该站点未配置根目录（root.path 为空），无法管理文件。'));
    return;
  }
  // 每次切到该 tab 都重置到根目录：FM 工具的常见行为，避免
  // 切走再切回时带着陈旧 cwd 误操作。
  let cwd = '';
  let entries = [];
  const selected = new Set();

  // ---------- 顶部条 ----------
  const breadcrumb = h('div', { class: 'fm-breadcrumb' });
  const tbody = h('tbody');
  const selectAll = h('input', {
    type: 'checkbox',
    title: '全选当前目录',
    onchange: (e) => {
      selected.clear();
      if (e.target.checked) entries.forEach((x) => selected.add(x.name));
      renderList(entries);
    },
  });
  const table = h('table', { class: 'fm-table' }, [
    h('thead', {}, h('tr', {}, [
      h('th', { style: { width: '34px' } }, selectAll),
      h('th', {}, '名称'),
      h('th', { style: { width: '120px' } }, '大小'),
      h('th', { style: { width: '160px' } }, '修改时间'),
      h('th', { style: { width: '200px' } }, '操作'),
    ])),
    tbody,
  ]);
  const listArea = h('div', { class: 'fm-list' }, table);

  const refreshBtn = button('刷新', () => load(cwd), 'default', 'refresh');
  const newFileBtn = button('新建文件', () => onNewFile(), 'default', 'plus');
  const newDirBtn = button('新建目录', () => onNewDir(), 'default', 'folder');
  const uploadBtn = button('上传', () => fileInput.click(), 'default', 'upload');
  const fetchBtn = button('远程下载', onFetch, 'default', 'download');
  const compressBtn = button('压缩', onCompress, 'default', 'backup');
  const extractBtn = button('解压', onExtract, 'default', 'folder');
  const downloadBtn = button('下载', onDownload, 'default', 'download');
  const batchDeleteBtn = button('删除选中', onBatchDelete, 'danger', 'trash');
  const chmodBtn = button('权限', onChmod, 'default', 'key');
  const searchBtn = button('搜索', onSearch, 'default', 'search');

  // 隐藏的 file input：multiple 支持多选
  const fileInput = h('input', {
    type: 'file',
    multiple: true,
    style: { display: 'none' },
  });
  fileInput.addEventListener('change', () => {
    if (fileInput.files && fileInput.files.length > 0) {
      const files = Array.from(fileInput.files);
      fileInput.value = ''; // 清空，允许同一文件再次选择
      onUploadFiles(files);
    }
  });

  const dropzone = h('div', { class: 'fm-dropzone' }, '把文件拖到此处上传，或点击上方"上传"按钮');
  const progressBox = h('div', { class: 'fm-upload-progress', style: { display: 'none' } });

  const header = h('div', { class: 'fm-toolbar' }, [
    breadcrumb,
    h('div', { class: 'fm-toolbar-right' }, [
      uploadBtn, newFileBtn, newDirBtn, fetchBtn, searchBtn,
      compressBtn, extractBtn, downloadBtn, chmodBtn, batchDeleteBtn, refreshBtn,
    ]),
  ]);
  const body = h('div', { class: 'fm-body' }, [listArea, dropzone, progressBox]);
  clear(parent).appendChild(h('div', { class: 'fm-root' }, [header, body, fileInput]));

  // ---------- 渲染：面包屑 ----------
  function renderBreadcrumb() {
    clear(breadcrumb);
    const parts = cwd ? cwd.split('/') : [];
    const crumbs = [h('a', { href: '#', class: 'fm-crumb', onclick: (e) => { e.preventDefault(); load(''); } }, rootPath)];
    let acc = '';
    for (const p of parts) {
      acc = acc ? acc + '/' + p : p;
      const target = acc;
      crumbs.push(h('span', { class: 'fm-crumb-sep' }, '/'));
      crumbs.push(h('a', {
        href: '#', class: 'fm-crumb', onclick: (e) => { e.preventDefault(); load(target); },
      }, p));
    }
    appendAll(breadcrumb, crumbs);
  }

  // ---------- 渲染：列表 ----------
  function renderList(list) {
    clear(tbody);
    selectAll.checked = list.length > 0 && selected.size === list.length;
    selectAll.indeterminate = selected.size > 0 && selected.size < list.length;
    if (!list || list.length === 0) {
      tbody.appendChild(h('tr', {}, h('td', { colspan: 5, class: 'fm-empty' }, '（空目录）')));
      updateActionState();
      return;
    }
    for (const e of list) {
      const isDir = e.type === 'dir';
      const checkbox = h('input', {
        type: 'checkbox',
        checked: selected.has(e.name),
        onchange: (ev) => {
          if (ev.target.checked) selected.add(e.name);
          else selected.delete(e.name);
          selectAll.checked = selected.size === list.length;
          selectAll.indeterminate = selected.size > 0 && selected.size < list.length;
          updateActionState();
        },
      });
      const nameCell = h('td', { class: 'fm-name' }, [
        h('span', { class: `fm-icon fm-icon-${e.type}` }, isDir ? '📁' : '📄'),
        h('a', {
          href: '#',
          onclick: (ev) => {
            ev.preventDefault();
            if (isDir) load(resolveChild(cwd, e.name));
            else onOpen(e);
          },
        }, e.name),
        h('span', { class: 'fm-mode' }, e.mode || ''),
      ]);
      const actions = h('td', { class: 'fm-actions' }, [
        !isDir && looksTextual(e.name, e.size)
          ? button('编辑', () => onOpen(e), 'default', 'edit')
          : null,
        button('重命名', () => onRename(e), 'default', 'edit'),
        button('删除', () => onDelete(e), 'danger', 'trash'),
      ]);
      tbody.appendChild(h('tr', {}, [
        h('td', { class: 'fm-check' }, checkbox),
        nameCell,
        h('td', { class: 'fm-size' }, isDir ? '-' : formatSize(e.size)),
        h('td', { class: 'fm-mtime' }, formatMTime(e.mtime)),
        actions,
      ]));
    }
    updateActionState();
  }

  function selectedEntries() {
    return entries.filter((e) => selected.has(e.name));
  }

  function isArchiveName(name) {
    return /\.(zip|tar|tgz|tar\.gz)$/i.test(String(name || ''));
  }

  function updateActionState() {
    const n = selected.size;
    const sel = selectedEntries();
    compressBtn.disabled = n === 0;
    extractBtn.disabled = n !== 1 || !isArchiveName(sel[0] && sel[0].name);
    downloadBtn.disabled = n !== 1 || !!(sel[0] && sel[0].type === 'dir');
    batchDeleteBtn.disabled = n === 0;
    chmodBtn.disabled = n !== 1;
  }

  // ---------- 拖拽上传 ----------
  // 把整块 body 区域做成 dropzone：dragover 高亮 + drop 接收 files。
  // 关键：必须 preventDefault 才能触发 drop；dragenter/dragover 之间
  // 需要 counter 区分跨子元素冒泡——这里 dropzone 简单不分子元素，
  // 直接 dragover/drop 两个事件 + timeout 清除高亮。
  let dragDepth = 0;
  function showProgress(text, kind) {
    progressBox.textContent = text;
    progressBox.classList.remove('fm-progress-error', 'fm-progress-done');
    if (kind === 'error') progressBox.classList.add('fm-progress-error');
    if (kind === 'done') progressBox.classList.add('fm-progress-done');
    progressBox.style.display = 'block';
  }
  function hideProgress() {
    progressBox.style.display = 'none';
    progressBox.textContent = '';
  }

  ['dragenter', 'dragover'].forEach((evt) => {
    body.addEventListener(evt, (e) => {
      if (!e.dataTransfer || !Array.from(e.dataTransfer.types || []).includes('Files')) return;
      e.preventDefault();
      dragDepth++;
      dropzone.classList.add('fm-dropzone-active');
    });
  });
  ['dragleave', 'dragend'].forEach((evt) => {
    body.addEventListener(evt, (e) => {
      if (!e.dataTransfer) return;
      dragDepth = Math.max(0, dragDepth - 1);
      if (dragDepth === 0) dropzone.classList.remove('fm-dropzone-active');
    });
  });
  body.addEventListener('drop', (e) => {
    if (!e.dataTransfer) return;
    e.preventDefault();
    dragDepth = 0;
    dropzone.classList.remove('fm-dropzone-active');
    const files = Array.from(e.dataTransfer.files || []);
    if (files.length === 0) return;
    onUploadFiles(files);
  });

  // ---------- 上传文件（含 .zip 自动解压） ----------
  // 流程：
  //   1. 把所有文件 multipart 上传到 cwd
  //   2. 如果其中有 .zip，等上传完成后再调 unzip API 把它解到 cwd
  //   3. 上传/解压失败单独 toast，不阻断其他文件
  async function onUploadFiles(files) {
    if (!files || files.length === 0) return;

    // 分离 zip 与普通文件
    const isZip = (f) => /\.zip$/i.test(f.name);
    const zipFiles = files.filter(isZip);
    const otherFiles = files.filter((f) => !isZip(f));

    const totalBytes = files.reduce((s, f) => s + (f.size || 0), 0);
    showProgress(`正在上传 ${files.length} 个文件（${formatSize(totalBytes)}）…`, null);

    try {
      // 一次性把全部 file 上传（含 zip）；后端按文件名落盘到 cwd
      await api.sites.files.upload(siteId, cwd, files);
      showProgress(`已上传 ${files.length} 个文件`, 'done');

      // 如果含 zip，调 unzip 让其自动展开到 cwd
      if (zipFiles.length > 0) {
        showProgress(`解压 ${zipFiles.length} 个 zip…`, null);
        for (const zf of zipFiles) {
          const zipPath = joinPath(cwd, zf.name);
          const r = await api.sites.files.unzip(siteId, zipPath, cwd);
          toast(`已解压 ${zf.name}（${r.extracted} 个文件）`, 'success');
        }
        showProgress(`完成（${files.length} 上传 / ${zipFiles.length} 解压）`, 'done');
      } else {
        toast(`已上传 ${files.length} 个文件`, 'success');
      }
      await load(cwd);
      setTimeout(hideProgress, 1800);
    } catch (e) {
      showProgress(`上传失败：${e.message || e}`, 'error');
      toast('上传失败：' + (e.message || e), 'error', 4800);
    }
  }

  // ---------- 加载 ----------
  async function load(p) {
    cwd = p || '';
    selected.clear();
    renderBreadcrumb();
    clear(listArea);
    listArea.appendChild(loading('正在加载…'));
    try {
      const res = await api.sites.files.list(siteId, cwd);
      entries = res.entries || [];
      selected.clear();
      renderList(entries);
      // 重新挂回（loading 已被清掉）
      clear(listArea);
      listArea.appendChild(table);
    } catch (e) {
      entries = [];
      clear(listArea).appendChild(errorState(`加载失败：${e.message || e}`, () => load(cwd)));
    }
  }

  // ---------- 新建文件 ----------
  async function onNewFile() {
    const name = await promptModal({ title: '新建文件', label: '文件名（含扩展名）', placeholder: 'index.html' });
    if (!name) return;
    const full = joinPath(cwd, name);
    try {
      await api.sites.files.write(siteId, full, textToB64(''));
      toast('已创建', 'success');
      await load(cwd);
    } catch (e) {
      toast('创建失败：' + (e.message || e), 'error', 3600);
    }
  }

  // ---------- 新建目录 ----------
  async function onNewDir() {
    const name = await promptModal({ title: '新建目录', label: '目录名', placeholder: 'uploads' });
    if (!name) return;
    const full = joinPath(cwd, name);
    try {
      await api.sites.files.mkdir(siteId, full);
      toast('已创建', 'success');
      await load(cwd);
    } catch (e) {
      toast('创建失败：' + (e.message || e), 'error', 3600);
    }
  }

  // ---------- 重命名 ----------
  async function onRename(entry) {
    const newName = await promptModal({ title: '重命名', label: '新名称', initial: entry.name });
    if (!newName || newName === entry.name) return;
    const from = joinPath(cwd, entry.name);
    const to = joinPath(cwd, newName);
    try {
      await api.sites.files.rename(siteId, from, to);
      toast('已重命名', 'success');
      await load(cwd);
    } catch (e) {
      toast('重命名失败：' + (e.message || e), 'error', 3600);
    }
  }

  // ---------- 删除 ----------
  async function onDelete(entry) {
    const ok = await confirmDialog({
      title: '确认删除',
      message: `确定要删除 ${entry.type === 'dir' ? '目录' : '文件'} "${entry.name}" 吗？此操作不可撤销。`,
      okText: '删除',
      danger: true,
    });
    if (!ok) return;
    const full = joinPath(cwd, entry.name);
    try {
      await api.sites.files.remove(siteId, full);
      toast('已删除', 'success');
      await load(cwd);
    } catch (e) {
      toast('删除失败：' + (e.message || e), 'error', 3600);
    }
  }

  // ---------- 打开（仅文本走编辑器；其它文件提示类型） ----------
  async function onOpen(entry) {
    if (!looksTextual(entry.name, entry.size)) {
      toast(`暂不支持在线预览 ${entry.name || '此类型文件'}，请使用重命名 / 删除`, 'info', 3200);
      return;
    }
    const full = joinPath(cwd, entry.name);
    let raw;
    try {
      raw = await api.sites.files.read(siteId, full);
    } catch (e) {
      toast('读取失败：' + (e.message || e), 'error', 3600);
      return;
    }
    let initial = '';
    let isBinary = false;
    if (typeof raw === 'string') {
      initial = raw;
    } else if (raw && typeof raw === 'object' && raw.encoding === 'base64') {
      try {
        initial = b64ToText(raw.content);
        isBinary = true;
      } catch {
        toast('文件不是有效的 UTF-8 文本', 'error', 3200);
        return;
      }
    } else {
      // ⚠️ 未知形状：绝不能当成"空文件"打开。
      //
      // 早期这里把不匹配的两个分支都落空，initial 保持 ''、isBinary 保持 false，
      // 于是编辑器打开一个空白 textarea，用户以为文件是空的，随手点保存
      // 就把空内容写回去——**原文件内容被清空**。
      //
      // 触发路径：读文件接口对文本文件返回裸文本、对二进制返回
      // {content, encoding:'base64'}；早期 api.js 靠"能否 JSON.parse"猜类型，
      // 文件内容恰好是合法 JSON 时会被解析成对象，两边都不匹配。
      // （api.js 已改为按 Content-Type 分流，这里是第二道保险。）
      toast('文件内容格式无法识别，已阻止编辑以免覆盖原文件', 'error', 4200);
      return;
    }
    openEditor({ path: full, name: entry.name, size: entry.size, initial, isBinary });
  }

  // ---------- 编辑器 ----------
  function openEditor({ path, name, size, initial, isBinary }) {
    const ta = textArea(initial, () => {}, { rows: 22, mono: true });
    ta.style.width = '100%';
    const dirtyRef = { v: false };
    ta.addEventListener('input', () => { dirtyRef.v = true; });

    const saveBtn = button('保存', async () => {
      saveBtn.disabled = true;
      try {
        await api.sites.files.write(siteId, path, textToB64(ta.value));
        toast('已保存', 'success');
        dirtyRef.v = false;
        closeEditor();
        await load(cwd);
      } catch (e) {
        toast('保存失败：' + (e.message || e), 'error', 3600);
        saveBtn.disabled = false;
      }
    }, 'primary', 'save');
    const cancelBtn = button('取消', () => closeEditor(), 'default', 'close');

    let warnHandler;
    function closeEditor() {
      if (dirtyRef.v) {
        // 简化：dirty 时也允许关闭；用户主动关就关。避免再加一层确认。
      }
      if (warnHandler) window.removeEventListener('beforeunload', warnHandler);
      m.close();
    }
    warnHandler = (e) => { if (dirtyRef.v) { e.preventDefault(); e.returnValue = ''; } };
    window.addEventListener('beforeunload', warnHandler);

    const m = modal({
      title: `编辑：${name}`,
      content: h('div', {}, [
        h('div', { class: 'fm-editor-meta' }, [
          h('span', { class: 'mono-text' }, path),
          ' · ',
          formatSize(size),
          isBinary ? ' · 二进制按字节读取' : '',
        ]),
        ta,
      ]),
      width: '800px',
      footer: h('div', { class: 'fm-editor-footer' }, [
        h('span', { class: 'fm-dirty', id: 'fmDirtyHint' }, ''),
        cancelBtn,
        saveBtn,
      ]),
    });
    // 同步 dirty 提示
    const hint = document.getElementById('fmDirtyHint');
    ta.addEventListener('input', () => {
      if (hint) hint.textContent = dirtyRef.v ? '● 未保存' : '';
    });
    setTimeout(() => ta.focus(), 50);
  }

  // ---------- v0.9.0：压缩 / 解压 / 下载 / 批量删除 / 权限 / 远程下载 / 搜索 ----------
  async function onCompress() {
    const sel = selectedEntries();
    if (sel.length === 0) return;
    const raw = await promptModal({
      title: `压缩 ${sel.length} 个选中项`,
      label: '格式（zip 或 tar.gz）',
      initial: 'zip',
    });
    if (raw === null) return;
    const format = raw.toLowerCase().includes('tar') ? 'tar.gz' : 'zip';
    try {
      const res = await api.sites.files.compress(
        siteId, cwd, sel.map((e) => joinPath(cwd, e.name)), format,
      );
      toast(`已生成 ${res.path}`, 'success', 4200);
      await load(cwd);
    } catch (e) {
      toast('压缩失败：' + (e.message || e), 'error', 5200);
    }
  }

  async function onExtract() {
    const sel = selectedEntries();
    if (sel.length !== 1) return;
    const e = sel[0];
    const dest = await promptModal({
      title: `解压 ${e.name}`,
      label: '目标目录（相对站点根；留空 = 压缩包所在目录）',
      initial: cwd || '.',
    });
    if (dest === null) return;
    try {
      const r = await api.sites.files.extract(siteId, joinPath(cwd, e.name), dest);
      toast(`已解压 ${r.extracted} 个条目`, 'success', 3600);
      await load(cwd);
    } catch (err) {
      toast('解压失败：' + (err.message || err), 'error', 5200);
    }
  }

  function onDownload() {
    const sel = selectedEntries();
    if (sel.length !== 1 || sel[0].type === 'dir') return;
    window.location.href = api.sites.files.downloadURL(siteId, joinPath(cwd, sel[0].name));
  }

  async function onBatchDelete() {
    const sel = selectedEntries();
    if (sel.length === 0) return;
    const hasDir = sel.some((e) => e.type === 'dir');
    const ok = await confirmDialog({
      title: '批量删除',
      message: `将删除 ${sel.length} 个选中项${hasDir ? '；目录会连同内部内容递归删除' : ''}。此操作不可恢复。`,
      okText: '删除',
      danger: true,
    });
    if (!ok) return;
    try {
      const r = await api.sites.files.removeBatch(
        siteId, sel.map((e) => joinPath(cwd, e.name)), hasDir,
      );
      toast(`已删除 ${r.deleted} 项`, 'success');
      await load(cwd);
    } catch (e) {
      toast('删除失败：' + (e.message || e), 'error', 5200);
    }
  }

  async function onChmod() {
    const sel = selectedEntries();
    if (sel.length !== 1) return;
    const e = sel[0];
    const mode = await promptModal({
      title: `修改权限：${e.name}`,
      label: '八进制权限（如 644 / 0755）',
      initial: e.mode || '0644',
    });
    if (mode === null) return;
    try {
      await api.sites.files.chmod(siteId, joinPath(cwd, e.name), mode);
      toast('权限已修改', 'success');
      await load(cwd);
    } catch (err) {
      toast('修改失败：' + (err.message || err), 'error', 4200);
    }
  }

  function onFetch() {
    const urlInp = textInput('', null, { placeholder: 'https://example.com/file.tar.gz' });
    urlInp.style.width = '100%';
    const nameInp = textInput('', null, { placeholder: '留空使用 URL 文件名' });
    nameInp.style.width = '100%';
    const err = h('div', { class: 'login-error', style: { display: 'none' } });
    let m;
    const run = async () => {
      err.style.display = 'none';
      const u = urlInp.value.trim();
      if (!u) {
        err.textContent = '请输入下载地址';
        err.style.display = 'block';
        return;
      }
      try {
        const r = await api.sites.files.fetch(siteId, u, cwd, nameInp.value.trim());
        m.close();
        toast(`已下载到 ${r.path}（${formatSize(r.bytes)}）`, 'success', 4200);
        await load(cwd);
      } catch (e) {
        err.textContent = e.message || '下载失败';
        err.style.display = 'block';
      }
    };
    m = modal({
      title: '远程下载到当前目录',
      width: '560px',
      content: h('div', {}, [
        h('div', { class: 'fm-prompt-label' }, '下载地址（仅 http/https）'),
        urlInp,
        h('div', { class: 'fm-prompt-label' }, '保存文件名（可选）'),
        nameInp,
        h('div', { class: 'field-hint' }, '上限 100MB、60 秒；拒绝回环/链路本地/云元数据地址；目标文件已存在时不覆盖。'),
        err,
      ]),
      footer: h('div', { class: 'fm-editor-footer' }, [
        button('取消', () => m.close(), 'default', 'close'),
        button('开始下载', run, 'primary', 'download'),
      ]),
    });
    setTimeout(() => urlInp.focus(), 30);
  }

  function onSearch() {
    const inp = textInput('', null, { placeholder: '输入文件名关键字，回车搜索' });
    inp.style.width = '100%';
    const results = h('div', { class: 'fm-search-results mini-list' });
    let m;
    const run = async () => {
      const kw = inp.value.trim();
      if (!kw) return;
      clear(results).appendChild(loading('搜索中…'));
      try {
        const r = await api.sites.files.search(siteId, kw, 200);
        clear(results);
        const list = r.entries || [];
        if (list.length === 0) {
          results.appendChild(emptyState('没有匹配项', null, 'search'));
          return;
        }
        for (const e of list) {
          results.appendChild(h('div', { class: 'mini-item' }, [
            h('span', { class: 'fm-icon' }, e.type === 'dir' ? '📁' : '📄'),
            h('a', {
              href: '#',
              onclick: (ev) => {
                ev.preventDefault();
                m.close();
                if (e.type === 'dir') {
                  load(e.path);
                } else {
                  const dir = e.path.includes('/') ? e.path.slice(0, e.path.lastIndexOf('/')) : '';
                  load(dir).then(() => onOpen({ name: e.name, size: e.size, type: 'file' }));
                }
              },
            }, e.path),
            h('span', { class: 'mini-item-meta' }, e.type === 'dir' ? '目录' : formatSize(e.size)),
          ]));
        }
      } catch (err) {
        clear(results).appendChild(errorState(err.message || '搜索失败'));
      }
    };
    inp.addEventListener('keydown', (ev) => {
      if (ev.key === 'Enter') {
        ev.preventDefault();
        run();
      }
    });
    m = modal({
      title: '搜索文件',
      width: '680px',
      content: h('div', {}, [
        inp,
        h('div', { class: 'field-hint' }, '最多返回 200 条；跳过站点 logs 与 .well-known。'),
        results,
      ]),
      footer: [
        button('关闭', () => m.close(), 'default', 'close'),
        button('搜索', run, 'primary', 'search'),
      ],
    });
    setTimeout(() => inp.focus(), 30);
  }

  // 初始加载
  load(cwd);
}

// ---------- 简单 prompt 模态：返回 Promise<string|null> ----------
//
// 在 modal 工具里，footer 按钮 onclick 会触发 m.close()，close 内部又会调用
// onClose()。但 onClose 拿不到 input.value（因为它只是通知，不传值）。
// 用 capture 阶段监听 OK 按钮的 click：在 m.close() 之前先 resolve(value)，
// 这样用户"点确定"走我们的 resolve 路径，"点取消/overlay/ESC"走 onClose(null)。
function promptModal({ title, label, initial = '', placeholder = '' }) {
  return new Promise((resolve) => {
    const input = textInput(initial, () => {}, { placeholder });
    input.style.width = '100%';
    let resolved = false;
    const safeResolve = (v) => { if (!resolved) { resolved = true; resolve(v); } };

    const m = modal({
      title,
      content: h('div', {}, [
        h('div', { class: 'fm-prompt-label' }, label),
        input,
      ]),
      width: '440px',
      footer: h('div', { class: 'fm-editor-footer' }, [
        button('取消', () => m.close(), 'default', 'close'),
        button('确定', () => m.close(), 'primary', 'check'),
      ]),
      onClose: () => safeResolve(null),
    });
    // capture 阶段拦截 OK 按钮的 click，resolve(input.value) 先于 m.close()
    const ok = m.box.querySelector('.modal-foot .btn-primary');
    if (ok) {
      ok.addEventListener('click', () => safeResolve(input.value.trim() || null), true);
    }
    setTimeout(() => input.focus(), 30);
  });
}