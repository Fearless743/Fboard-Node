package limiter

import (
	"fmt"
	"testing"

	"github.com/fearless743/fboard-node/internal/model"
)

// genUsers builds n users with the given device/speed limit ratios so the
// benchmarks cover both the lock-free and bucketed paths.
func genUsers(n, deviceLimit, speedLimit int, withLimits bool) []model.UserSpec {
	users := make([]model.UserSpec, n)
	for i := 0; i < n; i++ {
		u := model.UserSpec{
			ID:          int(i + 1),
			UUID:        fmt.Sprintf("uuid-%d", i),
			DeviceLimit: 0,
			SpeedLimit:  0,
		}
		if withLimits {
			u.DeviceLimit = deviceLimit
			u.SpeedLimit = speedLimit
		}
		users[i] = u
	}
	return users
}

// BenchmarkLimiter_GetDeviceLimitByUUID benchmarks the per-connection device-limit
// lookup — called once for every new connection through the kernel gate.
func BenchmarkLimiter_GetDeviceLimitByUUID(b *testing.B) {
	l := New()
	l.UpdateUsers(genUsers(100, 2, 0, true))
	uuid := "uuid-50"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.GetDeviceLimitByUUID(uuid)
	}
}

// BenchmarkLimiter_GetDeviceLimitByUUID_NoLimits benchmarks the lock-free fast
// path used when no user sets a device limit.
func BenchmarkLimiter_GetDeviceLimitByUUID_NoLimits(b *testing.B) {
	l := New()
	l.UpdateUsers(genUsers(100, 0, 0, false))
	uuid := "uuid-50"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.GetDeviceLimitByUUID(uuid)
	}
}

// BenchmarkLimiter_UpdateUsers measures the cost of rebuilding the user index,
// which runs on every user sync (low frequency but O(users)).
func BenchmarkLimiter_UpdateUsers(b *testing.B) {
	l := New()
	users := genUsers(500, 2, 0, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.UpdateUsers(users)
	}
}

// BenchmarkSpeedTracker_GetLimiter benchmarks the speed-limiter hot path for a
// new connection: atomic snapshot load + UUID→userID lookup.
func BenchmarkSpeedTracker_GetLimiter(b *testing.B) {
	l := New()
	st := NewSpeedTracker(l)
	l.UpdateUsers(genUsers(100, 0, 10, true))
	st.UpdateBuckets()
	uuid := "uuid-50"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.GetLimiter(uuid)
	}
}

// BenchmarkSpeedTracker_GetLimiter_Unlimited covers the (common) case where the
// user has no speed limit configured.
func BenchmarkSpeedTracker_GetLimiter_Unlimited(b *testing.B) {
	l := New()
	st := NewSpeedTracker(l)
	l.UpdateUsers(genUsers(100, 0, 0, false))
	st.UpdateBuckets()
	uuid := "uuid-50"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.GetLimiter(uuid)
	}
}

// BenchmarkSpeedTracker_UpdateBuckets measures the full snapshot rebuild on user
// sync.
func BenchmarkSpeedTracker_UpdateBuckets(b *testing.B) {
	l := New()
	st := NewSpeedTracker(l)
	l.UpdateUsers(genUsers(500, 0, 10, true))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.UpdateBuckets()
	}
}
