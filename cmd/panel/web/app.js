/* nodepanel · 前端主体
   精简界面 + 中英双语（词典见 i18n.js）。原生 ES Module，无构建步骤。 */

import { drawChart, seriesOf, fmt, PALETTE } from './chart.js';
import { t, setLang, getLang } from './i18n.js';

const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
}[c]));
const isEN = () => getLang() === 'en';

/* ------------------------------------------------------------------ 基础 */

const state = { authed: false, overview: null, timers: [], timerKeys: {}, tab: 'monitor', range: 3600, cache: {} };

async function api(path, opt = {}) {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(opt.headers || {}) },
    ...opt,
    body: opt.body && typeof opt.body !== 'string' ? JSON.stringify(opt.body) : opt.body,
  });
  if (res.status === 401) {
    state.authed = false;
    renderLogin(t('登录已过期，请重新登录'));
    throw new Error(t('未登录'));
  }
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { raw: text }; }
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`);
  return data;
}

function toast(msg, kind = '') {
  const host = $('#toasts');
  if (!host) return;
  const el = document.createElement('div');
  el.className = 'toast ' + kind;
  el.textContent = msg;
  host.appendChild(el);
  setTimeout(() => { el.style.opacity = '0'; setTimeout(() => el.remove(), 250); }, kind === 'err' ? 6000 : 2600);
}

const badge = (text, kind = '') => `<span class="badge ${kind}">${esc(text)}</span>`;

function bar(pct, kind) {
  const v = Math.max(0, Math.min(100, Number(pct) || 0));
  const cls = kind || (v >= 90 ? 'bad' : v >= 75 ? 'warn' : 'ok');
  return `<div class="bar ${cls}"><i style="width:${v.toFixed(1)}%"></i></div>`;
}

function clearTimers() { state.timers.forEach(clearInterval); state.timers = []; state.timerKeys = {}; }

function setIntervalSafe(key, ms, fn) {
  clearTimer(key);
  const id = setInterval(fn, ms);
  state.timerKeys[key] = id;
  state.timers.push(id);
  return id;
}

function clearTimer(key) {
  if (!state.timerKeys[key]) return;
  clearInterval(state.timerKeys[key]);
  state.timers = state.timers.filter((x) => x !== state.timerKeys[key]);
  delete state.timerKeys[key];
}

/* 到期时间：0 = 永久 */
function expiryInfo(exp) {
  if (!exp) return { text: t('永久'), kind: '' };
  const days = Math.ceil((exp * 1000 - Date.now()) / 86400000);
  if (days < 0) return { text: t('已过期'), kind: 'bad' };
  if (days <= 30) return { text: `${t('剩余')} ${days}${isEN() ? 'd' : ' 天'}`, kind: days <= 7 ? 'bad' : 'warn' };
  const d = new Date(exp * 1000);
  const p = (n) => String(n).padStart(2, '0');
  return { text: `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`, kind: '' };
}

function fmtDateInput(exp) {
  if (!exp) return '';
  const d = new Date(exp * 1000);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

function modeLabel(mode) {
  return { tunnel: 'cloudflared', domain: isEN() ? 'domain' : '域名', ssh: 'SSH', direct: isEN() ? 'direct' : '直连' }[mode] || mode;
}

/* 物理地址一行 */
function netLine(v) {
  const st = (v.runtime || {}).stats;
  if (!st) return '';
  const h = st.host || {};
  const pub = h.public_ip || (v.runtime || {}).remote_ip || '';
  const items = [];
  if (h.hostname) items.push(`<span class="ni" title="${t('主机名')}">${esc(h.hostname)}</span>`);
  if (h.private_ip) items.push(`<span class="ni" title="${t('内网 IP')}">${esc(h.private_ip)}${h.iface ? `<span class="mute">/${esc(h.iface)}</span>` : ''}</span>`);
  if (pub) items.push(`<span class="ni" title="${t('公网 IP')}">${esc(pub)}</span>`);
  const mac = (h.macs || [])[0];
  if (mac) items.push(`<span class="ni" title="${t('网卡 MAC')}">${esc(mac)}</span>`);
  return items.length ? `<div class="net-line">${items.join('')}</div>` : '';
}

/* ------------------------------------------------------------------ 登录 */

function renderLogin(msg) {
  clearTimers();
  $('#root').innerHTML = `
  <div class="login-wrap"><form class="login-card" id="login-form">
    <h1>nodepanel</h1>
    <p>${t('多节点服务器管理面板 · 本机运行')}</p>
    ${msg ? `<div class="badge bad" style="margin-bottom:10px">${esc(msg)}</div>` : ''}
    <label class="field"><span>${t('管理密码')}</span>
      <input type="password" id="pw" autocomplete="current-password" placeholder="${t('首次运行请查看面板终端输出')}" autofocus>
    </label>
    <button class="btn primary" style="width:100%" type="submit">${t('登录')}</button>
    <p class="hint">${t('密码保存在 ~/.nodepanel/panel.json，修改：nodemgr-panel -set-password 新密码')}</p>
    <p style="margin:10px 0 0"><a href="#" id="lang-toggle">${isEN() ? '中文' : 'English'}</a></p>
  </form></div>`;
  $('#lang-toggle').addEventListener('click', (e) => {
    e.preventDefault();
    setLang(isEN() ? 'zh' : 'en');
    renderLogin(msg);
  });
  $('#login-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = $('#login-form button');
    btn.disabled = true;
    try {
      await api('/api/login', { method: 'POST', body: { password: $('#pw').value } });
      state.authed = true;
      renderShell();
      go(location.hash || '#/overview');
    } catch (err) { renderLogin(err.message); }
  });
}

/* ------------------------------------------------------------------ 外壳 */

const NAV = [
  { hash: '#/overview', label: '总览' },
  { hash: '#/nodes', label: '节点' },
  { hash: '#/todos', label: '待办' },
  { hash: '#/settings', label: '设置' },
];

function renderShell() {
  $('#root').innerHTML = `
  <div class="app">
    <aside class="sidebar">
      <div class="brand"><span class="dot"></span><div>nodepanel<small id="ver-line">v—</small></div></div>
      <nav class="nav">${NAV.map(n => `<a href="${n.hash}" data-nav="${n.hash}">${t(n.label)}</a>`).join('')}</nav>
      <div class="sidebar-foot">
        <div id="side-sum" class="mute"></div>
        <div class="foot-row">
          <a href="#" id="lang-btn">${isEN() ? '中文' : 'English'}</a>
          <a href="#" id="logout">${t('退出登录')}</a>
        </div>
      </div>
    </aside>
    <main class="main" id="view"></main>
  </div>`;
  $('#logout').addEventListener('click', async (e) => {
    e.preventDefault();
    await api('/api/logout', { method: 'POST' });
    renderLogin();
  });
  $('#lang-btn').addEventListener('click', (e) => {
    e.preventDefault();
    setLang(isEN() ? 'zh' : 'en');
    renderShell();
    route();
  });
}

const setActiveNav = (hash) => $$('[data-nav]').forEach(a => a.classList.toggle('active', a.dataset.nav === hash));

/* ------------------------------------------------------------------ 路由 */

let suppressRoute = false;

function go(hash) {
  if (location.hash !== hash) { suppressRoute = true; location.hash = hash; }
  route();
}

function route() {
  if (!state.authed) return;
  clearTimers();
  const parts = (location.hash || '#/overview').replace(/^#\/?/, '').split('/');
  const name = parts[0] || 'overview';
  setActiveNav(`#/${name}`);
  if (name === 'node' && parts[1]) return viewNode(parts[1]);
  if (name === 'nodes') return viewNodes();
  if (name === 'todos') return viewTodos();
  if (name === 'settings') return viewSettings();
  return viewOverview();
}

/* ------------------------------------------------------------------ 概览 */

function statusBadge(rt) {
  if (!rt || !rt.checked_at) return badge(t('等待采集'), 'warn');
  return rt.online ? badge(t('在线'), 'ok')
    : `<span class="badge bad" title="${esc(rt.last_error || '')}">${t('离线')}</span>`;
}

function nodeCard(v) {
  const n = v.node, rt = v.runtime || {}, st = rt.stats;
  const cpu = v._cpu || 0;
  const memP = st && st.mem.total ? st.mem.used / st.mem.total * 100 : 0;
  const disk = st && st.disks && st.disks.length ? Math.max(...st.disks.map(d => d.used_pct)) : 0;
  const exp = expiryInfo(n.expires_at);
  const addr = `${n.host}${n.port && n.mode !== 'tunnel' && n.mode !== 'ssh' ? ':' + n.port : ''}`;
  return `
  <div class="card node-card" data-node="${n.id}">
    <div class="top">
      <div style="flex:1;min-width:0">
        <div class="name">${esc(n.name)}</div>
        <div class="addr">${esc(addr)} <span class="mute">· ${modeLabel(n.mode)}</span></div>
      </div>
      <div style="text-align:right">
        ${statusBadge(rt)}
        <div class="mute xs">${rt.latency_ms ? rt.latency_ms + ' ms' : ''}</div>
      </div>
    </div>
    <div class="mini-bars">
      <div class="mini"><span class="k">CPU</span>${bar(cpu)}<span class="v">${fmt.pct(cpu, 0)}</span></div>
      <div class="mini"><span class="k">${t('内存')}</span>${bar(memP)}<span class="v">${fmt.pct(memP, 0)}</span></div>
      <div class="mini"><span class="k">${t('磁盘')}</span>${bar(disk)}<span class="v">${fmt.pct(disk, 0)}</span></div>
    </div>
    <div class="row meta">
      <span>↓${fmt.rate(v._rx || 0)} ↑${fmt.rate(v._tx || 0)}</span>
      <span>Σ ${st ? st.totals.rx_h : '-'} / ${st ? st.totals.tx_h : '-'}</span>
      <span>${t('访问')} ${fmt.int(rt.requests || 0)}</span>
      <span class="exp ${exp.kind}">${t('到期')} ${exp.text}</span>
    </div>
    ${netLine(v)}
    ${n.note ? `<div class="note">${esc(n.note)}</div>` : ''}
    ${rt.online ? '' : `<div class="mute err-line">${esc(rt.last_error || '')}</div>`}
  </div>`;
}

async function viewOverview() {
  $('#view').innerHTML = `
    <div class="page-head"><h2>${t('总览')}</h2><span class="sub">${t('全部节点资源与访问量')}</span>
      <div class="spacer"></div><button class="btn primary" id="add-node">+ ${t('添加节点')}</button></div>
    <div id="ov-body"><div class="empty"><span class="spin"></span> ${t('加载中…')}</div></div>`;
  $('#add-node').addEventListener('click', () => nodeDialog(null));

  const load = async () => {
    let data;
    try { data = await api('/api/overview'); } catch { return; }
    state.overview = data;
    const s = data.summary;
    $('#ver-line').textContent = 'v' + data.version;
    $('#side-sum').textContent = `${s.online}/${s.nodes} ${t('台在线')} · ${s.todos_open} ${t('待办')}`;
    await Promise.all(data.nodes.map(async (v) => {
      try {
        const m = await api(`/api/nodes/${v.node.id}/metrics?span=300&max=1`);
        const last = (m.samples || [])[m.samples.length - 1];
        if (last) { v._cpu = last.cpu; v._rx = last.rx; v._tx = last.tx; }
      } catch { /* 离线节点忽略 */ }
    }));
    renderOverview(data);
  };
  renderOverview(state.overview || { summary: {}, nodes: [], alerts: [], todos: [] });
  await load();
  setIntervalSafe('overview', 5000, load);
}

function renderOverview(data) {
  const body = $('#ov-body');
  if (!body) return;
  const s = data.summary || {};
  const alerts = (data.alerts || []).map(a =>
    `<div class="alert">⚠ ${esc(a.node)} · ${esc(a.kind)} ${fmt.pct(a.value)}</div>`).join('');
  body.innerHTML = `
    <div class="grid cols-4">
      <div class="stat"><div class="label">${t('节点')}</div>
        <div class="value">${s.online || 0}<small>/ ${s.nodes || 0}</small></div>
        <div class="foot">${s.offline ? s.offline + ' ' + t('个离线') : t('全部在线')}</div></div>
      <div class="stat"><div class="label">${t('累计流量')}</div>
        <div class="value">${esc(s.rx_total_h || '0 B')}<small>↓</small></div>
        <div class="foot">↑ ${esc(s.tx_total_h || '0 B')}</div></div>
      <div class="stat"><div class="label">${t('今日访问')}</div>
        <div class="value">${fmt.int(s.requests_today || 0)}</div>
        <div class="foot">${t('访问量')} ${fmt.int(s.requests || 0)}</div></div>
      <div class="stat"><div class="label">${t('平均负载')}</div>
        <div class="value">${fmt.pct(s.cpu_avg || 0, 0)}<small>CPU</small></div>
        <div class="foot">${t('内存')} ${fmt.pct(s.mem_avg || 0, 0)}</div></div>
    </div>
    ${alerts}
    <div class="grid cols-3" id="node-grid">
      ${(data.nodes || []).map(nodeCard).join('') || `<div class="card empty">${t('还没有节点。点右上角「添加节点」，或在服务器上安装 nodemgr-agent 后填入地址与令牌。')}</div>`}
    </div>
    <div class="card">
      <div class="card-head">${t('待办事项')}<div class="spacer"></div><a href="#/todos">${t('全部')} →</a></div>
      <div id="ov-todos">${todoList((data.todos || []).slice(0, 5))}</div>
    </div>`;
  $$('#node-grid .node-card').forEach(c => c.addEventListener('click', () => go(`#/node/${c.dataset.node}`)));
  bindTodoList($('#ov-todos'));
}

/* ------------------------------------------------------------------ 节点列表 */

async function viewNodes() {
  $('#view').innerHTML = `
    <div class="page-head"><h2>${t('节点')}</h2><span class="sub">${t('服务器清单')}</span>
      <div class="spacer"></div><button class="btn primary" id="add-node">+ ${t('添加节点')}</button></div>
    <div class="card table-card" id="nodes-body"><div class="empty"><span class="spin"></span> ${t('加载中…')}</div></div>`;
  $('#add-node').addEventListener('click', () => nodeDialog(null));

  const load = async () => {
    let data;
    try { data = await api('/api/nodes'); } catch { return; }
    const rows = data.nodes.map((v) => {
      const n = v.node, rt = v.runtime || {}, st = rt.stats;
      const h = st ? (st.host || {}) : {};
      const exp = expiryInfo(n.expires_at);
      const phys = [h.private_ip || '', h.public_ip || rt.remote_ip || '', (h.macs || [])[0] || ''].filter(Boolean).join(' · ');
      const addr = `${n.host}${n.port && n.mode !== 'tunnel' && n.mode !== 'ssh' ? ':' + n.port : ''}`;
      return `<tr data-node="${n.id}">
        <td><b>${esc(n.name)}</b><div class="mute mono xs">${esc(addr)} · ${modeLabel(n.mode)}</div>
          ${phys ? `<div class="mute mono xs">${esc(phys)}</div>` : ''}</td>
        <td>${statusBadge(rt)}</td>
        <td class="mono">${rt.latency_ms ? rt.latency_ms + ' ms' : '-'}</td>
        <td class="mono">${st ? st.host.cpu_cores + (isEN() ? 'c' : ' 核') : '-'}</td>
        <td class="mono">${st ? fmt.bytes(st.mem.total, 0) : '-'}</td>
        <td class="mono">${st ? st.totals.rx_h : '-'}</td>
        <td class="mono">${st ? st.totals.tx_h : '-'}</td>
        <td class="mono">${fmt.int(rt.requests || 0)}</td>
        <td><span class="badge ${exp.kind}">${exp.text}</span></td>
        <td>${n.group ? badge(n.group, 'brand') : '<span class="mute">-</span>'}</td>
        <td class="right nowrap">
          <button class="btn small" data-act="edit">${t('编辑')}</button>
          <button class="btn small" data-act="term">${t('终端')}</button>
        </td></tr>`;
    }).join('');
    const body = $('#nodes-body');
    if (!body) return;
    body.innerHTML = `<table class="tbl">
      <thead><tr><th>${t('服务器')}</th><th>${t('状态')}</th><th>${t('延迟')}</th><th>CPU</th><th>${t('内存')}</th>
        <th>${t('累计下行')}</th><th>${t('累计上行')}</th><th>${t('访问量')}</th><th>${t('到期时间')}</th><th>${t('分组')}</th><th></th></tr></thead>
      <tbody>${rows || `<tr><td colspan="11" class="empty">${t('暂无数据')}</td></tr>`}</tbody></table>`;
    $$('#nodes-body tr[data-node]').forEach(tr => {
      tr.addEventListener('click', (e) => {
        const act = e.target.dataset.act;
        const item = data.nodes.find(x => x.node.id === tr.dataset.node);
        if (act === 'edit') { e.stopPropagation(); nodeDialog(item); return; }
        if (act === 'term') { e.stopPropagation(); state.tab = 'terminal'; go(`#/node/${tr.dataset.node}`); return; }
        state.tab = state.tab || 'monitor';
        go(`#/node/${tr.dataset.node}`);
      });
    });
  };
  await load();
  setIntervalSafe('nodes', 5000, load);
}

/* ------------------------------------------------------------------ 节点详情 */

const TABS = [
  { id: 'monitor', label: '监控' },
  { id: 'service', label: '服务' },
  { id: 'process', label: '进程' },
  { id: 'terminal', label: '终端' },
  { id: 'files', label: '文件/日志' },
  { id: 'note', label: '备注/任务' },
];

async function viewNode(id) {
  const view = $('#view');
  let data;
  try { data = await api('/api/nodes/' + id); }
  catch { view.innerHTML = `<div class="empty">${t('节点不存在')}</div>`; return; }
  const n = data.node, rt = data.runtime || {}, st = rt.stats;
  const tab = state.tab || 'monitor';
  const exp = expiryInfo(n.expires_at);
  const addr = `${n.host}${n.port && n.mode !== 'tunnel' && n.mode !== 'ssh' ? ':' + n.port : ''}`;
  const phys = st ? [st.host.hostname, st.host.private_ip, st.host.public_ip || rt.remote_ip, (st.host.macs || [])[0]]
    .filter(Boolean).join(' · ') : '';
  view.innerHTML = `
  <div class="page-head">
    <a href="#/nodes" class="btn small">←</a>
    <h2>${esc(n.name)}</h2>
    <span id="node-status">${statusBadge(rt)}</span>
    <span class="sub mono">${esc(addr)} · ${modeLabel(n.mode)}</span>
    <span class="badge ${exp.kind}">${t('到期')} ${exp.text}</span>
    <div class="spacer"></div>
    <button class="btn small" id="edit-node">${t('编辑')}</button>
  </div>
  ${phys ? `<div class="page-sub mono">${esc(phys)}</div>` : ''}
  <div class="tabs">${TABS.map(x => `<button data-tab="${x.id}" class="${x.id === tab ? 'active' : ''}">${t(x.label)}</button>`).join('')}</div>
  <div id="tab-body"></div>`;
  $$('[data-tab]').forEach(b => b.addEventListener('click', () => {
    state.tab = b.dataset.tab;
    $$('[data-tab]').forEach(x => x.classList.toggle('active', x === b));
    ['monitor', 'service', 'process', 'fit'].forEach(clearTimer);
    renderTab(b.dataset.tab, { id, n, rt, st, data });
  }));
  $('#edit-node').addEventListener('click', () => nodeDialog({ node: n, runtime: rt, series: data.series }));
  renderTab(tab, { id, n, rt, st, data });

  if (tab !== 'terminal') {
    setIntervalSafe('head', 6000, async () => {
      try {
        const d = await api('/api/nodes/' + id);
        const box = $('#node-status');
        if (box) box.innerHTML = statusBadge(d.runtime);
      } catch { /* ignore */ }
    });
  }
}

function renderTab(tab, ctx) {
  const body = $('#tab-body');
  if (!body) return;
  const fn = { monitor: tabMonitor, service: tabService, process: tabProcess, terminal: tabTerminal, files: tabFiles, note: tabNote }[tab] || tabMonitor;
  fn(body, ctx);
}

/* ---------- 监控 ---------- */

async function tabMonitor(body, ctx) {
  const { id, n } = ctx;
  const rt = await latestRuntime(id, ctx);
  const st = rt.stats;
  body.innerHTML = `
  <div class="grid cols-4">
    <div class="stat"><div class="label">${t('CPU 使用率')}</div><div class="value" id="m-cpu">-</div><div class="foot" id="m-load">-</div></div>
    <div class="stat"><div class="label">${t('内存')}</div><div class="value" id="m-mem">-</div><div class="foot" id="m-mem2">-</div></div>
    <div class="stat"><div class="label">${t('磁盘 I/O')}</div><div class="value" id="m-io">-</div><div class="foot" id="m-io2">-</div></div>
    <div class="stat"><div class="label">${t('网络')}</div><div class="value" id="m-net">-</div><div class="foot" id="m-net2">-</div></div>
  </div>
  <div class="card">
    <div class="card-head">${t('CPU / 内存 使用率')}
      <div class="spacer"></div>
      <div class="seg">${[[3600, '1 小时'], [86400, '24 小时'], [604800, '7 天']].map(([v, l]) =>
        `<button data-range="${v}" class="${v === state.range ? 'on' : ''}">${t(l)}</button>`).join('')}</div>
    </div>
    <div class="chart-box" id="chart-cpu"></div>
  </div>
  <div class="grid cols-2">
    <div class="card"><div class="card-head">${t('网络吞吐')}</div><div class="chart-box" id="chart-net"></div></div>
    <div class="card"><div class="card-head">${t('磁盘 I/O')}</div><div class="chart-box" id="chart-io"></div></div>
  </div>
  <div class="grid cols-2">
    <div class="card"><div class="card-head">${t('系统信息')}</div><div class="kv" id="m-info"></div></div>
    <div class="card"><div class="card-head">${t('磁盘挂载')}</div><div id="m-disks" class="scroll-x"></div></div>
  </div>
  <div class="card"><div class="card-head">${t('流量统计')}</div><div class="kv" id="m-traffic"></div></div>`;

  body.querySelectorAll('[data-range]').forEach(b => b.addEventListener('click', () => {
    state.range = Number(b.dataset.range);
    body.querySelectorAll('[data-range]').forEach(x => x.classList.toggle('on', x === b));
    loadMetrics();
  }));

  async function loadMetrics() {
    if (!document.getElementById('chart-cpu')) { clearTimer('monitor'); return; }
    const fresh = await latestRuntime(id, ctx);
    paintMonitor({ node: n, runtime: fresh }, state.cache.lastSample);
    let m;
    try { m = await api(`/api/nodes/${id}/metrics?span=${state.range}&max=420`); }
    catch (e) { $('#chart-cpu').innerHTML = `<div class="empty">${t('读取历史失败：{0}', e.message)}</div>`; return; }
    const samples = m.samples || [];
    if (!samples.length) { $('#chart-cpu').innerHTML = `<div class="empty">${t('还没有历史数据，等待采集…')}</div>`; return; }
    const wide = state.range > 86400;
    const fx = (v) => fmt.time(v, wide);
    drawChart($('#chart-cpu'), {
      height: 150, yMax: 100, formatY: (v) => v.toFixed(0) + '%', formatX: fx,
      series: [
        { name: 'CPU', color: PALETTE.blue, points: seriesOf(samples, 'cpu'), format: (v) => fmt.pct(v) },
        { name: t('内存'), color: PALETTE.purple, points: seriesOf(samples, 'mem_p'), format: (v) => fmt.pct(v) },
        { name: 'IOwait', color: PALETTE.amber, points: seriesOf(samples, 'iow'), format: (v) => fmt.pct(v) },
      ],
    });
    drawChart($('#chart-net'), {
      height: 140, sharedScale: false, formatY: (v) => fmt.bytes(v, 0), formatX: fx,
      series: [
        { name: t('下行'), color: PALETTE.cyan, points: seriesOf(samples, 'rx'), format: fmt.rate },
        { name: t('上行'), color: PALETTE.green, points: seriesOf(samples, 'tx'), format: fmt.rate },
      ],
    });
    drawChart($('#chart-io'), {
      height: 140, sharedScale: false, formatY: (v) => fmt.bytes(v, 0), formatX: fx,
      series: [
        { name: t('读'), color: PALETTE.blue, points: seriesOf(samples, 'dr'), format: fmt.rate },
        { name: t('写'), color: PALETTE.amber, points: seriesOf(samples, 'dw'), format: fmt.rate },
      ],
    });
    paintMonitor({ node: n, runtime: fresh }, samples[samples.length - 1]);
  }
  await loadMetrics();
  setIntervalSafe('monitor', 6000, loadMetrics);
}

function paintMonitor(d, last) {
  const rt = d.runtime || {}, st = rt.stats;
  if (!st) return;
  if (last) state.cache.lastSample = last;
  const s = last || state.cache.lastSample || {};
  const put = (sel, v) => { const e = $(sel); if (e) e.textContent = v; };
  put('#m-cpu', fmt.pct(s.cpu || 0));
  put('#m-load', `${t('负载')} ${fmt.num(s.l1 || 0)} / ${fmt.num(s.l5 || 0)} / ${fmt.num(s.l15 || 0)}`);
  if (st) {
    put('#m-mem', fmt.bytes(st.mem.used, 1));
    const mp = st.mem.total ? st.mem.used / st.mem.total * 100 : 0;
    put('#m-mem2', `${fmt.bytes(st.mem.total, 1)} · ${fmt.pct(mp)}`);
  }
  put('#m-io', fmt.rate(s.dr || 0));
  put('#m-io2', `${t('写')} ${fmt.rate(s.dw || 0)} · ${fmt.pct(s.dio || 0)}`);
  put('#m-net', `↓${fmt.rate(s.rx || 0)}`);
  put('#m-net2', `↑${fmt.rate(s.tx || 0)} · TCP ${s.tcp_est || 0}`);

  const info = $('#m-info');
  if (info && st) {
    const h = st.host;
    info.innerHTML = `
      <div class="k">${t('主机名')}</div><div>${esc(h.hostname)}</div>
      <div class="k">${t('系统')}</div><div>${esc(h.os)}</div>
      <div class="k">${t('内核')}</div><div class="mono">${esc(h.kernel)}</div>
      <div class="k">CPU</div><div>${esc(h.cpu_model)} · ${h.cpu_cores}${isEN() ? ' cores' : ' 核'}</div>
      <div class="k">${t('内网 IP')}</div><div class="mono">${esc(h.private_ip || '-')}${h.iface ? ` <span class="mute">(${esc(h.iface)})</span>` : ''}</div>
      <div class="k">${t('公网 IP')}</div><div class="mono">${esc(h.public_ip || '-')}</div>
      <div class="k">${t('面板观测 IP')}</div><div class="mono">${esc(rt.remote_ip || '-')}</div>
      <div class="k">${t('网卡 MAC')}</div><div class="mono">${esc((h.macs || []).join(' ') || '-')}</div>
      <div class="k">${t('虚拟化')}</div><div>${esc(h.virt || '-')}</div>
      <div class="k">${t('运行时长')}</div><div>${fmt.dur(st.uptime)}</div>
      <div class="k">${t('进程 / 线程')}</div><div>${s.procs || '-'} / ${s.run || '-'}</div>
      <div class="k">${t('TCP 连接')}</div><div>${t('建立')} ${st.tcp.established} · ${t('监听')} ${st.tcp.listen} · WAIT ${st.tcp.time_wait}</div>
      <div class="k">Agent</div><div>v${esc(st.version)} · ${t('采样')} ${st.interval}s</div>`;
  }
  const disks = $('#m-disks');
  if (disks && st) {
    disks.innerHTML = `<table class="tbl"><thead><tr><th>${t('挂载点')}</th><th>${t('设备')}</th><th>${t('使用率')}</th><th class="right nowrap">${t('已用 / 总量')}</th></tr></thead><tbody>
      ${st.disks.map(x => `<tr>
        <td class="mono">${esc(x.mount)}<div class="mute xs">${esc(x.fstype)}</div></td>
        <td class="mono mute ellip">${esc(x.device)}</td>
        <td>${bar(x.used_pct)}<span class="mute nowrap xs">${fmt.pct(x.used_pct)}</span></td>
        <td class="right mono nowrap">${fmt.bytes(x.used, 1)}<span class="mute"> / ${fmt.bytes(x.total, 1)}</span></td></tr>`).join('')}
      </tbody></table>`;
  }
  const tr = $('#m-traffic');
  if (tr && st) {
    const days = Object.entries(rt.days || {}).sort().slice(-7);
    tr.innerHTML = `
      <div class="k">${t('Agent 累计下行')}</div><div class="mono">${esc(st.totals.rx_h)}</div>
      <div class="k">${t('Agent 累计上行')}</div><div class="mono">${esc(st.totals.tx_h)}</div>
      <div class="k">${t('面板统计下行')}</div><div class="mono">${fmt.bytes(rt.rx_total || 0, 2)}</div>
      <div class="k">${t('面板统计上行')}</div><div class="mono">${fmt.bytes(rt.tx_total || 0, 2)}</div>
      <div class="k">${t('今日请求')}</div><div class="mono">${fmt.int(rt.requests_today || 0)}</div>
      <div class="k">${t('近 7 天')}</div><div>${days.length ? days.map(([d, v]) =>
        `<div class="day-row"><span class="mono">${esc(d)}</span><span>${fmt.int(v.requests || 0)}</span>
         <span class="mute">↓${fmt.bytes(v.rx || 0, 1)} ↑${fmt.bytes(v.tx || 0, 1)}</span></div>`).join('') : `<span class="mute">${t('暂无')}</span>`}</div>`;
  }
}

/* ---------- 服务 / 访问量 ---------- */

async function tabService(body, ctx) {
  const { id, n } = ctx;
  // 详情页首屏可能还没采集到数据，这里取一次最新状态并使用它。
  const rt = await latestRuntime(id, ctx);
  body.innerHTML = `
  <div class="grid cols-4">
    <div class="stat"><div class="label">${t('累计访问量')}</div><div class="value">${fmt.int(rt.requests || 0)}</div>
      <div class="foot">${t('今日')} ${fmt.int(rt.requests_today || 0)}</div></div>
    <div class="stat"><div class="label">${t('监听端口')}</div><div class="value" id="s-ports">-</div><div class="foot">TCP</div></div>
    <div class="stat"><div class="label">${t('HTTP 探测')}</div><div class="value" id="s-probe">-</div><div class="foot">127.0.0.1</div></div>
    <div class="stat"><div class="label">${t('TCP 连接')}</div><div class="value" id="s-tcp">-</div><div class="foot">established</div></div>
  </div>
  <div class="card table-card"><div class="card-head">${t('监听端口与服务')}</div><div id="s-porttable"><div class="empty"><span class="spin"></span></div></div></div>
  <div class="grid cols-2">
    <div class="card"><div class="card-head">${t('近 14 天访问量')}</div><div class="chart-box" id="chart-req"></div></div>
    <div class="card"><div class="card-head">${t('近 14 天流量')}</div><div class="chart-box" id="chart-day"></div></div>
  </div>
  <div class="card">
    <div class="card-head">${t('面板侧连通性探测')}</div>
    <div class="row" style="align-items:flex-end">
      <label class="field" style="flex:3;margin:0"><span>${t('探测 URL（默认节点地址）')}</span>
        <input type="text" id="probe-url" value="${esc(nodeWebURL(n))}"></label>
      <button class="btn primary" id="probe-btn" style="flex:0 0 auto">${t('探测')}</button>
    </div>
    <div id="probe-out" class="mute sm" style="margin-top:6px"></div>
  </div>`;
  $('#probe-btn').addEventListener('click', async () => {
    $('#probe-out').innerHTML = `<span class="spin"></span> ${t('探测中…')}`;
    try {
      const r = await api('/api/probe?url=' + encodeURIComponent($('#probe-url').value));
      $('#probe-out').innerHTML = r.error
        ? `<span class="badge bad">${t('失败')}</span> ${esc(r.error)}`
        : `<span class="badge ${r.code < 400 ? 'ok' : 'warn'}">HTTP ${r.code}</span> ${r.ms} ms`;
    } catch (e) { $('#probe-out').innerHTML = `<span class="badge bad">${t('失败')}</span> ${esc(e.message)}`; }
  });
  await updateService(id, rt);
  loadDayCharts({ rt });
  setIntervalSafe('service', 15000, async () => {
    if (!document.getElementById('s-porttable')) { clearTimer('service'); return; }
    updateService(id, await latestRuntime(id, ctx));
  });
}

function nodeWebURL(n) {
  const scheme = n.scheme || 'http';
  if (n.mode === 'tunnel' || n.mode === 'ssh' || !n.port || n.port === 80 || n.port === 443) return `${scheme}://${n.host}`;
  return `${scheme}://${n.host}:${n.port}`;
}

// latestRuntime 返回节点最新的运行状态（首屏快照为空时会去取一次）。
async function latestRuntime(id, ctx) {
  let rt = (ctx && ctx.rt) || {};
  if (!rt.stats) {
    try {
      const fresh = await api('/api/nodes/' + id);
      if (fresh && fresh.runtime) rt = fresh.runtime;
    } catch { /* 用已有数据 */ }
  }
  if (ctx) { ctx.rt = rt; ctx.st = rt.stats; }
  return rt;
}

async function updateService(id, rt) {
  rt = rt || {};
  let st = rt.stats;
  const put = (sel, v) => { const e = $(sel); if (e) e.textContent = v; };
  put('#s-tcp', st ? st.tcp.established : '-');
  const table = $('#s-porttable');
  if (!table) return;
  const read = async () => {
    const r = await api(`/api/nodes/${id}/ports`);
    return [(r && r.ports) || [], (r && r.probes) || []];
  };
  let list = [], probes = [], err = '';
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      [list, probes] = await read();
      if (list.length) break;
    } catch (e) { err = e.message; }
    // agent 的端口缓存可能正在重建（首次扫描较慢），重试几次
    await new Promise((res) => setTimeout(res, 2000));
    if (!document.getElementById('s-porttable')) return;
  }
  if (!list.length && err) {
    table.innerHTML = `<div class="empty">${t('读取失败')}：${esc(err)}</div>`;
    return;
  }
  put('#s-ports', list.length);
  put('#s-probe', probes.length ? `${probes.filter(p => p.ok).length}/${probes.length}` : '-');
  table.innerHTML = list.length ? `<table class="tbl">
    <thead><tr><th>${t('端口')}</th><th>${isEN() ? 'Proto' : '协议'}</th><th>${t('服务')}</th><th>${t('进程')}</th>
      <th class="right">${t('建立')}</th><th class="right">TIME_WAIT</th></tr></thead>
    <tbody>${list.map(p => `<tr>
      <td class="mono"><b>${p.port}</b></td><td class="mute">${esc(p.proto)}</td>
      <td>${esc(p.service || '-')}</td><td class="mono mute ellip">${esc(p.process || '-')}</td>
      <td class="right mono">${p.established}</td><td class="right mono mute">${p.time_wait}</td></tr>`).join('')}
    </tbody></table>
    ${probes.length ? `<div class="row" style="margin-top:10px">${probes.map(p =>
      `<span class="badge ${p.ok ? 'ok' : 'bad'}">:${p.port} → ${p.ok ? 'HTTP ' + p.code : t('失败')} ${p.ms}ms</span>`).join('')}</div>` : ''}`
    : `<div class="empty">${t('没有检测到监听端口（可能需要 root 权限读取 /proc/net/tcp）')}</div>`;
}

function loadDayCharts(ctx) {
  const days = Object.entries(ctx.rt.days || {}).sort().slice(-14);
  if (!days.length) {
    $('#chart-req').innerHTML = `<div class="empty">${t('还没有历史统计，面板运行一段时间后这里会显示每天的数据')}</div>`;
    $('#chart-day').innerHTML = '';
    return;
  }
  const pts = (key) => days.map(([d, v]) => ({ t: new Date(d + 'T12:00:00').getTime(), v: Number(v[key]) || 0 }));
  const fx = (v) => fmt.time(v, true);
  drawChart($('#chart-req'), {
    height: 130, formatY: (v) => fmt.int(v), formatX: fx,
    series: [{ name: t('访问量'), color: PALETTE.blue, points: pts('requests'), format: (v) => fmt.int(v) }],
  });
  drawChart($('#chart-day'), {
    height: 130, sharedScale: false, formatY: (v) => fmt.bytes(v, 0), formatX: fx,
    series: [
      { name: t('下行'), color: PALETTE.cyan, points: pts('rx'), format: fmt.bytes },
      { name: t('上行'), color: PALETTE.green, points: pts('tx'), format: fmt.bytes },
    ],
  });
}

/* ---------- 进程 ---------- */

async function tabProcess(body, ctx) {
  const { id } = ctx;
  body.innerHTML = `<div class="card table-card"><div class="empty"><span class="spin"></span> ${t('加载中…')}</div></div>`;
  const load = async () => {
    let data;
    try { data = await api(`/api/nodes/${id}/processes?n=40`); }
    catch (e) {
      if (document.contains(body)) body.innerHTML = `<div class="card empty">${t('读取失败')}：${esc(e.message)}</div>`;
      return;
    }
    if (!document.contains(body)) return;
    const ps = (data.processes || []).slice().sort((a, b) => b.cpu - a.cpu);
    body.innerHTML = `<div class="card table-card"><table class="tbl">
      <thead><tr><th>PID</th><th>${t('名称')}</th><th>${t('状态')}</th><th class="right">CPU</th><th class="right">${t('内存')}</th>
        <th class="right">${t('线程')}</th><th>${t('命令')}</th></tr></thead>
      <tbody>${ps.map(p => `<tr>
        <td class="mono">${p.pid}</td><td><b>${esc(p.name)}</b></td><td class="mute">${esc(p.state)}</td>
        <td class="right mono">${fmt.pct(p.cpu)}</td>
        <td class="right mono">${fmt.bytes(p.mem_rss, 1)} <span class="mute">${fmt.pct(p.mem_pct, 0)}</span></td>
        <td class="right mono mute">${p.threads}</td>
        <td class="mono mute ellip" title="${esc(p.cmdline)}">${esc(p.cmdline)}</td></tr>`).join('')}
      </tbody></table></div><p class="mute sm">${t('CPU 为该进程在两次采样间的平均占用（按 100% = 单核计）。')}</p>`;
  };
  await load();
  setIntervalSafe('process', 5000, () => {
    if (!document.getElementById('tab-body')) { clearTimer('process'); return; }
    load();
  });
}

/* ---------- SSH 终端 ---------- */

function tabTerminal(body, ctx) {
  const { n } = ctx;
  const saved = !!n.ssh_pass;
  body.innerHTML = `
  <div class="term-head">
    <span class="badge brand">${esc((n.ssh_user || 'root') + '@' + n.host)}:${n.ssh_port || 22}</span>
    <span class="mute sm">${n.mode === 'ssh' ? t('通过 SSH 隧道') : n.use_relay ? t('经 Agent 中继（适合内网穿透节点）') : t('面板直连')}</span>
    <div class="spacer"></div>
    <span id="term-state" class="badge warn">${t('未连接')}</span>
    <button class="btn small" id="term-clear">${t('清屏')}</button>
  </div>
  <div class="card" id="term-auth">
    <div class="row" style="align-items:flex-end">
      <label class="field" style="flex:2;margin:0"><span>${t('SSH 密码（{0}@{1}）', n.ssh_user || 'root', n.host)}</span>
        <input type="password" id="ssh-pw" value="${esc(n.ssh_pass || '')}" placeholder="${t('留空则使用已保存的密码')}" autocomplete="off"></label>
      <button class="btn small" id="pw-eye" style="flex:0 0 auto;margin-bottom:2px">${t('显示')}</button>
      <button class="btn small" id="pw-copy" style="flex:0 0 auto;margin-bottom:2px">${t('复制')}</button>
      <label class="row" style="flex:0 0 auto;align-items:center;gap:6px;font-size:12.5px;margin:0 0 6px">
        <input type="checkbox" id="remember-pw" ${saved ? 'checked' : ''} style="width:auto"> ${t('记住密码（仅本机保存）')}
      </label>
      <button class="btn primary" id="term-go" style="flex:0 0 auto;margin-bottom:2px">${t('连接终端')}</button>
    </div>
    <div class="mute sm">${t('密码只用于本次 SSH 认证，不经 URL 传递')}${saved ? t('；勾选「记住密码」后保存在本机面板数据目录。') : t('。')}</div>
  </div>
  <div class="term-wrap"><div id="terminal"></div></div>`;

  let term = null, fit = null, ws = null;
  const setState = (text, kind) => { const e = $('#term-state'); if (e) { e.className = 'badge ' + kind; e.textContent = text; } };
  const remember = () => $('#remember-pw') && $('#remember-pw').checked;
  const savePassword = async (pass) => {
    try { await api('/api/nodes/' + n.id, { method: 'PATCH', body: { ssh_pass: pass, remember: true } }); } catch { /* ignore */ }
  };

  const connect = (password) => {
    const host = $('#terminal');
    if (!host) return;
    if (term && term.dispose) { try { term.dispose(); } catch { /* ignore */ } }
    if (ws && ws.readyState <= 1) { try { ws.close(); } catch { /* ignore */ } }
    if (!window.Terminal) { host.innerHTML = '<div class="empty">xterm.js missing</div>'; return; }

    term = new window.Terminal({
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 13, scrollback: 4000, cursorBlink: true, allowProposedApi: true,
      theme: { background: '#000', foreground: '#e7eaf0', cursor: '#4c8dff', selectionBackground: '#29405f' },
    });
    fit = new window.FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(host);
    try { fit.fit(); } catch { /* ignore */ }

    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(`${proto}://${location.host}/api/nodes/${n.id}/ssh`);
    ws.binaryType = 'arraybuffer';
    setState(t('连接中…'), 'warn');
    ws.onopen = () => {
      try { fit.fit(); } catch { /* ignore */ }
      ws.send(JSON.stringify({ type: 'auth', payload: password || '', cols: term.cols, rows: term.rows }));
      setState(t('已连接'), 'ok');
      term.focus();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        let msg; try { msg = JSON.parse(ev.data); } catch { return; }
        if (msg.type === 'error') { term.write(`\r\n\x1b[31m${msg.payload}\x1b[0m\r\n`); setState(t('连接失败'), 'bad'); }
        else if (msg.type === 'exit') { term.write(`\r\n\x1b[33m[session closed]\x1b[0m\r\n`); setState(t('已断开'), 'warn'); }
      } else { term.write(new Uint8Array(ev.data)); }
    };
    ws.onclose = () => setState(t('已断开'), 'warn');
    ws.onerror = () => setState(t('连接错误'), 'bad');
    term.onData((d) => { if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'input', payload: d })); });
    term.onResize(({ cols, rows }) => { if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', cols, rows })); });
    setIntervalSafe('fit', 4000, () => {
      if (!document.getElementById('terminal')) { clearTimer('fit'); return; }
      try { fit.fit(); } catch { /* ignore */ }
    });
  };

  const doConnect = () => {
    const pw = $('#ssh-pw') ? $('#ssh-pw').value : '';
    if (remember() && pw) savePassword(pw);
    if (!remember()) api('/api/nodes/' + n.id, { method: 'PATCH', body: { remember: false } }).catch(() => {});
    connect(pw);
  };

  $('#term-go').addEventListener('click', doConnect);
  $('#term-clear').addEventListener('click', () => term && term.clear());
  const pwInput = $('#ssh-pw');
  if (pwInput) pwInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') doConnect(); });
  $('#pw-eye').addEventListener('click', () => {
    const el = $('#ssh-pw');
    if (!el) return;
    const show = el.type === 'password';
    el.type = show ? 'text' : 'password';
    $('#pw-eye').textContent = show ? t('隐藏') : t('显示');
  });
  $('#pw-copy').addEventListener('click', async () => {
    const el = $('#ssh-pw');
    if (!el || !el.value) { toast(t('暂无数据'), 'err'); return; }
    try { await navigator.clipboard.writeText(el.value); toast(t('已复制'), 'ok'); }
    catch { el.select(); document.execCommand('copy'); toast(t('已复制'), 'ok'); }
  });
  setState(t('未连接'), 'warn');
}

/* ---------- 文件 / 日志 ---------- */

function tabFiles(body, ctx) {
  const { id } = ctx;
  body.innerHTML = `
  <div class="grid cols-2">
    <div class="card">
      <div class="card-head">${t('目录浏览（只读）')}</div>
      <div class="row" style="margin-bottom:8px">
        <input type="text" id="f-path" value="/var/log" style="flex:3">
        <button class="btn" id="f-go" style="flex:0 0 auto">${t('查看')}</button>
      </div>
      <pre class="out" id="f-out">${t('输入路径后点击查看')}</pre>
    </div>
    <div class="card">
      <div class="card-head">${t('日志尾部')}</div>
      <div class="row" style="margin-bottom:8px">
        <input type="text" id="l-path" value="/var/log/syslog" style="flex:3">
        <input type="number" id="l-lines" value="200" style="flex:0 0 90px">
        <button class="btn" id="l-go" style="flex:0 0 auto">${t('读取')}</button>
      </div>
      <pre class="out" id="l-out">${t('常用日志：/var/log/syslog、/var/log/messages、/var/log/nginx/access.log、')}
/var/log/auth.log</pre>
    </div>
  </div>
  <p class="mute sm">${t('命令通过节点自身的 SSH 通道执行（需要 agent 安装时设置了密码）。出于安全考虑只允许浏览')}
  /var/log、/etc、/tmp、/var/lib、/opt、/srv、/home ${t('下的路径。')}</p>`;
  $('#f-go').addEventListener('click', async () => {
    $('#f-out').textContent = t('加载中…');
    try {
      const r = await api(`/api/nodes/${id}/files?path=` + encodeURIComponent($('#f-path').value));
      $('#f-out').textContent = r.output || r.error || '-';
    } catch (e) { $('#f-out').textContent = e.message; }
  });
  $('#l-go').addEventListener('click', async () => {
    $('#l-out').textContent = t('加载中…');
    const p = `/api/nodes/${id}/logs?path=` + encodeURIComponent($('#l-path').value) + '&lines=' + encodeURIComponent($('#l-lines').value);
    try { const r = await api(p); $('#l-out').textContent = r.output || r.error || '-'; }
    catch (e) { $('#l-out').textContent = e.message; }
  });
}

/* ---------- 备注 / 任务 ---------- */

function tabNote(body, ctx) {
  const { id, n } = ctx;
  const exp = expiryInfo(n.expires_at);
  latestRuntime(id, ctx);
  body.innerHTML = `
  <div class="grid cols-2">
    <div class="card">
      <div class="card-head">${t('服务器备注')}</div>
      <textarea id="note-text" rows="8" placeholder="${t('用途、到期时间、注意事项…')}">${esc(n.note || '')}</textarea>
      <div class="row" style="margin-top:10px;align-items:center">
        <button class="btn primary" id="note-save" style="flex:0 0 auto">${t('保存备注')}</button>
        <span class="mute sm" id="note-state"></span>
      </div>
      <div class="card-head" style="margin-top:16px">${t('基本信息')}</div>
      <div class="kv">
        <div class="k">${t('节点 ID')}</div><div class="mono">${esc(n.id)}</div>
        <div class="k">${t('地址')}</div><div class="mono">${esc(n.host)}${n.mode === 'tunnel' || n.mode === 'ssh' ? '' : ':' + (n.port || '-')}</div>
        <div class="k">${t('接入方式')}</div><div>${modeLabel(n.mode)}</div>
        <div class="k">SSH</div><div class="mono">${esc(n.ssh_user || 'root')}@${esc(n.host)}:${n.ssh_port || 22}${n.ssh_pass ? ' · ' + t('已保存密码') : ''}</div>
        <div class="k">${t('到期时间')}</div><div>${exp.text}</div>
        <div class="k">${t('创建时间')}</div><div>${fmt.datetime(n.created_at)}</div>
        <div class="k">${t('历史数据点')}</div><div>${fmt.int((ctx.data.series || {}).points || 0)} · ${fmt.bytes((ctx.data.series || {}).bytes || 0, 1)}</div>
      </div>
    </div>
    <div class="card">
      <div class="card-head">${t('该节点的待办')}</div>
      <div class="row" style="margin-bottom:10px">
        <input type="text" id="todo-input" placeholder="${t('添加一条与该服务器相关的待办…')}">
        <button class="btn primary" id="todo-add" style="flex:0 0 auto">${t('添加')}</button>
      </div>
      <div id="node-todos"><span class="spin"></span></div>
    </div>
  </div>`;
  $('#note-save').addEventListener('click', async () => {
    try {
      await api('/api/nodes/' + id, { method: 'PATCH', body: { note: $('#note-text').value } });
      $('#note-state').textContent = t('已保存');
    } catch (e) { $('#note-state').textContent = e.message; }
  });
  const loadTodos = async () => {
    const d = await api('/api/todos');
    const list = d.todos.filter(x => x.node_id === id);
    const host = $('#node-todos');
    if (!host) return;
    host.innerHTML = todoList(list);
    bindTodoList(host, loadTodos);
  };
  $('#todo-add').addEventListener('click', async () => {
    const text = $('#todo-input').value.trim();
    if (!text) return;
    await api('/api/todos', { method: 'POST', body: { text, node_id: id } });
    $('#todo-input').value = '';
    loadTodos();
  });
  loadTodos();
}

/* ------------------------------------------------------------------ 待办 */

function todoList(todos) {
  if (!todos || !todos.length) return `<div class="empty">${t('暂无待办事项')}</div>`;
  return todos.map(x => `
    <div class="todo ${x.done ? 'done' : ''}" data-id="${x.id}">
      <span class="pri ${x.priority ? 'high' : ''}"></span>
      <input type="checkbox" ${x.done ? 'checked' : ''} data-act="toggle">
      <span class="t">${esc(x.text)}</span>
      ${x.node_id ? `<span class="badge brand" data-node="${x.node_id}">${esc(nodeName(x.node_id))}</span>` : ''}
      <button class="btn small danger" data-act="del">${t('删除')}</button>
    </div>`).join('');
}

function nodeName(id) {
  const hit = state.overview && (state.overview.nodes || []).find(v => v.node.id === id);
  return hit ? hit.node.name : id;
}

function bindTodoList(host, reload) {
  if (!host) return;
  host.querySelectorAll('.todo').forEach(row => {
    const id = row.dataset.id;
    const toggle = row.querySelector('[data-act=toggle]');
    if (toggle) toggle.addEventListener('change', async () => {
      await api('/api/todos/' + id, { method: 'PATCH', body: { done: toggle.checked } });
      row.classList.toggle('done', toggle.checked);
      if (reload) reload(); else route();
    });
    const del = row.querySelector('[data-act=del]');
    if (del) del.addEventListener('click', async () => {
      await api('/api/todos/' + id, { method: 'DELETE' });
      if (reload) reload(); else route();
    });
    const nb = row.querySelector('.badge[data-node]');
    if (nb) { nb.style.cursor = 'pointer'; nb.addEventListener('click', () => go('#/node/' + nb.dataset.node)); }
  });
}

async function viewTodos() {
  $('#view').innerHTML = `
  <div class="page-head"><h2>${t('待办')}</h2><span class="sub">${t('运维待办、巡检项、续费提醒')}</span></div>
  <div class="card">
    <div class="row">
      <input type="text" id="t-text" placeholder="${t('新的待办事项…')}" style="flex:4">
      <select id="t-node" style="flex:1"><option value="">${t('不关联节点')}</option></select>
      <select id="t-pri" style="flex:0 0 110px"><option value="0">${t('普通')}</option><option value="1">${t('重要')}</option></select>
      <button class="btn primary" id="t-add" style="flex:0 0 auto">${t('添加')}</button>
    </div>
  </div>
  <div class="card" id="t-list"><div class="empty"><span class="spin"></span></div></div>`;
  const ov = await api('/api/overview');
  state.overview = ov;
  const sel = $('#t-node');
  (ov.nodes || []).forEach(v => {
    const o = document.createElement('option');
    o.value = v.node.id;
    o.textContent = v.node.name;
    sel.appendChild(o);
  });
  const load = async () => {
    const d = await api('/api/todos');
    const open = d.todos.filter(x => !x.done), done = d.todos.filter(x => x.done);
    const host = $('#t-list');
    if (!host) return;
    host.innerHTML = `<div class="card-head">${t('进行中')} (${open.length})</div>${todoList(open)}
      ${done.length ? `<div class="card-head" style="margin-top:14px">${t('已完成')} (${done.length})</div>${todoList(done)}` : ''}`;
    bindTodoList(host, load);
  };
  $('#t-add').addEventListener('click', async () => {
    const text = $('#t-text').value.trim();
    if (!text) return;
    await api('/api/todos', { method: 'POST', body: { text, node_id: $('#t-node').value, priority: Number($('#t-pri').value) } });
    $('#t-text').value = '';
    load();
  });
  $('#t-text').addEventListener('keydown', (e) => { if (e.key === 'Enter') $('#t-add').click(); });
  await load();
}

/* ------------------------------------------------------------------ 设置 */

async function viewSettings() {
  const d = await api('/api/settings');
  const s = d.settings;
  $('#view').innerHTML = `
  <div class="page-head"><h2>${t('设置')}</h2><span class="sub">${t('面板参数')}</span></div>
  <div class="grid cols-2">
    <div class="card">
      <div class="card-head">${t('采集与告警')}</div>
      <div class="row">
        <label class="field"><span>${t('在线节点采集间隔（秒）')}</span><input type="number" id="set-poll" value="${s.poll_seconds}"></label>
        <label class="field"><span>${t('离线节点重试间隔（秒）')}</span><input type="number" id="set-offline" value="${s.offline_seconds}"></label>
      </div>
      <div class="row">
        <label class="field"><span>${t('CPU 告警阈值 (%)')}</span><input type="number" id="set-cpu" value="${s.alert_cpu}"></label>
        <label class="field"><span>${t('内存告警阈值 (%)')}</span><input type="number" id="set-mem" value="${s.alert_mem}"></label>
        <label class="field"><span>${t('磁盘告警阈值 (%)')}</span><input type="number" id="set-disk" value="${s.alert_disk}"></label>
      </div>
      <label class="field"><span>${t('历史保留天数')}</span><input type="number" id="set-keep" value="${s.keep_days}"></label>
      <div class="row" style="align-items:center">
        <button class="btn primary" id="set-save" style="flex:0 0 auto">${t('保存设置')}</button>
        <span class="mute sm" id="set-state"></span>
      </div>
    </div>
    <div class="card">
      <div class="card-head">${t('安全')}</div>
      <label class="field"><span>${t('修改面板密码（至少 4 位）')}</span>
        <input type="password" id="set-pw" placeholder="${t('留空则不修改')}"></label>
      <button class="btn" id="set-pw-save">${t('修改密码')}</button>
      <div class="card-head" style="margin-top:16px">${t('运行信息')}</div>
      <div class="kv">
        <div class="k">${t('面板版本')}</div><div class="mono">v${esc(d.version)}</div>
        <div class="k">${t('数据目录')}</div><div class="mono ellip">${esc(d.dir)}</div>
        <div class="k">${t('监听地址')}</div><div class="mono">${esc(s.listen)}</div>
        <div class="k">${t('界面语言')}</div><div>
          <span class="seg"><button data-lang="zh" class="${!isEN() ? 'on' : ''}">中文</button><button data-lang="en" class="${isEN() ? 'on' : ''}">English</button></span>
        </div>
      </div>
    </div>
  </div>
  <div class="card">
    <div class="card-head">${t('节点接入')}</div>
    <p class="mute sm">${t('在目标服务器上执行安装命令（root）：')}</p>
    <div class="copy-box"><input type="text" readonly id="cmd-1" value="sudo ./nodemgr-agent install -port 8899 -password 'root密码'"><button class="btn small" data-copy="cmd-1">${t('复制')}</button></div>
    <div class="copy-box"><input type="text" readonly id="cmd-2" value="sudo ./nodemgr-agent install -port 8899 -bind 127.0.0.1 -password 'root密码'"><button class="btn small" data-copy="cmd-2">${t('复制')}</button></div>
    <div class="copy-box"><input type="text" readonly id="cmd-3" value="sudo ./nodemgr-agent install -password 'root密码' -tunnel"><button class="btn small" data-copy="cmd-3">${t('复制')}</button></div>
    <p class="mute sm">${t('第二条把 agent 限制在 127.0.0.1，配合「SSH 隧道」接入方式使用；第三条用于没有公网端口、需要 cloudflared 内网穿透的服务器。')}
    ${t('安装完成后终端会输出访问令牌与隧道地址，填到本面板即可。')}</p>
  </div>`;
  $('#set-save').addEventListener('click', async () => {
    $('#set-state').innerHTML = `<span class="spin"></span>`;
    try {
      await api('/api/settings', {
        method: 'PATCH',
        body: {
          poll_seconds: Number($('#set-poll').value), offline_seconds: Number($('#set-offline').value),
          alert_cpu: Number($('#set-cpu').value), alert_mem: Number($('#set-mem').value),
          alert_disk: Number($('#set-disk').value), keep_days: Number($('#set-keep').value),
        },
      });
      $('#set-state').textContent = t('已保存');
    } catch (e) { $('#set-state').textContent = e.message; }
  });
  $('#set-pw-save').addEventListener('click', async () => {
    const pw = $('#set-pw').value;
    if (pw.length < 4) { toast(t('密码至少 4 位'), 'err'); return; }
    await api('/api/settings', { method: 'PATCH', body: { password: pw } });
    $('#set-pw').value = '';
    toast(t('密码已修改'), 'ok');
  });
  $$('[data-lang]').forEach(b => b.addEventListener('click', () => {
    setLang(b.dataset.lang);
    renderShell();
    route();
  }));
  $$('[data-copy]').forEach(b => b.addEventListener('click', () => {
    const input = document.getElementById(b.dataset.copy);
    input.select();
    document.execCommand('copy');
    toast(t('已复制'), 'ok');
  }));
}

/* ------------------------------------------------------------------ 节点弹窗 */

function closeModal() { $('#modal-host').innerHTML = ''; }

function nodeDialog(view) {
  const n = (view && view.node) ? view.node : (view || {});
  const st = view && view.runtime ? view.runtime.stats : null;
  const isEdit = !!n.id;
  $('#modal-host').innerHTML = `
  <div class="modal-host" id="mh"><div class="modal">
    <h3>${isEdit ? t('编辑') : t('添加节点')}</h3>
    <div class="row">
      <label class="field"><span>${t('名称')} *</span><input type="text" id="n-name" value="${esc(n.name || '')}" placeholder="${t('例如 主站 / HK-01')}"></label>
      <label class="field"><span>${t('分组')}</span><input type="text" id="n-group" value="${esc(n.group || '')}" placeholder="${t('例如 生产 / 测试')}"></label>
    </div>
    <div class="row">
      <label class="field" style="flex:3"><span>${t('IP 或域名')} *</span>
        <input type="text" id="n-host" value="${esc(n.host || '')}" placeholder="1.2.3.4 / srv.example.com"></label>
      <label class="field" style="flex:1"><span>${t('端口')}</span><input type="number" id="n-port" value="${n.port ?? 8899}"></label>
    </div>
    <label class="field"><span>${t('接入方式')}</span>
      <select id="n-mode">
        <option value="ssh" ${n.mode === 'ssh' ? 'selected' : ''}>${t('SSH 隧道（agent 只监听 127.0.0.1）')}</option>
        <option value="direct" ${n.mode === 'direct' || !n.mode ? 'selected' : ''}>${t('IP/域名 + 端口（agent 直接监听）')}</option>
        <option value="domain" ${n.mode === 'domain' ? 'selected' : ''}>${t('域名反向代理（80/443）')}</option>
        <option value="tunnel" ${n.mode === 'tunnel' ? 'selected' : ''}>${t('cloudflared 内网穿透（无端口，自动 https）')}</option>
      </select></label>
    <div class="row">
      <label class="field" style="flex:0 0 130px"><span>${t('协议')}</span>
        <select id="n-scheme"><option value="http" ${n.scheme !== 'https' ? 'selected' : ''}>http</option><option value="https" ${n.scheme === 'https' ? 'selected' : ''}>https</option></select></label>
      <label class="field" style="flex:1"><span>${t('到期时间（留空表示永久）')}</span>
        <input type="datetime-local" id="n-expires" value="${fmtDateInput(n.expires_at)}"></label>
    </div>
    <label class="field"><span>${t('访问令牌（agent 安装时输出）')} *</span>
      <input type="text" id="n-token" value="${esc(n.token || '')}"></label>
    <div class="row">
      <label class="field"><span>${t('SSH 用户名')}</span><input type="text" id="n-sshuser" value="${esc(n.ssh_user || 'root')}"></label>
      <label class="field"><span>${t('SSH 端口')}</span><input type="number" id="n-sshport" value="${n.ssh_port || 22}"></label>
    </div>
    <label class="field"><span>${t('SSH 密码')}（${t('查看/修改 SSH 密码')}）</span>
      <div class="copy-box">
        <input type="text" id="n-sshpass" value="${esc(n.ssh_pass || '')}" autocomplete="off">
        <button class="btn small" id="n-pweye" type="button">${t('显示')}</button>
        <button class="btn small" id="n-pwcopy" type="button">${t('复制')}</button>
      </div></label>
    <label class="field check">
      <input type="checkbox" id="n-remember" ${n.remember || n.ssh_pass ? 'checked' : ''}>
      <span>${t('记住密码（仅本机保存）')}</span>
    </label>
    <label class="field check">
      <input type="checkbox" id="n-relay" ${n.use_relay ? 'checked' : ''}>
      <span>${t('通过 Agent 中继 SSH（节点没有开放 22 端口时使用）')}</span>
    </label>
    <label class="field"><span>${t('服务器备注')}</span>
      <textarea id="n-note" rows="2" placeholder="${t('用途、到期时间、注意事项…')}">${esc(n.note || '')}</textarea></label>
    <label class="field"><span>${t('采集间隔（秒，默认跟随面板设置）')}</span>
      <input type="number" id="n-interval" value="${n.interval || 5}"></label>
    <div id="n-test-out" class="mute sm"></div>
    ${st ? `<div class="mute sm">${esc(st.host.hostname)} · ${esc(st.host.os)} · ${st.host.cpu_cores}${isEN() ? ' cores' : ' 核'} · agent v${esc(st.version)}</div>` : ''}
    <div class="actions">
      ${isEdit ? `<button class="btn danger" id="n-del">${t('删除节点')}</button>` : ''}
      <button class="btn" id="n-test">${t('测试连接')}</button>
      <div class="spacer"></div>
      <button class="btn" id="n-cancel">${t('取消')}</button>
      <button class="btn primary" id="n-save">${isEdit ? t('保存') : t('添加并连接')}</button>
    </div>
  </div></div>`;

  const collect = () => {
    const exp = $('#n-expires').value ? Math.floor(new Date($('#n-expires').value).getTime() / 1000) : 0;
    const body = {
      name: $('#n-name').value.trim(), host: $('#n-host').value.trim(),
      port: Number($('#n-port').value) || 0, mode: $('#n-mode').value, scheme: $('#n-scheme').value,
      token: $('#n-token').value.trim(), note: $('#n-note').value, group: $('#n-group').value.trim(),
      ssh_user: $('#n-sshuser').value.trim() || 'root', ssh_port: Number($('#n-sshport').value) || 22,
      ssh_pass: $('#n-sshpass').value, remember: $('#n-remember').checked,
      use_relay: $('#n-relay').checked, interval: Number($('#n-interval').value) || 5,
      expires_at: exp,
    };
    if (!body.name) body.name = body.host;
    return body;
  };

  $('#n-sshpass').type = 'password';
  $('#n-cancel').addEventListener('click', closeModal);
  $('#mh').addEventListener('click', (e) => { if (e.target.id === 'mh') closeModal(); });
  $('#n-pweye').addEventListener('click', () => {
    const el = $('#n-sshpass');
    const show = el.type === 'password';
    el.type = show ? 'text' : 'password';
    $('#n-pweye').textContent = show ? t('隐藏') : t('显示');
  });
  $('#n-pwcopy').addEventListener('click', async () => {
    const el = $('#n-sshpass');
    if (!el.value) { toast(t('暂无数据'), 'err'); return; }
    try { await navigator.clipboard.writeText(el.value); toast(t('已复制'), 'ok'); }
    catch { el.select(); document.execCommand('copy'); toast(t('已复制'), 'ok'); }
  });
  if (isEdit) $('#n-del').addEventListener('click', async () => {
    if (!confirm(t('确定删除节点「{0}」？其历史数据也会被清除。', n.name))) return;
    await api('/api/nodes/' + n.id, { method: 'DELETE' });
    closeModal();
    toast(t('节点已删除'), 'ok');
    go('#/nodes');
  });
  $('#n-test').addEventListener('click', async () => {
    const out = $('#n-test-out');
    out.innerHTML = `<span class="spin"></span> ${t('测试中…')}`;
    try {
      const r = await api('/api/ssh-test', { method: 'POST', body: collect() });
      out.innerHTML = `<span class="badge ok">${t('SSH 登录成功')}</span> ${esc(r.detail || '')}`;
    } catch (e) { out.innerHTML = `<span class="badge bad">${t('失败')}</span> ${esc(e.message)}`; }
  });
  $('#n-save').addEventListener('click', async () => {
    const body = collect();
    if (!body.host) { toast(t('请填写 IP 或域名'), 'err'); return; }
    if (!body.token) { toast(t('请填写节点访问令牌'), 'err'); return; }
    try {
      if (isEdit) await api('/api/nodes/' + n.id, { method: 'PATCH', body });
      else await api('/api/nodes', { method: 'POST', body });
      closeModal();
      toast(isEdit ? t('已保存') : t('节点已添加，正在连接…'), 'ok');
      go(isEdit ? '#/node/' + n.id : '#/nodes');
    } catch (e) { toast(e.message, 'err'); }
  });
}

/* ------------------------------------------------------------------ 启动 */

async function boot() {
  setLang(getLang());
  try {
    const r = await fetch('/api/overview', { credentials: 'same-origin' });
    state.authed = r.status === 200;
  } catch { state.authed = false; }
  if (!state.authed) { renderLogin(); return; }
  renderShell();
  route();
}

window.addEventListener('hashchange', () => {
  if (suppressRoute) { suppressRoute = false; return; }
  route();
});
window.addEventListener('beforeunload', clearTimers);
window.addEventListener('error', (e) => { window.__npErr = String(e.message); });
boot();
