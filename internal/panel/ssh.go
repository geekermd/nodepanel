package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/geekermd/nodepanel/internal/shared"
	"github.com/geekermd/nodepanel/internal/store"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  8192,
	WriteBufferSize: 8192,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type wsMessage struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

// handleSSH exposes /api/nodes/{id}/ssh as a terminal WebSocket.
//
// Protocol (identical for direct and relayed sessions):
//
//	binary frame           raw terminal bytes (both directions)
//	text frame {"type":"input","payload":"ls\n"}    keyboard input
//	text frame {"type":"resize","cols":120,"rows":30}
//	text frame {"type":"error","payload":"..."}     server -> browser
//	text frame {"type":"exit"}                      session finished
func (s *Server) handleSSH(w http.ResponseWriter, r *http.Request, n *store.Node) {
	if !s.authed(r) {
		shared.WriteWSError(w, http.StatusUnauthorized, "未登录")
		return
	}
	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ssh: 升级 websocket 失败: %v", err)
		return
	}
	defer ws.Close()

	// The first frame may carry credentials the operator typed in the UI.
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, first, err := ws.ReadMessage()
	if err != nil {
		return
	}
	_ = ws.SetReadDeadline(time.Time{})
	var hello wsMessage
	if err := json.Unmarshal(first, &hello); err != nil || hello.Type != "auth" {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{Type: "error", Payload: "握手格式错误"}))
		return
	}

	password := hello.Payload
	if password == "" && n.Remember {
		password = n.SSHPass
	}

	if n.UseRelay {
		s.relaySSH(ws, n, password, hello.Cols, hello.Rows)
		return
	}
	s.directSSH(ws, n, password, hello.Cols, hello.Rows)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (s *Server) sshAuth(n *store.Node, password string) ([]ssh.AuthMethod, string) {
	var methods []ssh.AuthMethod
	desc := ""
	if password != "" {
		methods = append(methods, ssh.Password(password))
		desc = "密码"
	}
	if k := pickKey(n.SSHPass, password); k != "" {
		if signer, err := ssh.ParsePrivateKey([]byte(k)); err == nil {
			methods = append(methods, ssh.PublicKeys(signer))
			desc += "+密钥"
		}
	}
	if len(methods) == 0 {
		methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, qs []string, echos []bool) ([]string, error) {
			return nil, fmt.Errorf("需要密码")
		}))
	}
	return methods, desc
}

func pickKey(_ ...string) string { return "" }

// directSSH dials the node's sshd from the panel.
func (s *Server) directSSH(ws *websocket.Conn, n *store.Node, password string, cols, rows int) {
	host := n.Host
	port := n.SSHPort
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	methods, _ := s.sshAuth(n, password)

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            defaultUser(n.SSHUser),
		Auth:            methods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         12 * time.Second,
		ClientVersion:   "SSH-2.0-nodepanel_" + shared.Version,
	})
	if err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{
			Type:    "error",
			Payload: "SSH 连接 " + addr + " 失败: " + err.Error() + "\n提示: 若该节点只能通过 cloudflared 访问，请在节点设置中开启「通过 Agent 中继 SSH」。",
		}))
		return
	}
	defer client.Close()
	s.pumpSSH(ws, client, cols, rows)
}

// relaySSH tunnels the terminal through the agent, which dials its own sshd.
func (s *Server) relaySSH(ws *websocket.Conn, n *store.Node, password string, cols, rows int) {
	base := AgentURL(n)
	if base == "" {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{Type: "error", Payload: "节点地址无效"}))
		return
	}
	u := "ws" + base[4:] + "/api/v1/ssh/relay?token=" + urlQueryEscape(n.Token)
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, ReadBufferSize: 8192, WriteBufferSize: 8192}
	remote, resp, err := dialer.Dial(u, nil)
	if err != nil {
		msg := err.Error()
		if resp != nil {
			if b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048)); len(b) > 0 {
				msg = string(b)
			}
		}
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{
			Type: "error", Payload: "无法通过 Agent 建立 SSH 通道: " + msg,
		}))
		return
	}
	defer remote.Close()

	// The agent authenticates with the password configured at install time, so
	// an empty payload is fine here.
	_ = remote.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{Type: "auth", Payload: password}))
	if cols > 0 && rows > 0 {
		_ = remote.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{Type: "resize", Cols: cols, Rows: rows}))
	}

	done := make(chan struct{}, 2)
	pipe := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			typ, data, err := src.ReadMessage()
			if err != nil {
				_ = dst.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := dst.WriteMessage(typ, data); err != nil {
				return
			}
		}
	}
	go pipe(remote, ws)
	go pipe(ws, remote)
	<-done
}

// pumpSSH connects a live SSH session to the browser terminal.
func (s *Server) pumpSSH(ws *websocket.Conn, client *ssh.Client, cols, rows int) {
	session, err := client.NewSession()
	if err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(wsMessage{Type: "error", Payload: err.Error()}))
		return
	}
	defer session.Close()

	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 30
	}
	if err := session.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{
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

	var wmu sync.Mutex
	sink := writerFunc(func(p []byte) (int, error) {
		wmu.Lock()
		defer wmu.Unlock()
		if err := ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
			return 0, err
		}
		return len(p), nil
	})
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
			if typ == websocket.BinaryMessage {
				if _, err := stdin.Write(data); err != nil {
					return
				}
				continue
			}
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
				c, r := msg.Cols, msg.Rows
				if c > 0 && r > 0 {
					_ = session.WindowChange(r, c)
				}
			}
		}
	}()

	waitErr := session.Wait()
	out := wsMessage{Type: "exit"}
	if waitErr != nil {
		out.Payload = waitErr.Error()
	}
	wmu.Lock()
	_ = ws.WriteMessage(websocket.TextMessage, mustJSON(out))
	wmu.Unlock()
	<-done
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func defaultUser(u string) string {
	if u == "" {
		return "root"
	}
	return u
}

func urlQueryEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b = append(b, c)
			continue
		}
		b = append(b, '%', hex[c>>4], hex[c&15])
	}
	return string(b)
}
