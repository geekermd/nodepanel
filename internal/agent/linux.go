//go:build linux

package agent

import (
	"runtime"

	"golang.org/x/sys/unix"
)

func runtimeNumCPU() int { return runtime.NumCPU() }

func runtimeGOARCH() string { return runtime.GOARCH }

func numGoroutine() int { return runtime.NumGoroutine() }

func diskUsage(device, mount, fstype string) DiskUsage {
	du := DiskUsage{Mount: mount, Device: device, FSType: fstype}
	var st unix.Statfs_t
	if err := unix.Statfs(mount, &st); err != nil {
		return du
	}
	bs := float64(st.Bsize)
	du.Total = float64(st.Blocks) * bs
	du.Free = float64(st.Bavail) * bs
	du.Used = du.Total - float64(st.Bfree)*bs
	if du.Total > 0 {
		du.UsedPct = round1(du.Used / du.Total * 100)
	}
	du.InodesUsed = float64(st.Files - st.Ffree)
	du.InodesFree = float64(st.Ffree)
	return du
}

// PortStat describes one listening TCP port together with a best-effort guess
// at the service behind it. The panel turns this into the "访问量 / 服务状态"
// view without needing an extra agent or nginx module.
type PortStat struct {
	Port        int    `json:"port"`
	Proto       string `json:"proto"`
	Service     string `json:"service"`
	Process     string `json:"process"`
	Established int    `json:"established"`
	TimeWait    int    `json:"time_wait"`
	Local       int    `json:"local"`
}

// ListeningPorts parses /proc/net/tcp{,6} for LISTEN sockets and counts the
// established connections per local port.
func ListeningPorts() []PortStat {
	type key struct {
		port  int
		proto string
	}
	listeners := map[key]*PortStat{}
	counts := map[key]*PortStat{}

	for _, src := range []struct {
		path  string
		proto string
	}{
		{"/proc/net/tcp", "tcp"},
		{"/proc/net/tcp6", "tcp6"},
	} {
		lines := readProcNet(src.path)
		// First pass: remember LISTEN sockets.
		for _, f := range lines {
			if len(f) < 4 || f[3] != "0A" {
				continue
			}
			port := hexPort(f[1])
			if port == 0 {
				continue
			}
			k := key{port, src.proto}
			if _, ok := listeners[k]; !ok {
				listeners[k] = &PortStat{Port: port, Proto: src.proto}
			}
		}
		// Second pass: count connection states for the ports we care about.
		for _, f := range lines {
			if len(f) < 4 {
				continue
			}
			port := hexPort(f[1])
			k := key{port, src.proto}
			st, ok := counts[k]
			if !ok {
				st = &PortStat{Port: port, Proto: src.proto}
				counts[k] = st
			}
			switch f[3] {
			case "01":
				st.Established++
			case "06":
				st.TimeWait++
			}
		}
	}

	out := make([]PortStat, 0, len(listeners))
	for k, ls := range listeners {
		if c, ok := counts[k]; ok {
			ls.Established = c.Established
			ls.TimeWait = c.TimeWait
		}
		ls.Service = ServiceName(ls.Port)
		ls.Process = socketOwner(ls.Port)
		ls.Local = 1
		out = append(out, *ls)
	}
	sortPorts(out)
	return out
}

func readProcNet(path string) [][]string {
	f, err := openFile(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out [][]string
	sc := newScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := splitFields(sc.Text())
		if len(fields) >= 4 {
			out = append(out, fields)
		}
	}
	return out
}

// hexPort converts the "0100007F:1F90" style local_address field to a port.
func hexPort(localAddr string) int {
	i := lastIndexByte(localAddr, ':')
	if i < 0 {
		return 0
	}
	v := 0
	for _, c := range localAddr[i+1:] {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v += int(c - '0')
		case c >= 'A' && c <= 'F':
			v += int(c-'A') + 10
		case c >= 'a' && c <= 'f':
			v += int(c-'a') + 10
		default:
			return 0
		}
	}
	return v
}

// ServiceName maps well known ports to a friendly label.
func ServiceName(port int) string {
	switch port {
	case 22:
		return "SSH"
	case 21:
		return "FTP"
	case 25, 465, 587:
		return "SMTP"
	case 53:
		return "DNS"
	case 80:
		return "HTTP"
	case 443:
		return "HTTPS"
	case 1433:
		return "MSSQL"
	case 2375, 2376:
		return "Docker API"
	case 3000:
		return "Node/Grafana"
	case 3306:
		return "MySQL"
	case 5432:
		return "PostgreSQL"
	case 6379:
		return "Redis"
	case 8080, 8000, 8888:
		return "HTTP (alt)"
	case 8443:
		return "HTTPS (alt)"
	case 9000:
		return "PHP-FPM/Portainer"
	case 9090:
		return "Prometheus"
	case 27017:
		return "MongoDB"
	}
	return ""
}
