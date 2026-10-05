//go:build linux

// Package agent implements the node side of nodepanel: a tiny metrics collector
// that reads /proc, plus an SSH relay so a node behind a cloudflared tunnel can
// still be administered from the panel.
package agent

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// /proc readers. Everything is best effort: a missing file simply yields zero
// values so the agent keeps working on unusual kernels or inside containers.
// ---------------------------------------------------------------------------

func readFileTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func parseInt(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

// CPUCounters is a snapshot of the aggregate /proc/stat "cpu" line.
type CPUCounters struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal float64
}

func (c CPUCounters) total() float64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

func (c CPUCounters) busy() float64 {
	return c.total() - c.Idle - c.IOWait
}

func readCPUCounters() (CPUCounters, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return CPUCounters{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return CPUCounters{}, false
		}
		num := func(i int) float64 {
			if i < len(fields) {
				return parseFloat(fields[i])
			}
			return 0
		}
		return CPUCounters{
			User: num(1), Nice: num(2), System: num(3), Idle: num(4),
			IOWait: num(5), IRQ: num(6), SoftIRQ: num(7), Steal: num(8),
		}, true
	}
	return CPUCounters{}, false
}

func readLoadAvg() (l1, l5, l15 float64) {
	fields := strings.Fields(readFileTrim("/proc/loadavg"))
	if len(fields) >= 3 {
		return parseFloat(fields[0]), parseFloat(fields[1]), parseFloat(fields[2])
	}
	return 0, 0, 0
}

// MemInfo holds the fields we display.
type MemInfo struct {
	Total, Free, Available, Buffers, Cached float64
	SwapTotal, SwapFree                     float64
}

func (m MemInfo) Used() float64 {
	used := m.Total - m.Free - m.Buffers - m.Cached
	if used < 0 {
		used = 0
	}
	return used
}

func readMemInfo() MemInfo {
	var m MemInfo
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val := parseFloat(fields[1]) * 1024 // kB -> bytes
		switch key {
		case "MemTotal":
			m.Total = val
		case "MemFree":
			m.Free = val
		case "MemAvailable":
			m.Available = val
		case "Buffers":
			m.Buffers = val
		case "Cached", "SReclaimable":
			m.Cached += val
		case "SwapTotal":
			m.SwapTotal = val
		case "SwapFree":
			m.SwapFree = val
		}
	}
	return m
}

// NetCounters is the running total for one interface.
type NetCounters struct {
	RxBytes, TxBytes, RxPackets, TxPackets, RxErrors, TxErrors, RxDropped, TxDropped float64
}

func (a NetCounters) minus(b NetCounters) NetCounters {
	return NetCounters{
		RxBytes: a.RxBytes - b.RxBytes, TxBytes: a.TxBytes - b.TxBytes,
		RxPackets: a.RxPackets - b.RxPackets, TxPackets: a.TxPackets - b.TxPackets,
		RxErrors: a.RxErrors - b.RxErrors, TxErrors: a.TxErrors - b.TxErrors,
		RxDropped: a.RxDropped - b.RxDropped, TxDropped: a.TxDropped - b.TxDropped,
	}
}

// readNetCounters sums all interfaces except loopback.
func readNetCounters() NetCounters {
	var total NetCounters
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return total
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if name == "lo" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") {
			continue
		}
		fields := strings.Fields(line[i+1:])
		if len(fields) < 16 {
			continue
		}
		total.RxBytes += parseFloat(fields[0])
		total.RxPackets += parseFloat(fields[1])
		total.RxErrors += parseFloat(fields[2])
		total.RxDropped += parseFloat(fields[3])
		total.TxBytes += parseFloat(fields[8])
		total.TxPackets += parseFloat(fields[9])
		total.TxErrors += parseFloat(fields[10])
		total.TxDropped += parseFloat(fields[11])
	}
	return total
}

// DiskCounters is the aggregate block-device I/O counter set.
type DiskCounters struct {
	ReadBytes, WriteBytes, ReadOps, WriteOps, IOTicks, IOTicksWeighted float64
}

func (a DiskCounters) minus(b DiskCounters) DiskCounters {
	return DiskCounters{
		ReadBytes: a.ReadBytes - b.ReadBytes, WriteBytes: a.WriteBytes - b.WriteBytes,
		ReadOps: a.ReadOps - b.ReadOps, WriteOps: a.WriteOps - b.WriteOps,
		IOTicks: a.IOTicks - b.IOTicks, IOTicksWeighted: a.IOTicksWeighted - b.IOTicksWeighted,
	}
}

// readDiskCounters sums whole disks only (kernel 4.19+), falling back to all
// entries on older kernels.
func readDiskCounters() DiskCounters {
	var total DiskCounters
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return total
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if !isWholeDisk(name) {
			continue
		}
		total.ReadOps += parseFloat(fields[3])
		total.ReadBytes += parseFloat(fields[5]) * 512
		total.WriteOps += parseFloat(fields[7])
		total.WriteBytes += parseFloat(fields[9]) * 512
		total.IOTicks += parseFloat(fields[12])
		total.IOTicksWeighted += parseFloat(fields[13])
	}
	return total
}

// isWholeDisk filters partitions, loops, ram disks and device-mapper children.
func isWholeDisk(name string) bool {
	switch {
	case strings.HasPrefix(name, "loop"), strings.HasPrefix(name, "ram"),
		strings.HasPrefix(name, "dm-"), strings.HasPrefix(name, "md"),
		strings.HasPrefix(name, "sr"):
		return false
	case strings.HasPrefix(name, "nvme"):
		// nvme0n1 (whole) vs nvme0n1p1 (partition)
		return !strings.Contains(name, "p") || !isDigit(name[strings.LastIndex(name, "p")+1:])
	case strings.HasPrefix(name, "sd"), strings.HasPrefix(name, "vd"),
		strings.HasPrefix(name, "hd"), strings.HasPrefix(name, "xvd"):
		return !isDigit(name[len(name)-1:])
	}
	return false
}

func isDigit(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// DiskUsage describes one mounted filesystem.
type DiskUsage struct {
	Mount      string  `json:"mount"`
	Device     string  `json:"device"`
	FSType     string  `json:"fstype"`
	Total      float64 `json:"total"`
	Used       float64 `json:"used"`
	Free       float64 `json:"free"`
	UsedPct    float64 `json:"used_pct"`
	InodesUsed float64 `json:"inodes_used"`
	InodesFree float64 `json:"inodes_free"`
}

// Mounts returns real filesystems, de-duplicated by device.
func Mounts() []DiskUsage {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []DiskUsage
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		device, mount, fstype := fields[0], unescapeMount(fields[1]), fields[2]
		switch fstype {
		case "proc", "sysfs", "devpts", "tmpfs", "cgroup", "cgroup2", "overlay", "squashfs",
			"devtmpfs", "securityfs", "debugfs", "tracefs", "fusectl", "mqueue", "hugetlbfs",
			"pstore", "bpf", "autofs", "binfmt_misc", "configfs", "efivarfs", "ramfs", "nsfs":
			continue
		}
		if !strings.HasPrefix(device, "/dev/") && !strings.Contains(device, ":") &&
			!strings.HasPrefix(device, "//") && fstype != "nfs" && fstype != "nfs4" && fstype != "cifs" {
			continue
		}
		if seen[mount] {
			continue
		}
		seen[mount] = true
		du := diskUsage(device, mount, fstype)
		if du.Total <= 0 {
			continue
		}
		out = append(out, du)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// CPUInfo summarises the first processor entry plus the model name.
type CPUInfo struct {
	Model string  `json:"model"`
	Cores int     `json:"cores"`
	MHz   float64 `json:"mhz"`
}

var (
	cpuInfoOnce sync.Once
	cpuInfoVal  CPUInfo
)

func CPUInfoCached() CPUInfo {
	cpuInfoOnce.Do(func() {
		f, err := os.Open("/proc/cpuinfo")
		if err != nil {
			cpuInfoVal.Cores = runtimeNumCPU()
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		cores := 0
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "processor"):
				cores++
			case strings.HasPrefix(line, "model name"), strings.HasPrefix(line, "Hardware"),
				strings.HasPrefix(line, "cpu model"):
				if cpuInfoVal.Model == "" {
					if i := strings.IndexByte(line, ':'); i > 0 {
						cpuInfoVal.Model = strings.TrimSpace(line[i+1:])
					}
				}
			case strings.HasPrefix(line, "cpu MHz"):
				if cpuInfoVal.MHz == 0 && cpuInfoVal.Model != "" {
					if i := strings.IndexByte(line, ':'); i > 0 {
						cpuInfoVal.MHz = parseFloat(line[i+1:])
					}
				}
			}
		}
		cpuInfoVal.Cores = cores
		if cpuInfoVal.Model == "" {
			cpuInfoVal.Model = "unknown"
		}
	})
	return cpuInfoVal
}

// HostInfo is the static description of a node. It doubles as the panel's
// "物理地址" block: 内网 IP、出站公网 IP 与各网卡 MAC.
type HostInfo struct {
	Hostname  string   `json:"hostname"`
	OS        string   `json:"os"`
	Kernel    string   `json:"kernel"`
	Arch      string   `json:"arch"`
	CPUCores  int      `json:"cpu_cores"`
	CPUModel  string   `json:"cpu_model"`
	BootTime  int64    `json:"boot_time"`
	Virt      string   `json:"virt"`
	PrivateIP string   `json:"private_ip"` // 默认路由网卡上的地址
	Iface     string   `json:"iface"`      // 对应的网卡名
	PublicIP  string   `json:"public_ip"`  // 出站地址（NAT 后即公网 IP）
	MACs      []string `json:"macs"`       // 各物理网卡 MAC
}

// HostInfoCached caches the static host description.
func HostInfoCached() HostInfo { hostInfoOnce.Do(collectHostInfo); return hostInfoVal }

var (
	hostInfoOnce sync.Once
	hostInfoVal  HostInfo
)

func collectHostInfo() {
	h := HostInfo{
		Hostname: readFileTrim("/proc/sys/kernel/hostname"),
		Kernel:   readFileTrim("/proc/sys/kernel/osrelease"),
		Arch:     runtimeGOARCH(),
	}
	if h.Hostname == "" {
		if name, err := os.Hostname(); err == nil {
			h.Hostname = name
		}
	}
	if h.Kernel == "" {
		h.Kernel = "unknown"
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				h.OS = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
			}
		}
	}
	if h.OS == "" {
		h.OS = h.Kernel
	}
	ci := CPUInfoCached()
	h.CPUCores = ci.Cores
	if h.CPUCores == 0 {
		h.CPUCores = runtimeNumCPU()
	}
	h.CPUModel = ci.Model
	h.BootTime = bootTime()
	h.Virt = virtType()
	h.PrivateIP, h.Iface, h.PublicIP, h.MACs = detectNetwork()
	hostInfoVal = h
}

// ---------------------------------------------------------------------------
// 网络身份：内网 IP / 出站 IP / MAC
// ---------------------------------------------------------------------------

var (
	netOnce      sync.Once
	netPrivateIP string
	netIface     string
	netEgressIP  string
	netMACs      []string
	netEgressErr error
)

func detectNetwork() (privateIP, iface, egressIP string, macs []string) {
	netOnce.Do(collectNetwork)
	return netPrivateIP, netIface, netEgressIP, netMACs
}

func collectNetwork() {
	ifname, egressIP := egressRoute()
	netIface = ifname
	// /proc/net/route 的 Source 字段在默认路由上通常是 0.0.0.0，这种"未知"
	// 要如实上报：面板会用自己的观测来源 IP 补上（NAT 场景下那才是真公网 IP）。
	if egressIP == "0.0.0.0" {
		egressIP = ""
	}
	netEgressIP = egressIP

	if ifname != "" {
		if ifi, err := net.InterfaceByName(ifname); err == nil {
			if v4 := firstIPv4(ifi); v4 != "" {
				netPrivateIP = v4
			}
			if mac := ifi.HardwareAddr.String(); mac != "" {
				netMACs = append(netMACs, mac)
			}
		}
	}
	if netPrivateIP == "" {
		netPrivateIP = firstNonLoopbackIPv4()
	}

	// 其余物理网卡的 MAC（跳过虚拟网卡），方便在机房里认机器。
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifi := range ifaces {
			if ifi.Flags&net.FlagLoopback != 0 || len(ifi.HardwareAddr) == 0 {
				continue
			}
			name := strings.ToLower(ifi.Name)
			if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") ||
				strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") ||
				strings.HasPrefix(name, "vnet") || strings.HasPrefix(name, "tun") ||
				strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "tailscale") ||
				strings.HasPrefix(name, "zt") || strings.HasPrefix(name, "wg") {
				continue
			}
			mac := ifi.HardwareAddr.String()
			if mac == "" || containsStr(netMACs, mac) || isVirtualMAC(mac) {
				continue
			}
			netMACs = append(netMACs, mac)
		}
	}
	if len(netMACs) > 4 {
		netMACs = netMACs[:4]
	}
}

// egressRoute reads /proc/net/route to find the interface and source address
// used for outbound traffic (that is the address the panel sees when NAT is in
// play).
func egressRoute() (iface, ip string) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if first {
			first = false
			continue
		}
		if len(fields) < 8 || fields[1] != "00000000" {
			continue // 只关心默认路由
		}
		return fields[0], hexIPv4(fields[7])
	}
	return "", ""
}

// hexIPv4 converts the little-endian hex form used by /proc/net/route.
func hexIPv4(h string) string {
	if len(h) != 8 {
		return ""
	}
	var b [4]int
	for i := 0; i < 4; i++ {
		v := 0
		for _, c := range h[i*2 : i*2+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v += int(c - '0')
			case c >= 'a' && c <= 'f':
				v += int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v += int(c-'A') + 10
			default:
				return ""
			}
		}
		b[i] = v
	}
	// /proc/net/route 是小端序
	return fmt.Sprintf("%d.%d.%d.%d", b[3], b[2], b[1], b[0])
}

func firstIPv4(ifi *net.Interface) string {
	if ifi == nil {
		return ""
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

func firstNonLoopbackIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagUp == 0 {
			continue
		}
		if v4 := firstIPv4(ifi); v4 != "" {
			return v4
		}
	}
	return ""
}

// LookupPublicIP asks a public "what is my IP" service for the outbound
// address. It is opt-in (`-public-ip-lookup`) because it is the only thing the
// agent ever sends to the internet.
func LookupPublicIP(timeout time.Duration) string {
	client := &http.Client{Timeout: timeout}
	for _, url := range []string{"https://api.ipify.org", "https://ifconfig.me/ip", "https://ipv4.icanhazip.com"} {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if err != nil {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(string(body)))
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() {
			continue
		}
		return ip.String()
	}
	return ""
}

// isVirtualMAC filters out locally administered addresses (bit 1 of the first
// octet), which is how bridges, veth pairs and VM/container NICs identify
// themselves. What is left is the real hardware address you would read off the
// machine in a rack.
func isVirtualMAC(mac string) bool {
	first := strings.SplitN(mac, ":", 2)[0]
	v, err := strconv.ParseUint(first, 16, 8)
	if err != nil {
		return false
	}
	return v&0x02 != 0
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func bootTime() int64 {
	if v := readFileTrim("/proc/stat"); v != "" {
		for _, line := range strings.Split(v, "\n") {
			if strings.HasPrefix(line, "btime ") {
				return parseInt(strings.TrimPrefix(line, "btime "))
			}
		}
	}
	return time.Now().Unix()
}

func virtType() string {
	// systemd-detect-virt writes to stdout, but reading the DMI files is cheaper.
	if v := readFileTrim("/sys/class/dmi/id/product_name"); v != "" {
		low := strings.ToLower(v)
		switch {
		case strings.Contains(low, "kvm"):
			return "kvm"
		case strings.Contains(low, "vmware"):
			return "vmware"
		case strings.Contains(low, "virtualbox"):
			return "virtualbox"
		case strings.Contains(low, "droplet"):
			return "digitalocean"
		case strings.Contains(low, "alibaba"), strings.Contains(low, "ecs"):
			return "alibaba-cloud"
		case strings.Contains(low, "google"):
			return "gce"
		case strings.Contains(low, "amazon"):
			return "aws"
		case strings.Contains(low, "microsoft"):
			return "hyper-v"
		}
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	if v := readFileTrim("/proc/1/cgroup"); strings.Contains(v, "docker") || strings.Contains(v, "kubepods") {
		return "container"
	}
	return "host"
}

// Process is one entry of the process table.
type Process struct {
	PID     int     `json:"pid"`
	Name    string  `json:"name"`
	User    string  `json:"user"`
	State   string  `json:"state"`
	CPU     float64 `json:"cpu"`
	MemPct  float64 `json:"mem_pct"`
	MemRSS  float64 `json:"mem_rss"`
	Threads int     `json:"threads"`
	Cmdline string  `json:"cmdline"`
}

// procTick stores the per-process CPU jiffies so we can compute a delta.
type procTick struct {
	jiffies float64
	at      time.Time
}

var (
	procMu      sync.Mutex
	procPrev    = map[int]procTick{}
	lastTotal   float64
	lastTotalAt time.Time
)

// TopProcesses returns the n processes using the most CPU since the previous
// call, with memory usage as reported by /proc/<pid>/status.
func TopProcesses(n int) []Process {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	clk := float64(100) // USER_HZ is 100 on every supported arch
	now := time.Now()
	mem := readMemInfo()

	type rawProc struct {
		p       Process
		jiffies float64
	}
	var procs []rawProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		statB, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		stat := string(statB)
		// comm may contain spaces / parens; parse around the last ')'.
		closeParen := strings.LastIndexByte(stat, ')')
		if closeParen < 0 {
			continue
		}
		openParen := strings.IndexByte(stat, '(')
		if openParen < 0 || openParen > closeParen {
			continue
		}
		name := stat[openParen+1 : closeParen]
		rest := strings.Fields(stat[closeParen+1:])
		if len(rest) < 22 {
			continue
		}
		utime := parseFloat(rest[11])
		stime := parseFloat(rest[12])
		threads := int(parseInt(rest[17]))
		rssPages := parseFloat(rest[21])

		p := Process{
			PID:     pid,
			Name:    name,
			State:   rest[0],
			Threads: threads,
			MemRSS:  rssPages * float64(os.Getpagesize()),
		}
		if mem.Total > 0 {
			p.MemPct = p.MemRSS / mem.Total * 100
		}
		if cmd, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline")); err == nil && len(cmd) > 0 {
			c := strings.ReplaceAll(strings.TrimRight(string(cmd), "\x00"), "\x00", " ")
			if len(c) > 200 {
				c = c[:200]
			}
			p.Cmdline = c
		} else {
			p.Cmdline = "[" + name + "]"
		}
		procs = append(procs, rawProc{p: p, jiffies: utime + stime})
	}

	procMu.Lock()
	totalDelta := 0.0
	if !lastTotalAt.IsZero() {
		totalDelta = 0 // filled below by caller-provided counters; keep simple
	}
	_ = totalDelta
	prev := procPrev
	next := make(map[int]procTick, len(procs))
	for i := range procs {
		pid := procs[i].p.PID
		next[pid] = procTick{jiffies: procs[i].jiffies, at: now}
		if old, ok := prev[pid]; ok {
			secs := now.Sub(old.at).Seconds()
			if secs > 0 {
				procs[i].p.CPU = (procs[i].jiffies - old.jiffies) / clk / secs * 100
			}
		}
		if procs[i].p.CPU < 0 {
			procs[i].p.CPU = 0
		}
	}
	procPrev = next
	lastTotalAt = now
	procMu.Unlock()

	sort.Slice(procs, func(i, j int) bool {
		if procs[i].p.CPU != procs[j].p.CPU {
			return procs[i].p.CPU > procs[j].p.CPU
		}
		return procs[i].p.MemRSS > procs[j].p.MemRSS
	})
	if len(procs) > n {
		procs = procs[:n]
	}
	out := make([]Process, 0, len(procs))
	for _, p := range procs {
		p.p.CPU = round1(p.p.CPU)
		p.p.MemPct = round1(p.p.MemPct)
		out = append(out, p.p)
	}
	return out
}

func round1(v float64) float64 {
	if v != v {
		return 0
	}
	return float64(int64(v*10+0.5)) / 10
}

// ProcessCount returns the number of running processes/threads.
func ProcessCount() (procs, running int) {
	v := readFileTrim("/proc/loadavg")
	fields := strings.Fields(v)
	if len(fields) >= 4 {
		parts := strings.Split(fields[3], "/")
		if len(parts) == 2 {
			return int(parseInt(parts[0])), int(parseInt(parts[1]))
		}
	}
	return 0, 0
}

// TCPStats counts TCP sockets by state from /proc/net/tcp and tcp6.
func TCPStats() (established, listen, timeWait, total int) {
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		first := true
		for sc.Scan() {
			if first {
				first = false
				continue
			}
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 {
				continue
			}
			total++
			switch fields[3] {
			case "01":
				established++
			case "0A":
				listen++
			case "06":
				timeWait++
			}
		}
		f.Close()
	}
	return
}
