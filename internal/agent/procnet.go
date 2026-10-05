//go:build linux

package agent

import (
	"bufio"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

func openFile(path string) (*os.File, error) { return os.Open(path) }

func newScanner(f *os.File) *bufio.Scanner { return bufio.NewScanner(f) }

func splitFields(s string) []string { return strings.Fields(s) }

func lastIndexByte(s string, b byte) int { return strings.LastIndexByte(s, b) }

func sortPorts(p []PortStat) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].Established != p[j].Established {
			return p[i].Established > p[j].Established
		}
		return p[i].Port < p[j].Port
	})
}

var (
	socketOwnerCache   = map[int]string{}
	socketOwnerCacheMu sync.Mutex
)

// socketOwner maps a listening port to the process that owns it by scanning
// /proc/<pid>/fd for socket inodes. It is only run on demand (the panel's
// "服务" tab) and the result is cached for a minute, because the scan is the
// most expensive thing the agent can do.
func socketOwner(port int) string {
	socketOwnerCacheMu.Lock()
	if v, ok := socketOwnerCache[port]; ok {
		socketOwnerCacheMu.Unlock()
		return v
	}
	socketOwnerCacheMu.Unlock()

	inodes := socketInodesForPort(port)
	if len(inodes) == 0 {
		return ""
	}
	name := findProcessByInode(inodes)
	socketOwnerCacheMu.Lock()
	socketOwnerCache[port] = name
	socketOwnerCacheMu.Unlock()
	return name
}

func socketInodesForPort(port int) map[string]bool {
	out := map[string]bool{}
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
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			if hexPort(fields[1]) != port {
				continue
			}
			out[fields[9]] = true
		}
		f.Close()
	}
	return out
}

func findProcessByInode(inodes map[string]bool) string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := "/proc/" + e.Name() + "/fd"
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(fdDir + "/" + fd.Name())
			if err != nil {
				continue
			}
			if !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if inodes[inode] {
				if comm, err := os.ReadFile("/proc/" + e.Name() + "/comm"); err == nil {
					return strings.TrimSpace(string(comm)) + " (pid " + strconv.Itoa(pid) + ")"
				}
				return "pid " + strconv.Itoa(pid)
			}
		}
	}
	return ""
}
