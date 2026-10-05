//go:build linux

package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TunnelStatus is what the panel shows for a node that has no reachable port
// and is published through a cloudflared tunnel instead.
type TunnelStatus struct {
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	URL     string `json:"url"`
	Mode    string `json:"mode"`
	Binary  string `json:"binary"`
	PID     int    `json:"pid"`
	Err     string `json:"err"`
	Log     string `json:"log"`
	Local   string `json:"local"`
}

// Tunnel supervises the cloudflared child process.
type Tunnel struct {
	cfg *Config

	mu      sync.Mutex
	cmd     *exec.Cmd
	url     string
	err     string
	logTail []string
	running bool
	pid     int
}

func NewTunnel(cfg *Config) *Tunnel { return &Tunnel{cfg: cfg} }

var urlRe = regexp.MustCompile(`https://[a-z0-9][a-z0-9-]*\.trycloudflare\.com`)

// Start launches cloudflared if configured. It never blocks the caller.
func (t *Tunnel) Start(ctx context.Context) error {
	if t == nil || !t.cfg.Tunnel.Enabled {
		return nil
	}
	if t.cfg.Tunnel.Mode == "token" && t.cfg.Tunnel.Token == "" {
		t.setErr("token 模式需要填写 cloudflared token")
		return fmt.Errorf("token 模式需要填写 cloudflared token")
	}

	bin := t.cfg.Tunnel.Binary
	if bin == "" {
		bin = "cloudflared"
	}
	resolved, err := ensureCloudflared(ctx, bin)
	if err != nil {
		t.setErr(err.Error())
		return err
	}
	t.cfg.Tunnel.Binary = resolved

	local := fmt.Sprintf("http://127.0.0.1:%d", internalPort(t.cfg))
	var args []string
	if t.cfg.Tunnel.Mode == "token" {
		args = []string{"tunnel", "--no-autoupdate", "--url", local, "run", "--token", t.cfg.Tunnel.Token}
	} else {
		args = []string{"tunnel", "--no-autoupdate", "--url", local}
	}

	cmd := exec.Command(resolved, args...)
	cmd.Env = append(os.Environ(), "NO_AUTOUPDATE=true")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.setErr(err.Error())
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.setErr(err.Error())
		return err
	}
	cmd.SysProcAttr = sysProcAttrChild()
	if err := cmd.Start(); err != nil {
		t.setErr("启动 cloudflared 失败: " + err.Error())
		return err
	}
	t.mu.Lock()
	t.cmd = cmd
	t.running = true
	t.pid = cmd.Process.Pid
	t.err = ""
	t.mu.Unlock()
	log.Printf("cloudflared 已启动 (pid %d, 模式 %s, 本地 %s)", cmd.Process.Pid, t.cfg.Tunnel.Mode, local)

	go t.consume(stderr)
	go t.consume(stdout)

	go func() {
		err := cmd.Wait()
		t.mu.Lock()
		t.running = false
		if err != nil {
			t.err = "cloudflared 退出: " + err.Error()
		}
		t.mu.Unlock()
		log.Printf("cloudflared 已退出: %v", err)
		if ctx.Err() == nil && t.cfg.Tunnel.Enabled {
			// Simple restart with backoff so a flaky uplink recovers.
			time.Sleep(10 * time.Second)
			if ctx.Err() == nil {
				_ = t.Start(ctx)
			}
		}
	}()
	return nil
}

func (t *Tunnel) consume(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		t.mu.Lock()
		t.logTail = append(t.logTail, line)
		if len(t.logTail) > 40 {
			t.logTail = t.logTail[len(t.logTail)-40:]
		}
		if m := urlRe.FindString(line); m != "" && t.url != m {
			t.url = m
			log.Printf("cloudflared 公网地址: %s", m)
		}
		t.mu.Unlock()
	}
}

func (t *Tunnel) setErr(msg string) {
	t.mu.Lock()
	t.err = msg
	t.mu.Unlock()
}

// Status returns a copy of the current tunnel state.
func (t *Tunnel) Status() TunnelStatus {
	if t == nil {
		return TunnelStatus{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return TunnelStatus{
		Enabled: t.cfg.Tunnel.Enabled,
		Running: t.running,
		URL:     t.url,
		Mode:    t.cfg.Tunnel.Mode,
		Binary:  t.cfg.Tunnel.Binary,
		PID:     t.pid,
		Err:     t.err,
		Log:     strings.Join(t.logTail, "\n"),
		Local:   fmt.Sprintf("http://127.0.0.1:%d", internalPort(t.cfg)),
	}
}

// localPort is the port cloudflared forwards to. When the public listener is
// disabled we still need a loopback port for the tunnel to reach.
func internalPort(cfg *Config) int {
	if cfg.Port > 0 {
		return cfg.Port
	}
	if cfg.Tunnel.LocalPort > 0 {
		return cfg.Tunnel.LocalPort
	}
	return 8899
}

// ensureCloudflared locates the binary, downloading a static build when it is
// missing (some hosts have no package for it).
func ensureCloudflared(ctx context.Context, bin string) (string, error) {
	if p, err := exec.LookPath(bin); err == nil {
		return p, nil
	}
	for _, c := range []string{"/usr/local/bin/cloudflared", "/usr/bin/cloudflared", "/opt/cloudflared/cloudflared"} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	if !autoDownload {
		return "", fmt.Errorf("未找到 cloudflared，请先安装后重试")
	}
	dst := filepath.Join(agentStateDir(), "cloudflared")
	if st, err := os.Stat(dst); err == nil && st.Mode()&0o111 != 0 {
		return dst, nil
	}
	const url = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64"
	log.Printf("未找到 cloudflared，正在下载 %s", url)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 cloudflared 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("下载 cloudflared 失败: HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	log.Printf("cloudflared 已下载到 %s", dst)
	return dst, nil
}

func agentStateDir() string {
	if v := os.Getenv("NODEMGR_HOME"); v != "" {
		return v
	}
	return "/etc/nodemgr-agent"
}

// autoDownload can be turned off for air-gapped hosts.
var autoDownload = os.Getenv("NODEMGR_NO_DOWNLOAD") != "1"

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}
