# cloudflared 内网穿透使用说明

节点端内置 cloudflared 支持，适合以下场景：

- 服务器**没有公网 IP / 端口不开放**（家宽、内网、公司防火墙）；
- 不想在云安全组里再来一个开放端口；
- 面板所在地无法直连服务器的 8899。

> 面板 ← HTTPS → Cloudflare 边缘 ← 隧道(出站连接) → cloudflared ← HTTP → 127.0.0.1:8899 (agent)

隧道是服务器**主动向外**建立的，因此**不需要开放任何入站端口**。

---

## 一、quick 模式（临时域名，零配置）

```bash
sudo ./nodemgr-agent install -password 'root密码' -tunnel
journalctl -u nodemgr-agent -f | grep trycloudflare
```

看到：

```
cloudflared 公网地址: https://plain-example-abc.trycloudflare.com
```

面板里添加节点：

| 字段 | 值 |
| --- | --- |
| 地址 | `https://plain-example-abc.trycloudflare.com` |
| 端口 | 留空 |
| 接入方式 | cloudflared 内网穿透 |
| 令牌 | 安装时打印的访问令牌 |

**特点**：不用 Cloudflare 账号，但域名在**每次 cloudflared 重启后随机变化**，
域名变化后需要到面板里更新节点地址。适合临时查看/演示。

> 小技巧：面板可以先用着旧地址，等域名变了以后打开节点「编辑」改成新地址即可，
> 历史数据不会丢（历史存在面板端）。

---

## 二、token 模式（自有域名，推荐长期使用）

### 1. 在 Cloudflare Zero Trust 创建隧道

1. 打开 <https://one.dash.cloudflare.com> → **Networks → Tunnels → Create a tunnel**；
2. 选择 **Cloudflared**，命名（例如 `node-web01`），保存；
3. 复制页面给出的 **token**（形如 `eyJhIjoi...` 的长字符串）；
4. 在 **Public Hostname** 标签添加一条：
   - Subdomain: `node1`，Domain: `example.com`
   - Service: `HTTP` → `127.0.0.1:8899`
5. 保存。

### 2. 在服务器安装

```bash
sudo ./nodemgr-agent install -password 'root密码' -port 0 \
     -tunnel -tunnel-mode token -tunnel-token 'eyJhIjoi...'
```

`-port 0` 让 agent 不再监听公网端口，只监听 `127.0.0.1:8899`，由 cloudflared 转发。

### 3. 面板添加节点

| 字段 | 值 |
| --- | --- |
| 地址 | `node1.example.com` |
| 端口 | 留空 |
| 协议 | https |
| 接入方式 | cloudflared 内网穿透 |

### 4. SSH 也要穿透？

勾选节点「编辑」里的 **通过 Agent 中继 SSH**。密码走加密的 WebSocket：

```
浏览器 ── WS ──▶ 面板 ── WS(https) ──▶ Cloudflare ── 隧道 ──▶ agent ── SSH ──▶ 127.0.0.1:22
```

---

## 三、手工安装 cloudflared（可选）

agent 找不到 cloudflared 时会自动从官方 releases 下载到 `/etc/nodemgr-agent/cloudflared`。
如果服务器无法访问 GitHub：

```bash
# 在能上网的机器上下载后 scp 到服务器
curl -L -o cloudflared \
  https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64
scp cloudflared root@server:/usr/local/bin/cloudflared
ssh root@server 'chmod +x /usr/local/bin/cloudflared'

# 或者禁用自动下载，只使用系统已安装的
NODEMGR_NO_DOWNLOAD=1
```

配置里也可以指定二进制路径：编辑 `/etc/nodemgr-agent/config.json` 的 `tunnel.binary`。

---

## 四、排查

| 现象 | 排查 |
| --- | --- |
| 看不到 trycloudflare 地址 | `journalctl -u nodemgr-agent -n 100`，看 cloudflared 是否报错（DNS/出站 443 被拦） |
| 面板显示离线 | 面板侧探测该 https 地址；确认隧道 Public Hostname 指向 `127.0.0.1:8899` |
| token 模式启动失败 | token 是否完整（含 `=`）、隧道是否已添加 Public Hostname |
| 域名变了 | quick 模式的固有行为，改用 token 模式 |
| 想让面板也走 Cloudflare | 面板在本机运行，只监听 127.0.0.1，通常不需要 |

API 层面可以直接验证隧道是否通：

```bash
curl -H "Authorization: Bearer <令牌>" https://node1.example.com/api/v1/ping
```
