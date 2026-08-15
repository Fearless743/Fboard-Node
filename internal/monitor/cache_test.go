package monitor

import (
	"testing"
	"time"
)

// TestCollect_ServesCacheWithinTTL verifies the coalescing fast path: a Collect()
// call made while a fresh cached sample exists must return that exact sample
// without re-running the (expensive) system sampling.
func TestCollect_ServesCacheWithinTTL(t *testing.T) {
	resetCache()

	sentinel := Status{Uptime: 1, CPU: 2.5}
	cacheMu.Lock()
	cacheTime = time.Now()
	cacheStat = sentinel
	cacheMu.Unlock()

	got := Collect()
	if got.Uptime != sentinel.Uptime || got.CPU != sentinel.CPU {
		t.Fatalf("Collect() = %+v, want cached sentinel %+v (cache not served)",
			got, sentinel)
	}
}

// TestCollect_RefreshesAfterTTL verifies that once the cached sample expires, a
// subsequent Collect() performs a fresh sampling pass (producing a new uptime
// rather than the stale sentinel).
func TestCollect_RefreshesAfterTTL(t *testing.T) {
	resetCache()

	stale := Status{Uptime: 1, CPU: 2.5}
	cacheMu.Lock()
	cacheTime = time.Now().Add(-collectTTL - time.Nanosecond) // force expiry
	cacheStat = stale
	cacheMu.Unlock()

	got := Collect()
	if got.Uptime == stale.Uptime && got.CPU == stale.CPU {
		t.Fatalf("Collect() returned the stale cached sample; want a refresh (Uptime=%d CPU=%v)",
			got.Uptime, got.CPU)
	}
}

// TestCollect_ConcurrentCalls share the same refreshed sample: a burst of
// concurrent Collect() calls must not each trigger a full re-sample. Because the
// refresh runs single-flight under cacheMu, every caller within the same TTL
// window must observe the same sample (identical Uptime + CPU doubles).
func TestCollect_ConcurrentCalls(t *testing.T) {
	resetCache()

	const n = 32
	results := make([]Status, n)
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			results[idx] = Collect()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}

	// The refresh is single-flight, so the burst all observe one sample identity.
	for i := 1; i < n; i++ {
		if results[i].Uptime != results[0].Uptime || results[i].CPU != results[0].CPU {
			t.Fatalf("call %d diverged from the shared sample: %+v vs %+v",
				i, results[i], results[0])
		}
	}
}

// resetCache clears the cached sample so tests start from a known state.
func resetCache() {
	cacheMu.Lock()
	cacheTime = time.Time{}
	cacheStat = Status{}
	cacheMu.Unlock()
}
