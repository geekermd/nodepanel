/* nodepanel · 前端主体：路由、状态、概览与管理页面
   仅使用浏览器原生能力（ES modules + fetch + WebSocket），无构建步骤。 */

import { drawChart, seriesOf, fmt, PALETTE } from './chart.js';

/* ------------------------------------------------------------------ 基础 */

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
}[c]));

async function api(path, opt = {}) {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(opt.headers || {}) },
    ...opt,
    body: opt.body && typeof opt.body !== 'string' ? JSON.stringify(opt.body) : opt.body,
  });
  if (res.status === 401) { state.authed = false; renderLogin('登录已过期，请重新登录'); throw new Error('未登录'); }
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { raw: text }; }
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`);
  return data;
}

function toast(msg, kind = '') {
  const host = $('#toasts');
  const node = document.createElement('div');
  node.className = 'toast ' + kind;
  node.textContent = msg;
  host.appendChild(node);
  setTimeout(() => { node.style.opacity = '0'; setTimeout(() => node.remove(), 300); }, kind === 'err' ? 6000 : 3000);
}

function badge(text, kind = '') { return `<span class="badge ${kind}">${esc(text)}</span>`; }

function bar(pct, kind) {
  const v = Math.max(0, Math.min(100, Number(pct) || 0));
  const cls = kind || (v >= 90 ? 'bad' : v >= 75 ? 'warn' : 'ok');
  return `<div class="bar ${cls}"><i style="width:${v.toFixed(1)}%"></i></div>`;
}

/* ------------------------------------------------------------------ 状态 */

const state = {
  authed: false,
  route: { name: 'overview', id: null, tab: 'monitor' },
  overview: null,
  timers: [],
  xterm: null,
  nodeCache: {},
};

function clearTimers() { state.timers.forEach(clearInterval); state.timers = []; }
function every(ms, fn) { fn(); state.timers.push(setInterval(fn, ms)); }

/* ------------------------------------------------------------------ 登录 */

function renderLogin(msg) {
  clearTimers();
  $('#root').innerHTML = `
  <div class="login-wrap"><form class="login-card" id="login-form">
    <h1>nodepanel</h1>
    <p>多节点服务器管理面板 · 本机运行</p>
    ${msg ? `<div class="badge bad" style="margin-bottom:10px">${esc(msg)}</div>` : ''}
    <label class="field"><span>管理密码</span>
      <input type="password" id="pw" autocomplete="current-password" placeholder="首次运行请查看面板终端输出" autofocus>
    </label>
    <button class="btn primary" style="width:100%" type="submit">登录</button>
    <p style="margin:12px 0 0;font-size:11.5px">密码保存在 <code>~/.nodepanel/panel.json</code>，
    修改：<code>nodemgr-panel -set-password 新密码</code></p>
  </form></div>`;
  $('#login-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = $('#login-form button');
    btn.disabled = true; btn.textContent = '登录中…';
    try {
      await api('/api/login', { method: 'POST', body: { password: $('#pw').value } });
      state.authed = true;
      renderShell();
      go(location.hash || '#/overview');
    } catch (err) {
      renderLogin(err.message);
    }
  });
}

/* ------------------------------------------------------------------ 外壳 */

const NAV = [
  { hash: '#/overview', label: '总览', ico: '▤' },
  { hash: '#/nodes', label: '节点', ico: '▥' },
  { hash: '#/todos', label: 'TODO', ico: '☑' },
  { hash: '#/settings', label: '设置', ico: '⚙' },
];

function renderShell() {
  $('#root').innerHTML = `
  <div class="app">
    <aside class="sidebar">
      <div class="brand"><span class="dot"></span><div>nodepanel<small id="ver-line">多节点管理</small></div></div>
      <nav class="nav">
        ${NAV.map(n => `<a href="${n.hash}" data-nav="${n.hash}"><span class="ico">${n.ico}</span>${n.label}</a>`).join('')}
      </nav>
      <div class="sidebar-foot">
        <div id="side-sum"></div>
        <div style="margin-top:8px"><a href="#" id="logout">退出登录</a></div>
      </div>
    </aside>
    <main class="main" id="view"></main>
  </div>`;
  $('#logout').addEventListener('click', async (e) => {
    e.preventDefault();
    await api('/api/logout', { method: 'POST' });
    renderLogin();
  });
}

function setActiveNav(hash) {
  $$('[data-nav]').forEach(a => a.classList.toggle('active', a.dataset.nav === hash));
}

/* ------------------------------------------------------------------ 路由 */

function go(hash) {
  // 同步更新 hash 并立刻渲染：只依赖 hashchange 会在「hash 没变」时丢渲染
  // （登录后跳转 #/overview 正是这种情况），而导航失败时又需要兜底。
  if (location.hash !== hash) {
    suppressRoute = true;
    location.hash = hash;
  }
  route();
}

function route() {
  if (!state.authed) return;
  clearTimers();
  const parts = (location.hash || '#/overview').replace(/^#\/?/, '').split('/');
  const name = parts[0] || 'overview';
  const id = parts[1] || null;
  state.route = { name, id, tab: state.nodeCache.tab || 'monitor' };
  setActiveNav(`#/${name}`);
  if (name === 'node' && id) return viewNode(id);
  if (name === 'nodes') return viewNodes();
  if (name === 'todos') return viewTodos();
  if (name === 'settings') return viewSettings();
  return viewOverview();
}

/* ------------------------------------------------------------------ 概览 */

function statusOf(rt) {
  if (!rt || rt.checked_at === 0) return badge('等待采集', 'warn');
  return rt.online
    ? badge('在线', 'ok').replace('badge ok', 'badge ok')
    : `<span class="badge bad" title="${esc(rt.last_error || '')}">离线</span>`;
}

function nodeCard(v) {
  const n = v.node, rt = v.runtime || {}, st = rt.stats;
  const cpu = st ? lastSeries(v, 'cpu') : 0;
  const memP = st && st.mem.total ? st.mem.used / st.mem.total * 100 : 0;
  const disk = st && st.disks && st.disks.length ? Math.max(...st.disks.map(d => d.used_pct)) : 0;
  const rx = lastSeries(v, 'rx'), tx = lastSeries(v, 'tx');
  const addr = `${n.host}${n.port && n.mode !== 'tunnel' ? ':' + n.port : ''}`;
  const modeLabel = n.mode === 'tunnel' ? 'cloudflared' : n.mode === 'domain' ? '域名' : '直连';
  return `
  <div class="card node-card" data-node="${n.id}">
    <div class="top">
      <div style="flex:1;min-width:0">
        <div class="name">${esc(n.name)}</div>
        <div class="addr">${esc(addr)} <span class="mute">· ${modeLabel}</span></div>
      </div>
      <div style="text-align:right">
        ${statusOf(rt)}
        <div class="mute" style="font-size:11px;margin-top:4px">${rt.latency_ms ? rt.latency_ms + 'ms' : ''}</div>
      </div>
    </div>
    <div class="mini-bars">
      <div class="mini"><span class="k">CPU</span>${bar(cpu)}<span class="v">${fmt.pct(cpu)}</span></div>
      <div class="mini"><span class="k">内存</span>${bar(memP)}<span class="v">${fmt.pct(memP)}</span></div>
      <div class="mini"><span class="k">磁盘</span>${bar(disk)}<span class="v">${fmt.pct(disk, 0)}</span></div>
    </div>
    <div class="row" style="font-size:12px;color:var(--text-dim);gap:14px">
      <span title="下行 / 上行速率">↓${fmt.rate(rx)} ↑${fmt.rate(tx)}</span>
      <span title="累计流量 (自 agent 启动)">Σ ${st ? st.totals.rx_h + ' / ' + st.totals.tx_h : '-'}</span>
      <span title="面板侧统计的访问量">访问 ${fmt.int(rt.requests || 0)}</span>
    </div>
    ${n.note ? `<div class="note">${esc(n.note)}</div>` : ''}
    <div class="row" style="gap:6px">
      ${(n.group ? badge(n.group, 'brand') : '')}
      ${st ? badge(`${st.host.cpu_cores} 核 · ${st.host.os}`, '') : ''}
      ${st && st.tunnel && st.tunnel.enabled ? badge('隧道' + (st.tunnel.url ? ' 已连接' : ''), st.tunnel.url ? 'ok' : 'warn') : ''}
      ${rt.online ? '' : `<span class="mute" style="font-size:11.5px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(rt.last_error || '')}</span>`}
    </div>
  </div>`;
}

function lastSeries(v, key) {
  const st = v.runtime && v.runtime.stats;
  if (!st) return 0;
  if (key === 'cpu') return v._cpu ?? 0;
  if (key === 'rx') return st.totals.rx > 0 ? (v._rx ?? 0) : 0;
  return 0;
}

async function viewOverview() {
  const view = $('#view');
  view.innerHTML = `<div class="page-head"><h2>总览</h2><span class="sub">全部节点资源与访问量</span>
    <div class="spacer"></div><button class="btn primary" id="add-node">+ 添加节点</button></div>
    <div id="ov-body"><div class="empty"><span class="spin"></span> 加载中…</div></div>`;
  $('#add-node').addEventListener('click', () => nodeDialog(null));

  const load = async () => {
    let data;
    try { data = await api('/api/overview'); } catch (e) { return; }
    state.overview = data;
    const s = data.summary;
    $('#ver-line').textContent = 'v' + data.version;
    $('#side-sum').innerHTML = `在线 ${s.online}/${s.nodes}<br>待办 ${s.todos_open}`;
    // 逐个节点取最新一个采样点用于卡片进度条（只取极少数据）
    await Promise.all(data.nodes.map(async (v) => {
      try {
        const m = await api(`/api/nodes/${v.node.id}/metrics?span=300&max=1`);
        if (m.samples && m.samples.length) {
          const last = m.samples[m.samples.length - 1];
          v._cpu = last.cpu; v._rx = last.rx; v._tx = last.tx;
        }
      } catch { /* 离线节点忽略 */ }
    }));
    renderOverview(data);
  };
  renderOverview(state.overview || { summary: {}, nodes: [], alerts: [], todos: [] });
  await load();
  state.timers.push(setInterval(load, 5000));
}

function renderOverview(data) {
  const s = data.summary || {};
  const body = $('#ov-body');
  if (!body) return;
  const alerts = (data.alerts || []).map(a =>
    `<div class="toast err" style="position:static;animation:none;max-width:none">⚠ ${esc(a.node)} ${esc(a.kind)} 使用率 ${fmt.pct(a.value)}</div>`).join('');
  body.innerHTML = `
    <div class="grid cols-4" style="margin-bottom:14px">
      <div class="stat"><div class="label">节点</div><div class="value">${s.online || 0}<small>/ ${s.nodes || 0} 在线</small></div>
        <div class="foot">${s.offline ? s.offline + ' 个离线' : '全部在线'}</div></div>
      <div class="stat"><div class="label">累计流量</div><div class="value">${esc(s.rx_total_h || '0 B')}<small>↓</small></div>
        <div class="foot">↑ ${esc(s.tx_total_h || '0 B')}</div></div>
      <div class="stat"><div class="label">访问量（面板统计）</div><div class="value">${fmt.int(s.requests_today || 0)}<small>今日</small></div>
        <div class="foot">累计 ${fmt.int(s.requests || 0)} 次请求</div></div>
      <div class="stat"><div class="label">平均负载</div><div class="value">${fmt.pct(s.cpu_avg || 0, 0)}<small>CPU</small></div>
        <div class="foot">内存 ${fmt.pct(s.mem_avg || 0, 0)} · 待办 ${s.todos_open || 0}</div></div>
    </div>
    ${alerts ? `<div class="grid" style="margin-bottom:14px;gap:8px">${alerts}</div>` : ''}
    <div class="grid cols-3" id="node-grid">
      ${(data.nodes || []).map(nodeCard).join('') || `<div class="card empty">还没有节点。点右上角「添加节点」，或在服务器上安装 nodemgr-agent 后填入地址与令牌。</div>`}
    </div>
    <div class="card" style="margin-top:16px">
      <h3>待办事项 <span class="spacer"></span><a href="#/todos" style="font-weight:400">全部 →</a></h3>
      <div id="ov-todos">${todoList((data.todos || []).slice(0, 5), true)}</div>
    </div>`;
  $$('#node-grid .node-card').forEach(c => c.addEventListener('click', () => go(`#/node/${c.dataset.node}`)));
  bindTodoList($('#ov-todos'));
}

/* ------------------------------------------------------------------ 节点列表 */

async function viewNodes() {
  const view = $('#view');
  view.innerHTML = `<div class="page-head"><h2>节点</h2><span class="sub">服务器清单</span>
    <div class="spacer"></div><button class="btn primary" id="add-node">+ 添加节点</button></div>
    <div id="nodes-body"></div>`;
  $('#add-node').addEventListener('click', () => nodeDialog(null));
  const load = async () => {
    let data;
    try { data = await api('/api/nodes'); } catch { return; }
    const rows = data.nodes.map(v => {
      const n = v.node, rt = v.runtime || {}, st = rt.stats;
      const addr = `${n.host}${n.port && n.mode !== 'tunnel' ? ':' + n.port : ''}`;
      return `<tr data-node="${n.id}" style="cursor:pointer">
        <td><b>${esc(n.name)}</b><div class="mute mono" style="font-size:11.5px">${esc(addr)}</div></td>
        <td>${statusOf(rt)}</td>
        <td class="mono">${rt.latency_ms || '-'} ms</td>
        <td class="mono">${st ? st.host.cpu_cores + ' 核' : '-'}</td>
        <td class="mono">${st ? fmt.bytes(st.mem.total, 0) : '-'}</td>
        <td class="mono">${st ? rt.stats.totals.rx_h : '-'}</td>
        <td class="mono">${st ? rt.stats.totals.tx_h : '-'}</td>
        <td class="mono">${fmt.int(rt.requests || 0)}</td>
        <td>${n.group ? badge(n.group, 'brand') : '<span class="mute">-</span>'}</td>
        <td class="right nowrap">
          <button class="btn small" data-act="edit">编辑</button>
          <button class="btn small" data-act="term">终端</button>
        </td></tr>`;
    }).join('');
    $('#nodes-body').innerHTML = `
    <div class="card" style="padding:0;overflow:auto">
      <table class="tbl">
        <thead><tr><th>节点</th><th>状态</th><th>延迟</th><th>CPU</th><th>内存</th><th>下行总量</th><th>上行总量</th><th>访问量</th><th>分组</th><th></th></tr></thead>
        <tbody>${rows || `<tr><td colspan="10" class="empty">暂无节点</td></tr>`}</tbody>
      </table>
    </div>
    <p class="mute" style="font-size:12px">提示：表格里的「访问量」是面板累计统计到的 agent 请求次数；单个节点的详细访问量/服务状态在节点详情页。</p>`;
    $$('#nodes-body tr[data-node]').forEach(tr => {
      tr.addEventListener('click', (e) => {
        const act = e.target.dataset.act;
        if (act === 'edit') { e.stopPropagation(); nodeDialog(v0(tr.dataset.node)); return; }
        if (act === 'term') { e.stopPropagation(); go(`#/node/${tr.dataset.node}`); state.nodeCache.tab = 'terminal'; return; }
        go(`#/node/${tr.dataset.node}`);
      });
    });
    function v0(id) { return data.nodes.find(x => x.node.id === id); }
  };
  await load();
  state.timers.push(setInterval(load, 5000));
}

/* ------------------------------------------------------------------ 节点详情 */

const TABS = [
  { id: 'monitor', label: '监控' },
  { id: 'service', label: '服务/访问量' },
  { id: 'process', label: '进程' },
  { id: 'terminal', label: 'SSH 终端' },
  { id: 'files', label: '文件/日志' },
  { id: 'note', label: '备注/任务' },
];

async function viewNode(id) {
  const view = $('#view');
  let data;
  try { data = await api('/api/nodes/' + id); } catch (e) {
    view.innerHTML = `<div class="empty">节点不存在或已删除</div>`; return;
  }
  const n = data.node, rt = data.runtime || {}, st = rt.stats;
  const tab = state.nodeCache.tab || 'monitor';
  const addr = `${n.host}${n.port && n.mode !== 'tunnel' ? ':' + n.port : ''}`;
  view.innerHTML = `
  <div class="page-head">
    <a href="#/overview" class="btn small">← 返回</a>
    <h2>${esc(n.name)}</h2>
    ${statusOf(rt)}
    <span class="sub mono">${esc(addr)} · ${n.mode === 'tunnel' ? 'cloudflared 隧道' : '直连'}</span>
    <div class="spacer"></div>
    <button class="btn small" id="edit-node">编辑</button>
    <button class="btn small" id="refresh-node">刷新</button>
  </div>
  <div class="tabs">${TABS.map(t => `<button data-tab="${t.id}" class="${t.id === tab ? 'active' : ''}">${t.label}</button>`).join('')}</div>
  <div id="tab-body"></div>`;
  $$('[data-tab]').forEach(b => b.addEventListener('click', () => {
    state.nodeCache.tab = b.dataset.tab;
    $$('[data-tab]').forEach(x => x.classList.toggle('active', x === b));
    renderTab(b.dataset.tab, { id, n, rt, st, data });
  }));
  $('#edit-node').addEventListener('click', () => nodeDialog({ node: n, runtime: rt, series: data.series }));
  $('#refresh-node').addEventListener('click', () => viewNode(id));
  renderTab(tab, { id, n, rt, st, data });

  // 实时刷新（终端页有自己的通道，不用轮询）
  if (tab !== 'terminal') {
    state.timers.push(setInterval(async () => {
      try {
        const d = await api('/api/nodes/' + id);
        const s = d.runtime && d.runtime.stats;
        const head = $('.page-head');
        if (head) {
          const b = head.querySelector('.badge');
          if (b) b.outerHTML = statusOf(d.runtime);
        }
        if (state.nodeCache.tab === 'monitor') updateMonitor(d);
        if (state.nodeCache.tab === 'service') updateService(d);
      } catch { /* ignore */ }
    }, 6000));
  }
}

function renderTab(tab, ctx) {
  const body = $('#tab-body');
  if (!body) return;
  if (tab === 'monitor') return tabMonitor(body, ctx);
  if (tab === 'service') return tabService(body, ctx);
  if (tab === 'process') return tabProcess(body, ctx);
  if (tab === 'terminal') return tabTerminal(body, ctx);
  if (tab === 'files') return tabFiles(body, ctx);
  return tabNote(body, ctx);
}

/* ---------- 监控 ---------- */

let monitorTimer = null;
async function tabMonitor(body, ctx) {
  const { id, n, rt, st } = ctx;
  const range = state.nodeCache.range || 3600;
  body.innerHTML = `
  <div class="grid cols-4" style="margin-bottom:12px">
    <div class="stat"><div class="label">CPU 使用率</div><div class="value" id="m-cpu">-</div>
      <div class="foot" id="m-load">负载 -</div></div>
    <div class="stat"><div class="label">内存</div><div class="value" id="m-mem">-</div>
      <div class="foot" id="m-mem2">-</div></div>
    <div class="stat"><div class="label">磁盘 I/O</div><div class="value" id="m-io">-</div>
      <div class="foot" id="m-io2">读 / 写</div></div>
    <div class="stat"><div class="label">网络</div><div class="value" id="m-net">-</div>
      <div class="foot" id="m-net2">↓ / ↑</div></div>
  </div>
  <div class="card" style="margin-bottom:12px">
    <h3>CPU / 内存 使用率
      <span class="spacer"></span>
      <span class="row" style="gap:4px;flex:none">
        ${[[3600, '1 小时'], [21600, '6 小时'], [86400, '24 小时'], [604800, '7 天']].map(([v, l]) =>
          `<button class="btn small ${v === range ? 'primary' : ''}" data-range="${v}">${l}</button>`).join('')}
      </span>
    </h3>
    <div class="chart-box" id="chart-cpu"></div>
  </div>
  <div class="grid cols-2">
    <div class="card"><h3>网络吞吐</h3><div class="chart-box" id="chart-net"></div></div>
    <div class="card"><h3>磁盘 I/O</h3><div class="chart-box" id="chart-io"></div></div>
  </div>
  <div class="grid cols-2" style="margin-top:12px">
    <div class="card"><h3>系统信息</h3><div class="kv" id="m-info"></div></div>
    <div class="card"><h3>磁盘挂载</h3><div id="m-disks"></div></div>
  </div>
  <div class="card" style="margin-top:12px"><h3>流量统计</h3><div class="kv" id="m-traffic"></div></div>`;

  body.querySelectorAll('[data-range]').forEach(b => b.addEventListener('click', () => {
    state.nodeCache.range = Number(b.dataset.range);
    body.querySelectorAll('[data-range]').forEach(x => x.classList.toggle('primary', x === b));
    loadMetrics();
  }));

  async function loadMetrics() {
    let m;
    try { m = await api(`/api/nodes/${id}/metrics?span=${state.nodeCache.range || 3600}&max=420`); }
    catch (e) { $('#chart-cpu').innerHTML = `<div class="empty">读取历史失败：${esc(e.message)}</div>`; return; }
    const samples = m.samples || [];
    if (!samples.length) {
      $('#chart-cpu').innerHTML = `<div class="empty">还没有历史数据，等待采集…</div>`;
      return;
    }
    const last = samples[samples.length - 1];
    drawChart($('#chart-cpu'), {
      height: 150,
      series: [
        { name: 'CPU', color: PALETTE.blue, points: seriesOf(samples, 'cpu'), format: (v) => fmt.pct(v) },
        { name: '内存', color: PALETTE.purple, points: seriesOf(samples, 'mem_p'), format: (v) => fmt.pct(v) },
        { name: 'IOwait', color: PALETTE.amber, points: seriesOf(samples, 'iow'), format: (v) => fmt.pct(v) },
      ],
      yMax: 100, formatY: (v) => v.toFixed(0) + '%', formatX: (t) => fmt.time(t, (state.nodeCache.range || 0) > 86400),
    });
    drawChart($('#chart-net'), {
      height: 140,
      series: [
        { name: '下行', color: PALETTE.cyan, points: seriesOf(samples, 'rx'), format: fmt.rate },
        { name: '上行', color: PALETTE.green, points: seriesOf(samples, 'tx'), format: fmt.rate },
      ],
      sharedScale: false,
      formatY: (v) => fmt.bytes(v, 0), formatX: (t) => fmt.time(t, (state.nodeCache.range || 0) > 86400),
    });
    drawChart($('#chart-io'), {
      height: 140,
      series: [
        { name: '读', color: PALETTE.blue, points: seriesOf(samples, 'dr'), format: fmt.rate },
        { name: '写', color: PALETTE.amber, points: seriesOf(samples, 'dw'), format: fmt.rate },
      ],
      sharedScale: false,
      formatY: (v) => fmt.bytes(v, 0), formatX: (t) => fmt.time(t, (state.nodeCache.range || 0) > 86400),
    });
    updateMonitor({ node: ctx.n, runtime: { ...ctx.rt, stats: ctx.st }, _last: last });
    renderInfo(ctx, last);
  }
  await loadMetrics();
  monitorTimer = setInterval(loadMetrics, 6000);
  state.timers.push(monitorTimer);
}

function updateMonitor(d) {
  const rt = d.runtime || {}, st = rt.stats;
  const last = d._last || (state.nodeCache.lastSample) || null;
  if (last) state.nodeCache.lastSample = last;
  const s = last || {};
  const set = (id, v) => { const e = $(id); if (e) e.textContent = v; };
  set('#m-cpu', fmt.pct(s.cpu || 0));
  set('#m-load', `负载 ${fmt.num(s.l1 || 0)} / ${fmt.num(s.l5 || 0)} / ${fmt.num(s.l15 || 0)}`);
  if (st) {
    set('#m-mem', `${fmt.bytes(st.mem.used, 1)}`);
    set('#m-mem2', `共 ${fmt.bytes(st.mem.total, 1)} · ${fmt.pct(st.mem.total ? st.mem.used / st.mem.total * 100 : 0)}${st.mem.swap_total ? ' · Swap ' + fmt.pct(st.mem.swap_total ? (st.mem.swap_total - st.mem.swap_free) / st.mem.swap_total * 100 : 0) : ''}`);
  }
  set('#m-io', fmt.rate(s.dr || 0));
  set('#m-io2', `写 ${fmt.rate(s.dw || 0)} · 利用率 ${fmt.pct(s.dio || 0)}`);
  set('#m-net', `↓${fmt.rate(s.rx || 0)}`);
  set('#m-net2', `↑${fmt.rate(s.tx || 0)} · 连接 ${s.tcp_est || 0}`);
  const info = $('#m-info');
  if (info && st) {
    info.innerHTML = `
      <div class="k">主机名</div><div>${esc(st.host.hostname)}</div>
      <div class="k">系统</div><div>${esc(st.host.os)}</div>
      <div class="k">内核</div><div class="mono">${esc(st.host.kernel)}</div>
      <div class="k">CPU</div><div>${esc(st.host.cpu_model)} · ${st.host.cpu_cores} 核</div>
      <div class="k">虚拟化</div><div>${esc(st.host.virt || '-')}</div>
      <div class="k">运行时长</div><div>${fmt.dur(st.uptime)}</div>
      <div class="k">进程 / 线程</div><div>${s.procs || '-'} / ${s.run || '-'}</div>
      <div class="k">TCP 连接</div><div>建立 ${st.tcp.established} · 监听 ${st.tcp.listen} · TIME_WAIT ${st.tcp.time_wait}</div>
      <div class="k">Agent</div><div>v${esc(st.version)} · 采样 ${st.interval}s · 缓存 ${st.history.points} 点</div>`;
  }
  const disks = $('#m-disks');
  if (disks && st) {
    disks.innerHTML = `<table class="tbl"><thead><tr><th>挂载点</th><th>设备</th><th style="width:30%">使用率</th><th class="right nowrap">已用 / 总量</th></tr></thead><tbody>
      ${st.disks.map(d => `<tr>
        <td class="mono">${esc(d.mount)}<div class="mute" style="font-size:11px">${esc(d.fstype)}</div></td>
        <td class="mono mute" style="max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc(d.device)}">${esc(d.device)}</td>
        <td>${bar(d.used_pct)}<span class="mute nowrap" style="font-size:11px">${fmt.pct(d.used_pct)}</span></td>
        <td class="right mono nowrap">${fmt.bytes(d.used, 1)}<span class="mute"> / ${fmt.bytes(d.total, 1)}</span></td></tr>`).join('')}
      </tbody></table>`;
  }
  const tr = $('#m-traffic');
  if (tr && st) {
    const days = Object.entries(rt.days || {}).sort().slice(-7);
    tr.innerHTML = `
      <div class="k">Agent 累计下行</div><div class="mono">${esc(st.totals.rx_h)}</div>
      <div class="k">Agent 累计上行</div><div class="mono">${esc(st.totals.tx_h)}</div>
      <div class="k">面板统计下行</div><div class="mono">${fmt.bytes(rt.rx_total || 0, 2)}</div>
      <div class="k">面板统计上行</div><div class="mono">${fmt.bytes(rt.tx_total || 0, 2)}</div>
      <div class="k">今日请求</div><div class="mono">${fmt.int(rt.requests_today || 0)} 次</div>
      <div class="k">近 7 天</div><div>${days.length ? days.map(([d, v]) =>
        `<div style="display:flex;gap:10px;font-size:12px"><span class="mono">${esc(d)}</span>
        <span>请求 ${fmt.int(v.requests || 0)}</span><span class="mute">↓${fmt.bytes(v.rx || 0, 1)} ↑${fmt.bytes(v.tx || 0, 1)}</span></div>`).join('') : '<span class="mute">暂无</span>'}
      </div>`;
  }
}

function renderInfo(ctx, last) {
  updateMonitor({ node: ctx.n, runtime: { ...ctx.rt, stats: ctx.st }, _last: last });
}

/* ---------- 服务 / 访问量 ---------- */

async function tabService(body, ctx) {
  const { id, n, rt, st } = ctx;
  body.innerHTML = `
  <div class="grid cols-4" style="margin-bottom:12px">
    <div class="stat"><div class="label">累计访问量</div><div class="value">${fmt.int(rt.requests || 0)}</div>
      <div class="foot">今日 ${fmt.int(rt.requests_today || 0)} 次</div></div>
    <div class="stat"><div class="label">监听端口</div><div class="value" id="s-ports">-</div><div class="foot">TCP 监听中</div></div>
    <div class="stat"><div class="label">HTTP 探测</div><div class="value" id="s-probe">-</div><div class="foot">节点本机自检</div></div>
    <div class="stat"><div class="label">TCP 连接</div><div class="value" id="s-tcp">-</div><div class="foot">established</div></div>
  </div>
  <div class="card" style="margin-bottom:12px"><h3>监听端口与服务</h3><div id="s-porttable"><span class="spin"></span></div></div>
  <div class="grid cols-2">
    <div class="card"><h3>近 14 天访问量</h3><div class="chart-box" id="chart-req"></div></div>
    <div class="card"><h3>近 14 天流量</h3><div class="chart-box" id="chart-day"></div></div>
  </div>
  <div class="card" style="margin-top:12px"><h3>面板侧连通性探测</h3>
    <div class="row" style="align-items:flex-end">
      <label class="field" style="flex:3"><span>探测 URL（默认节点地址）</span>
        <input type="text" id="probe-url" value="${esc(nodeWebURL(n))}"></label>
      <button class="btn primary" id="probe-btn" style="flex:0 0 auto">探测</button>
    </div>
    <div id="probe-out" class="mute" style="font-size:12.5px;margin-top:6px"></div>
  </div>`;
  $('#probe-btn').addEventListener('click', async () => {
    $('#probe-out').innerHTML = '<span class="spin"></span> 探测中…';
    try {
      const r = await api('/api/probe?url=' + encodeURIComponent($('#probe-url').value));
      $('#probe-out').innerHTML = r.error
        ? `<span class="badge bad">失败</span> ${esc(r.error)}`
        : `<span class="badge ${r.code < 400 ? 'ok' : 'warn'}">HTTP ${r.code}</span> 耗时 ${r.ms} ms`;
    } catch (e) { $('#probe-out').innerHTML = `<span class="badge bad">失败</span> ${esc(e.message)}`; }
  });
  updateService(ctx);
  loadDayCharts(ctx);
}

function nodeWebURL(n) {
  const scheme = n.scheme || 'http';
  if (n.mode === 'tunnel' || !n.port || n.port === 80 || n.port === 443) return `${scheme}://${n.host}`;
  return `${scheme}://${n.host}:${n.port}`;
}

async function updateService(d) {
  const rt = d.runtime || {}, st = rt.stats;
  const set = (id, v) => { const e = $(id); if (e) e.textContent = v; };
  set('#s-tcp', st ? st.tcp.established : '-');
  const table = $('#s-porttable');
  if (!table || !st) return;
  let ports = null;
  try { ports = await api(`/api/nodes/${d.node.id}/ports`); } catch (e) { table.innerHTML = `<div class="empty">读取失败：${esc(e.message)}</div>`; return; }
  const list = (ports && ports.ports) || [];
  const probes = (ports && ports.probes) || [];
  set('#s-ports', list.length);
  const probeOK = probes.filter(p => p.ok).length;
  set('#s-probe', probes.length ? `${probeOK}/${probes.length}` : '无 HTTP 服务');
  table.innerHTML = list.length ? `<table class="tbl">
    <thead><tr><th>端口</th><th>协议</th><th>服务</th><th>进程</th><th class="right">活动连接</th><th class="right">TIME_WAIT</th></tr></thead>
    <tbody>${list.map(p => `<tr>
      <td class="mono"><b>${p.port}</b></td><td class="mute">${esc(p.proto)}</td>
      <td>${esc(p.service || '-')}</td><td class="mono mute" style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(p.process || '-')}</td>
      <td class="right mono">${p.established}</td><td class="right mono mute">${p.time_wait}</td></tr>`).join('')}
    </tbody></table>
    ${probes.length ? `<div style="margin-top:10px" class="row">${probes.map(p =>
      `<span class="badge ${p.ok ? 'ok' : 'bad'}">${p.scheme}://127.0.0.1:${p.port} → ${p.ok ? 'HTTP ' + p.code : '无响应'} ${p.ms}ms</span>`).join('')}</div>` : ''}`
    : `<div class="empty">没有检测到监听端口（可能需要 root 权限读取 /proc/net/tcp）</div>`;
}

function loadDayCharts(ctx) {
  const days = Object.entries(ctx.rt.days || {}).sort().slice(-14);
  if (!days.length) {
    $('#chart-req').innerHTML = `<div class="empty">还没有历史统计，面板运行一段时间后这里会显示每天的数据</div>`;
    $('#chart-day').innerHTML = '';
    return;
  }
  const pts = (key) => days.map(([d, v], i) => ({ t: new Date(d + 'T12:00:00').getTime(), v: Number(v[key]) || 0 }));
  drawChart($('#chart-req'), {
    height: 130, series: [{ name: '请求数', color: PALETTE.blue, points: pts('requests'), format: (v) => fmt.int(v) + ' 次' }],
    formatY: (v) => fmt.int(v), formatX: (t) => fmt.time(t, true),
  });
  drawChart($('#chart-day'), {
    height: 130,
    series: [
      { name: '下行', color: PALETTE.cyan, points: pts('rx'), format: fmt.bytes },
      { name: '上行', color: PALETTE.green, points: pts('tx'), format: fmt.bytes },
    ],
    sharedScale: false,
    formatY: (v) => fmt.bytes(v, 0), formatX: (t) => fmt.time(t, true),
  });
}

/* ---------- 进程 ---------- */

async function tabProcess(body, ctx) {
  const { id } = ctx;
  body.innerHTML = `<div class="card" style="padding:0">${`<div class="empty"><span class="spin"></span> 加载进程…</div>`}</div>`;
  const load = async () => {
    let data;
    try { data = await api(`/api/nodes/${id}/processes?n=40`); }
    catch (e) { body.innerHTML = `<div class="card empty">读取失败：${esc(e.message)}</div>`; return; }
    const ps = (data.processes || []).slice().sort((a, b) => b.cpu - a.cpu);
    body.innerHTML = `<div class="card" style="padding:0;overflow:auto">
      <table class="tbl"><thead><tr><th>PID</th><th>名称</th><th>状态</th><th class="right">CPU</th><th class="right">内存</th><th class="right">线程</th><th>命令</th></tr></thead>
      <tbody>${ps.map(p => `<tr>
        <td class="mono">${p.pid}</td><td><b>${esc(p.name)}</b></td><td class="mute">${esc(p.state)}</td>
        <td class="right mono">${fmt.pct(p.cpu)}</td><td class="right mono">${fmt.bytes(p.mem_rss, 1)} <span class="mute">${fmt.pct(p.mem_pct, 0)}</span></td>
        <td class="right mono mute">${p.threads}</td>
        <td class="mono mute" style="max-width:420px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc(p.cmdline)}">${esc(p.cmdline)}</td>
      </tr>`).join('')}</tbody></table></div>
      <p class="mute" style="font-size:12px">CPU 为该进程在两次采样间的平均占用（按 100% = 单核计）。</p>`;
  };
  await load();
  state.timers.push(setInterval(load, 5000));
}

/* ---------- SSH 终端 ---------- */

function tabTerminal(body, ctx) {
  const { n } = ctx;
  body.innerHTML = `
  <div class="term-head">
    <span class="badge brand">${esc((n.ssh_user || 'root') + '@' + n.host)}:${n.ssh_port || 22}</span>
    <span class="mute" style="font-size:12px">${n.use_relay ? '经 Agent 中继（适合内网穿透节点）' : '面板直连'}</span>
    <div class="spacer"></div>
    <span id="term-state" class="badge warn">未连接</span>
    <button class="btn small" id="term-connect">连接</button>
    <button class="btn small" id="term-reconnect">重连</button>
    <button class="btn small" id="term-clear">清屏</button>
  </div>
  <div class="card" id="term-auth" style="margin-bottom:10px">
    <div class="row" style="align-items:flex-end">
      <label class="field" style="flex:2;margin:0"><span>SSH 密码（${esc(n.ssh_user || 'root')}@${esc(n.host)}）</span>
        <input type="password" id="ssh-pw" placeholder="留空则使用已保存的密码" autocomplete="off"></label>
      <label class="row" style="flex:0 0 auto;align-items:center;gap:6px;font-size:12.5px;margin:0 0 6px">
        <input type="checkbox" id="remember-pw" ${n.remember ? 'checked' : ''} style="width:auto"> 记住密码（仅本机保存）
      </label>
      <button class="btn primary" id="term-go" style="flex:0 0 auto;margin-bottom:4px">连接终端</button>
    </div>
    ${n.remember && n.ssh_pass_set === false ? '<div class="mute" style="font-size:12px">已记住密码，直接点「连接终端」。</div>' : ''}
    <div class="mute" style="font-size:12px">密码只用于本次 SSH 认证，不经 URL 传递${n.remember ? '；勾选「记住密码」后保存在本机面板数据目录。' : '。'}</div>
  </div>
  <div class="term-wrap"><div id="terminal"></div></div>
  <p class="mute" style="font-size:12px">若节点只能通过 cloudflared 访问，请在「编辑」里打开「通过 Agent 中继 SSH」，
  终端会改为经节点自身的 sshd 登录。</p>`;

  const state$ = () => $('#term-state');
  let ws = null;
  let term = null;
  let fit = null;
  let remembered = !!n.remember;

  const setState = (text, kind) => {
    const e = state$();
    if (e) { e.className = 'badge ' + kind; e.textContent = text; }
  };

  const savePassword = async (pass) => {
    try { await api('/api/nodes/' + n.id, { method: 'PATCH', body: { ssh_pass: pass, remember: true } }); remembered = true; } catch {}
  };

  const connect = (password) => {
    const termEl = $('#terminal');
    if (!termEl) return;
    if (term && term.dispose) { try { term.dispose(); } catch {} term = null; }
    if (ws && ws.readyState <= 1) { try { ws.close(); } catch {} }
    if (!window.Terminal) { termEl.innerHTML = '<div class="empty">终端组件未加载（vendor/xterm.js 缺失）</div>'; return; }

    term = new window.Terminal({
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      fontSize: 13,
      theme: { background: '#000000', foreground: '#e7eaf0', cursor: '#4c8dff', selectionBackground: '#29405f' },
      scrollback: 4000,
      cursorBlink: true,
      allowProposedApi: true,
    });
    fit = new window.FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(termEl);
    try { fit.fit(); } catch {}

    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(`${proto}://${location.host}/api/nodes/${n.id}/ssh`);
    ws.binaryType = 'arraybuffer';
    setState('连接中…', 'warn');

    ws.onopen = () => {
      try { fit.fit(); } catch {}
      ws.send(JSON.stringify({ type: 'auth', payload: password || '', cols: term.cols, rows: term.rows }));
      setState('已连接', 'ok');
      term.focus();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        let msg; try { msg = JSON.parse(ev.data); } catch { return; }
        if (msg.type === 'error') {
          term.write(`\r\n\x1b[31m${msg.payload}\x1b[0m\r\n`);
          setState('连接失败', 'bad');
        } else if (msg.type === 'exit') {
          term.write('\r\n\x1b[33m[session closed]\x1b[0m\r\n');
          setState('已断开', 'warn');
        }
      } else {
        term.write(new Uint8Array(ev.data));
      }
    };
    ws.onclose = () => setState('已断开', 'warn');
    ws.onerror = () => setState('连接错误', 'bad');

    term.onData((d) => { if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'input', payload: d })); });
    term.onResize(({ cols, rows }) => { if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', cols, rows })); });
    state.timers.push(setInterval(() => { try { fit.fit(); } catch {} }, 4000));
    window.addEventListener('resize', () => { try { fit.fit(); } catch {} });
  };

  const doConnect = () => {
    const pwEl = $('#ssh-pw');
    const password = pwEl ? pwEl.value : '';
    const remember = $('#remember-pw') && $('#remember-pw').checked;
    if (remember && password) savePassword(password);
    if (!remember) api('/api/nodes/' + n.id, { method: 'PATCH', body: { remember: false } }).catch(() => {});
    connect(password);
  };

  $('#term-go').addEventListener('click', doConnect);
  $('#term-connect').addEventListener('click', doConnect);
  $('#term-reconnect').addEventListener('click', doConnect);
  $('#term-clear').addEventListener('click', () => term && term.clear());
  const pwInput = $('#ssh-pw');
  if (pwInput) pwInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') doConnect(); });
  $('#remember-pw').addEventListener('change', (e) => {
    if (!e.target.checked) { api('/api/nodes/' + n.id, { method: 'PATCH', body: { remember: false } }).catch(() => {}); remembered = false; }
  });
  setState('未连接', 'warn');
}

/* ---------- 文件 / 日志 ---------- */

function tabFiles(body, ctx) {
  const { id } = ctx;
  body.innerHTML = `
  <div class="grid cols-2">
    <div class="card">
      <h3>目录浏览（只读）</h3>
      <div class="row" style="margin-bottom:8px">
        <input type="text" id="f-path" value="/var/log" style="flex:3">
        <button class="btn" id="f-go" style="flex:0 0 auto">查看</button>
      </div>
      <pre class="out" id="f-out">输入路径后点击查看</pre>
    </div>
    <div class="card">
      <h3>日志尾部</h3>
      <div class="row" style="margin-bottom:8px">
        <input type="text" id="l-path" value="/var/log/syslog" style="flex:3">
        <input type="number" id="l-lines" value="200" style="flex:0 0 92px">
        <button class="btn" id="l-go" style="flex:0 0 auto">读取</button>
      </div>
      <pre class="out" id="l-out">常用日志：/var/log/syslog、/var/log/messages、/var/log/nginx/access.log、
/var/log/auth.log、journal 可用 /var/log/journal</pre>
    </div>
  </div>
  <p class="mute" style="font-size:12px">命令通过节点自身的 SSH 通道执行（需要 agent 安装时设置了密码）。出于安全考虑只允许浏览
  /var/log、/etc、/tmp、/var/lib、/opt、/srv、/home 下的路径。</p>`;
  $('#f-go').addEventListener('click', async () => {
    $('#f-out').textContent = '读取中…';
    try { const r = await api(`/api/nodes/${id}/files?path=` + encodeURIComponent($('#f-path').value)); $('#f-out').textContent = r.output || r.error || '(空)'; }
    catch (e) { $('#f-out').textContent = '失败: ' + e.message; }
  });
  $('#l-go').addEventListener('click', async () => {
    $('#l-out').textContent = '读取中…';
    const p = `/api/nodes/${id}/logs?path=` + encodeURIComponent($('#l-path').value) + '&lines=' + encodeURIComponent($('#l-lines').value);
    try { const r = await api(p); $('#l-out').textContent = r.output || r.error || '(空)'; }
    catch (e) { $('#l-out').textContent = '失败: ' + e.message; }
  });
}

/* ---------- 备注 / 任务 ---------- */

function tabNote(body, ctx) {
  const { id, n } = ctx;
  body.innerHTML = `
  <div class="grid cols-2">
    <div class="card">
      <h3>服务器备注</h3>
      <textarea id="note-text" rows="9" placeholder="例如：主站前端，Nginx + PHP；到期日 2026-03；仅允许公司 IP">${esc(n.note || '')}</textarea>
      <div class="row" style="margin-top:10px">
        <button class="btn primary" id="note-save" style="flex:0 0 auto">保存备注</button>
        <span class="mute" id="note-state" style="font-size:12px"></span>
      </div>
      <h3 style="margin-top:18px">基本信息</h3>
      <div class="kv">
        <div class="k">节点 ID</div><div class="mono">${esc(n.id)}</div>
        <div class="k">地址</div><div class="mono">${esc(n.host)}:${n.port || '-'}</div>
        <div class="k">接入方式</div><div>${n.mode === 'tunnel' ? 'cloudflared 内网穿透' : n.mode === 'domain' ? '域名反代' : 'IP 直连'}</div>
        <div class="k">SSH</div><div class="mono">${esc(n.ssh_user || 'root')}@${esc(n.host)}:${n.ssh_port || 22} ${n.use_relay ? '(Agent 中继)' : ''}</div>
        <div class="k">创建时间</div><div>${fmt.datetime(n.created_at)}</div>
        <div class="k">历史数据点</div><div>${fmt.int((ctx.data.series || {}).points || 0)} 个 · ${fmt.bytes((ctx.data.series || {}).bytes || 0, 1)}</div>
      </div>
    </div>
    <div class="card">
      <h3>该节点的待办</h3>
      <div class="row" style="margin-bottom:10px">
        <input type="text" id="todo-input" placeholder="添加一条与该服务器相关的待办…">
        <button class="btn primary" id="todo-add" style="flex:0 0 auto">添加</button>
      </div>
      <div id="node-todos"><span class="spin"></span></div>
    </div>
  </div>`;
  $('#note-save').addEventListener('click', async () => {
    $('#note-state').innerHTML = '<span class="spin"></span>';
    try {
      await api('/api/nodes/' + id, { method: 'PATCH', body: { note: $('#note-text').value } });
      $('#note-state').textContent = '已保存 ' + new Date().toLocaleTimeString('zh-CN');
    } catch (e) { $('#note-state').textContent = '保存失败: ' + e.message; }
  });
  const loadTodos = async () => {
    const d = await api('/api/todos');
    const list = d.todos.filter(t => t.node_id === id);
    $('#node-todos').innerHTML = todoList(list, false) || '<div class="empty">暂无待办</div>';
    bindTodoList($('#node-todos'), loadTodos);
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

/* ------------------------------------------------------------------ TODO 页 */

function todoList(todos, compact) {
  if (!todos || !todos.length) return '<div class="empty">暂无待办事项</div>';
  return todos.map(t => `
    <div class="todo ${t.done ? 'done' : ''}" data-id="${t.id}">
      <span class="pri ${t.priority ? 'high' : ''}"></span>
      <input type="checkbox" ${t.done ? 'checked' : ''} data-act="toggle">
      <span class="t">${esc(t.text)}</span>
      ${t.node_id ? `<span class="badge brand" data-node="${t.node_id}">${esc(nodeName(t.node_id))}</span>` : ''}
      ${compact ? '' : `<button class="btn small danger" data-act="del">删除</button>`}
    </div>`).join('');
}

function nodeName(id) {
  const ov = state.overview;
  if (!ov) return id;
  const hit = (ov.nodes || []).find(v => v.node.id === id);
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
      if (reload) reload();
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
  const view = $('#view');
  view.innerHTML = `
  <div class="page-head"><h2>TODO LIST</h2><span class="sub">运维待办、巡检项、续费提醒</span></div>
  <div class="card" style="margin-bottom:14px">
    <div class="row">
      <input type="text" id="t-text" placeholder="新的待办事项…" style="flex:4">
      <select id="t-node" style="flex:1"><option value="">不关联节点</option></select>
      <select id="t-pri" style="flex:0 0 110px"><option value="0">普通</option><option value="1">重要</option></select>
      <button class="btn primary" id="t-add" style="flex:0 0 auto">添加</button>
    </div>
  </div>
  <div class="card" id="t-list"><span class="spin"></span></div>`;
  const ov = await api('/api/overview');
  state.overview = ov;
  const sel = $('#t-node');
  (ov.nodes || []).forEach(v => {
    const o = document.createElement('option');
    o.value = v.node.id; o.textContent = v.node.name;
    sel.appendChild(o);
  });
  const load = async () => {
    const d = await api('/api/todos');
    const open = d.todos.filter(t => !t.done), done = d.todos.filter(t => t.done);
    $('#t-list').innerHTML = `
      <h3>进行中 (${open.length})</h3>${todoList(open, false)}
      ${done.length ? `<h3 style="margin-top:16px">已完成 (${done.length})</h3>${todoList(done, false)}` : ''}`;
    bindTodoList($('#t-list'), load);
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
  const view = $('#view');
  const d = await api('/api/settings');
  const s = d.settings;
  view.innerHTML = `
  <div class="page-head"><h2>设置</h2><span class="sub">面板参数</span></div>
  <div class="grid cols-2">
    <div class="card">
      <h3>采集与告警</h3>
      <div class="row">
        <label class="field"><span>在线节点采集间隔（秒）</span><input type="number" id="set-poll" value="${s.poll_seconds}"></label>
        <label class="field"><span>离线节点重试间隔（秒）</span><input type="number" id="set-offline" value="${s.offline_seconds}"></label>
      </div>
      <div class="row">
        <label class="field"><span>CPU 告警阈值 (%)</span><input type="number" id="set-cpu" value="${s.alert_cpu}"></label>
        <label class="field"><span>内存告警阈值 (%)</span><input type="number" id="set-mem" value="${s.alert_mem}"></label>
        <label class="field"><span>磁盘告警阈值 (%)</span><input type="number" id="set-disk" value="${s.alert_disk}"></label>
      </div>
      <label class="field"><span>历史保留天数</span><input type="number" id="set-keep" value="${s.keep_days}"></label>
      <button class="btn primary" id="set-save">保存设置</button>
      <span class="mute" id="set-state" style="font-size:12px;margin-left:8px"></span>
    </div>
    <div class="card">
      <h3>安全</h3>
      <label class="field"><span>修改面板密码（至少 4 位）</span><input type="password" id="set-pw" placeholder="留空则不修改"></label>
      <button class="btn" id="set-pw-save">修改密码</button>
      <h3 style="margin-top:18px">运行信息</h3>
      <div class="kv">
        <div class="k">面板版本</div><div class="mono">v${esc(d.version)}</div>
        <div class="k">数据目录</div><div class="mono">${esc(d.dir)}</div>
        <div class="k">监听地址</div><div class="mono">${esc(s.listen)}</div>
        <div class="k">节点数量</div><div>${(state.overview ? state.overview.summary.nodes : '-')}</div>
      </div>
      <h3 style="margin-top:18px">节点接入</h3>
      <p class="mute" style="font-size:12.5px">在目标服务器上执行安装命令（root）：</p>
      <div class="copy-box"><input type="text" readonly id="cmd-1" value="sudo ./nodemgr-agent install -port 8899 -password '你的root密码'">
        <button class="btn small" data-copy="cmd-1">复制</button></div>
      <div class="copy-box" style="margin-top:6px"><input type="text" readonly id="cmd-2" value="sudo ./nodemgr-agent install -password '你的root密码' -tunnel">
        <button class="btn small" data-copy="cmd-2">复制</button></div>
      <p class="mute" style="font-size:12px">第二条用于没有公网端口、需要 cloudflared 内网穿透的服务器；
      安装完成后终端会输出<b>访问令牌</b>与隧道地址，填到本面板即可。</p>
    </div>
  </div>`;
  $('#set-save').addEventListener('click', async () => {
    $('#set-state').innerHTML = '<span class="spin"></span>';
    try {
      await api('/api/settings', { method: 'PATCH', body: {
        poll_seconds: Number($('#set-poll').value), offline_seconds: Number($('#set-offline').value),
        alert_cpu: Number($('#set-cpu').value), alert_mem: Number($('#set-mem').value),
        alert_disk: Number($('#set-disk').value), keep_days: Number($('#set-keep').value),
      } });
      $('#set-state').textContent = '已保存';
    } catch (e) { $('#set-state').textContent = '失败: ' + e.message; }
  });
  $('#set-pw-save').addEventListener('click', async () => {
    const pw = $('#set-pw').value;
    if (pw.length < 4) { toast('密码至少 4 位', 'err'); return; }
    await api('/api/settings', { method: 'PATCH', body: { password: pw } });
    $('#set-pw').value = '';
    toast('密码已修改', 'ok');
  });
  $$('[data-copy]').forEach(b => b.addEventListener('click', () => {
    const input = document.getElementById(b.dataset.copy);
    input.select(); document.execCommand('copy'); toast('已复制', 'ok');
  }));
}

/* ------------------------------------------------------------------ 添加/编辑节点弹窗 */

function closeModal() { $('#modal-host').innerHTML = ''; }

function nodeDialog(view) {
  const n = view && view.node ? view.node : (view || {});
  const st = view && view.runtime ? view.runtime.stats : null;
  const isEdit = !!n.id;
  $('#modal-host').innerHTML = `
  <div class="modal-host" id="mh">
    <div class="modal">
      <h3>${isEdit ? '编辑节点' : '添加节点'}</h3>
      <div class="row">
        <label class="field"><span>名称 *</span><input type="text" id="n-name" value="${esc(n.name || '')}" placeholder="例如 主站 / HK-01"></label>
        <label class="field"><span>分组</span><input type="text" id="n-group" value="${esc(n.group || '')}" placeholder="例如 生产 / 测试"></label>
      </div>
      <div class="row">
        <label class="field" style="flex:3"><span>IP 或域名 *</span>
          <input type="text" id="n-host" value="${esc(n.host || '')}" placeholder="1.2.3.4 或 srv.example.com 或 xxx.trycloudflare.com"></label>
        <label class="field" style="flex:1"><span>端口</span>
          <input type="number" id="n-port" value="${n.port ?? 8899}" placeholder="8899"></label>
      </div>
      <label class="field"><span>接入方式</span>
        <select id="n-mode">
          <option value="direct" ${n.mode === 'direct' || !n.mode ? 'selected' : ''}>IP/域名 + 端口（agent 直接监听）</option>
          <option value="domain" ${n.mode === 'domain' ? 'selected' : ''}>域名反向代理（80/443）</option>
          <option value="tunnel" ${n.mode === 'tunnel' ? 'selected' : ''}>cloudflared 内网穿透（无端口，自动 https）</option>
        </select></label>
      <label class="field"><span>协议</span>
        <select id="n-scheme">
          <option value="http" ${n.scheme !== 'https' ? 'selected' : ''}>http</option>
          <option value="https" ${n.scheme === 'https' ? 'selected' : ''}>https</option>
        </select></label>
      <label class="field"><span>访问令牌 *（agent 安装时输出）</span>
        <input type="text" id="n-token" value="${esc(n.token || '')}" placeholder="安装 nodemgr-agent 后终端会打印令牌"></label>
      <label class="field"><span>服务器备注</span>
        <textarea id="n-note" rows="2" placeholder="用途、到期时间、注意事项…">${esc(n.note || '')}</textarea></label>
      <div class="row">
        <label class="field"><span>SSH 用户名</span><input type="text" id="n-sshuser" value="${esc(n.ssh_user || 'root')}"></label>
        <label class="field"><span>SSH 端口</span><input type="number" id="n-sshport" value="${n.ssh_port || 22}"></label>
      </div>
      <label class="field" style="display:flex;gap:8px;align-items:center">
        <input type="checkbox" id="n-relay" ${n.use_relay ? 'checked' : ''} style="width:auto">
        <span style="margin:0">通过 Agent 中继 SSH（节点没有开放 22 端口时使用）</span>
      </label>
      <label class="field"><span>采集间隔（秒，默认跟随面板设置）</span>
        <input type="number" id="n-interval" value="${n.interval || 5}"></label>
      ${st ? `<div class="mute" style="font-size:12px">当前连接：${esc(st.host.hostname)} · ${esc(st.host.os)} · ${st.host.cpu_cores} 核 · agent v${esc(st.version)}</div>` : ''}
      <div class="actions">
        ${isEdit ? `<button class="btn danger" id="n-del">删除节点</button>` : ''}
        <div class="spacer"></div>
        <button class="btn" id="n-cancel">取消</button>
        <button class="btn primary" id="n-save">${isEdit ? '保存' : '添加并连接'}</button>
      </div>
    </div>
  </div>`;
  $('#n-cancel').addEventListener('click', closeModal);
  $('#mh').addEventListener('click', (e) => { if (e.target.id === 'mh') closeModal(); });
  if (isEdit) $('#n-del').addEventListener('click', async () => {
    if (!confirm(`确定删除节点「${n.name}」？其历史数据也会被清除。`)) return;
    await api('/api/nodes/' + n.id, { method: 'DELETE' });
    closeModal(); toast('节点已删除', 'ok'); go('#/nodes');
  });
  $('#n-save').addEventListener('click', async () => {
    const body = {
      name: $('#n-name').value.trim(), host: $('#n-host').value.trim(),
      port: Number($('#n-port').value) || 0, mode: $('#n-mode').value, scheme: $('#n-scheme').value,
      token: $('#n-token').value.trim(), note: $('#n-note').value, group: $('#n-group').value.trim(),
      ssh_user: $('#n-sshuser').value.trim() || 'root', ssh_port: Number($('#n-sshport').value) || 22,
      use_relay: $('#n-relay').checked, interval: Number($('#n-interval').value) || 5,
    };
    if (!body.name) body.name = body.host;
    if (!body.host) { toast('请填写 IP 或域名', 'err'); return; }
    if (!body.token) { toast('请填写节点访问令牌', 'err'); return; }
    try {
      if (isEdit) await api('/api/nodes/' + n.id, { method: 'PATCH', body });
      else await api('/api/nodes', { method: 'POST', body });
      closeModal();
      toast(isEdit ? '已保存' : '节点已添加，正在连接…', 'ok');
      if (isEdit) go('#/node/' + n.id); else go('#/nodes');
    } catch (e) { toast(e.message, 'err'); }
  });
}

/* ------------------------------------------------------------------ 启动 */

async function boot() {
  try {
    const r = await fetch('/api/overview', { credentials: 'same-origin' });
    state.authed = r.status === 200;
  } catch { state.authed = false; }
  if (!state.authed) { renderLogin(); return; }
  renderShell();
  route();
}

let suppressRoute = false;
window.addEventListener('hashchange', () => {
  if (suppressRoute) { suppressRoute = false; return; }
  route();
});
window.addEventListener('beforeunload', clearTimers);
window.addEventListener('error', (e) => { window.__npErr = String(e.message) + ' @' + e.filename + ':' + e.lineno; });
boot().catch((e) => { window.__npErr = 'boot: ' + (e && e.stack || e); renderLogin && renderLogin(String(e)); });
