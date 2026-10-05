# nodepanel · 轻量多节点服务器管理面板

在一台电脑上管理多台服务器：资源监控（CPU / 内存 / 磁盘 IO / 网络流量）、访问量统计、
SSH 网页终端（支持密码）、服务器备注与 TODO LIST。

**为小带宽而生**：节点端是单个静态二进制（Go，无运行时依赖，常驻内存约 10~15 MB），
面板每 5 秒只拉取**增量**数据（每次几 KB），5 Mbit/s 的服务器也能长期跑。
没有公网端口的机器**原生支持 cloudflared 内网穿透**，安装时选 `-tunnel` 即可。

```
┌────────────────────────┐        HTTP(S) 增量拉取        ┌──────────────────────────┐
│  你的电脑               │  ───────────────────────────▶  │  被管理的服务器            │
│  nodemgr-panel (面板)   │   /api/v1/stats  /metrics      │  nodemgr-agent (节点端)   │
│  · Web UI 127.0.0.1:8787│  ◀───────────────────────────  │  · 采集 /proc，2s 一个采样 │
│  · 本地历史库 (JSONL)    │        SSH WebSocket           │  · 可选 SSH 中继           │
│  · TODO / 备注 / 告警    │  ───────────────────────────▶  │  · 可选 cloudflared 隧道   │
└────────────────────────┘                                └──────────────────────────┘
```

---

## 目录

- [功能](#功能)
- [快速开始](#快速开始)
- [在服务器上安装节点端](#在服务器上安装节点端)
- [cloudflared 内网穿透（无公网端口）](#cloudflared-内网穿透无公网端口)
- [面板使用说明](#面板使用说明)
- [SSH 网页终端](#ssh-网页终端)
- [资源与带宽占用](#资源与带宽占用)
- [HTTP API](#http-api)
- [目录结构](#目录结构)
- [自行编译](#自行编译)
- [安全说明](#安全说明)
- [常见问题](#常见问题)

---

## 功能

### 资源监控（每个节点点进去看详情）

| 指标 | 说明 |
| --- | --- |
| CPU | 总使用率、user / system / iowait / steal、1·5·15 分钟负载 |
| 内存 | 已用 / 总量 / 可用 / 缓存、Swap |
| 磁盘 IO | 读写速率、IOPS、设备繁忙度（%util） |
| 磁盘容量 | 每个挂载点的使用率、inode |
| 网络 | 上下行速率、包速率、错误/丢包 |
| TCP | 已建立连接、监听端口、TIME_WAIT |
| 进程 | Top 进程（CPU / 内存 / 线程 / 命令行） |
| 系统信息 | 主机名、发行版、内核、CPU 型号、虚拟化、运行时长、Agent 版本 |

历史曲线：1 小时 / 6 小时 / 24 小时 / 7 天可切换；面板本地保存，节点端只保留最近 5 分钟。

### 访问量统计

- 概览页给出所有节点的**累计请求数 / 今日请求数 / 累计流量**。
- 节点「服务/访问量」页：监听端口清单（含端口对应进程）、HTTP 自检结果、
  面板侧连通性探测、近 14 天访问量与流量柱状图。
- 「访问量」来源是节点上 agent 的请求计数（含面板轮询），按天滚动归档在面板本地。

### 管理与协作

- **TODO LIST**：全局待办 + 每条待办可关联到某个服务器，支持重要标记、完成勾选。
- **服务器备注**：每个节点可写用途、到期日、注意事项，直接显示在概览卡片上。
- **分组**：给节点打分组标签，列表按分组排序。
- **告警**：CPU / 内存 / 磁盘超过阈值时在概览页顶部提示。
- **文件 / 日志**：通过节点自身的 SSH 通道只读浏览目录、查看日志尾部（白名单目录）。

---

## 快速开始

### 1. 电脑端（管理面板）

```bash
#  Linux amd64 示例，其它架构见 releases
chmod +x nodemgr-panel
./nodemgr-panel
```

首次启动会打印管理密码：

```
┌────────────────────────────────────────────────┐
│  nodepanel 首次启动，已生成管理密码            │
└────────────────────────────────────────────────┘
   地址: http://127.0.0.1:8787
   密码: xxxxxxxxxxx
```

浏览器打开 <http://127.0.0.1:8787> 即可。

常用参数：

```bash
./nodemgr-panel -listen 127.0.0.1:8787     # 监听地址（默认只监听本机）
./nodemgr-panel -lan                       # 允许局域网访问（0.0.0.0:8787）
./nodemgr-panel -home ~/.nodepanel         # 数据目录（历史库 / TODO / 配置）
./nodemgr-panel -set-password 新密码        # 修改面板密码后退出
```

### 2. 服务器端（节点）

把 `nodemgr-agent` 传到服务器，然后：

```bash
sudo ./nodemgr-agent install -port 8899 -password '你的root密码'
```

安装脚本会：写入配置 → 安装到 `/usr/local/bin` → 注册 systemd 服务 → 开机自启，
最后打印**访问令牌**：

```
  访问令牌  : dRpcaqJPY4BfZ6ZWY1KKcCuhM3MXMDS3
  面板添加节点时填写：
    地址: web-01
    端口: 8899
    令牌: dRpcaqJPY4BfZ6ZWY1KKcCuhM3MXMDS3
```

### 3. 在面板里添加节点

概览页右上角「+ 添加节点」→ 填名称、IP/域名、端口、令牌 → 保存。
接入方式三选一：

| 接入方式 | 填写 | 适用 |
| --- | --- | --- |
| IP/域名 + 端口 | `1.2.3.4` / `web.example.com`，端口 `8899` | agent 端口可直接访问 |
| 域名反向代理 | `panel.example.com`，端口留空 | 用 Nginx/CDN 反代到 agent（建议 https） |
| cloudflared 内网穿透 | `xxxx.trycloudflare.com`，端口留空 | 服务器没有公网端口 |

---

## 在服务器上安装节点端

### 安装命令

```bash
# 常规：开放 8899 端口
sudo ./nodemgr-agent install -port 8899 -password 'root密码'

# 内网穿透：不开放任何公网端口，用 cloudflared 临时域名
sudo ./nodemgr-agent install -password 'root密码' -tunnel

# 内网穿透 + 自有域名（Cloudflare Zero Trust 里创建的隧道 token）
sudo ./nodemgr-agent install -password 'root密码' \
     -tunnel -tunnel-mode token -tunnel-token 'eyJhIjoi...'
```

`install` 支持的参数：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-port` | `8899` | 公网监听端口；`0` = 只走内网穿透 |
| `-password` | 交互输入 | 管理密码，同时作为 SSH 中继的 root 密码 |
| `-token` | 自动生成 | 面板访问令牌 |
| `-ssh-user` / `-ssh-port` | `root` / `22` | SSH 中继目标 |
| `-ssh-password` | 同 `-password` | 单独指定 SSH 密码 |
| `-tunnel` | 关 | 启用 cloudflared |
| `-tunnel-mode` | `quick` | `quick`（临时域名）/ `token`（自有域名） |
| `-interval` | `2` | 采样间隔（秒） |
| `-history` | `300` | 节点端内存里保留的历史秒数 |

安装后的服务管理：

```bash
systemctl status nodemgr-agent      # 状态
systemctl restart nodemgr-agent     # 重启
journalctl -u nodemgr-agent -f      # 日志（quick 隧道地址也在这里）
```

### 一键安装脚本

仓库里的 `scripts/install-agent.sh` 会自动根据架构下载对应二进制并执行安装：

```bash
curl -fsSL https://raw.githubusercontent.com/geekermd/nodepanel/main/scripts/install-agent.sh \
  | sudo bash -s -- -port 8899 -password 'root密码'
```

### 常用运维

```bash
sudo nodemgr-agent -gen-token          # 重新生成令牌（旧令牌立即失效）
sudo nodemgr-agent uninstall           # 卸载服务与二进制
sudo nodemgr-agent -config /etc/nodemgr-agent/config.json -port 9000   # 临时改端口启动
```

---

## cloudflared 内网穿透（无公网端口）

节点端内置对 cloudflared 的支持，**不需要提前安装**（找不到时会自动从官方 releases 下载到
`/etc/nodemgr-agent/cloudflared`，可用 `NODEMGR_NO_DOWNLOAD=1` 关闭自动下载）。

### 方式 A：quick 临时域名（最省事）

```bash
sudo ./nodemgr-agent install -password 'root密码' -tunnel
journalctl -u nodemgr-agent -f | grep trycloudflare
# 输出：cloudflared 公网地址: https://xxxx-yyyy.trycloudflare.com
```

面板里添加节点：地址填 `https://xxxx-yyyy.trycloudflare.com`，端口留空，接入方式选
「cloudflared 内网穿透」。
⚠️ quick 隧道的域名**每次重启都会变**，长期使用建议方式 B。

### 方式 B：自有域名（token 模式，推荐）

1. Cloudflare Zero Trust → Networks → Tunnels → 新建隧道，复制 token；
2. 在隧道里添加 Public Hostname，例如 `node1.example.com` → `http://127.0.0.1:8899`；
3. 服务器上：

```bash
sudo ./nodemgr-agent install -password 'root密码' -port 0 \
     -tunnel -tunnel-mode token -tunnel-token 'eyJhIjoi...'
```

4. 面板里地址填 `node1.example.com`，端口留空，协议选 https。

`-port 0` 表示 agent 完全不监听公网端口，只监听 `127.0.0.1`，由 cloudflared 转发，安全性最好。

### SSHD 也能穿透：SSH 中继

如果服务器连 22 端口都不开放，只要安装时带了 `-password`，agent 就能**代你连本机 sshd**：
在面板节点「编辑」里勾选 **通过 Agent 中继 SSH**，网页终端即可正常工作，
SSH 密码走的是加密的 WebSocket（面板 ↔ agent）通道。

---

## 面板使用说明

| 页面 | 内容 |
| --- | --- |
| **总览** | 在线/离线数量、累计流量、今日访问量、平均负载、告警条、节点卡片（CPU/内存/磁盘进度条 + 备注）、待办速览 |
| **节点** | 表格清单：状态、延迟、CPU 核数、内存、上下行总量、访问量、分组，可直接编辑或进终端 |
| **节点详情** | 监控 / 服务·访问量 / 进程 / SSH 终端 / 文件·日志 / 备注·任务 六个标签页 |
| **TODO** | 全局待办，可关联节点、标记重要 |
| **设置** | 采集间隔、离线重试间隔、告警阈值、历史保留天数、修改密码、节点接入命令 |

节点详情里还有「面板侧连通性探测」：直接在面板上请求节点的 Web 地址，验证从面板
出去的网络链路是否通（内网穿透场景很有用）。

---

## SSH 网页终端

- 打开节点详情 → **SSH 终端**，首次会弹窗询问 SSH 密码（面板不会把它写进 URL）。
- 勾选「记住密码」后，密码保存在**本机**面板数据目录里（`panel.json`，权限 600）。
- 两种链路自适应：
  - **面板直连**：面板所在机器能访问节点的 22 端口（同一内网/VPN 时最快）；
  - **Agent 中继**：节点只能通过 cloudflared 访问时使用，由节点端自己连本机 sshd。
- 终端支持窗口自适应、复制粘贴、滚动回看（xterm.js 已内置，前端零外链）。

---

## 资源与带宽占用

节点端（agent）：

- 单二进制，Go 编写，**无 CGO、无运行时依赖**，常驻内存约 10~15 MB；
- systemd 单元里默认限制 `MemoryMax=128M`、`CPUWeight=20`、`IOWeight=20`、`Nice=5`；
- 采样只读 `/proc`（含 `/proc/diskstats`、`/proc/net/dev`），2 秒一次，单次开销 < 1 ms；
- 进程列表和端口进程名是**按需**扫描并带缓存，不会持续消耗 CPU。

带宽（面板连一个节点）：

| 行为 | 数据量 |
| --- | --- |
| 每 5 秒轮询 `/stats` + `/metrics?since=` | 约 3~8 KB |
| 折算平均 | 约 1~2 KB/s（≈ 10~16 kbit/s） |
| 每 30 分钟压缩历史 / 每分钟保存用量 | 可忽略 |
| 离线节点 | 自动退避到 15 秒一次，只有失败请求 |

即 50 个节点也只需不到 1 Mbit/s，5 Mbit/s 的服务器完全够用。历史数据存在**面板**本地：
原始 2 秒数据保留 2 小时，之后自动折叠成 1 分钟（24 小时）、5 分钟（90 天）粒度。

---

## HTTP API

节点端（`nodemgr-agent`）所有 `/api/v1/*` 都需要令牌：
`Authorization: Bearer <token>` 或 `?token=<token>`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 存活检查（免鉴权） |
| GET | `/api/v1/ping` | 版本、主机名、采样间隔 |
| GET | `/api/v1/stats` | 主机信息、内存、磁盘、TCP、累计流量、隧道状态、访问计数 |
| GET | `/api/v1/metrics?since=<ms>` | 增量时间序列（列式 JSON，只回传新采样点） |
| GET | `/api/v1/processes?n=30` | Top 进程 |
| GET | `/api/v1/ports` | 监听端口 + 端口对应进程 + HTTP 自检 |
| GET | `/api/v1/disks` | 挂载点容量 |
| GET | `/api/v1/tunnel` | cloudflared 状态与公网地址 |
| POST | `/api/v1/token/rotate` | 轮换令牌 |
| POST | `/api/v1/exec` | 通过本机 SSH 执行命令（面板的文件/日志功能使用） |
| WS | `/api/v1/ssh/relay?token=` | SSH 中继通道 |

面板端（`nodemgr-panel`，本地使用，用会话 Cookie 鉴权）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/login` `/api/logout` | 登录 / 退出 |
| GET | `/api/overview` | 汇总 + 节点卡片数据 + 待办 + 告警 |
| GET/POST | `/api/nodes` | 节点列表 / 新增 |
| GET/PATCH/DELETE | `/api/nodes/{id}` | 节点详情 / 修改 / 删除 |
| GET | `/api/nodes/{id}/metrics?span=3600` | 本地历史（自动选粒度） |
| GET | `/api/nodes/{id}/stats\|processes\|ports\|tunnel` | 代理节点端接口 |
| GET | `/api/nodes/{id}/files?path=` `/logs?path=&lines=` | 目录 / 日志（只读白名单） |
| WS | `/api/nodes/{id}/ssh` | 网页终端 |
| GET/POST | `/api/todos`，PATCH/DELETE `/api/todos/{id}` | 待办 |
| GET/PATCH | `/api/settings` | 面板设置 / 改密码 |
| GET | `/api/probe?url=` | 面板侧连通性探测 |

---

## 目录结构

```
nodepanel/
├── cmd/
│   ├── agent/            # 节点端入口 + install/uninstall 子命令
│   │   └── main.go install.go
│   └── panel/            # 面板入口
│       ├── main.go
│       └── web/          # 内嵌前端（原生 ES Module，无构建步骤）
│           ├── index.html app.js app.css chart.js
│           └── vendor/   # xterm.js（本地内置，不依赖 CDN）
├── internal/
│   ├── shared/           # 公共工具（令牌、URL 拼接、格式化）
│   ├── agent/            # 节点端：/proc 采集、环形缓冲、HTTP/WS、cloudflared
│   ├── panel/            # 面板：轮询器、API、SSH 客户端与中继
│   ├── store/            # 面板本地存储：节点、TODO、时间序列（JSONL + 分粒度归档）
│   └── sshtest/          # SSH 链路的端到端测试（含假 sshd）
├── scripts/
│   ├── build.sh          # 交叉编译到 dist/
│   ├── release.sh        # 打标签 + 编译 + 上传 GitHub Release
│   └── install-agent.sh  # 服务器上一键安装
└── docs/
    ├── cloudflared.md    # 内网穿透详细说明
    └── bandwidth.md      # 带宽与资源占用实测说明
```

---

## 自行编译

需要 Go 1.22+：

```bash
git clone https://github.com/geekermd/nodepanel.git
cd nodepanel

# 本机
go build -o dist/nodemgr-panel ./cmd/panel
go build -o dist/nodemgr-agent ./cmd/agent

# 全平台（amd64 / arm64 / armv7 / 386）
./scripts/build.sh

# 测试
go test ./...
```

交叉编译不需要 CGO：`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/agent`。

---

## 安全说明

- 面板默认只监听 `127.0.0.1`（本机浏览器访问），密码以 SHA-256 哈希保存在
  `~/.nodepanel/panel.json`；用 `-lan` 才会监听所有网卡，此时请自行加 Nginx + HTTPS。
- 节点端令牌同样只存哈希；建议给 agent 开 HTTPS 或只走 cloudflared（自动 HTTPS）。
- SSH 密码仅用于建立 SSH 连接；勾选「记住密码」时明文保存在面板本机数据目录（600 权限），
  不需要就留空。
- 文件/日志浏览只允许 `/var/log`、`/etc`、`/tmp`、`/var/lib`、`/opt`、`/srv`、`/home` 下的路径，
  且只执行面板发起的只读命令。

---

## 常见问题

**Q：面板显示节点离线，怎么排查？**
先点节点详情里的「面板侧连通性探测」，再在服务器上 `curl -H "Authorization: Bearer <令牌>" http://127.0.0.1:8899/api/v1/ping`。
常见原因：云安全组没放行端口、`-bind` 只监听了 127.0.0.1、令牌不匹配。

**Q：cloudflared 域名重启就变了？**
quick 模式本身如此，长期使用请用 `-tunnel-mode token` 配自有域名。

**Q：agent 端口不对外开放，还能看数据吗？**
可以，`-port 0` + cloudflared；连 SSH 也能通过 Agent 中继使用。

**Q：历史数据存哪里？占多少磁盘？**
面板数据目录 `~/.nodepanel/series/<节点ID>/`，单个节点 90 天约 10~30 MB（自动降采样）。

**Q：能不能监控 Windows？**
当前节点端只支持 Linux（读 `/proc`）。Windows 可以只作为面板运行端。

---

MIT License · 见 [LICENSE](LICENSE)
