package store

import (
	"testing"
	"time"
)

func mkSample(t int64, cpu float64) Sample {
	return Sample{T: t, CPU: cpu, MemP: 50, Rx: 1000, Tx: 500}
}

func TestBucketizeAverages(t *testing.T) {
	in := []Sample{
		mkSample(0, 10), mkSample(1000, 20), mkSample(2000, 30), mkSample(3000, 40),
	}
	out := bucketize(in, 2000)
	if len(out) != 2 {
		t.Fatalf("want 2 buckets, got %d (%v)", len(out), out)
	}
	if out[0].CPU != 15 {
		t.Fatalf("bucket0 avg = %v, want 15", out[0].CPU)
	}
	if out[1].CPU != 35 {
		t.Fatalf("bucket1 avg = %v, want 35", out[1].CPU)
	}
	if out[0].T != 0 || out[1].T != 2000 {
		t.Fatalf("bucket timestamps wrong: %v %v", out[0].T, out[1].T)
	}
}

func TestSeriesAppendAndRange(t *testing.T) {
	dir := t.TempDir()
	s := NewSeries(dir, "n1")
	// 1 小时的 2 秒采样（agent 默认节奏），最后一个点作为"现在"。
	now := time.Now().UnixMilli()
	const n = 1800
	var batch []Sample
	for i := 0; i < n; i++ {
		batch = append(batch, mkSample(now-int64(n-1-i)*minAggMillis, float64(i%100)))
	}
	if err := s.Append(batch); err != nil {
		t.Fatal(err)
	}
	if got := s.Stats().Points; got != n {
		t.Fatalf("points = %d, want %d", got, n)
	}
	last, ok := s.Latest()
	if !ok || last.CPU != float64((n-1)%100) {
		t.Fatalf("latest = %v ok=%v", last, ok)
	}
	// 重新加载：JSONL 必须原样回读。
	s2 := NewSeries(dir, "n1")
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := s2.Stats().Points; got != n {
		t.Fatalf("after reload points = %d, want %d", got, n)
	}
	// 1 小时窗口：raw 粒度，预算足够时保持原始精度。
	pts, tier := s2.Range(now-3600_000, now+1000, 4000)
	if tier != "raw" {
		t.Fatalf("tier = %q, want raw", tier)
	}
	if len(pts) < n-4 {
		t.Fatalf("range returned %d points, want ~%d", len(pts), n)
	}
	// 预算很小时必须降采样。
	thin, _ := s2.Range(now-3600_000, now+1000, 60)
	if len(thin) > 70 {
		t.Fatalf("maxPoints not honoured: got %d", len(thin))
	}
	if len(thin) == 0 {
		t.Fatal("downsampling lost all data")
	}
	// 30 天窗口落到 coarse 档，但数据仍要能取到。
	pts30, tier30 := s2.Range(now-30*24*3600_000, now+1000, 480)
	if tier30 != "coarse" {
		t.Fatalf("30d tier = %q, want coarse", tier30)
	}
	if len(pts30) == 0 {
		t.Fatal("30d window lost all data")
	}
}

func TestCompactFoldsOldSamples(t *testing.T) {
	dir := t.TempDir()
	s := NewSeries(dir, "n1")
	now := time.Now().UnixMilli()
	// Two hours of raw samples at 2s: everything older than rawKeepSec folds
	// into the minute tier.
	for i := 0; i < 3600; i++ {
		_ = s.Append([]Sample{mkSample(now-int64(7200-i*2)*1000, float64(i%100))})
	}
	before := s.Stats()
	if before.RawPoints != 3600 {
		t.Fatalf("setup: raw = %d", before.RawPoints)
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	after := s.Stats()
	if after.MinPoints == 0 {
		t.Fatalf("compaction did not create the minute tier: %+v", after)
	}
	if after.RawPoints >= before.RawPoints {
		t.Fatalf("compaction kept everything: %+v", after)
	}
	if after.Points == 0 {
		t.Fatalf("compaction lost all data")
	}
	// Compaction must be idempotent (no duplicated buckets on a second pass).
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	if again := s.Stats(); again.MinPoints != after.MinPoints {
		t.Fatalf("second compaction changed the minute tier: %d -> %d", after.MinPoints, again.MinPoints)
	}
}

func TestTodoAndNodeLifecycle(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.AddNode(&Node{Name: "web", Host: "1.2.3.4", Port: 8899, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == "" || n.SSHPort != 22 || n.SSHUser != "root" {
		t.Fatalf("node defaults not applied: %+v", n)
	}
	if len(st.ListNodes()) != 1 {
		t.Fatalf("node not listed")
	}
	if _, err := st.UpdateNode(n.ID, func(x *Node) { x.Note = "主站" }); err != nil {
		t.Fatal(err)
	}
	if got := st.Node(n.ID); got == nil || got.Note != "主站" {
		t.Fatalf("note not persisted: %+v", got)
	}

	td, err := st.AddTodo(&Todo{Text: "巡检", NodeID: n.ID, Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTodo(td.ID, func(x *Todo) { x.Done = true }); err != nil {
		t.Fatal(err)
	}
	if got := st.ListTodos()[0]; !got.Done || got.DoneAt == 0 {
		t.Fatalf("todo not completed: %+v", got)
	}
	// Deleting a node removes its todos and history.
	if err := st.RemoveNode(n.ID); err != nil {
		t.Fatal(err)
	}
	if len(st.ListNodes()) != 0 || len(st.ListTodos()) != 0 {
		t.Fatalf("cleanup failed: nodes=%d todos=%d", len(st.ListNodes()), len(st.ListTodos()))
	}
}

func TestRuntimeFileRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rf := &RuntimeFile{
		ID: "abc", Requests: 42, RequestsDay: 7, Day: "2026-10-05",
		Days:    map[string]*DayUsage{"2026-10-05": {Requests: 7, Rx: 1024, Tx: 512}},
		RxTotal: 1e6, TxTotal: 2e5,
	}
	if err := st.WriteRuntimeFile(rf); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadRuntime("abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests != 42 || got.Days["2026-10-05"].Requests != 7 || got.RxTotal != 1e6 {
		t.Fatalf("runtime round trip failed: %+v", got)
	}
	// A missing file must be a non-fatal empty value.
	empty, err := st.LoadRuntime("nope")
	if err != nil || empty.Requests != 0 {
		t.Fatalf("missing runtime should be empty, got %+v err=%v", empty, err)
	}
}
