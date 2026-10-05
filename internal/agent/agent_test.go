//go:build linux

package agent

import (
	"encoding/json"
	"testing"
)

func TestRingWrapAround(t *testing.T) {
	r := newRingSized(4)
	for i := 1; i <= 6; i++ {
		r.Push(Sample{T: int64(i), CPU: float64(i)})
	}
	if r.Len() != 4 {
		t.Fatalf("len = %d, want 4", r.Len())
	}
	all := r.Since(0)
	if len(all) != 4 {
		t.Fatalf("Since(0) = %d points", len(all))
	}
	// 环形缓冲必须按时间顺序返回，且丢掉最老的两条。
	if all[0].T != 3 || all[3].T != 6 {
		t.Fatalf("order wrong: %+v", all)
	}
	last, ok := r.Last()
	if !ok || last.T != 6 {
		t.Fatalf("Last = %+v", last)
	}
	// since 只返回严格更新的点。
	recent := r.Since(4)
	if len(recent) != 2 || recent[0].T != 5 {
		t.Fatalf("Since(4) = %+v", recent)
	}
	if got := r.Since(6); len(got) != 0 {
		t.Fatalf("Since(6) = %+v, want empty", got)
	}
}

func TestSamplerTickProducesSaneValues(t *testing.T) {
	s := NewSampler(60, 2)
	first := s.Tick()
	if first.T == 0 {
		t.Fatal("tick has no timestamp")
	}
	if first.MemU <= 0 || first.MemP < 0 || first.MemP > 100 {
		t.Fatalf("memory looks wrong: used=%v pct=%v", first.MemU, first.MemP)
	}
	if first.Procs <= 0 {
		t.Fatalf("process count = %d", first.Procs)
	}
	// 第二次 tick 才有 CPU/网络速率（需要两次采样求差）。
	second := s.Tick()
	if second.CPU < 0 || second.CPU > 100 {
		t.Fatalf("cpu out of range: %v", second.CPU)
	}
	if second.Rx < 0 || second.Tx < 0 || second.DR < 0 || second.DW < 0 {
		t.Fatalf("negative rates: %+v", second)
	}
	if second.DiskP < 0 || second.DiskP > 100 {
		t.Fatalf("disk pct out of range: %v", second.DiskP)
	}
	rx, tx, dr, dw, n := s.Totals()
	if n < 2 {
		t.Fatalf("ring has %d samples", n)
	}
	if rx < 0 || tx < 0 || dr < 0 || dw < 0 {
		t.Fatalf("totals negative: %v %v %v %v", rx, tx, dr, dw)
	}
}

func TestSampleJSONIsCompactAndStable(t *testing.T) {
	s := Sample{T: 1791207902204, CPU: 12.5, MemU: 1.2e10, Rx: 4096, TCPEst: 28}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	// 列式传输下每个采样点约 280 字节（含 29 个字段），5 秒一轮询只传新增点。
	if len(b) > 300 {
		t.Fatalf("sample payload too big (%d bytes): %s", len(b), b)
	}
	var back Sample
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.T != s.T || back.CPU != s.CPU || back.MemU != s.MemU || back.TCPEst != s.TCPEst {
		t.Fatalf("round trip mismatch: %+v", back)
	}
}

func TestHostInfoDetected(t *testing.T) {
	h := HostInfoCached()
	if h.Hostname == "" {
		t.Fatal("hostname not detected")
	}
	if h.Arch == "" || h.Kernel == "" {
		t.Fatalf("static info missing: %+v", h)
	}
	if h.CPUCores < 1 {
		t.Fatalf("cpu cores = %d", h.CPUCores)
	}
	if h.BootTime <= 0 {
		t.Fatalf("boot time = %d", h.BootTime)
	}
	// 缓存必须返回同一份数据（sync.Once 生效）。
	if again := HostInfoCached(); again.Hostname != h.Hostname || again.CPUCores != h.CPUCores ||
		again.PrivateIP != h.PrivateIP || len(again.MACs) != len(h.MACs) {
		t.Fatalf("cache changed: %+v vs %+v", again, h)
	}
	// 物理地址信息：至少要有内网 IP；公网 IP 允许为空（离线/无默认路由）。
	if h.PrivateIP == "" {
		t.Fatalf("private ip not detected: %+v", h)
	}
	if h.Iface == "" {
		t.Fatalf("egress interface not detected: %+v", h)
	}
	t.Logf("网络身份: iface=%s private=%s egress=%s macs=%v", h.Iface, h.PrivateIP, h.PublicIP, h.MACs)
}

func TestMountsAndPorts(t *testing.T) {
	mounts := Mounts()
	if len(mounts) == 0 {
		t.Fatal("no mounts detected")
	}
	root := false
	for _, m := range mounts {
		if m.Total <= 0 || m.Used < 0 || m.UsedPct < 0 || m.UsedPct > 100 {
			t.Fatalf("bad mount %+v", m)
		}
		if m.Mount == "/" {
			root = true
		}
	}
	if !root {
		t.Log("no root mount detected (unusual but tolerated in containers)")
	}

	ports := ListeningPorts()
	if len(ports) == 0 {
		t.Log("no listening ports visible (may need root)")
	}
	for _, p := range ports {
		if p.Port <= 0 || p.Port > 65535 {
			t.Fatalf("bad port %+v", p)
		}
	}
}

func TestServiceNameMapping(t *testing.T) {
	cases := map[int]string{22: "SSH", 80: "HTTP", 443: "HTTPS", 3306: "MySQL", 6379: "Redis"}
	for port, want := range cases {
		if got := ServiceName(port); got != want {
			t.Errorf("ServiceName(%d) = %q, want %q", port, got, want)
		}
	}
	if got := ServiceName(65000); got != "" {
		t.Errorf("unknown port should be empty, got %q", got)
	}
}

func TestHexPort(t *testing.T) {
	cases := map[string]int{
		"0100007F:1F90": 8080,
		"00000000:0050": 80,
		"00000000:01BB": 443,
		"bad":           0,
	}
	for in, want := range cases {
		if got := hexPort(in); got != want {
			t.Errorf("hexPort(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTCPStatsShape(t *testing.T) {
	est, lsn, tw, total := TCPStats()
	if est < 0 || lsn < 0 || tw < 0 || total < 0 {
		t.Fatalf("negative tcp counters: %d %d %d %d", est, lsn, tw, total)
	}
	if total > 0 && est+tw > total {
		t.Fatalf("inconsistent tcp counters: est=%d tw=%d total=%d", est, tw, total)
	}
}

func TestTopProcesses(t *testing.T) {
	// 第一次调用只建立基线，第二次才能算出 CPU 差值。
	_ = TopProcesses(5)
	procs := TopProcesses(5)
	if len(procs) == 0 || len(procs) > 5 {
		t.Fatalf("processes = %d", len(procs))
	}
	for _, p := range procs {
		if p.PID <= 0 || p.Name == "" {
			t.Fatalf("bad process entry: %+v", p)
		}
		if p.CPU < 0 || p.MemPct < 0 || p.MemRSS < 0 {
			t.Fatalf("negative process metrics: %+v", p)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	cfg := DefaultConfig()
	cfg.Port = 9001
	cfg.AdminHash = "hash"
	cfg.SSHRelayEnable = true
	cfg.Tunnel.Enabled = true
	cfg.Tunnel.Mode = "token"
	cfg.Tunnel.Token = "tok"
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path, []string{"-port", "9001"})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != 9001 || loaded.Tunnel.Token != "tok" || !loaded.SSHRelayEnable {
		t.Fatalf("config not round tripped: %+v", loaded)
	}
	if !loaded.CheckToken("") == false {
		t.Fatal("empty token must not validate against a set hash")
	}
	if loaded.CheckToken("wrong") {
		t.Fatal("wrong token accepted")
	}
}

func TestConfigFlagsOverrideFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	cfg := DefaultConfig()
	cfg.Port = 8899
	cfg.SSHUser = "root"
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path, []string{
		"-port", "0", "-ssh-user", "ubuntu", "-ssh-password", "pw",
		"-tunnel", "-tunnel-mode", "token", "-tunnel-token", "abc",
		"-interval", "5", "-history", "600",
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != 0 || loaded.SSHUser != "ubuntu" || !loaded.SSHRelayEnable {
		t.Fatalf("flags not applied: %+v", loaded)
	}
	if !loaded.Tunnel.Enabled || loaded.Tunnel.Mode != "token" || loaded.Tunnel.Token != "abc" {
		t.Fatalf("tunnel flags not applied: %+v", loaded.Tunnel)
	}
	if loaded.Interval != 5 || loaded.History != 600 {
		t.Fatalf("sampling flags not applied: %+v", loaded)
	}
}

// 端口 0（仅内网穿透）时，cloudflared 需要知道转发到哪个回环端口。
func TestInternalPort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Port = 8899
	if got := internalPort(cfg); got != 8899 {
		t.Fatalf("internalPort = %d, want 8899", got)
	}
	cfg.Port = 0
	cfg.Tunnel.LocalPort = 0
	if got := internalPort(cfg); got != 8899 {
		t.Fatalf("default internalPort = %d, want 8899", got)
	}
	cfg.Tunnel.LocalPort = 18080
	if got := internalPort(cfg); got != 18080 {
		t.Fatalf("internalPort = %d, want 18080", got)
	}
}
