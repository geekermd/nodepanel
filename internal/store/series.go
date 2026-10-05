package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Retention policy. Raw samples come in at the agent's sampling interval
// (2s by default); older data is folded into 1 minute and then 5 minute
// buckets so 90 days of history costs a few megabytes per node.
const (
	rawKeepSec    = 2 * 3600       // keep 2s samples for 2 hours
	minAggSec     = 60             // 1 minute buckets
	minKeepSec    = 24 * 3600      // up to 24 hours
	coarseAggSec  = 300            // 5 minute buckets
	coarseKeepSec = 90 * 24 * 3600 // up to 90 days

	maxSeriesLen = 60000 // hard cap per tier (~15 MB decoded worst case)

	// minAggMillis is the finest aggregation the charts can ask for; it matches
	// the agent's default 2s sampling interval.
	minAggMillis = 2000
)

// Sample is one aggregated time bucket.
type Sample struct {
	T      int64   `json:"t"`
	CPU    float64 `json:"cpu"`
	Usr    float64 `json:"usr"`
	Sys    float64 `json:"sys"`
	Iow    float64 `json:"iow"`
	Steal  float64 `json:"steal"`
	L1     float64 `json:"l1"`
	L5     float64 `json:"l5"`
	L15    float64 `json:"l15"`
	MemU   float64 `json:"mem_u"`
	MemP   float64 `json:"mem_p"`
	MemA   float64 `json:"mem_a"`
	SwapP  float64 `json:"swap_p"`
	DR     float64 `json:"dr"`
	DW     float64 `json:"dw"`
	DIO    float64 `json:"dio"`
	ROps   float64 `json:"rops"`
	WOps   float64 `json:"wops"`
	Rx     float64 `json:"rx"`
	Tx     float64 `json:"tx"`
	Rxp    float64 `json:"rxp"`
	Txp    float64 `json:"txp"`
	Rxe    float64 `json:"rxe"`
	Rxd    float64 `json:"rxd"`
	DiskP  float64 `json:"disk_p"`
	Procs  float64 `json:"procs"`
	Run    float64 `json:"run"`
	TCPEst float64 `json:"tcp_est"`
	TCPTW  float64 `json:"tcp_tw"`
}

// Series stores the history of a single node.
type Series struct {
	mu    sync.RWMutex
	dir   string
	id    string
	raw   []Sample
	min   []Sample
	coar  []Sample
	dirty bool
}

// NewSeries creates a series rooted at dir.
func NewSeries(dir, id string) *Series {
	return &Series{dir: dir, id: id}
}

func (s *Series) path(tier string) string { return filepath.Join(s.dir, tier+".jsonl") }

// Load reads the journal files back into memory.
func (s *Series) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.raw, err = readJSONL(s.path("raw")); err != nil {
		return err
	}
	if s.min, err = readJSONL(s.path("min")); err != nil {
		return err
	}
	s.coar, _ = readJSONL(s.path("coarse"))
	return nil
}

func readJSONL(path string) ([]Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := make([]Sample, 0, 512)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) < 8 || line[0] != '{' {
			continue
		}
		var v Sample
		if err := json.Unmarshal(line, &v); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// Append adds samples to the raw tier, writing them straight to the journal so
// a panel restart never loses more than the in-flight poll.
func (s *Series) Append(samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path("raw"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, v := range samples {
		line, err := json.Marshal(v)
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
		s.raw = append(s.raw, v)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if len(s.raw) > maxSeriesLen {
		s.raw = s.raw[len(s.raw)-maxSeriesLen:]
		s.dirty = true
	}
	return nil
}

// Latest returns the most recent sample.
func (s *Series) Latest() (Sample, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n := len(s.raw); n > 0 {
		return s.raw[n-1], true
	}
	if n := len(s.min); n > 0 {
		return s.min[n-1], true
	}
	if n := len(s.coar); n > 0 {
		return s.coar[n-1], true
	}
	return Sample{}, false
}

// Stats describes what is stored for a node.
type Stats struct {
	Points    int   `json:"points"`
	RawPoints int   `json:"raw_points"`
	MinPoints int   `json:"min_points"`
	OldPoints int   `json:"old_points"`
	First     int64 `json:"first"`
	Last      int64 `json:"last"`
	Bytes     int64 `json:"bytes"`
}

// Stats summarises the stored history.
func (s *Series) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{RawPoints: len(s.raw), MinPoints: len(s.min), OldPoints: len(s.coar)}
	st.Points = st.RawPoints + st.MinPoints + st.OldPoints
	if n := len(s.raw); n > 0 {
		st.First, st.Last = s.raw[0].T, s.raw[n-1].T
	} else if n := len(s.min); n > 0 {
		st.First, st.Last = s.min[0].T, s.min[n-1].T
	} else if n := len(s.coar); n > 0 {
		st.First, st.Last = s.coar[0].T, s.coar[n-1].T
	}
	for _, name := range []string{"raw", "min", "coarse"} {
		if fi, err := os.Stat(s.path(name)); err == nil {
			// The journal holds 1-minute and 5-minute tiers too; report the
			// total so the UI can show the real disk footprint.
			st.Bytes += fi.Size()
		}
	}
	return st
}

// Range selects and aggregates samples for a query window.
//
// fromMS/toMS are unix milliseconds; maxPoints caps the number of returned
// buckets. The tier is chosen automatically so a 30 day chart stays cheap.
func (s *Series) Range(fromMS, toMS int64, maxPoints int) ([]Sample, string) {
	if maxPoints <= 0 {
		maxPoints = 480
	}
	span := time.Duration(toMS-fromMS) * time.Millisecond
	tier := "raw"
	agg := int64(0)
	switch {
	case span <= 6*time.Hour:
		tier, agg = "raw", 0
	case span <= 48*time.Hour:
		tier, agg = "min", minAggSec*1000
	default:
		tier, agg = "coarse", coarseAggSec*1000
	}

	s.mu.RLock()
	var src []Sample
	switch tier {
	case "raw":
		src = append(src, s.raw...)
	case "min":
		src = append(src, s.min...)
		src = append(src, s.raw...)
	default:
		src = append(src, s.coar...)
		src = append(src, s.min...)
		src = append(src, s.raw...)
	}
	src = append([]Sample(nil), src...)
	s.mu.RUnlock()

	// Clip the window.
	lo := sort.Search(len(src), func(i int) bool { return src[i].T >= fromMS })
	hi := sort.Search(len(src), func(i int) bool { return src[i].T > toMS })
	if lo > 0 {
		lo--
	}
	if hi >= len(src) {
		hi = len(src)
	}
	window := src[lo:hi]

	// Any tier may contain mixed resolutions after a compaction; bucket by
	// timestamp so the chart shows real aggregates.
	// 聚合粒度：点数在预算内就保持原始精度（agent 默认 2 秒采样），
	// 超出预算时才按窗口/预算粗化，最低 2 秒、最高 5 分钟。
	if agg == 0 {
		agg = minAggMillis
		if len(window) > maxPoints {
			want := (toMS - fromMS) / int64(maxPoints)
			if want > agg {
				agg = clampAgg(want)
			}
		}
	}
	return bucketize(window, agg), tier
}

func clampAgg(v int64) int64 {
	if v < minAggMillis {
		return minAggMillis
	}
	if v > coarseAggSec*1000 {
		return coarseAggSec * 1000
	}
	return v
}

// bucketize averages samples into fixed time buckets aligned to the epoch.
func bucketize(in []Sample, aggMS int64) []Sample {
	if len(in) == 0 {
		return nil
	}
	if aggMS <= 0 {
		aggMS = 2000
	}
	out := make([]Sample, 0, len(in)/2+1)
	var cur Sample
	var cnt float64
	var zero Sample
	flush := func() {
		if cnt == 0 {
			return
		}
		// Divide the additive counters, keep the gauges as averages.
		cur.CPU /= cnt
		cur.Usr /= cnt
		cur.Sys /= cnt
		cur.Iow /= cnt
		cur.Steal /= cnt
		cur.L1 /= cnt
		cur.L5 /= cnt
		cur.L15 /= cnt
		cur.MemU /= cnt
		cur.MemP /= cnt
		cur.MemA /= cnt
		cur.SwapP /= cnt
		cur.DR /= cnt
		cur.DW /= cnt
		cur.DIO /= cnt
		cur.ROps /= cnt
		cur.WOps /= cnt
		cur.Rx /= cnt
		cur.Tx /= cnt
		cur.Rxp /= cnt
		cur.Txp /= cnt
		cur.Rxe /= cnt
		cur.Rxd /= cnt
		cur.DiskP /= cnt
		cur.Procs /= cnt
		cur.Run /= cnt
		cur.TCPEst /= cnt
		cur.TCPTW /= cnt
		out = append(out, cur)
		cur = zero
		cnt = 0
	}
	bucket := int64(-1)
	for _, v := range in {
		b := v.T / aggMS
		if b != bucket {
			flush()
			bucket = b
			cur.T = b * aggMS
		}
		addSample(&cur, v)
		cnt++
	}
	flush()
	return out
}

func addSample(dst *Sample, v Sample) {
	dst.CPU += v.CPU
	dst.Usr += v.Usr
	dst.Sys += v.Sys
	dst.Iow += v.Iow
	dst.Steal += v.Steal
	dst.L1 += v.L1
	dst.L5 += v.L5
	dst.L15 += v.L15
	dst.MemU += v.MemU
	dst.MemP += v.MemP
	dst.MemA += v.MemA
	dst.SwapP += v.SwapP
	dst.DR += v.DR
	dst.DW += v.DW
	dst.DIO += v.DIO
	dst.ROps += v.ROps
	dst.WOps += v.WOps
	dst.Rx += v.Rx
	dst.Tx += v.Tx
	dst.Rxp += v.Rxp
	dst.Txp += v.Txp
	dst.Rxe += v.Rxe
	dst.Rxd += v.Rxd
	dst.DiskP += v.DiskP
	dst.Procs += v.Procs
	dst.Run += v.Run
	dst.TCPEst += v.TCPEst
	dst.TCPTW += v.TCPTW
}

// Compact folds old raw samples into the minute and coarse tiers and drops
// anything past the retention window. It runs hourly.
func (s *Series) Compact() error {
	now := time.Now().UnixMilli()
	rawCut := now - rawKeepSec*1000

	s.mu.Lock()
	// Move finished raw samples into 1 minute buckets.
	var keepRaw, toFold []Sample
	for _, v := range s.raw {
		if v.T < rawCut {
			toFold = append(toFold, v)
		} else {
			keepRaw = append(keepRaw, v)
		}
	}
	if len(toFold) > 0 {
		s.min = mergeTier(s.min, bucketize(toFold, minAggSec*1000))
		s.raw = keepRaw
		s.dirty = true
	}
	// Move finished minute buckets into 5 minute buckets.
	minCut := now - minKeepSec*1000
	var keepMin, toCoarse []Sample
	for _, v := range s.min {
		if v.T < minCut {
			toCoarse = append(toCoarse, v)
		} else {
			keepMin = append(keepMin, v)
		}
	}
	if len(toCoarse) > 0 {
		s.coar = mergeTier(s.coar, bucketize(toCoarse, coarseAggSec*1000))
		s.min = keepMin
		s.dirty = true
	}
	// Retention.
	coarseCut := now - coarseKeepSec*1000
	if len(s.coar) > 0 && s.coar[0].T < coarseCut {
		i := sort.Search(len(s.coar), func(i int) bool { return s.coar[i].T >= coarseCut })
		s.coar = s.coar[i:]
		s.dirty = true
	}
	if len(s.min) > maxSeriesLen {
		s.min = s.min[len(s.min)-maxSeriesLen:]
		s.dirty = true
	}
	if len(s.coar) > maxSeriesLen {
		s.coar = s.coar[len(s.coar)-maxSeriesLen:]
		s.dirty = true
	}
	dirty := s.dirty
	s.dirty = false
	s.mu.Unlock()

	if !dirty {
		return nil
	}
	return s.rewrite()
}

// mergeTier merges freshly folded buckets into an existing tier, replacing
// overlapping timestamps instead of duplicating them.
func mergeTier(dst, add []Sample) []Sample {
	if len(add) == 0 {
		return dst
	}
	idx := make(map[int64]int, len(dst))
	for i, v := range dst {
		idx[v.T] = i
	}
	for _, v := range add {
		if i, ok := idx[v.T]; ok {
			dst[i] = v
			continue
		}
		dst = append(dst, v)
	}
	sort.Slice(dst, func(i, j int) bool { return dst[i].T < dst[j].T })
	return dst
}

func (s *Series) rewrite() error {
	s.mu.RLock()
	raw := append([]Sample(nil), s.raw...)
	min := append([]Sample(nil), s.min...)
	coar := append([]Sample(nil), s.coar...)
	s.mu.RUnlock()
	if err := writeJSONL(s.path("raw"), raw); err != nil {
		return err
	}
	if err := writeJSONL(s.path("min"), min); err != nil {
		return err
	}
	return writeJSONL(s.path("coarse"), coar)
}

func writeJSONL(path string, samples []Sample) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 256*1024)
	for _, v := range samples {
		line, err := json.Marshal(v)
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Purge deletes all stored history for the node.
func (s *Series) Purge() error {
	s.mu.Lock()
	s.raw, s.min, s.coar = nil, nil, nil
	s.mu.Unlock()
	return os.RemoveAll(s.dir)
}
