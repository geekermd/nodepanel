//go:build linux

package agent

import (
	"sync"
	"time"
)

// Sample is one row of the time series. Field names are kept short: a node
// behind a 5 Mbit/s link pulls these every few seconds, so payload size is a
// design constraint rather than an afterthought.
type Sample struct {
	T      int64   `json:"t"`       // unix milliseconds
	CPU    float64 `json:"cpu"`     // %
	Usr    float64 `json:"usr"`     // % user+nice
	Sys    float64 `json:"sys"`     // % system+irq+softirq
	Iow    float64 `json:"iow"`     // % iowait
	Steal  float64 `json:"steal"`   // % steal
	L1     float64 `json:"l1"`      // load 1m
	L5     float64 `json:"l5"`      // load 5m
	L15    float64 `json:"l15"`     // load 15m
	MemU   float64 `json:"mem_u"`   // bytes used
	MemP   float64 `json:"mem_p"`   // %
	MemA   float64 `json:"mem_a"`   // bytes available
	SwapP  float64 `json:"swap_p"`  // %
	DR     float64 `json:"dr"`      // disk read B/s
	DW     float64 `json:"dw"`      // disk write B/s
	DIO    float64 `json:"dio"`     // disk io utilisation %
	ROps   float64 `json:"rops"`    // read IOPS
	WOps   float64 `json:"wops"`    // write IOPS
	Rx     float64 `json:"rx"`      // B/s
	Tx     float64 `json:"tx"`      // B/s
	Rxp    float64 `json:"rxp"`     // packets/s
	Txp    float64 `json:"txp"`     // packets/s
	Rxe    float64 `json:"rxe"`     // errors/s
	Rxd    float64 `json:"rxd"`     // drops/s
	DiskP  float64 `json:"disk_p"`  // root/aggregate disk %
	Procs  int     `json:"procs"`   // total processes
	Run    int     `json:"run"`     // runnable+threads
	TCPEst int     `json:"tcp_est"` // established
	TCPTW  int     `json:"tcp_tw"`  // time_wait
}

// newRingSized builds a ring with an exact capacity. Tests use it to exercise
// the wrap-around logic with a tiny buffer; production code goes through
// NewRing, which enforces a minimum size.
func newRingSized(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]Sample, capacity)}
}

// Ring is a fixed-size circular buffer of samples, safe for concurrent use.
type Ring struct {
	mu   sync.RWMutex
	buf  []Sample
	head int // next write position
	n    int // number of valid entries
}

func NewRing(capacity int) *Ring {
	if capacity < 8 {
		capacity = 8
	}
	return &Ring{buf: make([]Sample, capacity)}
}

func (r *Ring) Push(s Sample) {
	r.mu.Lock()
	r.buf[r.head] = s
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
	r.mu.Unlock()
}

func (r *Ring) Last() (Sample, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.n == 0 {
		return Sample{}, false
	}
	return r.buf[(r.head-1+len(r.buf))%len(r.buf)], true
}

// Since returns every sample strictly newer than tMS (a unix millisecond
// stamp). Passing 0 yields the whole buffer.
func (r *Ring) Since(tMS int64) []Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.n == 0 {
		return nil
	}
	out := make([]Sample, 0, r.n)
	start := (r.head - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		s := r.buf[(start+i)%len(r.buf)]
		if tMS > 0 && s.T <= tMS {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.n
}

// Sampler owns the collection loop and the derived counters.
type Sampler struct {
	Ring *Ring

	mu       sync.Mutex
	prevCPU  CPUCounters
	prevNet  NetCounters
	prevDisk DiskCounters
	prevAt   time.Time
	havePrev bool

	// Cumulative traffic since the agent started (or since the last reset),
	// used for the "流量统计" view.
	RxTotal float64
	TxTotal float64
	DRTotal float64
	DWTotal float64

	// worstMount is the fullest filesystem, recomputed on every tick so the
	// chart shows disk usage over time without extra API calls.
	worstMount float64

	host HostInfo
}

func NewSampler(historySec, intervalSec int) *Sampler {
	if intervalSec <= 0 {
		intervalSec = 2
	}
	if historySec <= 0 {
		historySec = 300
	}
	cap := historySec / intervalSec
	if cap < 32 {
		cap = 32
	}
	if cap > 21600 {
		cap = 21600
	}
	s := &Sampler{Ring: NewRing(cap), host: HostInfoCached()}
	s.prevCPU, _ = readCPUCounters()
	s.prevNet = readNetCounters()
	s.prevDisk = readDiskCounters()
	s.prevAt = time.Now()
	s.havePrev = true
	return s
}

// Run collects a sample every interval until stop is closed.
func (s *Sampler) Run(interval time.Duration, stop <-chan struct{}) {
	// First sample immediately so the panel has data on the very first poll.
	s.Tick()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// Tick takes one measurement.
func (s *Sampler) Tick() Sample {
	now := time.Now()
	cpu := Sample{T: now.UnixNano() / int64(time.Millisecond)}

	cc, ok := readCPUCounters()
	net := readNetCounters()
	disk := readDiskCounters()
	mem := readMemInfo()
	l1, l5, l15 := readLoadAvg()
	procs, run := ProcessCount()
	est, _, tw, _ := TCPStats()

	s.mu.Lock()
	elapsed := now.Sub(s.prevAt).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}
	var rxRate, txRate, drRate, dwRate float64
	if s.havePrev {
		if ok {
			cd := CPUCounters{
				User: cc.User - s.prevCPU.User, Nice: cc.Nice - s.prevCPU.Nice,
				System: cc.System - s.prevCPU.System, Idle: cc.Idle - s.prevCPU.Idle,
				IOWait: cc.IOWait - s.prevCPU.IOWait, IRQ: cc.IRQ - s.prevCPU.IRQ,
				SoftIRQ: cc.SoftIRQ - s.prevCPU.SoftIRQ, Steal: cc.Steal - s.prevCPU.Steal,
			}
			tot := cd.total()
			if tot > 0 {
				busy := cd.busy()
				cpu.CPU = clampPct(busy / tot * 100)
				cpu.Usr = clampPct((cd.User + cd.Nice) / tot * 100)
				cpu.Sys = clampPct((cd.System + cd.IRQ + cd.SoftIRQ) / tot * 100)
				cpu.Iow = clampPct(cd.IOWait / tot * 100)
				cpu.Steal = clampPct(cd.Steal / tot * 100)
			}
		}
		nd := net.minus(s.prevNet)
		rxRate = nonNeg(nd.RxBytes / elapsed)
		txRate = nonNeg(nd.TxBytes / elapsed)
		cpu.Rxp = round1(nonNeg(nd.RxPackets / elapsed))
		cpu.Txp = round1(nonNeg(nd.TxPackets / elapsed))
		cpu.Rxe = round1(nonNeg(nd.RxErrors / elapsed))
		cpu.Rxd = round1(nonNeg(nd.RxDropped / elapsed))

		dd := disk.minus(s.prevDisk)
		drRate = nonNeg(dd.ReadBytes / elapsed)
		dwRate = nonNeg(dd.WriteBytes / elapsed)
		cpu.ROps = round1(nonNeg(dd.ReadOps / elapsed))
		cpu.WOps = round1(nonNeg(dd.WriteOps / elapsed))
		// I/O utilisation is the share of wall time the devices were busy.
		busyTicks := nonNeg(dd.IOTicks)
		if busyTicks > 0 {
			cpu.DIO = clampPct(busyTicks / (elapsed * 100) * 100)
		}
		s.RxTotal += rxRate * elapsed
		s.TxTotal += txRate * elapsed
		s.DRTotal += drRate * elapsed
		s.DWTotal += dwRate * elapsed
	}
	if ok {
		s.prevCPU = cc
	}
	s.prevNet = net
	s.prevDisk = disk
	s.prevAt = now
	s.havePrev = true
	s.mu.Unlock()

	cpu.Rx = round1(rxRate)
	cpu.Tx = round1(txRate)
	cpu.DR = round1(drRate)
	cpu.DW = round1(dwRate)
	cpu.CPU = round1(cpu.CPU)
	cpu.Usr = round1(cpu.Usr)
	cpu.Sys = round1(cpu.Sys)
	cpu.Iow = round1(cpu.Iow)
	cpu.Steal = round1(cpu.Steal)
	cpu.DIO = round1(cpu.DIO)
	cpu.L1, cpu.L5, cpu.L15 = round2(l1), round2(l5), round2(l15)
	cpu.MemU = mem.Used()
	cpu.MemA = mem.Available
	if mem.Total > 0 {
		cpu.MemP = round1(mem.Used() / mem.Total * 100)
	}
	if mem.SwapTotal > 0 {
		cpu.SwapP = round1((mem.SwapTotal - mem.SwapFree) / mem.SwapTotal * 100)
	}
	cpu.Procs, cpu.Run = procs, run
	cpu.TCPEst, cpu.TCPTW = est, tw
	cpu.DiskP = s.worstDiskPct()

	s.Ring.Push(cpu)
	return cpu
}

// worstDiskPct returns the usage percentage of the fullest mounted filesystem.
func (s *Sampler) worstDiskPct() float64 {
	worst := 0.0
	for _, d := range Mounts() {
		if d.UsedPct > worst {
			worst = d.UsedPct
		}
	}
	s.mu.Lock()
	s.worstMount = round1(worst)
	s.mu.Unlock()
	return s.worstMount
}

// Totals returns the cumulative counters plus the ring size.
func (s *Sampler) Totals() (rx, tx, dr, dw float64, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.RxTotal, s.TxTotal, s.DRTotal, s.DWTotal, s.Ring.Len()
}

func clampPct(v float64) float64 {
	if v < 0 || v != v {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func nonNeg(v float64) float64 {
	if v < 0 || v != v {
		return 0
	}
	return v
}

func round2(v float64) float64 {
	if v != v {
		return 0
	}
	return float64(int64(v*100+0.5)) / 100
}
