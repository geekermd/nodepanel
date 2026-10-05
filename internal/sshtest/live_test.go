package sshtest_test

// 这是一个"真实链路"冒烟测试：以当前运行的面板为目标，验证某台服务器节点的
// 终端通道能真正打开 shell。默认跳过，需要时这样跑：
//
//	NP_LIVE=1 NP_PANEL=http://127.0.0.1:8787 NP_PASS=密码 \
//	NP_NODE=节点ID NP_SSHPASS=服务器SSH密码 go test ./internal/sshtest/ -run Live -v

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestLiveTerminalOverTunnel(t *testing.T) {
	if os.Getenv("NP_LIVE") != "1" {
		t.Skip("设置 NP_LIVE=1 才会执行真实链路测试")
	}
	panel := env("NP_PANEL", "http://127.0.0.1:8787")
	nodeID := os.Getenv("NP_NODE")
	sshPass := os.Getenv("NP_SSHPASS")
	if nodeID == "" || sshPass == "" {
		t.Fatal("需要 NP_NODE 与 NP_SSHPASS")
	}

	// 登录拿会话 Cookie
	body := strings.NewReader(fmt.Sprintf(`{"password":%q}`, os.Getenv("NP_PASS")))
	resp, err := http.Post(panel+"/api/login", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("登录失败: %d", resp.StatusCode)
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == "np_session" {
			cookie = c.Name + "=" + c.Value
		}
	}

	hdr := http.Header{}
	hdr.Set("Cookie", cookie)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(panel, "http")+"/api/nodes/"+nodeID+"/ssh", hdr)
	if err != nil {
		t.Fatalf("终端 WebSocket 连接失败: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(map[string]any{"type": "auth", "payload": sshPass, "cols": 100, "rows": 30}); err != nil {
		t.Fatal(err)
	}

	// 等 shell 起来，然后跑一条命令并检查回显。
	// 注意：websocket 读超时会让连接进入不可恢复状态，所以这里给一个
	// 足够长的读窗口，而不是每秒重试。
	_ = ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	out := &strings.Builder{}
	sent := false
	for {
		typ, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("读取终端输出失败: %v（已收到 %d 字节）", err, out.Len())
		}
		if typ == websocket.TextMessage {
			var msg map[string]any
			if json.Unmarshal(data, &msg) == nil && msg["type"] == "error" {
				t.Fatalf("终端返回错误: %v", msg["payload"])
			}
			continue
		}
		out.Write(data)
		if !sent {
			sent = true
			_ = ws.WriteJSON(map[string]any{"type": "input", "payload": "echo NODEPANEL_LIVE_$((6*7))\n"})
		}
		if strings.Contains(out.String(), "NODEPANEL_LIVE_42") {
			t.Logf("终端通道正常，远端 shell 回显成功（%d 字节）", out.Len())
			return
		}
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
