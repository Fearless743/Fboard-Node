package xray

import (
	"sync/atomic"
	"testing"

	"github.com/fearless743/fboard-node/internal/model"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/transport"
)

func newTestDispatcher() *LimitDispatcher {
	return &LimitDispatcher{
		limitedIPs: make(map[string]map[string]int),
		globalIPs:  make(map[string]map[string]struct{}),
	}
}

type nopReader struct{}

func (nopReader) ReadMultiBuffer() (buf.MultiBuffer, error) { return nil, nil }

func TestLimitDispatcher_DeviceLimitCheck(t *testing.T) {
	ld := newTestDispatcher()

	users := []model.UserSpec{
		{ID: 1, UUID: "uuid-1", DeviceLimit: 2, SpeedLimit: 0},
		{ID: 2, UUID: "uuid-2", DeviceLimit: 0, SpeedLimit: 10},
	}

	emailToUID := make(map[string]int)
	deviceLimits := make(map[string]int)
	speedLimits := make(map[string]int)
	for _, u := range users {
		email := userEmail(u.ID)
		emailToUID[email] = u.ID
		if u.DeviceLimit > 0 {
			deviceLimits[email] = u.DeviceLimit
		}
		if u.SpeedLimit > 0 {
			speedLimits[email] = u.SpeedLimit
		}
	}
	ld.UpdateLimits(emailToUID, deviceLimits, speedLimits)

	email1 := userEmail(1)

	// hard cap for limit=2 with 30% grace is 3
	if got := hardDeviceLimit(2); got != 3 {
		t.Fatalf("hardDeviceLimit(2) = %d, want 3", got)
	}

	// First IP should be allowed
	if ld.checkDeviceLimit(email1, "1.1.1.1", true) {
		t.Error("first IP should be allowed")
	}

	// Second IP should be allowed (within configured limit)
	if ld.checkDeviceLimit(email1, "2.2.2.2", true) {
		t.Error("second IP should be allowed")
	}

	// Third unique IP is within 30% grace — still allowed
	if ld.checkDeviceLimit(email1, "3.3.3.3", true) {
		t.Error("third IP should be allowed under 30% grace (hard cap=3)")
	}

	// Fourth unique IP exceeds hard cap — rejected
	if !ld.checkDeviceLimit(email1, "4.4.4.4", true) {
		t.Error("fourth IP should be rejected (hard cap=3 for limit=2)")
	}

	// Same IP as first should be allowed (already connected)
	if ld.checkDeviceLimit(email1, "1.1.1.1", true) {
		t.Error("same IP should always be allowed")
	}

	// User 2 has no device limit — should always be allowed
	email2 := userEmail(2)
	for i := 0; i < 10; i++ {
		ip := "10.0.0." + string(rune('0'+i))
		if ld.checkDeviceLimit(email2, ip, true) {
			t.Errorf("user with no device limit should always be allowed (ip=%s)", ip)
		}
	}
}

func TestLimitDispatcher_DelConn(t *testing.T) {
	ld := newTestDispatcher()

	email := userEmail(1)
	deviceLimits := map[string]int{email: 2}
	ld.UpdateLimits(map[string]int{email: 1}, deviceLimits, nil)

	// Fill to hard cap (3)
	ld.checkDeviceLimit(email, "1.1.1.1", true)
	ld.checkDeviceLimit(email, "2.2.2.2", true)
	ld.checkDeviceLimit(email, "3.3.3.3", true)

	// Fourth should be rejected
	if !ld.checkDeviceLimit(email, "4.4.4.4", true) {
		t.Error("fourth IP should be rejected at hard cap")
	}

	// Remove first IP
	ld.delConn(email, "1.1.1.1")

	// Now a new IP should be allowed
	if ld.checkDeviceLimit(email, "4.4.4.4", true) {
		t.Error("after deleting one IP, new IP should be allowed")
	}
}

func TestHardDeviceLimit(t *testing.T) {
	cases := []struct {
		limit int
		want  int
	}{
		{0, 0},
		{1, 2},  // ceil(1.3)=2
		{2, 3},  // ceil(2.6)=3
		{3, 4},  // ceil(3.9)=4
		{10, 13},
	}
	for _, tc := range cases {
		if got := hardDeviceLimit(tc.limit); got != tc.want {
			t.Errorf("hardDeviceLimit(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}

func TestLimitDispatcher_GlobalDevices(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1, "uuid-1": 1}, map[string]int{email: 2, "uuid-1": 2}, nil)

	// Two remote IPs already online on other nodes.
	ld.UpdateGlobalDevices(map[int][]string{
		1: {"10.0.0.1", "10.0.0.2"},
	})

	// Local new IP is still within hard cap 3 → allowed
	if ld.checkDeviceLimit(email, "10.0.0.3", true) {
		t.Fatal("third fleet-wide IP should be allowed under grace")
	}

	// Fourth fleet-wide IP must be rejected
	if !ld.checkDeviceLimit(email, "10.0.0.4", true) {
		t.Fatal("fourth fleet-wide IP should be rejected")
	}

	// Already-local IP stays allowed
	if ld.checkDeviceLimit(email, "10.0.0.3", true) {
		t.Fatal("existing local IP should always be allowed")
	}

	// Clear global — only local 10.0.0.3 remains, room for more
	ld.ClearGlobalDevices()
	if ld.checkDeviceLimit(email, "10.0.0.5", true) {
		t.Fatal("after clearing remote devices, new IP should be allowed")
	}
}

func TestLimitDispatcher_UpdateGlobalDevicesSkipsLocal(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, map[string]int{email: 1}, nil)

	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("local IP should be allowed")
	}

	// Panel snapshot includes the local IP plus a remote one.
	ld.UpdateGlobalDevices(map[int][]string{
		1: {"1.1.1.1", "2.2.2.2"},
	})

	ld.mu.RLock()
	remote := ld.globalIPs[email]
	_, hasLocal := remote["1.1.1.1"]
	_, hasRemote := remote["2.2.2.2"]
	ld.mu.RUnlock()
	if hasLocal {
		t.Fatal("local IP must not be double-counted in globalIPs")
	}
	if !hasRemote {
		t.Fatal("remote IP should be stored in globalIPs")
	}

	// hard cap for limit=1 is 2; local+remote already at cap → new IP rejected
	if !ld.checkDeviceLimit(email, "3.3.3.3", true) {
		t.Fatal("new IP should be rejected when local+remote hit hard cap")
	}
}

func TestLimitDispatcher_GetConnectionState(t *testing.T) {
	ld := newTestDispatcher()

	email1 := userEmail(1)
	email2 := userEmail(2)
	ld.UpdateLimits(map[string]int{email1: 1, email2: 2}, nil, nil)

	ic1 := &ipCounter{}
	r1 := &atomic.Int64{}
	r1.Store(1)
	ic1.ips.Store("1.1.1.1", r1)
	r2 := &atomic.Int64{}
	r2.Store(1)
	ic1.ips.Store("2.2.2.2", r2)
	ld.unlimitedIPs.Store(email1, ic1)

	ic2 := &ipCounter{}
	r3 := &atomic.Int64{}
	r3.Store(1)
	ic2.ips.Store("3.3.3.3", r3)
	ld.unlimitedIPs.Store(email2, ic2)

	ld.connCount.Store(5)

	aliveIPs, connCount := ld.GetConnectionState()

	if connCount != 5 {
		t.Errorf("expected connCount=5, got %d", connCount)
	}
	if len(aliveIPs[1]) != 2 {
		t.Errorf("user 1 IPs: got %d, want 2", len(aliveIPs[1]))
	}
	if len(aliveIPs[2]) != 1 {
		t.Errorf("user 2 IPs: got %d, want 1", len(aliveIPs[2]))
	}
}

func TestLimitDispatcher_ResetConns(t *testing.T) {
	ld := newTestDispatcher()

	ld.mu.Lock()
	ld.limitedIPs["user@1"] = map[string]int{"1.1.1.1": 1}
	ld.mu.Unlock()
	ld.connCount.Store(3)

	ld.ResetConns()

	ld.mu.RLock()
	ipCount := len(ld.limitedIPs)
	ld.mu.RUnlock()
	if ipCount != 0 {
		t.Error("limitedIPs should be empty after reset")
	}

	if ld.connCount.Load() != 0 {
		t.Error("connCount should be 0 after reset")
	}
}

func TestLimitDispatcher_UnlimitedUserFastPath(t *testing.T) {
	ld := newTestDispatcher()

	email := userEmail(1)
	// No device limit set for this user
	ld.UpdateLimits(map[string]int{email: 1}, nil, nil)

	// Should use fast path (sync.Map), no lock needed
	for i := 0; i < 100; i++ {
		ip := "10.0.0." + string(rune('0'+i%10))
		if ld.checkDeviceLimit(email, ip, true) {
			t.Errorf("unlimited user should always be allowed (ip=%s)", ip)
		}
	}

	// Verify IPs are tracked in unlimitedIPs
	v, ok := ld.unlimitedIPs.Load(email)
	if !ok {
		t.Error("unlimited user should have entry in unlimitedIPs")
	}
	ic := v.(*ipCounter)
	ips := ic.aliveIPs()
	if len(ips) == 0 {
		t.Error("should have tracked some IPs")
	}
}


func TestLimitDispatcher_TrackLinkPreservesReader(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, map[string]int{email: 1}, nil)

	origReader := nopReader{}
	origWriter := buf.Discard
	link := &transport.Link{Reader: origReader, Writer: origWriter}

	ld.trackLink(link, email, "1.1.1.1", true)

	if link.Reader != origReader {
		t.Fatal("trackLink must not replace link.Reader")
	}
	if link.Writer == origWriter {
		t.Fatal("trackLink should wrap link.Writer for lifecycle callbacks")
	}
}

func TestLimitDispatcher_CloseTrackingWriterReleasesConn(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	// hard cap for limit=1 is 2; fill both slots so release is observable.
	ld.UpdateLimits(map[string]int{email: 1}, map[string]int{email: 1}, nil)
	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("first connection should be allowed")
	}
	if ld.checkDeviceLimit(email, "2.2.2.2", true) {
		t.Fatal("second connection should be allowed under grace")
	}
	if !ld.checkDeviceLimit(email, "3.3.3.3", true) {
		t.Fatal("third connection should be rejected at hard cap")
	}

	link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
	ld.trackLink(link, email, "1.1.1.1", true)

	if got := ld.connCount.Load(); got != 1 {
		t.Fatalf("expected connCount=1 after tracking, got %d", got)
	}
	cw, ok := link.Writer.(*closeTrackingWriter)
	if !ok {
		t.Fatal("expected closeTrackingWriter wrapper")
	}
	if err := cw.Close(); err != nil {
		t.Fatalf("closeTrackingWriter.Close() error = %v", err)
	}
	if got := ld.connCount.Load(); got != 0 {
		t.Fatalf("expected connCount=0 after close, got %d", got)
	}
	// Writer close must release the 1.1.1.1 slot so a new IP fits under hard cap.
	if ld.checkDeviceLimit(email, "3.3.3.3", true) {
		t.Fatal("device slot should be released after writer close")
	}
}
