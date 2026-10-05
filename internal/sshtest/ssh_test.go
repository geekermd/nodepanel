package sshtest_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/geekermd/nodepanel/internal/agent"
	pnl "github.com/geekermd/nodepanel/internal/panel"
	"github.com/geekermd/nodepanel/internal/shared"
	"github.com/geekermd/nodepanel/internal/store"
)

// fakeSSHServer accepts password auth and answers with a canned shell.
func fakeSSHServer(t *testing.T, password string) (host string, port int) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == password {
				return nil, nil
			}
			return nil, io.ErrUnexpectedEOF
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newChan := range chans {
					if newChan.ChannelType() != "session" {
						newChan.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					ch, chReqs, _ := newChan.Accept()
					go func() {
						for req := range chReqs {
							switch req.Type {
							case "shell":
								req.Reply(true, nil)
								io.WriteString(ch, "FAKE-SHELL-READY\r\n$ ")
								go func() {
									buf := make([]byte, 256)
									for {
										n, err := ch.Read(buf)
										if err != nil {
											return
										}
										io.WriteString(ch, "ECHO:"+string(buf[:n]))
									}
								}()
							case "pty-req":
								req.Reply(true, nil)
							default:
								req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func TestSSHRelayThroughAgent(t *testing.T) {
	const password = "test-pw-123"
	host, port := fakeSSHServer(t, password)

	// Agent configured to relay SSH to the fake server.
	cfg := agent.DefaultConfig()
	cfg.Port = 0
	cfg.Tunnel.LocalPort = freePort(t)
	cfg.AdminHash = hash("tok-abc")
	cfg.SSHRelayEnable = true
	cfg.SSHUser = "root"
	cfg.SSHHost = host
	cfg.SSHPort = port
	cfg.SSHPassword = password
	sampler := agent.NewSampler(60, 2)
	srv := agent.NewServer(cfg, sampler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	time.Sleep(300 * time.Millisecond)

	agentURL := "http://127.0.0.1:" + strconv.Itoa(cfg.Tunnel.LocalPort)

	// Panel with one node that must use the relay.
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node, err := st.AddNode(&store.Node{
		Name: "relay-node", Host: "127.0.0.1", Port: 1, Mode: store.ModeDirect,
		Token: "tok-abc", SSHUser: "root", SSHPort: port, UseRelay: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateSettings(func(s *store.Settings) { s.PasswordHash = hash("pw") })
	p := pnl.NewPoller(st)
	srvPan := pnl.NewServer(st, p, nil)
	handler := srvPan.Handler()

	// Bypass the relay's target URL: point the node at our test agent.
	node.Host = strings.TrimPrefix(agentURL, "http://")
	_ = st.Save()

	// Log in to get a session cookie.
	ts := httptest.NewServer(handler)
	defer ts.Close()
	cookies := login(t, ts.URL)

	// Open the terminal WebSocket and speak the protocol.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/nodes/" + node.ID + "/ssh"
	hdr := http.Header{}
	hdr.Set("Cookie", cookies)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial terminal ws: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(map[string]any{"type": "auth", "payload": password, "cols": 100, "rows": 30}); err != nil {
		t.Fatal(err)
	}
	got := readUntil(ws, "FAKE-SHELL-READY", 4*time.Second)
	if !strings.Contains(got, "FAKE-SHELL-READY") {
		t.Fatalf("no shell banner through relay, got %q", got)
	}
	if err := ws.WriteJSON(map[string]any{"type": "input", "payload": "hello\n"}); err != nil {
		t.Fatal(err)
	}
	echo := readUntil(ws, "ECHO:hello", 4*time.Second)
	if !strings.Contains(echo, "ECHO:hello") {
		t.Fatalf("input not echoed, got %q", echo)
	}
	t.Logf("relay SSH OK: banner+echo received")
}

// TestSSHDirectFromPanel exercises the panel's direct SSH client.
func TestSSHDirectFromPanel(t *testing.T) {
	const password = "direct-pw"
	host, port := fakeSSHServer(t, password)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node, err := st.AddNode(&store.Node{
		Name: "direct", Host: host, Port: 1, Mode: store.ModeDirect,
		Token: "x", SSHUser: "root", SSHPort: port,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateSettings(func(s *store.Settings) { s.PasswordHash = hash("pw") })
	p := pnl.NewPoller(st)
	srvPan := pnl.NewServer(st, p, nil)
	ts := httptest.NewServer(srvPan.Handler())
	defer ts.Close()
	cookies := login(t, ts.URL)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/nodes/" + node.ID + "/ssh"
	hdr := http.Header{}
	hdr.Set("Cookie", cookies)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()
	_ = ws.WriteJSON(map[string]any{"type": "auth", "payload": password, "cols": 80, "rows": 24})
	got := readUntil(ws, "FAKE-SHELL-READY", 4*time.Second)
	if !strings.Contains(got, "FAKE-SHELL-READY") {
		t.Fatalf("direct SSH failed, got %q", got)
	}
	t.Logf("direct SSH OK")
}

func TestSSHDirectWrongPassword(t *testing.T) {
	host, port := fakeSSHServer(t, "right")
	st, _ := store.Open(t.TempDir())
	node, _ := st.AddNode(&store.Node{Name: "d", Host: host, Port: 1, Token: "x", SSHUser: "root", SSHPort: port})
	_ = st.UpdateSettings(func(s *store.Settings) { s.PasswordHash = hash("pw") })
	srvPan := pnl.NewServer(st, pnl.NewPoller(st), nil)
	ts := httptest.NewServer(srvPan.Handler())
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/nodes/" + node.ID + "/ssh"
	hdr := http.Header{}
	hdr.Set("Cookie", login(t, ts.URL))
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.WriteJSON(map[string]any{"type": "auth", "payload": "wrong", "cols": 80, "rows": 24})
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("expected an error frame, got %v", err)
	}
	var msg map[string]any
	_ = json.Unmarshal(data, &msg)
	if msg["type"] != "error" {
		t.Fatalf("expected error frame, got %v", msg)
	}
	t.Logf("wrong password reported cleanly: %v", msg["payload"])
}

func TestAgentRelayRejectsBadToken(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.Port = 0
	cfg.Tunnel.LocalPort = freePort(t)
	cfg.AdminHash = hash("right-token")
	cfg.SSHRelayEnable = true
	cfg.SSHPassword = "x"
	srv := agent.NewServer(cfg, agent.NewSampler(60, 2))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	time.Sleep(250 * time.Millisecond)
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(cfg.Tunnel.LocalPort) + "/api/v1/ssh/relay?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	// Relay disabled case.
	cfg2 := agent.DefaultConfig()
	cfg2.Port = 0
	cfg2.Tunnel.LocalPort = freePort(t)
	cfg2.AdminHash = hash("t")
	srv2 := agent.NewServer(cfg2, agent.NewSampler(60, 2))
	go srv2.Run(ctx)
	time.Sleep(250 * time.Millisecond)
	resp2, err := http.Get("http://127.0.0.1:" + strconv.Itoa(cfg2.Tunnel.LocalPort) + "/api/v1/ssh/relay?token=t")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 when relay disabled, got %d", resp2.StatusCode)
	}
}

func TestAgentStatsShape(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.Port = 0
	cfg.Tunnel.LocalPort = freePort(t)
	cfg.AdminHash = hash("t")
	srv := agent.NewServer(cfg, agent.NewSampler(60, 1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	time.Sleep(1200 * time.Millisecond)
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(cfg.Tunnel.LocalPort)+"/api/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	host, _ := out["host"].(map[string]any)
	if host["hostname"] == "" || host["hostname"] == nil {
		t.Fatalf("hostname empty in stats: %v", host)
	}
	if host["cpu_cores"].(float64) < 1 {
		t.Fatalf("cpu_cores not detected: %v", host["cpu_cores"])
	}
	t.Logf("agent stats ok: host=%v cores=%v disks=%d", host["hostname"], host["cpu_cores"], len(out["disks"].([]any)))
}

/* ------------------------------ helpers ------------------------------ */

func hash(s string) string { return shared.HashToken(s) }

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func login(t *testing.T, base string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": "pw"})
	resp, err := http.Post(base+"/api/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == "np_session" {
			cookie = c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatal("no session cookie")
	}
	return cookie
}

func readUntil(ws *websocket.Conn, needle string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	var sb strings.Builder
	for time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		typ, data, err := ws.ReadMessage()
		if err != nil {
			if strings.Contains(sb.String(), needle) {
				return sb.String()
			}
			continue
		}
		if typ == websocket.TextMessage {
			var msg map[string]any
			if json.Unmarshal(data, &msg) == nil && msg["type"] == "error" {
				sb.WriteString("[error] " + msg["payload"].(string))
				return sb.String()
			}
			continue
		}
		sb.Write(data)
		if strings.Contains(sb.String(), needle) {
			return sb.String()
		}
	}
	return sb.String()
}
