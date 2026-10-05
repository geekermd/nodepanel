package panel

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/geekermd/nodepanel/internal/shared"
	"github.com/geekermd/nodepanel/internal/store"
)

// Server exposes the panel API and the web UI.
type Server struct {
	Store   *store.Store
	Poller  *Poller
	Assets  fs.FS
	Version string

	sessions map[string]session
	smu      sync.RWMutex
}

type session struct {
	Token   string
	Created time.Time
	IP      string
}

// NewServer wires a panel server.
func NewServer(st *store.Store, pl *Poller, assets fs.FS) *Server {
	return &Server{Store: st, Poller: pl, Assets: assets, Version: shared.Version, sessions: map[string]session{}}
}

// Handler builds the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/overview", s.auth(s.handleOverview))
	mux.HandleFunc("/api/nodes", s.auth(s.handleNodes))
	mux.HandleFunc("/api/nodes/", s.auth(s.handleNode))
	mux.HandleFunc("/api/todos", s.auth(s.handleTodos))
	mux.HandleFunc("/api/todos/", s.auth(s.handleTodo))
	mux.HandleFunc("/api/settings", s.auth(s.handleSettings))
	mux.HandleFunc("/api/probe", s.auth(s.handleProbe))
	mux.HandleFunc("/api/ssh-test", s.auth(s.handleSSHTest))
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		shared.JSON(w, 200, map[string]any{"ok": true, "version": s.Version, "nodes": len(s.Store.ListNodes())})
	})
	// The web UI is embedded; it never leaks outside the panel.
	mux.HandleFunc("/", s.handleStatic)
	return s.logRequests(mux)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/overview" {
			// Keep the panel log useful without being chatty.
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.Error(w, 405, "仅支持 POST")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	set := s.Store.GetSettings()
	// The panel is a localhost tool: require whatever password is configured,
	// and if none is set, accept the auto-generated one printed at startup.
	if set.PasswordHash == "" {
		shared.Error(w, 500, "面板未初始化密码，请重启面板")
		return
	}
	if !shared.CheckToken(set.PasswordHash, body.Password) {
		shared.Error(w, http.StatusUnauthorized, "密码错误")
		return
	}
	tok := shared.RandomToken(32)
	s.smu.Lock()
	s.sessions[tok] = session{Token: tok, Created: time.Now(), IP: shared.ClientIP(r)}
	s.smu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: "np_session", Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600,
	})
	shared.JSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("np_session"); err == nil {
		s.smu.Lock()
		delete(s.sessions, c.Value)
		s.smu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "np_session", Value: "", Path: "/", MaxAge: -1})
	shared.JSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie("np_session")
	if err != nil {
		return false
	}
	s.smu.RLock()
	sess, ok := s.sessions[c.Value]
	s.smu.RUnlock()
	if !ok {
		return false
	}
	if time.Since(sess.Created) > 30*24*time.Hour {
		s.smu.Lock()
		delete(s.sessions, c.Value)
		s.smu.Unlock()
		return false
	}
	return true
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			shared.Error(w, http.StatusUnauthorized, "未登录")
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

type nodeView struct {
	Node    *store.Node `json:"node"`
	Runtime *Runtime    `json:"runtime"`
	Series  store.Stats `json:"series"`
}

// buildNodeViews merges inventory, live state and series stats.
func (s *Server) buildNodeViews() []nodeView {
	nodes := s.Store.ListNodes()
	out := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeView{
			Node:    n,
			Runtime: s.Poller.Runtime(n.ID),
			Series:  s.Store.Series(n.ID).Stats(),
		})
	}
	return out
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	views := s.buildNodeViews()
	todos := s.Store.ListTodos()

	var online, offline int
	var totalRx, totalTx, totalReq, reqToday float64
	var cpuSum, memSum float64
	var cpuN, memN int
	var alerts []map[string]any

	for _, v := range views {
		if v.Runtime.Online {
			online++
		} else {
			offline++
		}
		totalRx += v.Runtime.RxTotal
		totalTx += v.Runtime.TxTotal
		totalReq += float64(v.Runtime.Requests)
		reqToday += float64(v.Runtime.RequestsDay)
		st := v.Runtime.Stats
		if st != nil && v.Runtime.Online {
			cpuSum += lastSampleValue(s.Store, v.Node.ID, "cpu")
			cpuN++
			if st.Mem.Total > 0 {
				memSum += st.Mem.Used / st.Mem.Total * 100
				memN++
			}
			set := s.Store.GetSettings()
			if p := lastSampleValue(s.Store, v.Node.ID, "cpu"); p >= set.AlertCPU {
				alerts = append(alerts, map[string]any{"node": v.Node.Name, "kind": "cpu", "value": p, "level": "warn"})
			}
			if st.Mem.Total > 0 {
				p := st.Mem.Used / st.Mem.Total * 100
				if p >= set.AlertMem {
					alerts = append(alerts, map[string]any{"node": v.Node.Name, "kind": "mem", "value": p, "level": "warn"})
				}
			}
			for _, d := range st.Disks {
				if d.UsedPct >= set.AlertDisk {
					alerts = append(alerts, map[string]any{"node": v.Node.Name, "kind": "disk:" + d.Mount, "value": d.UsedPct, "level": "warn"})
				}
			}
		}
	}

	openTodos := 0
	for _, t := range todos {
		if !t.Done {
			openTodos++
		}
	}

	shared.JSON(w, 200, map[string]any{
		"now":     time.Now().Unix(),
		"version": s.Version,
		"summary": map[string]any{
			"nodes": len(views), "online": online, "offline": offline,
			"rx_total": totalRx, "tx_total": totalTx,
			"rx_total_h": shared.Bytes(totalRx), "tx_total_h": shared.Bytes(totalTx),
			"requests": int64(totalReq), "requests_today": int64(reqToday),
			"cpu_avg": avg(cpuSum, cpuN), "mem_avg": avg(memSum, memN),
			"todos_open": openTodos,
		},
		"nodes":  views,
		"todos":  todos,
		"alerts": alerts,
	})
}

func avg(sum float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return shared.Round1(sum / float64(n))
}

func lastSampleValue(st *store.Store, nodeID, key string) float64 {
	v, ok := st.Series(nodeID).Latest()
	if !ok {
		return 0
	}
	switch key {
	case "cpu":
		return v.CPU
	case "mem":
		return v.MemP
	}
	return 0
}

// ---------------------------------------------------------------------------
// Nodes
// ---------------------------------------------------------------------------

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		shared.JSON(w, 200, map[string]any{"nodes": s.buildNodeViews()})
	case http.MethodPost:
		var in store.Node
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			shared.Error(w, 400, "请求格式错误: %v", err)
			return
		}
		if in.Name == "" {
			in.Name = in.Host
		}
		if in.Host == "" {
			shared.Error(w, 400, "请填写节点 IP 或域名")
			return
		}
		if in.Token == "" {
			shared.Error(w, 400, "请填写节点访问令牌（agent 安装时输出）")
			return
		}
		in.Host = shared.NormalizeHost(in.Host)
		if in.Mode == "" {
			in.Mode = store.ModeDirect
		}
		if in.SSHUser == "" {
			in.SSHUser = "root"
		}
		if in.SSHPort == 0 {
			in.SSHPort = 22
		}
		n, err := s.Store.AddNode(&in)
		if err != nil {
			shared.Error(w, 500, "保存节点失败: %v", err)
			return
		}
		s.Poller.Ensure(context.Background(), n.ID)
		// Immediate first poll so the UI shows something right away.
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = s.Poller.pollOnce(context.Background(), n)
		}()
		shared.JSON(w, 200, map[string]any{"node": n, "url": AgentURL(n)})
	default:
		shared.Error(w, 405, "不支持的方法")
	}
}

// handleNode dispatches /api/nodes/{id}/...
func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/nodes/")
	if rest == "" {
		shared.Error(w, 404, "缺少节点 ID")
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]
	n := s.Store.Node(id)
	if n == nil {
		shared.Error(w, 404, "节点不存在")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			shared.JSON(w, 200, map[string]any{
				"node": n, "runtime": s.Poller.Runtime(id),
				"series": s.Store.Series(id).Stats(), "agent_url": AgentURL(n),
			})
		case http.MethodPatch, http.MethodPut:
			var in map[string]any
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
				shared.Error(w, 400, "请求格式错误")
				return
			}
			updated, err := s.Store.UpdateNode(id, func(node *store.Node) {
				applyNodePatch(node, in)
			})
			if err != nil {
				shared.Error(w, 500, "保存失败: %v", err)
				return
			}
			s.Poller.Forget(id)
			s.Poller.Ensure(context.Background(), id)
			shared.JSON(w, 200, map[string]any{"node": updated})
		case http.MethodDelete:
			if err := s.Store.RemoveNode(id); err != nil {
				shared.Error(w, 500, "删除失败: %v", err)
				return
			}
			s.Poller.Forget(id)
			shared.JSON(w, 200, map[string]any{"ok": true})
		default:
			shared.Error(w, 405, "不支持的方法")
		}
	case "metrics":
		span := int64(3600)
		if v, err := strconv.ParseInt(r.URL.Query().Get("span"), 10, 64); err == nil && v > 0 {
			span = v
		}
		if v, err := strconv.Atoi(r.URL.Query().Get("max")); err == nil && v > 0 {
			// max points hint
		}
		to := time.Now().UnixMilli()
		if v, err := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64); err == nil && v > 0 {
			to = v
		}
		from := to - span*1000
		maxPts := 480
		if v, err := strconv.Atoi(r.URL.Query().Get("max")); err == nil && v > 0 && v <= 2000 {
			maxPts = v
		}
		samples, tier := s.Store.Series(id).Range(from, to, maxPts)
		shared.JSON(w, 200, map[string]any{
			"node_id": id, "tier": tier, "points": len(samples),
			"from": from, "to": to, "samples": samples,
		})
	case "processes":
		s.proxyAgentJSON(w, r, n, "/api/v1/processes?"+r.URL.RawQuery)
	case "stats":
		s.proxyAgentJSON(w, r, n, "/api/v1/stats")
	case "ports":
		s.proxyAgentJSON(w, r, n, "/api/v1/ports")
	case "tunnel":
		s.proxyAgentJSON(w, r, n, "/api/v1/tunnel")
	case "ssh":
		s.handleSSH(w, r, n)
	case "files":
		s.handleFiles(w, r, n)
	case "logs":
		s.handleLogs(w, r, n)
	default:
		shared.Error(w, 404, "未知的子资源 %s", action)
	}
}

func applyNodePatch(node *store.Node, in map[string]any) {
	str := func(k string) (string, bool) {
		v, ok := in[k]
		if !ok {
			return "", false
		}
		s, _ := v.(string)
		return s, true
	}
	num := func(k string) (int, bool) {
		v, ok := in[k]
		if !ok {
			return 0, false
		}
		switch t := v.(type) {
		case float64:
			return int(t), true
		case string:
			i, err := strconv.Atoi(t)
			return i, err == nil
		}
		return 0, false
	}
	boolean := func(k string) (bool, bool) {
		v, ok := in[k]
		if !ok {
			return false, false
		}
		b, _ := v.(bool)
		return b, true
	}
	if v, ok := str("name"); ok && v != "" {
		node.Name = v
	}
	if v, ok := str("host"); ok && v != "" {
		node.Host = shared.NormalizeHost(v)
	}
	if v, ok := num("port"); ok {
		node.Port = v
	}
	if v, ok := str("scheme"); ok && v != "" {
		node.Scheme = v
	}
	if v, ok := str("mode"); ok && v != "" {
		node.Mode = v
	}
	if v, ok := str("token"); ok && v != "" {
		node.Token = v
	}
	if v, ok := str("note"); ok {
		node.Note = v
	}
	if v, ok := str("group"); ok {
		node.Group = v
	}
	if v, ok := str("ssh_user"); ok && v != "" {
		node.SSHUser = v
	}
	if v, ok := num("ssh_port"); ok && v > 0 {
		node.SSHPort = v
	}
	if v, ok := str("ssh_pass"); ok {
		node.SSHPass = v
	}
	if v, ok := str("ssh_key"); ok {
		node.SSHKey = v
	}
	if v, ok := boolean("remember"); ok {
		node.Remember = v
	}
	if v, ok := boolean("use_relay"); ok {
		node.UseRelay = v
	}
	if v, ok := num("interval"); ok && v >= 2 {
		node.Interval = v
	}
	if v, ok := in["expires_at"]; ok {
		switch t := v.(type) {
		case float64:
			node.ExpiresAt = int64(t)
		case string:
			if ts, err := strconv.ParseInt(t, 10, 64); err == nil {
				node.ExpiresAt = ts
			}
		}
	}
	if v, ok := str("agent_addr"); ok {
		node.AgentAddr = v
	}
	if v, ok := boolean("enabled"); ok {
		node.Enabled = v
	}
	// 「记住密码」关闭时立刻清掉本机保存的密码。
	if !node.Remember {
		node.SSHPass = ""
	}
}

// proxyAgentJSON forwards a read-only agent API call, so the browser never
// needs to know a node token.
func (s *Server) proxyAgentJSON(w http.ResponseWriter, r *http.Request, n *store.Node, path string) {
	base := AgentURL(n)
	if base == "" {
		shared.Error(w, 400, "节点地址无效")
		return
	}
	reqCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", base+path, nil)
	if err != nil {
		shared.Error(w, 500, "构建请求失败: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+n.Token)
	req.Header.Set("User-Agent", "nodepanel-panel/"+shared.Version)
	resp, err := s.Poller.clientFor(reqCtx, n).Do(req)
	if err != nil {
		shared.Error(w, 502, "节点不可达: %v", err)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}

// ---------------------------------------------------------------------------
// TODO list
// ---------------------------------------------------------------------------

func (s *Server) handleTodos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		shared.JSON(w, 200, map[string]any{"todos": s.Store.ListTodos()})
	case http.MethodPost:
		var in store.Todo
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			shared.Error(w, 400, "请求格式错误")
			return
		}
		if strings.TrimSpace(in.Text) == "" {
			shared.Error(w, 400, "内容不能为空")
			return
		}
		t, err := s.Store.AddTodo(&in)
		if err != nil {
			shared.Error(w, 500, "保存失败: %v", err)
			return
		}
		shared.JSON(w, 200, map[string]any{"todo": t})
	default:
		shared.Error(w, 405, "不支持的方法")
	}
}

func (s *Server) handleTodo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/todos/")
	if id == "" {
		shared.Error(w, 404, "缺少 ID")
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		var in map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			shared.Error(w, 400, "请求格式错误")
			return
		}
		t, err := s.Store.UpdateTodo(id, func(t *store.Todo) {
			if v, ok := in["text"].(string); ok && v != "" {
				t.Text = v
			}
			if v, ok := in["done"].(bool); ok {
				t.Done = v
			}
			if v, ok := in["node_id"].(string); ok {
				t.NodeID = v
			}
			if v, ok := in["priority"].(float64); ok {
				t.Priority = int(v)
			}
			if v, ok := in["due_at"].(float64); ok {
				t.DueAt = int64(v)
			}
		})
		if err != nil {
			shared.Error(w, 500, "保存失败: %v", err)
			return
		}
		shared.JSON(w, 200, map[string]any{"todo": t})
	case http.MethodDelete:
		if err := s.Store.RemoveTodo(id); err != nil {
			shared.Error(w, 500, "删除失败: %v", err)
			return
		}
		shared.JSON(w, 200, map[string]any{"ok": true})
	default:
		shared.Error(w, 405, "不支持的方法")
	}
}

// ---------------------------------------------------------------------------
// Settings / probe
// ---------------------------------------------------------------------------

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		set := s.Store.GetSettings()
		set.PasswordHash = ""
		shared.JSON(w, 200, map[string]any{
			"settings": set, "version": s.Version, "dir": s.Store.Dir(),
		})
	case http.MethodPatch, http.MethodPut:
		var in map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			shared.Error(w, 400, "请求格式错误")
			return
		}
		err := s.Store.UpdateSettings(func(set *store.Settings) {
			if v, ok := in["poll_seconds"].(float64); ok && v >= 2 {
				set.PollSeconds = int(v)
			}
			if v, ok := in["offline_seconds"].(float64); ok && v >= 5 {
				set.OfflineEvery = int(v)
			}
			if v, ok := in["keep_days"].(float64); ok && v >= 1 {
				set.KeepDays = int(v)
			}
			if v, ok := in["alert_cpu"].(float64); ok {
				set.AlertCPU = v
			}
			if v, ok := in["alert_mem"].(float64); ok {
				set.AlertMem = v
			}
			if v, ok := in["alert_disk"].(float64); ok {
				set.AlertDisk = v
			}
			if v, ok := in["theme"].(string); ok && v != "" {
				set.Theme = v
			}
			if v, ok := in["password"].(string); ok && len(v) >= 4 {
				set.PasswordHash = shared.HashToken(v)
			}
		})
		if err != nil {
			shared.Error(w, 500, "保存失败: %v", err)
			return
		}
		shared.JSON(w, 200, map[string]any{"ok": true})
	default:
		shared.Error(w, 405, "不支持的方法")
	}
}

// handleSSHTest verifies SSH credentials (and the agent channel in SSH-tunnel
// mode) without saving anything, so the UI can offer a "test connection" button.
func (s *Server) handleSSHTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.Error(w, 405, "仅支持 POST")
		return
	}
	var in store.Node
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		shared.Error(w, 400, "请求格式错误: %v", err)
		return
	}
	if strings.TrimSpace(in.Host) == "" {
		shared.Error(w, 400, "请填写 IP 或域名")
		return
	}
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	if in.SSHUser == "" {
		in.SSHUser = "root"
	}
	if in.Port == 0 {
		in.Port = 8899
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	detail, err := s.Poller.TestSSH(ctx, &in, in.SSHPass)
	if err != nil {
		shared.Error(w, 502, "%v", err)
		return
	}
	shared.JSON(w, 200, map[string]any{"ok": true, "detail": detail})
}

// handleProbe lets the panel check a URL from its own network position.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	nodeID := r.URL.Query().Get("node")
	if target == "" && nodeID != "" {
		if n := s.Store.Node(nodeID); n != nil {
			target = AgentURL(n)
		}
	}
	if target == "" {
		shared.Error(w, 400, "缺少 url 参数")
		return
	}
	code, ms, err := s.Poller.ProbeURL(r.Context(), target)
	out := map[string]any{"url": target, "code": code, "ms": ms}
	if err != nil {
		out["error"] = err.Error()
	}
	shared.JSON(w, 200, out)
}

// ---------------------------------------------------------------------------
// Files / logs (read-only, rooted at a whitelist)
// ---------------------------------------------------------------------------

var allowedRoots = []string{"/var/log", "/etc", "/tmp", "/var/lib", "/opt", "/srv", "/home"}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request, n *store.Node) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/var/log"
	}
	if !allowedPath(path) {
		shared.Error(w, 403, "该目录不在允许范围内")
		return
	}
	cmd := "ls -lah --time-style=long-iso " + shellQuote(path) + " 2>&1 | head -80"
	out, err := s.Poller.RunCommand(r.Context(), n, cmd)
	res := map[string]any{"path": path, "output": out}
	if err != nil {
		res["error"] = err.Error()
	}
	shared.JSON(w, 200, res)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, n *store.Node) {
	path := r.URL.Query().Get("path")
	lines := r.URL.Query().Get("lines")
	if lines == "" {
		lines = "200"
	}
	if path == "" || !allowedPath(path) {
		shared.Error(w, 403, "路径不合法")
		return
	}
	cmd := "tail -n " + strconv.Itoa(atoiClamp(lines, 20, 2000)) + " " + shellQuote(path) + " 2>&1"
	out, err := s.Poller.RunCommand(r.Context(), n, cmd)
	res := map[string]any{"path": path, "output": out}
	if err != nil {
		res["error"] = err.Error()
	}
	shared.JSON(w, 200, res)
}

// allowedPath guards the read-only file/log browser. It rejects any traversal
// attempt outright instead of relying on the cleaned path alone: "/var/log/../.."
// cleans to something that may still sit under an allowed root.
func allowedPath(p string) bool {
	if p == "" || !strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return false
	}
	clean := filepath.Clean(p)
	if !strings.HasPrefix(clean, "/") {
		return false
	}
	for _, root := range allowedRoots {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func atoiClamp(s string, lo, hi int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// Static assets
// ---------------------------------------------------------------------------

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	switch {
	case strings.HasPrefix(path, "vendor/") || strings.HasSuffix(path, ".svg") ||
		strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".ico"):
		// 第三方/静态资源：可以长缓存（升级面板时文件名不变，因此仍带 ETag 校验）
		w.Header().Set("Cache-Control", "public, max-age=3600")
	default:
		// 面板自身的前端代码必须每次校验，否则升级后浏览器会一直跑旧版本。
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
	}
	f, err := s.Assets.Open(path)
	if err != nil {
		// SPA fallback.
		f2, err2 := s.Assets.Open("index.html")
		if err2 != nil {
			http.NotFound(w, r)
			return
		}
		defer f2.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, f2)
		return
	}
	defer f.Close()
	switch {
	case strings.HasSuffix(path, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case strings.HasSuffix(path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(path, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(path, ".json"):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	case strings.HasSuffix(path, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	_, _ = io.Copy(w, f)
}

// ---------------------------------------------------------------------------
// Helpers used by the CLI
// ---------------------------------------------------------------------------

// EnsurePassword makes sure the panel has a password, returning the generated
// one when it had to create it.
func (s *Server) EnsurePassword() (string, bool) {
	set := s.Store.GetSettings()
	if set.PasswordHash != "" {
		return "", false
	}
	pw := shared.RandomToken(9)
	_ = s.Store.UpdateSettings(func(st *store.Settings) {
		st.PasswordHash = shared.HashToken(pw)
	})
	return pw, true
}

// ListenAndServe runs the panel until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("管理面板已启动: http://%s", addr)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

var _ = subtle.ConstantTimeCompare
var _ = sort.Strings
