package monitor

import (
	"testing"
	"time"
)

// BenchmarkCollect measures the cost of a single Collect() call AFTER the cache
// is warm (i.e., the common case in machine mode where N node services all call
// within the same push window). The first call triggers a full refresh; every
// subsequent call within collectTTL returns the cached sample.
func BenchmarkCollect_Cached(b *testing.B) {
	// Warm the cache with one full sample first.
	Collect()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Collect()
	}
}

// BenchmarkCollect_Refresh measures the cost of a full sampling pass — the
// cost that the original (uncached) Collect() paid on every call. Each iteration
// starts with a forced cache expiry so the benchmark reflects the real syscall
// overhead.
func BenchmarkCollect_Refresh(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cacheMu.Lock()
		cacheTime = time.Time{}
		cacheMu.Unlock()
		Collect()
	}
}
