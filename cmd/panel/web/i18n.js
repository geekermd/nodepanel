/* nodepanel · 国际化（中/英）
   词典以中文原文为 key，缺失时回退到原文，方便增量翻译。 */

const EN = {
  // 通用
  '总览': 'Overview', '节点': 'Nodes', '设置': 'Settings', '待办': 'Tasks',
  '节点详情': 'Node detail', '返回': 'Back', '刷新': 'Refresh', '编辑': 'Edit',
  '删除': 'Delete', '删除节点': 'Delete node', '取消': 'Cancel', '保存': 'Save',
  '保存备注': 'Save note', '添加': 'Add', '添加节点': 'Add node', '添加并连接': 'Add & connect',
  '探测': 'Probe', '重连': 'Reconnect', '清屏': 'Clear', '连接': 'Connect',
  '连接终端': 'Connect', '复制': 'Copy', '已复制': 'Copied', '登录': 'Sign in',
  '退出登录': 'Sign out', '加载中…': 'Loading…', '暂无数据': 'No data yet', '暂无': 'None',
  '在线': 'Online', '离线': 'Offline', '等待采集': 'Waiting', '正常': 'OK',
  '失败': 'Failed', '加载失败': 'Load failed', '读取失败': 'Read failed', '保存失败': 'Save failed',
  '未连接': 'Disconnected', '连接中…': 'Connecting…', '已连接': 'Connected', '已断开': 'Disconnected',
  '连接失败': 'Connection failed', '连接错误': 'Connection error', '未知': 'Unknown',
  '全部': 'All', '近 7 天': 'Last 7 days', '近 14 天': 'Last 14 days', '今日': 'Today',
  '永久': 'Never', '已过期': 'Expired', '剩余': 'Left', '到期': 'Expires', '到期时间': 'Expiry',

  // 面板 / 概览
  '多节点服务器管理面板 · 本机运行': 'Multi-node server manager · runs locally',
  '管理密码': 'Admin password', '首次运行请查看面板终端输出': 'See the panel terminal output on first run',
  '密码保存在 ~/.nodepanel/panel.json，修改：nodemgr-panel -set-password 新密码':
    'Stored in ~/.nodepanel/panel.json · change with: nodemgr-panel -set-password <new>',
  '登录已过期，请重新登录': 'Session expired, please sign in again',
  '全部节点资源与访问量': 'All nodes at a glance',
  '台在线': 'online', '累计流量': 'Total traffic', '访问量': 'Requests', '访问': 'Req',
  '今日访问': 'Today', '平均负载': 'Avg load', 'CPU 平均': 'CPU avg',
  '服务器清单': 'Server inventory', '服务器': 'Server', '状态': 'Status', '延迟': 'Latency',
  '分组': 'Group', '备注': 'Note', '内存': 'Memory', '磁盘': 'Disk', '端口': 'Port',
  '下载': 'Down', '上传': 'Up', '累计下行': 'Total down', '累计上行': 'Total up',
  '终端': 'Terminal', '历史数据点': 'History points',
  '全部在线': 'All online', '个离线': 'offline', '待办事项': 'Tasks',
  '还没有节点。点右上角「添加节点」，或在服务器上安装 nodemgr-agent 后填入地址与令牌。':
    'No nodes yet. Use "Add node" — or install nodemgr-agent on a server and enter its address and token.',

  // 节点卡片
  '主机名': 'Hostname', '内网 IP': 'Private IP', '公网 IP': 'Public IP',
  '面板观测 IP': 'Observed IP', '网卡 MAC': 'NIC MAC', '网卡': 'Interface',
  '系统': 'OS', '内核': 'Kernel', '虚拟化': 'Virtualization', '运行时长': 'Uptime',
  '进程 / 线程': 'Processes / threads', 'TCP 连接': 'TCP conns', '磁盘挂载': 'Filesystems',
  '系统信息': 'System', '流量统计': 'Traffic', '挂载点': 'Mount', '设备': 'Device',
  '类型': 'Type', '使用率': 'Usage', '已用 / 总量': 'Used / total',
  'Agent 累计下行': 'Agent total down', 'Agent 累计上行': 'Agent total up',
  '面板统计下行': 'Panel total down', '面板统计上行': 'Panel total up',
  '今日请求': 'Requests today', '建立': 'established', '监听': 'listen', '采样': 'interval',
  '缓存': 'buffer', '负载': 'Load', '耗时': 'took',

  // 监控
  '监控': 'Metrics', '服务': 'Services', '服务/访问量': 'Services', '进程': 'Processes',
  '文件/日志': 'Files', '备注/任务': 'Notes',
  'CPU 使用率': 'CPU usage', '磁盘 I/O': 'Disk I/O', '网络': 'Network',
  'CPU / 内存 使用率': 'CPU / memory usage', '网络吞吐': 'Network throughput',
  '1 小时': '1h', '6 小时': '6h', '24 小时': '24h', '7 天': '7d',
  '还没有历史数据，等待采集…': 'No history yet, waiting for samples…',
  '读取历史失败：{0}': 'Failed to read history: {0}',
  '读': 'Read', '写': 'Write', '下行': 'Down', '上行': 'Up',
  '累计访问量': 'Total requests', '监听端口': 'Listening ports', 'HTTP 探测': 'HTTP probe',
  '监听端口与服务': 'Listening ports & services', '近 14 天访问量': 'Requests (14d)',
  '近 14 天流量': 'Traffic (14d)', '面板侧连通性探测': 'Reachability probe from panel',
  '探测 URL（默认节点地址）': 'Probe URL (defaults to node address)',
  '探测中…': 'Probing…', '没有检测到监听端口（可能需要 root 权限读取 /proc/net/tcp）':
    'No listening ports found (reading /proc/net/tcp may require root)',
  '还没有历史统计，面板运行一段时间后这里会显示每天的数据':
    'No daily stats yet — they appear after the panel has been running for a while',
  '进程 Top': 'Top processes', '命令': 'Command', '线程': 'Threads', '状态码': 'State',
  'CPU 为该进程在两次采样间的平均占用（按 100% = 单核计）。':
    'CPU is the average between two samples (100% = one core).',
  '目录浏览（只读）': 'Directory browse (read-only)', '查看': 'List', '日志尾部': 'Log tail',
  '读取': 'Read', '常用日志：/var/log/syslog、/var/log/messages、/var/log/nginx/access.log、':
    'Common logs: /var/log/syslog, /var/log/messages, /var/log/nginx/access.log, ',
  '输入路径后点击查看': 'Enter a path and click List',
  '命令通过节点自身的 SSH 通道执行（需要 agent 安装时设置了密码）。出于安全考虑只允许浏览':
    'Commands run over the node\'s own SSH channel (agent must be installed with a password). For safety only paths under',
  '下的路径。': 'are allowed.',
  '服务器备注': 'Server note', '基本信息': 'Details', '节点 ID': 'Node ID', '地址': 'Address',
  '接入方式': 'Access mode', '创建时间': 'Created', '该节点的待办': 'Tasks for this node',
  '添加一条与该服务器相关的待办…': 'Add a task for this server…',
  '直接点「连接终端」': 'just click Connect',
  '已记住密码，直接点「连接终端」。': 'Password is remembered — just click Connect.',
  '记住密码（仅本机保存）': 'Remember password (this machine only)',
  '密码只用于本次 SSH 认证，不经 URL 传递': 'The password is used for this SSH login only and is never put in a URL',
  '；勾选「记住密码」后保存在本机面板数据目录。': '; when remembered it is stored in the local panel data directory.',
  '。': '.',
  'SSH 密码（{0}@{1}）': 'SSH password ({0}@{1})',
  '留空则使用已保存的密码': 'Leave empty to use the saved password',
  '经 Agent 中继（适合内网穿透节点）': 'Relayed by the agent (for tunnel-only nodes)',
  '面板直连': 'Direct from panel', '通过 SSH 隧道': 'Over SSH tunnel',
  '若节点只能通过 cloudflared 访问，请在「编辑」里打开「通过 Agent 中继 SSH」，':
    'If the node is only reachable through cloudflared, enable "Relay SSH through agent" in Edit; ',
  '终端会改为经节点自身的 sshd 登录。': 'the terminal then logs in via the node\'s own sshd.',
  '已保存密码': 'Saved password', '显示': 'Show', '隐藏': 'Hide',
  '查看/修改 SSH 密码': 'View / change SSH password',

  // TODO
  '运维待办、巡检项、续费提醒': 'Ops todos, checks and renewals',
  '新的待办事项…': 'New task…', '不关联节点': 'No node', '普通': 'Normal', '重要': 'High',
  '进行中': 'Open', '已完成': 'Done', '暂无待办事项': 'No tasks',
  '添加一条与该服务器相关的待办…': 'Add a task for this server…',

  // 设置
  '面板参数': 'Panel settings', '采集与告警': 'Polling & alerts', '安全': 'Security',
  '在线节点采集间隔（秒）': 'Poll interval, online (s)',
  '离线节点重试间隔（秒）': 'Retry interval, offline (s)',
  'CPU 告警阈值 (%)': 'CPU alert (%)', '内存告警阈值 (%)': 'Memory alert (%)',
  '磁盘告警阈值 (%)': 'Disk alert (%)', '历史保留天数': 'History retention (days)',
  '保存设置': 'Save settings', '已保存': 'Saved', '修改面板密码（至少 4 位）': 'New panel password (min 4)',
  '留空则不修改': 'leave empty to keep', '修改密码': 'Change password', '运行信息': 'Runtime',
  '面板版本': 'Panel version', '数据目录': 'Data directory', '监听地址': 'Listen address',
  '节点数量': 'Nodes', '节点接入': 'Add a node', '界面语言': 'Language',
  '在目标服务器上执行安装命令（root）：': 'Run this on the target server (as root):',
  '第二条用于没有公网端口、需要 cloudflared 内网穿透的服务器；':
    'The second one is for servers without a public port that need a cloudflared tunnel; ',
  '安装完成后终端会输出访问令牌与隧道地址，填到本面板即可。':
    'the installer prints the access token and tunnel URL — paste them here.',
  '密码至少 4 位': 'Password must be at least 4 characters', '密码已修改': 'Password changed',

  // 弹窗
  '名称': 'Name', 'IP 或域名': 'IP or domain', '协议': 'Scheme',
  '访问令牌（agent 安装时输出）': 'Access token (printed by the agent installer)',
  '服务器备注': 'Server note', 'SSH 用户名': 'SSH user', 'SSH 端口': 'SSH port',
  'SSH 密码': 'SSH password', '通过 Agent 中继 SSH（节点没有开放 22 端口时使用）':
    'Relay SSH through the agent (when port 22 is not exposed)',
  '通过 SSH 隧道访问 agent（只需 SSH 端口，推荐云服务器）':
    'Reach the agent over an SSH tunnel (only SSH port needed; recommended for cloud servers)',
  '采集间隔（秒，默认跟随面板设置）': 'Poll interval (s, defaults to panel setting)',
  'IP/域名 + 端口（agent 直接监听）': 'IP/domain + port (agent listens publicly)',
  '域名反向代理（80/443）': 'Domain reverse proxy (80/443)',
  'cloudflared 内网穿透（无端口，自动 https）': 'cloudflared tunnel (no port, https)',
  'SSH 隧道（agent 只监听 127.0.0.1）': 'SSH tunnel (agent bound to 127.0.0.1)',
  '到期时间（留空表示永久）': 'Expiry date (empty = never)',
  '用途、到期时间、注意事项…': 'Purpose, expiry, notes…',
  '例如 主站 / HK-01': 'e.g. web-01 / HK-01',
  '例如 生产 / 测试': 'e.g. production / test',
  '请填写 IP 或域名': 'IP or domain is required',
  '请填写节点访问令牌': 'Node access token is required',
  '确定删除节点「{0}」？其历史数据也会被清除。': 'Delete node "{0}"? Its history will be removed too.',
  '节点已删除': 'Node deleted', '已保存': 'Saved', '节点已添加，正在连接…': 'Node added, connecting…',
  '尚未采集': 'Not collected yet', '未启用': 'Disabled', '已启用': 'Enabled',
  '测试连接': 'Test connection', '测试中…': 'Testing…',
  'SSH 登录成功': 'SSH login OK',

  // 状态 / 错误
  '未登录': 'Not signed in', '密码错误': 'Wrong password', '令牌无效': 'Invalid token',
  '节点不存在': 'Node not found', '节点不可达': 'Node unreachable',
  '未配置 SSH 密码或私钥': 'No SSH password or key configured',
  '第二条把 agent 限制在 127.0.0.1，配合「SSH 隧道」接入方式使用；第三条用于没有公网端口、需要 cloudflared 内网穿透的服务器。':
    'The second pins the agent to 127.0.0.1 and pairs with the "SSH tunnel" access mode; the third is for servers with no public port that need a cloudflared tunnel.',
  '该节点未启用 SSH 中继：请在节点上带 -ssh-password=<root密码> 重启 agent':
    'SSH relay is disabled on this node: restart the agent with -ssh-password=<root password>',
};

let LANG = (typeof localStorage !== 'undefined' && localStorage.getItem('np_lang')) || 'zh';

export function setLang(lang) {
  LANG = lang === 'en' ? 'en' : 'zh';
  try { localStorage.setItem('np_lang', LANG); } catch { /* ignore */ }
  document.documentElement.lang = LANG === 'en' ? 'en' : 'zh-CN';
}

export function getLang() { return LANG; }

/** t('中文原文') 或 t('带 {0} 的文案', 值…) */
export function t(zh, ...args) {
  let out = zh;
  if (LANG === 'en') out = EN[zh] || zh;
  if (args.length) {
    args.forEach((v, i) => { out = out.split('{' + i + '}').join(String(v)); });
  }
  return out;
}

export function isEN() { return LANG === 'en'; }
