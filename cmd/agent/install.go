//go:build linux

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/geekermd/nodepanel/internal/agent"
	"github.com/geekermd/nodepanel/internal/shared"
)

const unitPath = "/etc/systemd/system/nodemgr-agent.service"
const installDir = "/usr/local/bin"
const configDir = "/etc/nodemgr-agent"

// runInstall writes the config, installs the binary and registers a systemd
// unit. Port and password are the two things the operator always sets, so they
// are the two things the installer asks for.
func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	port := fs.Int("port", 8899, "监听端口，0 = 仅内网穿透")
	password := fs.String("password", "", "面板访问密码（同时作为本机 root 密码用于 SSH 中继）")
	token := fs.String("token", "", "面板访问令牌（留空则自动生成）")
	sshUser := fs.String("ssh-user", "root", "SSH 用户名")
	sshPort := fs.Int("ssh-port", 22, "SSH 端口")
	sshPassword := fs.String("ssh-password", "", "SSH 密码（默认与访问密码相同）")
	tunnel := fs.Bool("tunnel", false, "启用 cloudflared 内网穿透")
	tunnelMode := fs.String("tunnel-mode", "quick", "quick | token")
	tunnelToken := fs.String("tunnel-token", "", "cloudflared token（token 模式）")
	interval := fs.Int("interval", 2, "采样间隔（秒）")
	history := fs.Int("history", 300, "内存中保留的历史时长（秒）")
	binPath := fs.String("bin", "", "已安装的二进制路径（默认复制当前可执行文件）")
	noStart := fs.Bool("no-start", false, "只写配置，不启动服务")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "参数错误:", err)
		return 2
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "错误: 安装需要 root 权限，请使用 sudo 运行。")
		return 1
	}

	// Ask interactively for anything not provided, but never block a
	// non-interactive run (e.g. `curl ... | bash`).
	if *password == "" && isTTY() {
		*password = prompt("请设置管理密码（面板采集与 SSH 登录共用）: ", true)
	}
	if *password == "" {
		*password = shared.RandomToken(12)
		fmt.Printf("未指定密码，已自动生成: %s\n", *password)
	}

	tok := *token
	if tok == "" {
		tok = shared.RandomToken(24)
	}
	if *sshPassword == "" {
		*sshPassword = *password
	}

	// 1. Install the binary.
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法定位当前可执行文件:", err)
		return 1
	}
	if *binPath == "" {
		*binPath = filepath.Join(installDir, "nodemgr-agent")
	}
	if err := copyFile(self, *binPath, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "安装二进制失败:", err)
		return 1
	}

	// 2. Config file.
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "创建配置目录失败:", err)
		return 1
	}
	cfg := agent.DefaultConfig()
	cfg.Port = *port
	cfg.AdminHash = shared.HashToken(tok)
	cfg.SSHUser = *sshUser
	cfg.SSHPort = *sshPort
	cfg.SSHPassword = *sshPassword
	cfg.SSHRelayEnable = true
	cfg.Interval = *interval
	cfg.History = *history
	cfg.Tunnel.Enabled = *tunnel
	cfg.Tunnel.Mode = *tunnelMode
	cfg.Tunnel.Token = *tunnelToken
	if err := cfg.SaveTo(filepath.Join(configDir, "config.json")); err != nil {
		fmt.Fprintln(os.Stderr, "写入配置失败:", err)
		return 1
	}

	// 3. systemd unit.
	unit := `[Unit]
Description=nodepanel node agent (轻量服务器监控)
Documentation=https://github.com/geekermd/nodepanel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=NODEMGR_HOME=` + configDir + `
ExecStart=` + *binPath + ` -config ` + configDir + `/config.json
Restart=always
RestartSec=5
# 轻量且受限：只读访问 /proc /sys
Nice=5
CPUWeight=20
IOWeight=20
MemoryMax=128M
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=` + configDir + `
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`
	if err := shared.WriteFileAtomic(unitPath, []byte(unit), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写入 systemd 单元失败:", err)
		return 1
	}

	_ = exec.Command("systemctl", "daemon-reload").Run()
	if !*noStart {
		if out, err := exec.Command("systemctl", "enable", "--now", "nodemgr-agent").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "启动服务失败: %v\n%s\n", err, out)
			fmt.Fprintln(os.Stderr, "可手动排查: systemctl status nodemgr-agent")
			return 1
		}
	}

	// 4. Report.
	host := agent.HostInfoCached()
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("  nodepanel 节点端安装完成")
	fmt.Println("========================================")
	fmt.Printf("  主机名    : %s\n", host.Hostname)
	fmt.Printf("  系统      : %s (%s)\n", host.OS, host.Arch)
	fmt.Printf("  监听端口  : %d\n", *port)
	fmt.Printf("  访问令牌  : %s\n", tok)
	fmt.Printf("  管理密码  : %s\n", maskIf(*password, tok))
	fmt.Printf("  SSH 中继  : %s\n", onOff(*sshPassword != ""))
	fmt.Printf("  内网穿透  : %s\n", tunnelDesc(*tunnel, *tunnelMode))
	fmt.Println("----------------------------------------")
	fmt.Println("  面板添加节点时填写：")
	fmt.Printf("    地址: %s\n", host.Hostname)
	fmt.Printf("    端口: %d\n", *port)
	fmt.Printf("    令牌: %s\n", tok)
	fmt.Println("========================================")
	fmt.Println("  服务管理: systemctl status|restart nodemgr-agent")
	fmt.Printf("  配置位置: %s/config.json\n", configDir)
	if *tunnel {
		fmt.Println("  穿透地址: journalctl -u nodemgr-agent -f | grep trycloudflare")
	}
	return 0
}

func runUninstall() int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "错误: 需要 root 权限。")
		return 1
	}
	_ = exec.Command("systemctl", "disable", "--now", "nodemgr-agent").Run()
	_ = os.Remove(unitPath)
	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = os.Remove(filepath.Join(installDir, "nodemgr-agent"))
	fmt.Println("已卸载 nodemgr-agent 服务与二进制。")
	fmt.Printf("配置目录保留在 %s，如需彻底清理: rm -rf %s\n", configDir, configDir)
	return 0
}

func isTTY() bool {
	st, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func prompt(label string, secret bool) string {
	fmt.Print(label)
	if !secret {
		var s string
		fmt.Scanln(&s)
		return strings.TrimSpace(s)
	}
	// `stty -echo` keeps the password off the screen without pulling in a
	// terminal library.
	_ = exec.Command("stty", "-echo").Run()
	var s string
	fmt.Scanln(&s)
	_ = exec.Command("stty", "echo").Run()
	fmt.Println()
	return strings.TrimSpace(s)
}

func maskIf(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func onOff(b bool) string {
	if b {
		return "已启用"
	}
	return "未启用"
}

func tunnelDesc(enabled bool, mode string) string {
	if !enabled {
		return "未启用"
	}
	if mode == "token" {
		return "已启用 (token 模式，使用自有域名)"
	}
	return "已启用 (quick 模式，临时域名)"
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Never copy onto ourselves.
	if a, err1 := filepath.Abs(src); err1 == nil {
		if b, err2 := filepath.Abs(dst); err2 == nil && a == b {
			return nil
		}
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

var _ = strconv.Itoa
