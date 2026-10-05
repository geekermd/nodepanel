//go:build linux

package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/geekermd/nodepanel/internal/shared"
)

func sysProcAttrChild() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func runtimeNumGoroutine() int { return numGoroutine() }

// ---------------------------------------------------------------------------
// SSH relay.
//
// The panel normally opens SSH directly (it is a desktop on the operator's
// LAN). For nodes that are only reachable through a cloudflared tunnel the SSH
// port is not exposed at all, so the agent dials its own sshd and pumps bytes
// between the panel's WebSocket and the SSH channel. The panel never needs to
// know which of the two paths is in use.
// ---------------------------------------------------------------------------

type wsMessage struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

type wsWriter struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func (s *Server) handleSSHRelay(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.SSHRelayEnable {
		shared.WriteWSError(w, http.StatusForbidden,
			"该节点未启用 SSH 中继：请在节点上带 -ssh-password=<root密码> 重启 agent")
		return
	}
	if s.cfg.AdminHash == "" || !shared.CheckToken(s.cfg.AdminHash, shared.BearerToken(r)) {
		shared.WriteWSError(w, http.StatusUnauthorized, "令牌无效")
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ssh relay: websocket 升级失败: %v", err)
		return
	}
	defer ws.Close()

	host := s.cfg.SSHHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := s.cfg.SSHPort
	if port == 0 {
		port = 22
	}
	user := s.cfg.SSHUser
	if user == "" {
		user = "root"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	var authMethods []ssh.AuthMethod
	if s.cfg.SSHPassword != "" {
		authMethods = append(authMethods, ssh.Password(s.cfg.SSHPassword))
	}
	if len(authMethods) == 0 {
		shared.WriteWSError(w, http.StatusForbidden, "该节点未配置 SSH 密码，无法中继")
		return
	}

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
		ClientVersion:   "SSH-2.0-nodepanel_" + shared.Version,
	})
	if err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{
			Type: "error", Payload: "无法连接本机 SSH (" + addr + "): " + err.Error(),
		}))
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{Type: "error", Payload: err.Error()}))
		return
	}
	defer session.Close()

	if err := session.RequestPty("xterm-256color", 30, 100, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{Type: "error", Payload: err.Error()}))
		return
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return
	}
	if err := session.Shell(); err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{Type: "error", Payload: err.Error()}))
		return
	}

	sink := &wsWriter{ws: ws}
	go func() { _, _ = io.Copy(sink, stdout) }()
	go func() { _, _ = io.Copy(sink, stderr) }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			typ, data, err := ws.ReadMessage()
			if err != nil {
				_ = session.Close()
				return
			}
			switch typ {
			case websocket.BinaryMessage:
				if _, err := stdin.Write(data); err != nil {
					return
				}
			case websocket.TextMessage:
				var msg wsMessage
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}
				switch msg.Type {
				case "input":
					if _, err := io.WriteString(stdin, msg.Payload); err != nil {
						return
					}
				case "resize":
					cols, rows := msg.Cols, msg.Rows
					if cols <= 0 {
						cols = 100
					}
					if rows <= 0 {
						rows = 30
					}
					_ = session.WindowChange(rows, cols)
				}
			}
		}
	}()

	waitErr := session.Wait()
	msg := wsMessage{Type: "exit"}
	if waitErr != nil {
		msg.Payload = waitErr.Error()
	}
	_ = ws.WriteMessage(websocket.TextMessage, mustJSON(msg))
	<-done
}

// ExecuteCommand runs a shell command on the node over the local SSH channel.
// It backs the panel's "节点自检 / 一键更新" action.
func (s *Server) ExecuteCommand(cmdline string, timeout time.Duration) (string, error) {
	if !s.cfg.SSHRelayEnable || s.cfg.SSHPassword == "" {
		return "", fmt.Errorf("未配置 SSH 凭据")
	}
	host := s.cfg.SSHHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := s.cfg.SSHPort
	if port == 0 {
		port = 22
	}
	user := s.cfg.SSHUser
	if user == "" {
		user = "root"
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)), &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(s.cfg.SSHPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		return "", err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	type result struct {
		out []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		out, err := session.CombinedOutput(cmdline)
		ch <- result{out, err}
	}()
	select {
	case r := <-ch:
		return string(r.out), r.err
	case <-time.After(timeout):
		_ = session.Signal(ssh.SIGKILL)
		return "", fmt.Errorf("命令执行超时")
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

var _ = strings.TrimSpace
