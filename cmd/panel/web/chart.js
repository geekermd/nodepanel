/* nodepanel · 极简 SVG 图表（零依赖，单图 < 1000 个点也流畅）
   支持：面积渐变、网格、悬停十字线与提示框、多序列叠加。 */

export const PALETTE = {
  blue: '#4c8dff', green: '#37c07a', cyan: '#35c7d8', amber: '#f2b23e',
  red: '#f0574a', purple: '#a97bf5', pink: '#f072b6', gray: '#8b93a7',
};

export const fmt = {
  bytes(v, digits = 1) {
    if (!isFinite(v)) return '-';
    const neg = v < 0; v = Math.abs(v);
    const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let i = 0;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return (neg ? '-' : '') + (i === 0 ? v.toFixed(0) : v.toFixed(digits)) + ' ' + u[i];
  },
  rate(v) { return fmt.bytes(v) + '/s'; },
  pct(v, d = 1) { return (isFinite(v) ? v.toFixed(d) : '-') + '%'; },
  num(v, d = 2) { return isFinite(v) ? v.toFixed(d) : '-'; },
  int(v) { return isFinite(v) ? Math.round(v).toLocaleString('zh-CN') : '-'; },
  time(ms, withDate = false) {
    const d = new Date(ms);
    const p = (n) => String(n).padStart(2, '0');
    const t = `${p(d.getHours())}:${p(d.getMinutes())}`;
    return withDate ? `${d.getMonth() + 1}/${d.getDate()} ${t}` : t;
  },
  datetime(ms) {
    if (!ms) return '-';
    const d = new Date(ms * 1000);
    const p = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  },
  dur(sec) {
    sec = Math.max(0, Math.round(sec || 0));
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
    if (d) return `${d}天${h}小时`;
    if (h) return `${h}小时${m}分`;
    if (m) return `${m}分${sec % 60}秒`;
    return `${sec}秒`;
  },
};

/* 让坐标轴刻度落在整齐的数值上（避免出现 112% 这种刻度） */
function niceScale(max, min = 0, ticks = 4) {
  if (!isFinite(max) || max <= 0) return { max: 1, min: 0, ticks, step: 1 };
  const span = Math.max(max - min, 1e-9);
  const raw = span / ticks;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const norm = raw / mag;
  const step = (norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 2.5 ? 2.5 : norm <= 5 ? 5 : 10) * mag;
  const niceMax = Math.ceil(max / step) * step;
  return { max: niceMax, min, ticks, step };
}

const SVG_NS = 'http://www.w3.org/2000/svg';
const el = (name, attrs = {}) => {
  const node = document.createElementNS(SVG_NS, name);
  for (const k in attrs) node.setAttribute(k, attrs[k]);
  return node;
};

/* 把序列裁剪成不超过 maxPoints 个点（按时间窗口取平均） */
function downsample(points, maxPoints) {
  if (points.length <= maxPoints) return points;
  const step = Math.ceil(points.length / maxPoints);
  const out = [];
  for (let i = 0; i < points.length; i += step) {
    let sum = 0, n = 0, t = 0;
    for (let j = i; j < Math.min(i + step, points.length); j++) { sum += points[j].v; n++; t = points[j].t; }
    out.push({ t, v: n ? sum / n : 0 });
  }
  return out;
}

/**
 * 渲染一个图表。
 * @param {HTMLElement} host 容器
 * @param {object} opt {series:[{name,color,points:[{t,v}],area,format}], height, yMax, unit, zeroBased}
 */
export function drawChart(host, opt) {
  const series = (opt.series || []).filter(s => s && s.points && s.points.length);
  host.innerHTML = '';
  const height = opt.height || 132;
  if (!series.length) {
    host.innerHTML = '<div class="empty">暂无数据</div>';
    return;
  }
  const W = Math.max(240, host.clientWidth || 600);
  const H = height;
  const padL = 46, padR = 8, padT = 8, padB = 16;

  const needsOwnScale = series.length > 1 && opt.sharedScale === false;
  let all = [];
  const prepared = series.map(s => {
    const pts = downsample(s.points, 700);
    let scale;
    if (needsOwnScale) {
      const mx = Math.max(...pts.map(p => p.v), 0);
      scale = niceScale(opt.yMax || mx, opt.zeroBased === false ? Math.min(...pts.map(p => p.v), 0) : 0, 4);
    } else {
      pts.forEach(p => all.push(p));
    }
    return { ...s, pts, scale };
  });
  const tMin = Math.min(...all.concat(...prepared.map(s => s.pts)).map(p => p.t));
  const tMax = Math.max(...all.concat(...prepared.map(s => s.pts)).map(p => p.t));
  let vMin = 0, vMax = 1, ticks = 4;
  if (needsOwnScale) {
    // 每条曲线独立量程时，网格以第一条序列为准。
    const sc = prepared[0].scale;
    vMin = sc.min; vMax = sc.max; ticks = sc.ticks;
  } else {
    const mx = Math.max(...all.map(p => p.v), 0);
    const mn = opt.zeroBased === false ? Math.min(...all.map(p => p.v), 0) : 0;
    const sc = niceScale(opt.yMax || mx, mn, 4);
    vMin = sc.min; vMax = sc.max; ticks = sc.ticks;
  }
  const spanV = Math.max(vMax - vMin, 1e-9);
  const spanT = Math.max(tMax - tMin, 1);

  const x = (t) => padL + (t - tMin) / spanT * (W - padL - padR);
  const y = (v) => padT + (1 - (v - vMin) / spanV) * (H - padT - padB);
  const yOf = (s, v) => (s.scale ? padT + (1 - (v - s.scale.min) / Math.max(s.scale.max - s.scale.min, 1e-9)) * (H - padT - padB) : y(v));

  const svg = el('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, width: '100%', height: H, preserveAspectRatio: 'none' });

  // 网格与 Y 轴刻度
  for (let i = 0; i <= ticks; i++) {
    const v = vMin + spanV * (i / ticks);
    const yy = y(v);
    svg.appendChild(el('line', { x1: padL, y1: yy, x2: W - padR, y2: yy, stroke: '#1f242e', 'stroke-width': 1 }));
    const label = el('text', { x: padL - 6, y: yy + 3.5, fill: '#6b7488', 'font-size': 10, 'text-anchor': 'end' });
    label.textContent = opt.formatY ? opt.formatY(v) : fmt.num(v, v >= 100 ? 0 : 1);
    svg.appendChild(label);
  }
  // X 轴时间刻度
  for (let i = 0; i <= 3; i++) {
    const t = tMin + spanT * (i / 3);
    const xx = x(t);
    const label = el('text', {
      x: Math.min(Math.max(xx, padL + 12), W - padR - 12), y: H - 3,
      fill: '#6b7488', 'font-size': 10, 'text-anchor': i === 0 ? 'start' : i === 3 ? 'end' : 'middle',
    });
    label.textContent = opt.formatX ? opt.formatX(t) : fmt.time(t, spanT > 86400000);
    svg.appendChild(label);
  }

  const defs = el('defs');
  prepared.forEach((s, i) => {
    if (s.area === false) return;
    const gid = `grad-${Math.random().toString(36).slice(2, 8)}-${i}`;
    const grad = el('linearGradient', { id: gid, x1: 0, y1: 0, x2: 0, y2: 1 });
    grad.appendChild(el('stop', { offset: '0%', 'stop-color': s.color || PALETTE.blue, 'stop-opacity': 0.32 }));
    grad.appendChild(el('stop', { offset: '100%', 'stop-color': s.color || PALETTE.blue, 'stop-opacity': 0.02 }));
    defs.appendChild(grad);
    s._gid = gid;
  });
  svg.appendChild(defs);

  prepared.forEach((s) => {
    const color = s.color || PALETTE.blue;
    let d = '';
    s.pts.forEach((p, i) => { d += `${i ? 'L' : 'M'}${x(p.t).toFixed(1)},${yOf(s, p.v).toFixed(1)}`; });
    if (s._gid) {
      const area = `${d}L${x(s.pts[s.pts.length - 1].t).toFixed(1)},${(H - padB).toFixed(1)}L${x(s.pts[0].t).toFixed(1)},${(H - padB).toFixed(1)}Z`;
      svg.appendChild(el('path', { d: area, fill: `url(#${s._gid})`, stroke: 'none' }));
    }
    svg.appendChild(el('path', { d, fill: 'none', stroke: color, 'stroke-width': 1.6, 'stroke-linejoin': 'round' }));
    const last = s.pts[s.pts.length - 1];
    svg.appendChild(el('circle', { cx: x(last.t), cy: yOf(s, last.v), r: 2.4, fill: color }));
  });

  // 悬停十字线
  const cross = el('line', { x1: 0, y1: padT, x2: 0, y2: H - padB, stroke: '#5b647a', 'stroke-width': 1, 'stroke-dasharray': '3 3', opacity: 0 });
  svg.appendChild(cross);
  host.appendChild(svg);

  const tip = document.createElement('div');
  tip.className = 'chart-tip';
  host.appendChild(tip);

  svg.addEventListener('mousemove', (ev) => {
    const rect = svg.getBoundingClientRect();
    const px = (ev.clientX - rect.left) / rect.width * W;
    cross.setAttribute('x1', px); cross.setAttribute('x2', px); cross.setAttribute('opacity', 1);
    const tAt = tMin + (px - padL) / (W - padL - padR) * spanT;
    const lines = [`<b>${fmt.time(tAt, true)}</b>`];
    prepared.forEach(s => {
      let best = null, bd = Infinity;
      for (const p of s.pts) { const d = Math.abs(p.t - tAt); if (d < bd) { bd = d; best = p; } }
      if (best) {
        const f = s.format || fmt.num;
        lines.push(`<span style="color:${s.color || PALETTE.blue}">■</span> ${s.name || ''} ${f(best.v)}`);
      }
    });
    tip.innerHTML = lines.join('<br>');
    tip.style.left = ((ev.clientX - rect.left) / rect.width * 100) + '%';
    tip.style.top = Math.max(28, ev.clientY - rect.top) + 'px';
    tip.style.opacity = 1;
  });
  svg.addEventListener('mouseleave', () => { cross.setAttribute('opacity', 0); tip.style.opacity = 0; });
}

/* 迷你火花线，用于卡片/表格 */
export function sparkline(values, color = PALETTE.blue, width = 108, height = 26) {
  if (!values || !values.length) return '';
  const max = Math.max(...values, 1), min = Math.min(...values, 0);
  const span = Math.max(max - min, 1e-9);
  const step = values.length > 1 ? width / (values.length - 1) : width;
  let d = '';
  values.forEach((v, i) => { d += `${i ? 'L' : 'M'}${(i * step).toFixed(1)},${(height - 2 - (v - min) / span * (height - 6)).toFixed(1)}`; });
  return `<svg class="chart" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}">
    <path d="${d}" fill="none" stroke="${color}" stroke-width="1.4"/></svg>`;
}

export function seriesOf(samples, key) {
  return samples.map(s => ({ t: s.t, v: Number(s[key]) || 0 }));
}
