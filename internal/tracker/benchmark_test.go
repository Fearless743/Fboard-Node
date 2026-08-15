package tracker

import "testing"

// generateTraffic builds a realistic per-user cumulative traffic map.
// For each of the n users, uplink/downlink cycle upward each iteration so
// Process() computes non-trivial deltas (exercising the delta hot path).
// Pass a step that advances between cumulative snapshots (0 == all-zero to
// exercise the no-traffic fast path).
func generateTraffic(n, step int64) map[int][2]int64 {
	m := make(map[int][2]int64, n)
	var i int64
	for i = 0; i < n; i++ {
		up := step * (i + 1)
		down := step * (i + 1) * 2
		m[int(i)] = [2]int64{up, down}
	}
	return m
}

// BenchmarkTracker_Process_100Users measures the per-cycle cost of the tracker
// hot path — the 10s track tick that computes traffic deltas for ~100 users and
// publishes a new lock-free snapshot.
func BenchmarkTracker_Process_100Users(b *testing.B) {
	tr := New()
	traffic := generateTraffic(100, 1)
	alive := map[int]map[string]bool{0: {"10.0.0.1": true}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Bump counters each iteration so deltas are non-trivial.
		b.StopTimer()
		for uid := range traffic {
			c := traffic[uid]
			c[0]++
			c[1]++
			traffic[uid] = c
		}
		b.StartTimer()
		tr.Process(traffic, alive, 1)
	}
}

// BenchmarkTracker_Process_FewUsers benchmarks the common small-node case where
// only a handful of users are connected.
func BenchmarkTracker_Process_FewUsers(b *testing.B) {
	tr := New()
	traffic := generateTraffic(5, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.Process(traffic, nil, 5)
	}
}

// BenchmarkTracker_Process_NoUsers exercises the empty fast path (no online users).
func BenchmarkTracker_Process_NoUsers(b *testing.B) {
	tr := New()
	empty := map[int][2]int64{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.Process(empty, nil, 0)
	}
}

// BenchmarkTracker_FlushTraffic benchmarks draining the pending accumulator,
// which runs on the ~60s push tick.
func BenchmarkTracker_FlushTraffic(b *testing.B) {
	tr := New()
	traffic := generateTraffic(100, 1)
	tr.Process(traffic, nil, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.FlushTraffic()
	}
}

// BenchmarkTracker_CurrentOnline measures the lock-free read snapshot copy.
func BenchmarkTracker_CurrentOnline(b *testing.B) {
	tr := New()
	alive := generateAliveIPs(100)
	tr.Process(generateTraffic(100, 1), alive, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.CurrentOnline()
	}
}

func generateAliveIPs(n int) map[int]map[string]bool {
	m := make(map[int]map[string]bool, n)
	for i := 0; i < n; i++ {
		m[i] = map[string]bool{segmentString(i): true}
	}
	return m
}

func segmentString(i int) string {
	return "10.0." + itoa(i/250) + "." + itoa(i%250+1)
}

// itoa is a tiny int→decimal converter avoiding strconv for the benchmark
// helpers above.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [3]byte
	p := len(buf)
	for v > 0 {
		p--
		buf[p] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[p:])
}