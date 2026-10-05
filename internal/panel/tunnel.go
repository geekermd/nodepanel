package panel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/geekermd/nodepanel/internal/shared"
	"github.com/geekermd/nodepanel/internal/store"
)

// sshTunnel keeps a persistent SSH connection to a node and dials the node's
// agent through it (ssh direct-tcpip channel). This is the recommended mode
// for cloud servers whose firewall only exposes SSH: no extra port, and the
// agent can stay bound to 127.0.0.1.
type sshTunnel struct {
	mu      sync.Mutex
	client  *ssh.Client
	fp      string // 主机密钥指纹
	created time.Time
	err     string
}

// HostKeyFingerprint returns the recorded host key (trust on first use).
func (t *sshTunnel) Fingerprint() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fp
}

func (t *sshTunnel) snapshot() (*ssh.Client, string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.client, t.err, t.fp
}

// alive probes the connection. Cloud servers routinely drop idle SSH sessions,
// so a cached client must be validated before reuse.
func (t *sshTunnel) alive(client *ssh.Client) bool {
	if client == nil {
		return false
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(5 * time.Second):
		return false
	}
}

func (t *sshTunnel) setErr(err error) {
	t.mu.Lock()
	t.err = err.Error()
	t.mu.Unlock()
}

// connect (re)establishes the tunnel if needed.
func (t *sshTunnel) connect(ctx context.Context, n *store.Node, password, knownHostKey string) (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		// 复用前先探活：SSH 连接静默断开时直接发数据才发现会拖慢轮询。
		if _, _, err := t.client.Conn.SendRequest("keepalive@openssh.com", true, nil); err == nil {
			return t.client, nil
		}
		_ = t.client.Close()
		t.client = nil
	}

	auth, err := sshAuthMethods(n, password)
	if err != nil {
		t.err = err.Error()
		return nil, err
	}
	host := shared.NormalizeHost(n.Host)
	port := n.SSHPort
	if port == 0 {
		port = 22
	}
	var seenFP string
	cfg := &ssh.ClientConfig{
		User: defaultUser(n.SSHUser),
		Auth: auth,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			seenFP = ssh.FingerprintSHA256(key)
			if knownHostKey == "" || knownHostKey == seenFP {
				return nil
			}
			return fmt.Errorf("主机密钥已变化（可能是重装了系统或被中间人劫持）: 记录=%s 现在=%s",
				knownHostKey, seenFP)
		},
		Timeout:       12 * time.Second,
		ClientVersion: "SSH-2.0-nodepanel_" + shared.Version,
	}
	dialer := &net.Dialer{Timeout: 12 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.err = "SSH 连接失败: " + err.Error()
		return nil, fmt.Errorf("%s", t.err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(host, strconv.Itoa(port)), cfg)
	if err != nil {
		conn.Close()
		t.err = "SSH 认证失败: " + err.Error()
		return nil, fmt.Errorf("%s", t.err)
	}
	client := ssh.NewClient(c, chans, reqs)
	t.client = client
	t.created = time.Now()
	t.err = ""
	if seenFP != "" && seenFP != knownHostKey {
		t.fp = seenFP
	}
	// 保持连接活跃，并自动清理死连接。
	go keepAlive(client, t)
	return client, nil
}

func keepAlive(client *ssh.Client, t *sshTunnel) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for range tick.C {
		if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			t.mu.Lock()
			if t.client == client {
				t.client = nil
			}
			t.mu.Unlock()
			_ = client.Close()
			return
		}
	}
}

func (t *sshTunnel) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		_ = t.client.Close()
		t.client = nil
	}
}

// dial opens a TCP channel to the agent through the tunnel. It validates the
// cached connection first and transparently reconnects once when it has died.
func (t *sshTunnel) dial(ctx context.Context, n *store.Node, password string) (net.Conn, error) {
	for attempt := 1; attempt <= 2; attempt++ {
		client, _ := t.usableClient(ctx, n, password)
		if client == nil {
			return nil, fmt.Errorf("SSH 隧道不可用")
		}
		conn, err := t.openChannel(ctx, client, n)
		if err == nil {
			return conn, nil
		}
		// 连接可能已被服务端回收：丢弃后重试一次。
		t.close()
		if attempt == 2 {
			return nil, err
		}
	}
	return nil, fmt.Errorf("SSH 隧道不可用")
}

// usableClient returns a connection that answers a keepalive, reconnecting if
// the cached one is stale.
func (t *sshTunnel) usableClient(ctx context.Context, n *store.Node, password string) (*ssh.Client, error) {
	client, _, _ := t.snapshot()
	if client != nil && t.alive(client) {
		return client, nil
	}
	if client != nil {
		t.close()
	}
	return t.connect(ctx, n, password, n.SSHHostKey)
}

func (t *sshTunnel) openChannel(ctx context.Context, client *ssh.Client, n *store.Node) (net.Conn, error) {
	host := n.AgentHost()
	port := n.Port
	if port <= 0 {
		port = 8899
	}
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	go func() {
		c, err := client.Dial("tcp", addr)
		if err != nil {
			log.Printf("ssh隧道: 建立通道失败 node=%s (%s -> %s, ssh=%s): %v",
				n.Name, n.Mode, addr, client.RemoteAddr(), err)
		}
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("通过 SSH 隧道连接 %s 失败: %w", addr, r.err)
		}
		return r.conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("通过 SSH 隧道连接 %s 超时", addr)
	}
}

// sshAuthMethods builds the auth list: password (typed or remembered) and key.
func sshAuthMethods(n *store.Node, password string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if password == "" {
		password = n.SSHPass
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
	}
	if strings.TrimSpace(n.SSHKey) != "" {
		signer, err := ssh.ParsePrivateKey([]byte(n.SSHKey))
		if err != nil {
			return nil, fmt.Errorf("SSH 私钥解析失败: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("未配置 SSH 密码或私钥")
	}
	return methods, nil
}

// ---------------------------------------------------------------------------
// Poller 上的隧道管理
// ---------------------------------------------------------------------------

func (p *Poller) tunnelFor(id string) *sshTunnel {
	p.tmu.Lock()
	defer p.tmu.Unlock()
	t, ok := p.tunnels[id]
	if !ok {
		t = &sshTunnel{}
		p.tunnels[id] = t
	}
	return t
}

// TunnelStatus 用于面板展示 SSH 隧道状态。
func (p *Poller) TunnelStatus(id string) map[string]any {
	p.tmu.Lock()
	t, ok := p.tunnels[id]
	p.tmu.Unlock()
	if !ok {
		return map[string]any{"connected": false}
	}
	client, errMsg, fp := t.snapshot()
	out := map[string]any{
		"connected":      client != nil,
		"host_key":       fp,
		"host_key_short": shortFingerprint(fp),
	}
	if errMsg != "" {
		out["error"] = errMsg
	}
	return out
}

func shortFingerprint(fp string) string {
	if fp == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fp))
	return base64.RawStdEncoding.EncodeToString(sum[:6])
}

// ForgetTunnel drops the tunnel of a removed/edited node.
func (p *Poller) ForgetTunnel(id string) {
	p.tmu.Lock()
	t := p.tunnels[id]
	delete(p.tunnels, id)
	p.tmu.Unlock()
	if t != nil {
		t.close()
	}
}

// TestSSH checks credentials and connectivity without saving anything.
func (p *Poller) TestSSH(ctx context.Context, n *store.Node, password string) (string, error) {
	auth, err := sshAuthMethods(n, password)
	if err != nil {
		return "", err
	}
	port := n.SSHPort
	if port == 0 {
		port = 22
	}
	var fp string
	cfg := &ssh.ClientConfig{
		User: defaultUser(n.SSHUser),
		Auth: auth,
		HostKeyCallback: func(h string, r net.Addr, key ssh.PublicKey) error {
			fp = ssh.FingerprintSHA256(key)
			return nil
		},
		Timeout:       10 * time.Second,
		ClientVersion: "SSH-2.0-nodepanel_" + shared.Version,
	}
	addr := net.JoinHostPort(shared.NormalizeHost(n.Host), strconv.Itoa(port))
	d := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	defer conn.Close()
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		return "", fmt.Errorf("SSH 认证失败: %w", err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	// 顺手确认一下 agent 通道能不能建立。
	out := "SSH 登录成功 · " + addr
	if n.Mode == store.ModeSSH {
		agentAddr := net.JoinHostPort(n.AgentHost(), strconv.Itoa(n.AgentPortOrDefault()))
		ch, err := client.Dial("tcp", agentAddr)
		if err != nil {
			out += " · 但隧道到 agent (" + agentAddr + ") 失败: " + err.Error()
		} else {
			ch.Close()
			out += " · 隧道到 agent (" + agentAddr + ") 正常"
		}
	}
	if fp != "" {
		out += " · 主机密钥 " + fp
	}
	return out, nil
}

func (p *Poller) logf(format string, args ...any) { log.Printf(format, args...) }
