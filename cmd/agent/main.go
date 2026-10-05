//go:build linux

// Command nodemgr-agent is the node side of nodepanel: a single static binary
// that reports resource metrics and relays SSH for hosts that are only
// reachable through a cloudflared tunnel.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/geekermd/nodepanel/internal/agent"
	"github.com/geekermd/nodepanel/internal/shared"
)

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[agent] ")

	cfgPath := defaultConfigPath()
	args := os.Args[1:]

	// `nodemgr-agent install ...` writes the config and a systemd unit.
	if len(args) > 0 && args[0] == "install" {
		os.Exit(runInstall(args[1:]))
	}
	if len(args) > 0 && args[0] == "uninstall" {
		os.Exit(runUninstall())
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		usage()
		return
	}
	if len(args) > 0 && (args[0] == "version" || args[0] == "-v") {
		fmt.Println("nodemgr-agent", shared.Version)
		return
	}

	for i, a := range args {
		if a == "-config" && i+1 < len(args) {
			cfgPath = args[i+1]
		}
	}

	cfg, err := agent.LoadConfig(cfgPath, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage()
			return
		}
		log.Fatalf("加载配置失败: %v", err)
	}
	if cfg.AdminHash == "" {
		log.Printf("警告: 尚未设置访问令牌，面板无法采集本节点。")
		log.Printf("       请执行: %s -gen-token  然后把输出的令牌填进面板。", os.Args[0])
	}

	sampler := agent.NewSampler(cfg.History, cfg.Interval)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Liveness file: the install script and systemd use it for health checks.
	writePID()

	stop := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(stop)
	}()
	go sampler.Run(time.Duration(cfg.Interval)*time.Second, stop)

	srv := agent.NewServer(cfg, sampler)
	go func() {
		// Tiny self-probe every 60s so "访问量/请求数" reflects ongoing traffic
		// even when nobody is looking at the panel.
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		client := &http.Client{Timeout: 3 * time.Second}
		url := fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Port)
		if cfg.Port <= 0 {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if resp, err := client.Get(url); err == nil {
					resp.Body.Close()
				}
			}
		}
	}()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("%v", err)
	}
}

func defaultConfigPath() string {
	if v := os.Getenv("NODEMGR_CONFIG"); v != "" {
		return v
	}
	if os.Geteuid() == 0 {
		return "/etc/nodemgr-agent/config.json"
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return home + "/.nodemgr-agent.json"
}

func writePID() {
	dir := os.Getenv("NODEMGR_HOME")
	if dir == "" {
		dir = "/etc/nodemgr-agent"
	}
	if _, err := os.Stat(dir); err != nil {
		return
	}
	_ = os.WriteFile(dir+"/agent.pid", []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
}

func usage() {
	fmt.Print(`nodepanel 节点端 - nodemgr-agent ` + shared.Version + `

用法:
  nodemgr-agent [选项]                 启动采集与 API 服务
  nodemgr-agent install [选项]         安装为 systemd 服务（需 root）
  nodemgr-agent uninstall              卸载 systemd 服务
  nodemgr-agent -gen-token             生成新的访问令牌并写入配置

常用选项:
  -config <path>        配置文件路径 (默认 /etc/nodemgr-agent/config.json)
  -port <n>             公网监听端口，0 = 仅 cloudflared 内网穿透 (默认 8899)
  -bind <addr>          监听地址 (默认 0.0.0.0)
  -token <tok>          设置面板访问令牌
  -ssh-user <name>      SSH 用户名 (默认 root)
  -ssh-port <n>         SSH 端口 (默认 22)
  -ssh-password <pw>    允许面板通过本节点中继 SSH（内网穿透场景）
  -tunnel               启用 cloudflared 内网穿透
  -tunnel-mode <m>      quick (临时域名) 或 token (自有域名)
  -tunnel-token <tok>   token 模式所需的 cloudflared token
  -interval <sec>       采样间隔 (默认 2)
  -history <sec>        常驻内存的历史时长 (默认 300)

安装示例:
  sudo ./nodemgr-agent install -port 8899 -password 'MyRootPw' -tunnel
  sudo ./nodemgr-agent install -password 'MyRootPw' -tunnel -tunnel-mode token -tunnel-token eyJ...
`)
}
