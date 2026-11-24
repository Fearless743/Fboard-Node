package monitor

import "testing"

func TestCached(t *testing.T) {
	first := Cached()
	if first.Goroutines == 0 {
		t.Fatal("expected a non-zero goroutine count")
	}

	// A second read should be served from the cached sample, so it must return
	// essentially the same snapshot rather than re-running all gopsutil calls.
	second := Cached()
	if diff := first.Uptime - second.Uptime; diff > 1 {
		t.Fatalf("cached uptime drifted by %d seconds", diff)
	}
	if second.Goroutines == 0 {
		t.Fatal("expected a non-zero goroutine count on second read")
	}
}
