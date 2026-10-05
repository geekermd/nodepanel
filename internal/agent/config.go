//go:build linux

package agent

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/geekermd/nodepanel/internal/shared"
)

// Config is the agent configuration, stored as JSON. Command line flags and
// environment variables override the file, which keeps the systemd unit simple
// while still allowing `nodemgr-agent -port 9000` one-off runs.
type Config struct {
	// Port is the HTTP port the agent listens on. 0 means "cloudflared only"
	// (no public listener), which is the safe default for tunnel-only nodes.
	Port int    `json:"port"`
	Bind string `json:"bind"`

	// Token is the plaintext token, only used on first install. The running
	// agent authenticates against AdminHash.
	AdminHash string `json:"admin_tok_hash"`

	// SSHPassword lets the agent act as an SSH relay for hosts where the SSH
	// port itself is not reachable (tunnel-only nodes). Disable by leaving it
	// empty; SSHPasswordEnabled=false keeps the relay off entirely.
	SSHUser        string `json:"ssh_user"`
	SSHPassword    string `json:"ssh_password"`
	SSHHost        string `json:"ssh_host"`
	SSHPort        int    `json:"ssh_port"`
	SSHRelayEnable bool   `json:"ssh_relay"`

	Tunnel struct {
		Enabled   bool   `json:"enabled"`
		Mode      string `json:"mode"` // quick | token
		Token     string `json:"token"`
		Name      string `json:"name"`
		URL       string `json:"url"`        // last known public URL (quick tunnels)
		Binary    string `json:"binary"`     // cloudflared path
		LocalPort int    `json:"local_port"` // loopback port cloudflared forwards to
	} `json:"tunnel"`

	Interval int `json:"interval_sec"`
	History  int `json:"history_sec"`

	// PublicIPLookup makes the agent query a public "what is my IP" service
	// once after start so the panel can show the node's public address.
	PublicIPLookup bool `json:"public_ip_lookup"`

	// statePath is where the config was loaded from (not serialised).
	statePath string
}

// DefaultConfig returns the built-in defaults.
func DefaultConfig() *Config {
	c := &Config{
		Port:     8899,
		Bind:     "0.0.0.0",
		Interval: 2,
		History:  300,
		SSHUser:  "root",
		SSHPort:  22,
	}
	c.Tunnel.Mode = "quick"
	c.Tunnel.Binary = "cloudflared"
	return c
}

// LoadConfig reads the config file, applies env and flag overrides.
func LoadConfig(path string, args []string) (*Config, error) {
	c := DefaultConfig()
	c.statePath = path
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(b, c); err != nil {
				return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	fs := flag.NewFlagSet("nodemgr-agent", flag.ContinueOnError)
	cfgFlag := fs.String("config", path, "配置文件路径")
	localPort := fs.Int("tunnel-local-port", c.Tunnel.LocalPort, "cloudflared 转发的本地端口 (port=0 时使用)")
	port := fs.Int("port", c.Port, "监听端口 (0 = 仅内网穿透)")
	bindFlag := fs.String("bind", c.Bind, "监听地址，127.0.0.1 = 只允许本机/SSH 隧道访问")
	token := fs.String("token", "", "面板访问令牌 (留空则使用配置中的)")
	sshPass := fs.String("ssh-password", c.SSHPassword, "SSH 密码 (供面板内的终端使用)")
	sshUser := fs.String("ssh-user", c.SSHUser, "SSH 用户名")
	sshPort := fs.Int("ssh-port", c.SSHPort, "SSH 端口")
	sshRelay := fs.Bool("ssh-relay", c.SSHRelayEnable, "启用 SSH 中继")
	tunnel := fs.Bool("tunnel", c.Tunnel.Enabled, "启用 cloudflared 内网穿透")
	tunnelMode := fs.String("tunnel-mode", c.Tunnel.Mode, "穿透模式: quick|token")
	tunnelToken := fs.String("tunnel-token", c.Tunnel.Token, "cloudflared token (token 模式)")
	tunnelName := fs.String("tunnel-name", c.Tunnel.Name, "隧道名称 (仅用于展示)")
	publicIP := fs.Bool("public-ip-lookup", c.PublicIPLookup, "启动时查询一次公网 IP（会访问外网接口）")
	interval := fs.Int("interval", c.Interval, "采样间隔(秒)")
	history := fs.Int("history", c.History, "本地保留的历史时长(秒)")
	genToken := fs.Bool("gen-token", false, "生成随机令牌并写入配置文件后退出")
	showVersion := fs.Bool("version", false, "打印版本")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *showVersion {
		fmt.Println("nodemgr-agent", shared.Version)
		os.Exit(0)
	}

	c.Port = *port
	c.Bind = *bindFlag
	c.SSHUser = *sshUser
	c.SSHPort = *sshPort
	if *sshPass != "" {
		c.SSHPassword = *sshPass
		c.SSHRelayEnable = true
	}
	if *sshRelay {
		c.SSHRelayEnable = true
	}
	c.Tunnel.LocalPort = *localPort
	c.Tunnel.Enabled = *tunnel
	c.Tunnel.Mode = *tunnelMode
	c.Tunnel.Token = *tunnelToken
	c.Tunnel.Name = *tunnelName
	c.Interval = *interval
	c.History = *history
	c.PublicIPLookup = *publicIP

	if v := os.Getenv("NODEMGR_PORT"); v != "" && *port == c.Port {
		if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			c.Port = p
		}
	}
	if *cfgFlag != "" {
		c.statePath = *cfgFlag
	}
	if *token != "" {
		c.AdminHash = shared.HashToken(*token)
		if err := c.Save(); err != nil {
			return nil, err
		}
	}
	if *genToken {
		t := shared.RandomToken(24)
		c.AdminHash = shared.HashToken(t)
		if err := c.Save(); err != nil {
			return nil, err
		}
		fmt.Printf("新令牌: %s\n", t)
		fmt.Printf("已写入: %s\n", c.statePath)
		os.Exit(0)
	}

	if c.Bind == "" {
		c.Bind = "0.0.0.0"
	}
	if c.Interval < 1 {
		c.Interval = 2
	}
	if c.History < 60 {
		c.History = 300
	}
	return c, nil
}

// Save writes the configuration back to disk with owner-only permissions.
func (c *Config) Save() error {
	if c.statePath == "" {
		return nil
	}
	if d := dirOf(c.statePath); d != "" {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return shared.WriteFileAtomic(c.statePath, b, 0o600)
}

func dirOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return ""
	}
	return p[:i]
}

// CheckToken validates a plaintext token against the configured hash.
func (c *Config) CheckToken(plain string) bool {
	return shared.CheckToken(c.AdminHash, plain)
}

// SaveTo writes the configuration to an explicit path.
func (c *Config) SaveTo(path string) error {
	prev := c.statePath
	c.statePath = path
	err := c.Save()
	c.statePath = prev
	return err
}

// HumanUptime renders the host uptime.
func (s *Sampler) HumanUptime() string {
	up := time.Now().Unix() - s.host.BootTime
	return shared.Duration(float64(up))
}
