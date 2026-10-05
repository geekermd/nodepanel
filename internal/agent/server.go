//go:build linux

package agent

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/geekermd/nodepanel/internal/shared"
)

// Server is the agent HTTP/WebSocket surface.
type Server struct {
	cfg     *Config
	sampler *Sampler
	start   time.Time

	reqCount  atomic.Int64
	apiCount  atomic.Int64
	lastReqIP atomic.Value
	lastReqAt atomic.Int64

	procCache struct {
		mu   sync.Mutex
		at   time.Time
		data []Process
	}
	portCache struct {
		mu   sync.Mutex
		at   time.Time
		data []PortStat
	}
	portScanning atomic.Bool

	tunnel *Tunnel
}

// NewServer builds the agent server.
func NewServer(cfg *Config, s *Sampler) *Server {
	srv := &Server{cfg: cfg, sampler: s, start: time.Now()}
	srv.tunnel = NewTunnel(cfg)
	return srv
}

// Tunnel exposes the tunnel manager (may be nil-safe).
func (s *Server) Tunnel() *Tunnel { return s.tunnel }

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		shared.JSON(w, 200, map[string]any{
			"ok": true, "name": "nodemgr-agent", "version": shared.Version,
			"time": time.Now().Unix(),
		})
	})

	api := http.NewServeMux()
	api.HandleFunc("/api/v1/ping", s.handlePing)
	api.HandleFunc("/api/v1/stats", s.handleStats)
	api.HandleFunc("/api/v1/metrics", s.handleMetrics)
	api.HandleFunc("/api/v1/processes", s.handleProcesses)
	api.HandleFunc("/api/v1/ports", s.handlePorts)
	api.HandleFunc("/api/v1/disks", s.handleDisks)
	api.HandleFunc("/api/v1/tunnel", s.handleTunnel)
	api.HandleFunc("/api/v1/token/rotate", s.handleRotate)
	api.HandleFunc("/api/v1/exec", s.handleExec)
	api.HandleFunc("/api/v1/ssh/relay", s.handleSSHRelay)
	mux.Handle("/api/", s.auth(api))

	// Count every inbound request so the panel can show 访问量 per node.
	return s.countRequests(mux)
}

func (s *Server) countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reqCount.Add(1)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.apiCount.Add(1)
		}
		ip := shared.ClientIP(r)
		s.lastReqIP.Store(ip)
		s.lastReqAt.Store(time.Now().Unix())
		next.ServeHTTP(w, r)
	})
}

// auth guards the API. The token may arrive as a bearer header, a query
// parameter (needed for WebSockets from a browser) or a cookie.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SSH relay performs its own WS-appropriate check but still needs a
		// valid token before we upgrade.
		tok := shared.BearerToken(r)
		if s.cfg.AdminHash == "" {
			shared.Error(w, http.StatusForbidden,
				"agent 尚未设置访问令牌，请在服务器上执行: nodemgr-agent -gen-token")
			return
		}
		if !shared.CheckToken(s.cfg.AdminHash, tok) {
			shared.Error(w, http.StatusUnauthorized, "令牌无效")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	host := s.sampler.host
	var up string
	if host.BootTime > 0 {
		up = shared.Duration(float64(time.Now().Unix() - host.BootTime))
	}
	tun := s.tunnel.Status()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>nodepanel agent · %s</title>
<style>
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,"Noto Sans SC",sans-serif;background:#0f1115;color:#e6e8ee;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center}
.card{background:#171a21;border:1px solid #262b36;border-radius:14px;padding:28px 32px;min-width:320px;box-shadow:0 10px 30px rgba(0,0,0,.4)}
h1{font-size:17px;margin:0 0 4px}
.muted{color:#8b93a7;font-size:13px}
table{margin-top:16px;border-collapse:collapse;font-size:13px;width:100%%}
td{padding:4px 0}td:first-child{color:#8b93a7;padding-right:18px}
code{background:#0b0d11;padding:2px 6px;border-radius:5px;font-size:12px;color:#7ee787}
.ok{color:#3fb950}
</style></head><body><div class="card">
<h1>nodepanel agent <span class="ok">运行中</span></h1>
<div class="muted">本节点已接入管理面板，此页面仅用于连通性验证。</div>
<table>
<tr><td>主机名</td><td>%s</td></tr>
<tr><td>系统</td><td>%s (%s)</td></tr>
<tr><td>架构</td><td>%s · %d 核</td></tr>
<tr><td>运行时长</td><td>%s</td></tr>
<tr><td>Agent 版本</td><td>%s</td></tr>
<tr><td>监听端口</td><td><code>%d</code></td></tr>
<tr><td>内网穿透</td><td>%s</td></tr>
</table>
<div class="muted" style="margin-top:14px">API: <code>GET /api/v1/stats?token=***</code></div>
</div></body></html>`,
		host.Hostname, host.Hostname, host.OS, host.Kernel, host.Arch, host.CPUCores, up,
		shared.Version, s.cfg.Port, tunnelText(tun))
}

func tunnelText(t TunnelStatus) string {
	if !t.Enabled {
		return "未启用"
	}
	if t.URL != "" {
		return "已启用 · " + t.URL
	}
	return "已启用 · 等待地址"
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	shared.JSON(w, 200, map[string]any{
		"ok": true, "version": shared.Version, "hostname": s.sampler.host.Hostname,
		"time": time.Now().Unix(), "interval": s.cfg.Interval,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	rx, tx, dr, dw, n := s.sampler.Totals()
	est, lsn, tw, tot := TCPStats()
	mem := readMemInfo()
	out := map[string]any{
		"host":     s.sampler.Host(),
		"ungent":   shared.Version,
		"version":  shared.Version,
		"uptime":   time.Now().Unix() - s.sampler.host.BootTime,
		"now":      time.Now().Unix(),
		"interval": s.cfg.Interval,
		"history":  map[string]any{"points": n, "span_sec": s.cfg.History, "interval_sec": s.cfg.Interval},
		"totals": map[string]any{
			"rx": rx, "tx": tx, "disk_read": dr, "disk_write": dw,
			"rx_h": shared.Bytes(rx), "tx_h": shared.Bytes(tx),
		},
		"mem": map[string]any{
			"total": mem.Total, "used": mem.Used(), "free": mem.Free, "available": mem.Available,
			"cached": mem.Cached, "buffers": mem.Buffers,
			"swap_total": mem.SwapTotal, "swap_free": mem.SwapFree,
		},
		"tcp":    map[string]any{"established": est, "listen": lsn, "time_wait": tw, "total": tot},
		"disks":  Mounts(),
		"tunnel": s.tunnel.Status(),
		"access": map[string]any{
			"requests": s.reqCount.Load(), "api_requests": s.apiCount.Load(),
			"last_ip": s.lastReqIP.Load(), "last_at": s.lastReqAt.Load(),
			"agent_uptime": int(time.Since(s.start).Seconds()),
		},
		"go": map[string]any{"goroutines": goroutineCount()},
	}
	shared.JSON(w, 200, out)
}

func goroutineCount() int { return runtimeNumGoroutine() }

// handleMetrics returns the time series in a columnar layout: one timestamp
// array plus one array per metric, which is far smaller than an array of
// objects and compresses well over a slow tunnel.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	samples := s.sampler.Ring.Since(since)
	cur, _ := s.sampler.Ring.Last()

	m := map[string][]float64{
		"t": {}, "cpu": {}, "usr": {}, "sys": {}, "iow": {}, "steal": {},
		"l1": {}, "l5": {}, "l15": {}, "mem_u": {}, "mem_p": {}, "mem_a": {}, "swap_p": {},
		"dr": {}, "dw": {}, "dio": {}, "rops": {}, "wops": {},
		"rx": {}, "tx": {}, "rxp": {}, "txp": {}, "rxe": {}, "rxd": {},
		"disk_p": {}, "procs": {}, "run": {}, "tcp_est": {}, "tcp_tw": {},
	}
	for _, v := range samples {
		m["t"] = append(m["t"], float64(v.T))
		m["cpu"] = append(m["cpu"], v.CPU)
		m["usr"] = append(m["usr"], v.Usr)
		m["sys"] = append(m["sys"], v.Sys)
		m["iow"] = append(m["iow"], v.Iow)
		m["steal"] = append(m["steal"], v.Steal)
		m["l1"] = append(m["l1"], v.L1)
		m["l5"] = append(m["l5"], v.L5)
		m["l15"] = append(m["l15"], v.L15)
		m["mem_u"] = append(m["mem_u"], v.MemU)
		m["mem_p"] = append(m["mem_p"], v.MemP)
		m["mem_a"] = append(m["mem_a"], v.MemA)
		m["swap_p"] = append(m["swap_p"], v.SwapP)
		m["dr"] = append(m["dr"], v.DR)
		m["dw"] = append(m["dw"], v.DW)
		m["dio"] = append(m["dio"], v.DIO)
		m["rops"] = append(m["rops"], v.ROps)
		m["wops"] = append(m["wops"], v.WOps)
		m["rx"] = append(m["rx"], v.Rx)
		m["tx"] = append(m["tx"], v.Tx)
		m["rxp"] = append(m["rxp"], v.Rxp)
		m["txp"] = append(m["txp"], v.Txp)
		m["rxe"] = append(m["rxe"], v.Rxe)
		m["rxd"] = append(m["rxd"], v.Rxd)
		m["disk_p"] = append(m["disk_p"], v.DiskP)
		m["procs"] = append(m["procs"], float64(v.Procs))
		m["run"] = append(m["run"], float64(v.Run))
		m["tcp_est"] = append(m["tcp_est"], float64(v.TCPEst))
		m["tcp_tw"] = append(m["tcp_tw"], float64(v.TCPTW))
	}

	shared.JSON(w, 200, map[string]any{
		"now":     time.Now().UnixMilli(),
		"count":   len(samples),
		"series":  m,
		"current": cur,
	})
}

func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	n := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 200 {
		n = v
	}
	shared.JSON(w, 200, map[string]any{"processes": s.cachedProcesses(n), "now": time.Now().Unix()})
}

func (s *Server) cachedProcesses(n int) []Process {
	s.procCache.mu.Lock()
	defer s.procCache.mu.Unlock()
	if time.Since(s.procCache.at) > 2500*time.Millisecond || s.procCache.data == nil {
		s.procCache.data = TopProcesses(200)
		s.procCache.at = time.Now()
	}
	if len(s.procCache.data) > n {
		return s.procCache.data[:n]
	}
	return s.procCache.data
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	// 端口->进程 的映射要扫 /proc/<pid>/fd，在 2 核小机上可能要好几秒。
	// 因此这里永远先用缓存秒回，过期时在后台刷新，避免面板等一个慢请求。
	s.portCache.mu.Lock()
	stale := time.Since(s.portCache.at) > 20*time.Second || s.portCache.data == nil
	data := s.portCache.data
	s.portCache.mu.Unlock()
	if stale && !s.portScanning.Swap(true) {
		go func() {
			defer s.portScanning.Store(false)
			list := ListeningPorts()
			s.portCache.mu.Lock()
			s.portCache.data = list
			s.portCache.at = time.Now()
			s.portCache.mu.Unlock()
		}()
	}
	if data == nil {
		// 第一次请求：同步扫一次，保证面板打开就有数据。
		data = ListeningPorts()
		s.portCache.mu.Lock()
		s.portCache.data = data
		s.portCache.at = time.Now()
		s.portCache.mu.Unlock()
	}

	type probe struct {
		Port   int    `json:"port"`
		Scheme string `json:"scheme"`
		Code   int    `json:"code"`
		Ms     int64  `json:"ms"`
		OK     bool   `json:"ok"`
		Server string `json:"server"`
	}
	var probes []probe
	for _, p := range data {
		if p.Established == 0 {
			continue
		}
		switch p.Port {
		case 80, 8080, 8000, 8888, 3000, 443, 8443, 9000:
		default:
			continue
		}
		scheme := "http"
		if p.Port == 443 || p.Port == 8443 {
			scheme = "https"
		}
		started := time.Now()
		pr := probe{Port: p.Port, Scheme: scheme}
		client := &http.Client{Timeout: 3 * time.Second}
		req, err := http.NewRequest("GET", fmt.Sprintf("%s://127.0.0.1:%d/", scheme, p.Port), nil)
		if err == nil {
			req.Header.Set("User-Agent", "nodepanel-probe/"+shared.Version)
			resp, err := client.Do(req)
			if err == nil {
				pr.Code = resp.StatusCode
				pr.OK = true
				pr.Server = resp.Header.Get("Server")
				resp.Body.Close()
			}
		}
		pr.Ms = time.Since(started).Milliseconds()
		probes = append(probes, pr)
		if len(probes) >= 6 {
			break
		}
	}
	sort.Slice(data, func(i, j int) bool { return data[i].Port < data[j].Port })
	shared.JSON(w, 200, map[string]any{"ports": data, "probes": probes})
}

func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	shared.JSON(w, 200, map[string]any{"disks": Mounts()})
}

func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	shared.JSON(w, 200, s.tunnel.Status())
}

// handleExec runs a whitelisted read-only command through the node's own SSH
// connection. It backs the panel's file browser and log viewer; the command
// itself is validated by the panel before it gets here.
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.Error(w, 405, "仅支持 POST")
		return
	}
	if !s.cfg.SSHRelayEnable || s.cfg.SSHPassword == "" {
		shared.Error(w, 503, "节点未配置 SSH 凭据，无法执行命令")
		return
	}
	cmd := r.FormValue("cmd")
	if strings.TrimSpace(cmd) == "" {
		shared.Error(w, 400, "缺少 cmd 参数")
		return
	}
	if len(cmd) > 4096 {
		shared.Error(w, 400, "命令过长")
		return
	}
	out, err := s.ExecuteCommand(cmd, 20*time.Second)
	res := map[string]any{"output": out}
	if err != nil {
		res["error"] = err.Error()
	}
	shared.JSON(w, 200, res)
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	tok := shared.RandomToken(24)
	s.cfg.AdminHash = shared.HashToken(tok)
	if err := s.cfg.Save(); err != nil {
		shared.Error(w, 500, "写入配置失败: %v", err)
		return
	}
	shared.JSON(w, 200, map[string]any{"token": tok, "saved": s.cfg.statePath})
}

// ListenAndServe starts the HTTP listener (unless the port is disabled) and
// the cloudflared tunnel, then blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	handler := s.Handler()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}

	// Listeners. cloudflared always forwards to loopback, so when a public port
	// is configured the agent listens there *and* on 127.0.0.1 only if that is
	// a different socket (two binds of the same port would fail with EADDRINUSE).
	type listener struct {
		ln   net.Listener
		desc string
	}
	var listeners []listener
	opened := map[string]bool{}
	add := func(addr, desc string) error {
		if opened[addr] {
			return nil
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("监听 %s 失败: %w", addr, err)
		}
		opened[addr] = true
		listeners = append(listeners, listener{ln, desc})
		return nil
	}

	pubAddr := ""
	if s.cfg.Port > 0 {
		pubAddr = net.JoinHostPort(s.cfg.Bind, strconv.Itoa(s.cfg.Port))
		if err := add(pubAddr, "公网"); err != nil {
			return err
		}
	}
	localAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(internalPort(s.cfg)))
	if s.cfg.Port != internalPort(s.cfg) {
		if err := add(localAddr, "本机"); err != nil {
			return err
		}
	}
	if len(listeners) == 0 {
		return fmt.Errorf("没有可用的监听地址")
	}

	for _, l := range listeners {
		ln := l.ln
		log.Printf("%s接口 http://%s", l.desc, ln.Addr().String())
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTP 服务退出: %v", err)
			}
		}()
	}
	log.Printf("nodepanel agent %s 已启动", shared.Version)
	log.Printf("主机: %s · %s · %d 核 · 采样 %ds",
		s.sampler.host.Hostname, s.sampler.host.OS, s.sampler.host.CPUCores, s.cfg.Interval)
	if s.cfg.Port <= 0 {
		log.Printf("未开放公网端口，仅通过 cloudflared 内网穿透访问")
	}

	go func() {
		if !s.cfg.PublicIPLookup {
			return
		}
		if ip := LookupPublicIP(6 * time.Second); ip != "" {
			s.sampler.SetPublicIP(ip)
			log.Printf("公网 IP: %s", ip)
		}
	}()

	if err := s.tunnel.Start(ctx); err != nil {
		log.Printf("内网穿透启动失败: %v", err)
	}

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Printf("nodepanel agent 已停止")
	return nil
}
