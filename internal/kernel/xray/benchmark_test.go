package xray

import (
	"fmt"
	"testing"

	"github.com/fearless743/fboard-node/internal/model"
)

// newBenchDispatcher builds a LimitDispatcher with `n` limited users so the
// device-limit admission hot path is exercised.
func newBenchDispatcher(n int) *LimitDispatcher {
	d := &LimitDispatcher{
		limitedIPs: make(map[string]map[string]int),
		globalIPs:  make(map[string]map[string]struct{}),
	}
	users := make([]model.UserSpec, n)
	emailToUID := make(map[string]int, n)
	deviceLimits := make(map[string]int, n)
	for i := 0; i < n; i++ {
		users[i] = model.UserSpec{ID: i + 1, UUID: fmt.Sprintf("uuid-%d", i), DeviceLimit: 2}
		email := userEmail(i + 1)
		emailToUID[email] = i + 1
		deviceLimits[email] = 2
	}
	d.UpdateLimits(emailToUID, deviceLimits, nil)
	return d
}

// BenchmarkDispatcher_CheckDeviceLimit measures the per-new-connection device
// admission check (fast path: user already admitted / unlimited).
func BenchmarkDispatcher_CheckDeviceLimit(b *testing.B) {
	d := newBenchDispatcher(100)
	email := userEmail(1)
	// Pre-admit source IP so we hit the "already-known local IP" fast path.
	d.checkDeviceLimit(email, "1.1.1.1", true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.checkDeviceLimit(email, "1.1.1.1", true)
	}
}

// BenchmarkDispatcher_CheckDeviceLimit_Unlimited measures the lock-free fast
// path used for users with no device limit configured.
func BenchmarkDispatcher_CheckDeviceLimit_Unlimited(b *testing.B) {
	d := newBenchDispatcher(100)
	// Overwrite limits so no user has a device limit → lock-free path.
	d.UpdateLimits(map[string]int{}, map[string]int{}, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.checkDeviceLimit("user@1", "1.1.1.1", true)
	}
}

// BenchmarkDispatcher_GetConnectionState measures the state snapshot used for
// tracking/reporting every track tick.
func BenchmarkDispatcher_GetConnectionState(b *testing.B) {
	d := newBenchDispatcher(50)
	for i := 0; i < 50; i++ {
		email := userEmail(i + 1)
		d.checkDeviceLimit(email, fmt.Sprintf("10.0.0.%d", i), true)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.GetConnectionState()
	}
}
