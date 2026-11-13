package xray

import (
	"strconv"
	"sync"
	"testing"
)

// TestUserEmailConcurrent guards the process-wide email cache: several panel
// instances may build xray configs at the same time (e.g. one panel
// reconnecting after a WS outage while another keeps serving), so the cache
// must be race-free. Run with -race to validate.
func TestUserEmailConcurrent(t *testing.T) {
	const (
		workers    = 8
		iterations = 200
	)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				id := offset + i%50
				if got, want := userEmail(id), "user@"+strconv.Itoa(id); got != want {
					t.Errorf("userEmail(%d) = %q, want %q", id, got, want)
					return
				}
			}
		}(w * 7)
	}
	wg.Wait()
}
