//go:build linux

// Command nodemgr-panel is the local management panel. It runs on your own
// machine (or wherever you keep the browser), polls every configured node over
// HTTP and keeps the history locally, so the nodes themselves stay tiny.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/geekermd/nodepanel/internal/panel"
	"github.com/geekermd/nodepanel/internal/shared"
	"github.com/geekermd/nodepanel/internal/store"
)

//go:embed all:web
var webFS embed.FS

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[panel] ")

	var (
		listen  = flag.String("listen", "", "监听地址 (默认 127.0.0.1:8787)")
		home    = flag.String("home", "", "数据目录 (默认 ~/.nodepanel)")
		openAll = flag.Bool("lan", false, "允许局域网访问（监听 0.0.0.0）")
		setPw   = flag.String("set-password", "", "设置面板密码后退出")
		showVer = flag.Bool("version", false, "打印版本")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("nodemgr-panel", shared.Version)
		return
	}

	dir := *home
	if dir == "" {
		dir = shared.DefaultStateDir()
	}
	st, err := store.Open(dir)
	if err != nil {
		log.Fatalf("打开数据目录失败: %v", err)
	}

	if *setPw != "" {
		if len(*setPw) < 4 {
			log.Fatalf("密码至少 4 位")
		}
		if err := st.UpdateSettings(func(s *store.Settings) { s.PasswordHash = shared.HashToken(*setPw) }); err != nil {
			log.Fatalf("保存密码失败: %v", err)
		}
		fmt.Println("密码已更新。")
		return
	}

	assets, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("加载内置前端失败: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	poller := panel.NewPoller(st)
	poller.Start(ctx)

	srv := panel.NewServer(st, poller, assets)
	if pw, created := srv.EnsurePassword(); created {
		fmt.Println()
		fmt.Println("┌────────────────────────────────────────────────┐")
		fmt.Println("│  nodepanel 首次启动，已生成管理密码            │")
		fmt.Println("└────────────────────────────────────────────────┘")
		fmt.Printf("   地址: http://%s\n", listenAddr(*listen, st, *openAll))
		fmt.Printf("   密码: %s\n", pw)
		fmt.Println("   （可用 nodemgr-panel -set-password 新密码 修改）")
		fmt.Println()
	}

	addr := listenAddr(*listen, st, *openAll)
	if err := st.UpdateSettings(func(s *store.Settings) { s.Listen = addr }); err != nil {
		log.Printf("保存监听设置失败: %v", err)
	}

	// Hourly history compaction on top of the poller's own schedule.
	go func() {
		t := time.NewTicker(2 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, n := range st.ListNodes() {
					_ = st.Series(n.ID).Compact()
				}
			}
		}
	}()

	if err := srv.ListenAndServe(ctx, addr); err != nil {
		log.Fatalf("%v", err)
	}
}

func listenAddr(flagVal string, st *store.Store, lan bool) string {
	if flagVal != "" {
		return flagVal
	}
	if lan {
		return "0.0.0.0:8787"
	}
	set := st.GetSettings()
	if set.Listen != "" && strings.HasSuffix(set.Listen, ":8787") {
		return set.Listen
	}
	return "127.0.0.1:8787"
}
