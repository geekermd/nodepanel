// Package panel is the local controller: it polls every configured node,
// stores the history, serves the web UI and proxies SSH sessions.
package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/geekermd/nodepanel/internal/shared"
	"github.com/geekermd/nodepanel/internal/store"
)

// AgentStats mirrors the agent's /api/v1/stats response.
type AgentStats struct {
	Host struct {
		Hostname  string   `json:"hostname"`
		OS        string   `json:"os"`
		Kernel    string   `json:"kernel"`
		Arch      string   `json:"arch"`
		CPUCores  int      `json:"cpu_cores"`
		CPUModel  string   `json:"cpu_model"`
		BootTime  int64    `json:"boot_time"`
		Virt      string   `json:"virt"`
		PrivateIP string   `json:"private_ip"`
		Iface     string   `json:"iface"`
		PublicIP  string   `json:"public_ip"`
		MACs      []string `json:"macs"`
	} `json:"host"`
	Version  string `json:"version"`
	Uptime   int64  `json:"uptime"`
	Now      int64  `json:"now"`
	Interval int    `json:"interval"`
	History  struct {
		Points      int `json:"points"`
		SpanSec     int `json:"span_sec"`
		IntervalSec int `json:"interval_sec"`
	} `json:"history"`
	Totals struct {
		Rx        float64 `json:"rx"`
		Tx        float64 `json:"tx"`
		DiskRead  float64 `json:"disk_read"`
		DiskWrite float64 `json:"disk_write"`
		RxH       string  `json:"rx_h"`
		TxH       string  `json:"tx_h"`
	} `json:"totals"`
	Mem struct {
		Total     float64 `json:"total"`
		Used      float64 `json:"used"`
		Free      float64 `json:"free"`
		Available float64 `json:"available"`
		Cached    float64 `json:"cached"`
		Buffers   float64 `json:"buffers"`
		SwapTotal float64 `json:"swap_total"`
		SwapFree  float64 `json:"swap_free"`
	} `json:"mem"`
	TCP struct {
		Established int `json:"established"`
		Listen      int `json:"listen"`
		TimeWait    int `json:"time_wait"`
		Total       int `json:"total"`
	} `json:"tcp"`
	Disks []struct {
		Mount      string  `json:"mount"`
		Device     string  `json:"device"`
		FSType     string  `json:"fstype"`
		Total      float64 `json:"total"`
		Used       float64 `json:"used"`
		Free       float64 `json:"free"`
		UsedPct    float64 `json:"used_pct"`
		InodesUsed float64 `json:"inodes_used"`
		InodesFree float64 `json:"inodes_free"`
	} `json:"disks"`
	Tunnel struct {
		Enabled bool   `json:"enabled"`
		Running bool   `json:"running"`
		URL     string `json:"url"`
		Mode    string `json:"mode"`
		Err     string `json:"err"`
	} `json:"tunnel"`
	Ports  []PortInfo `json:"ports"`
	Access struct {
		Requests    int64  `json:"requests"`
		APIRequests int64  `json:"api_requests"`
		LastIP      string `json:"last_ip"`
		LastAt      int64  `json:"last_at"`
		AgentUptime int64  `json:"agent_uptime"`
	} `json:"access"`
	Probes []Probe `json:"probes"`
}

// PortInfo is one listening port reported by the agent.
type PortInfo struct {
	Port        int    `json:"port"`
	Proto       string `json:"proto"`
	Service     string `json:"service"`
	Process     string `json:"process"`
	Established int    `json:"established"`
	TimeWait    int    `json:"time_wait"`
}

// Probe is an HTTP self-probe performed by the agent.
type Probe struct {
	Port   int    `json:"port"`
	Scheme string `json:"scheme"`
	Code   int    `json:"code"`
	Ms     int64  `json:"ms"`
	OK     bool   `json:"ok"`
	Server string `json:"server"`
}

// DayUsage is the per-day access/traffic rollup shown on the node page.
type DayUsage struct {
	Requests int64   `json:"requests"`
	Rx       float64 `json:"rx"`
	Tx       float64 `json:"tx"`
}

// Runtime is the live state of one node, kept in memory only.
type Runtime struct {
	ID        string      `json:"id"`
	Online    bool        `json:"online"`
	Since     int64       `json:"since"` // when the current state started
	CheckedAt int64       `json:"checked_at"`
	LatencyMS int64       `json:"latency_ms"`
	LastErr   string      `json:"last_error"`
	Fails     int         `json:"fails"`
	AgentURL  string      `json:"agent_url"`
	AgentVer  string      `json:"agent_version"`
	RemoteIP  string      `json:"remote_ip"` // 面板观测到的来源 IP（通常即公网 IP）
	Stats     *AgentStats `json:"stats,omitempty"`

	Requests    int64                `json:"requests"` // lifetime (panel side)
	RequestsDay int64                `json:"requests_today"`
	Days        map[string]*DayUsage `json:"days"`
	RxTotal     float64              `json:"rx_total"`
	TxTotal     float64              `json:"tx_total"`
	ProbeOK     bool                 `json:"probe_ok"`
	Probes      []Probe              `json:"probes"`
	UpdatedAt   int64                `json:"updated_at"`
}

// Poller polls every node and keeps the runtime state.
type Poller struct {
	store *store.Store
	mu    sync.RWMutex
	rt    map[string]*Runtime

	// SSH 隧道（mode=ssh 的节点走这里访问 agent）
	tunnels map[string]*sshTunnel
	tmu     sync.Mutex

	// 每个节点复用一个 HTTP 客户端，避免在 SSH 上堆积空闲通道
	clients map[string]*http.Client
	cmu     sync.Mutex
	hc      *http.Client
	stop    chan struct{}
	once    sync.Once
}

// NewPoller builds a poller for the given store.
func NewPoller(st *store.Store) *Poller {
	p := &Poller{
		store:   st,
		rt:      map[string]*Runtime{},
		tunnels: map[string]*sshTunnel{},
		clients: map[string]*http.Client{},
		hc: &http.Client{
			Timeout: 12 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  false,
				DialContext: (&net.Dialer{
					Timeout:   8 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		},
		stop: make(chan struct{}),
	}
	return p
}

// clientFor returns the HTTP client used to reach a node. Nodes in "ssh" mode
// are reached through an SSH direct-tcpip tunnel, so the agent never needs a
// public port — the cloud firewall can keep blocking everything but SSH.
//
// The client is cached per node: creating a fresh transport per request leaks
// SSH channels (each keep-alive holds one) until sshd hits MaxSessions and
// starts refusing new ones.
func (p *Poller) clientFor(ctx context.Context, n *store.Node) *http.Client {
	if n.Mode != store.ModeSSH {
		return p.hc
	}
	p.cmu.Lock()
	defer p.cmu.Unlock()
	if c, ok := p.clients[n.ID]; ok {
		return c
	}
	tun := p.tunnelFor(n.ID)
	tr := &http.Transport{
		MaxIdleConns:        2,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     45 * time.Second,
		DisableCompression:  false,
		DialContext: func(dctx context.Context, network, addr string) (net.Conn, error) {
			return tun.dial(dctx, n, n.SSHPass)
		},
	}
	c := &http.Client{Timeout: 25 * time.Second, Transport: tr}
	p.clients[n.ID] = c
	return c
}

// AgentURL builds the base URL used to reach a node's agent.
func AgentURL(n *store.Node) string {
	scheme := n.Scheme
	if scheme == "" {
		scheme = "http"
	}
	if n.Mode == store.ModeSSH {
		// 真正的连接由 SSH 隧道接管，这里只需要一个稳定的 URL 占位符。
		return fmt.Sprintf("http://%s:%d", n.AgentHost(), n.AgentPortOrDefault())
	}
	if n.Mode == store.ModeTunnel {
		// Tunnel addresses carry their own scheme and never a port.
		port := n.Port
		if port == 0 || port == 80 || port == 443 {
			if strings.Contains(n.Host, "trycloudflare.com") || strings.Contains(n.Host, "cfargotunnel.com") {
				scheme = "https"
			}
			port = 0
		}
		return shared.BaseURL(n.Host, port, scheme)
	}
	return shared.BaseURL(n.Host, n.Port, scheme)
}

// Start begins background polling.
func (p *Poller) Start(ctx context.Context) {
	p.once.Do(func() {
		for _, n := range p.store.ListNodes() {
			node := n
			go p.loop(ctx, node.ID)
		}
		go p.compactLoop(ctx)
		go p.syncLoop(ctx)
	})
}

// Ensure makes sure a node has a polling goroutine.
func (p *Poller) Ensure(ctx context.Context, id string) {
	go p.loop(ctx, id)
}

func (p *Poller) loop(ctx context.Context, id string) {
	p.seed(id)

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		n := p.store.Node(id)
		if n == nil {
			delete(p.runtimeMap(), id)
			return
		}
		if !n.Enabled {
			timer.Reset(time.Duration(maxInt(n.Interval, 5)) * time.Second)
			continue
		}
		err := p.pollOnce(ctx, n)
		wait := time.Duration(maxInt(n.Interval, 2)) * time.Second
		if err != nil {
			// Back off on failure: the panel must stay cheap on a 5 Mbit link.
			fails := p.fails(id)
			wait = time.Duration(minInt(5*(fails+1), p.store.GetSettings().OfflineEvery)) * time.Second
		}
		timer.Reset(wait)
	}
}

func (p *Poller) runtimeMap() map[string]*Runtime { return p.rt }

func (p *Poller) fails(id string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if r, ok := p.rt[id]; ok {
		return r.Fails
	}
	return 0
}

// pollOnce performs one full poll (stats + incremental metrics).
func (p *Poller) pollOnce(ctx context.Context, n *store.Node) error {
	base := AgentURL(n)
	if base == "" {
		return fmt.Errorf("节点未配置地址")
	}
	client := p.clientFor(ctx, n)
	started := time.Now()
	stats, remoteIP, err := p.fetchStats(ctx, client, base, n.Token)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		p.markFailure(n, base, err)
		return err
	}
	// Service probes ride along with the stats call, so a poll costs exactly
	// two small HTTP requests: /stats and /metrics?since=...
	p.markSuccess(n, base, stats, remoteIP, latency)

	// Incremental series fetch: ask only for what we have not seen yet.
	ser := p.store.Series(n.ID)
	last := int64(0)
	if v, ok := ser.Latest(); ok {
		last = v.T
	}
	samples, err := p.fetchSeries(ctx, client, base, n.Token, last)
	if err == nil && len(samples) > 0 {
		if err := ser.Append(samples); err != nil {
			log.Printf("节点 %s 历史写入失败: %v", n.Name, err)
		}
	}
	return nil
}

func (p *Poller) fetchStats(ctx context.Context, client *http.Client, base, token string) (*AgentStats, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", base+"/api/v1/stats", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "nodepanel-panel/"+shared.Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("连接失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error != "" {
			return nil, "", fmt.Errorf("agent 返回 %d: %s", resp.StatusCode, e.Error)
		}
		return nil, "", fmt.Errorf("agent 返回 HTTP %d", resp.StatusCode)
	}
	var st AgentStats
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, "", fmt.Errorf("解析响应失败: %w", err)
	}
	// 隧道场景下 HTTP 头里可能有 Cloudflare 的原始 IP。
	remote := shared.ClientIP(req)
	if v := resp.Header.Get("CF-Connecting-IP"); v != "" {
		remote = v
	}
	return &st, remote, nil
}

// columnar is the agent's metric payload.
type columnar struct {
	Now    int64                `json:"now"`
	Count  int                  `json:"count"`
	Series map[string][]float64 `json:"series"`
}

func (p *Poller) fetchSeries(ctx context.Context, client *http.Client, base, token string, since int64) ([]store.Sample, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	u := base + "/api/v1/metrics"
	if since > 0 {
		u += "?since=" + strconv.FormatInt(since, 10)
	}
	req, err := http.NewRequestWithContext(reqCtx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var c columnar
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, err
	}
	ts := c.Series["t"]
	out := make([]store.Sample, 0, len(ts))
	for i := range ts {
		s := store.Sample{T: int64(ts[i])}
		at := func(k string) float64 {
			v := c.Series[k]
			if i < len(v) {
				return v[i]
			}
			return 0
		}
		s.CPU, s.Usr, s.Sys, s.Iow, s.Steal = at("cpu"), at("usr"), at("sys"), at("iow"), at("steal")
		s.L1, s.L5, s.L15 = at("l1"), at("l5"), at("l15")
		s.MemU, s.MemP, s.MemA, s.SwapP = at("mem_u"), at("mem_p"), at("mem_a"), at("swap_p")
		s.DR, s.DW, s.DIO, s.ROps, s.WOps = at("dr"), at("dw"), at("dio"), at("rops"), at("wops")
		s.Rx, s.Tx, s.Rxp, s.Txp, s.Rxe, s.Rxd = at("rx"), at("tx"), at("rxp"), at("txp"), at("rxe"), at("rxd")
		s.DiskP = at("disk_p")
		s.Procs, s.Run = at("procs"), at("run")
		s.TCPEst, s.TCPTW = at("tcp_est"), at("tcp_tw")
		out = append(out, s)
	}
	return out, nil
}

func (p *Poller) markFailure(n *store.Node, base string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.rt[n.ID]
	if r == nil {
		r = &Runtime{ID: n.ID, Days: map[string]*DayUsage{}}
		p.rt[n.ID] = r
	}
	if r.Online {
		r.Since = time.Now().Unix()
	}
	r.Online = false
	r.Fails++
	r.LastErr = err.Error()
	r.CheckedAt = time.Now().Unix()
	r.AgentURL = base
	r.UpdatedAt = time.Now().Unix()
}

func (p *Poller) markSuccess(n *store.Node, base string, stats *AgentStats, remoteIP string, latency int64) {
	now := time.Now()
	day := now.Format("2006-01-02")
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.rt[n.ID]
	if r == nil {
		r = &Runtime{ID: n.ID, Days: map[string]*DayUsage{}}
		p.rt[n.ID] = r
	}
	if !r.Online {
		r.Since = now.Unix()
	}
	if r.Days == nil {
		r.Days = map[string]*DayUsage{}
	}
	// Access counters are cumulative *within one agent process*; keep a
	// panel-side lifetime total plus a daily rollup for "访问量" statistics.
	if r.Stats != nil && r.Stats.Access.Requests > 0 && stats.Access.Requests >= r.Stats.Access.Requests {
		delta := stats.Access.Requests - r.Stats.Access.Requests
		r.Requests += delta
		r.RequestsDay += delta
		if d := r.Days[day]; d != nil {
			d.Requests += delta
		} else {
			r.Days[day] = &DayUsage{Requests: delta}
		}
	} else if r.Days[day] == nil {
		r.Days[day] = &DayUsage{}
	}
	// Traffic totals: the agent's own counters, tracked as deltas so an agent
	// restart does not produce a bogus spike.
	if r.Stats != nil {
		drx := stats.Totals.Rx - r.Stats.Totals.Rx
		dtx := stats.Totals.Tx - r.Stats.Totals.Tx
		if drx >= 0 && dtx >= 0 {
			r.RxTotal += drx
			r.TxTotal += dtx
			if d := r.Days[day]; d != nil {
				d.Rx += drx
				d.Tx += dtx
			}
		}
	}
	// Prune the daily rollup to the configured retention.
	if len(r.Days) > 400 {
		cut := now.AddDate(0, 0, -p.store.GetSettings().KeepDays).Format("2006-01-02")
		for k := range r.Days {
			if k < cut {
				delete(r.Days, k)
			}
		}
	}
	r.Online = true
	r.Fails = 0
	r.LastErr = ""
	r.CheckedAt = now.Unix()
	r.LatencyMS = latency
	r.AgentURL = base
	r.AgentVer = stats.Version
	r.RemoteIP = remoteIP
	if ip := usableRemoteIP(remoteIP, n); ip != "" {
		r.RemoteIP = ip
	}
	r.Stats = stats
	r.Probes = stats.Probes
	r.ProbeOK = len(stats.Probes) > 0
	r.UpdatedAt = now.Unix()
}

// usableRemoteIP 在"面板观测到的来源 IP"没有意义时（SSH 隧道/本机回环），
// 回退到节点自身的接入地址，保证界面上始终有一个可用的公网 IP。
func usableRemoteIP(remote string, n *store.Node) string {
	ip := net.ParseIP(strings.TrimSpace(remote))
	if ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
		return ip.String()
	}
	host := shared.NormalizeHost(n.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if parsed := net.ParseIP(host); parsed != nil && !parsed.IsLoopback() && !parsed.IsUnspecified() {
		return parsed.String()
	}
	return ""
}

// Runtime returns the live state for a node.
func (p *Poller) Runtime(id string) *Runtime {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r := p.rt[id]
	if r == nil {
		return &Runtime{ID: id, LastErr: "尚未采集", Days: map[string]*DayUsage{}}
	}
	cp := *r
	return &cp
}

// Snapshot returns the live state of every node.
func (p *Poller) Snapshot() map[string]*Runtime {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]*Runtime, len(p.rt))
	for k, v := range p.rt {
		cp := *v
		out[k] = &cp
	}
	return out
}

// Forget drops the runtime state of a removed node and closes its tunnel.
func (p *Poller) Forget(id string) {
	p.mu.Lock()
	delete(p.rt, id)
	p.mu.Unlock()
	p.cmu.Lock()
	if c, ok := p.clients[id]; ok {
		if tr, ok := c.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
		delete(p.clients, id)
	}
	p.cmu.Unlock()
	p.ForgetTunnel(id)
}

// compactLoop folds history and keeps the disk footprint bounded.
func (p *Poller) compactLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, n := range p.store.ListNodes() {
				if err := p.store.Series(n.ID).Compact(); err != nil {
					log.Printf("压缩 %s 历史失败: %v", n.Name, err)
				}
			}
		}
	}
}

// syncLoop persists the runtime rollups (access counts) every minute so they
// survive a panel restart.
func (p *Poller) syncLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.mu.RLock()
			snapshot := make(map[string]*Runtime, len(p.rt))
			for k, v := range p.rt {
				cp := *v
				snapshot[k] = &cp
			}
			p.mu.RUnlock()
			for id, r := range snapshot {
				if err := p.store.WriteRuntimeFile(runtimeFileFrom(id, r)); err != nil {
					log.Printf("保存 %s 用量统计失败: %v", id, err)
				}
			}
		}
	}
}

// ProbeURL issues a request to the node's web endpoint from the panel, which
// is how "访问量/服务状态" is verified from the operator's side.
func (p *Poller) ProbeURL(ctx context.Context, raw string) (int, int64, error) {
	if raw == "" {
		return 0, 0, fmt.Errorf("地址为空")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	if _, err := url.Parse(raw); err != nil {
		return 0, 0, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", raw, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "nodepanel-panel/"+shared.Version)
	started := time.Now()
	resp, err := p.hc.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, time.Since(started).Milliseconds(), nil
}

// RunCommand executes a command on a node through the agent's SSH relay.
func (p *Poller) RunCommand(ctx context.Context, n *store.Node, cmd string) (string, error) {
	base := AgentURL(n)
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	form := url.Values{}
	form.Set("cmd", cmd)
	req, err := http.NewRequestWithContext(reqCtx, "POST", base+"/api/v1/exec", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+n.Token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.clientFor(reqCtx, n).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(body), nil
}

// runtimeFileFrom converts live runtime state into its persisted form.
func runtimeFileFrom(id string, r *Runtime) *store.RuntimeFile {
	days := make(map[string]*store.DayUsage, len(r.Days))
	for k, v := range r.Days {
		days[k] = &store.DayUsage{Requests: v.Requests, Rx: v.Rx, Tx: v.Tx}
	}
	return &store.RuntimeFile{
		ID: id, Requests: r.Requests, RequestsDay: r.RequestsDay,
		Day: time.Now().Format("2006-01-02"), Days: days,
		RxTotal: r.RxTotal, TxTotal: r.TxTotal,
	}
}

// seed loads persisted rollups for a node so access statistics survive a panel
// restart.
func (p *Poller) seed(id string) {
	rf, err := p.store.LoadRuntime(id)
	if err != nil || rf == nil {
		return
	}
	days := make(map[string]*DayUsage, len(rf.Days))
	for k, v := range rf.Days {
		days[k] = &DayUsage{Requests: v.Requests, Rx: v.Rx, Tx: v.Tx}
	}
	if rf.Day != "" && rf.Day != time.Now().Format("2006-01-02") {
		// New day: keep the lifetime total, reset today's counter.
		rf.RequestsDay = 0
	}
	p.mu.Lock()
	r := p.rt[id]
	if r == nil {
		r = &Runtime{ID: id, Days: map[string]*DayUsage{}}
		p.rt[id] = r
	}
	r.Requests = rf.Requests
	r.RequestsDay = rf.RequestsDay
	r.RxTotal = rf.RxTotal
	r.TxTotal = rf.TxTotal
	for k, v := range days {
		r.Days[k] = v
	}
	p.mu.Unlock()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
