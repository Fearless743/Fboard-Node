package xray

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	_ "unsafe"

	xrayDispatcher "github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"

	"github.com/fearless743/fboard-node/internal/nlog"
)

// Access xray's internal config creator registry so we can replace the
// default dispatcher factory with ours. This runs AFTER xray's init()
// functions because our package imports xray (dependency order guarantee).
//
//go:linkname typeCreatorRegistry github.com/xtls/xray-core/common.typeCreatorRegistry
var typeCreatorRegistry map[reflect.Type]common.ConfigCreator

var origDispatcherFactory common.ConfigCreator

// globalLimitDispatcher is set when the factory creates a LimitDispatcher.
// The Xray kernel reads it to configure limits and get connections.
var globalLimitDispatcher atomic.Pointer[LimitDispatcher]

func init() {
	configType := reflect.TypeOf((*xrayDispatcher.Config)(nil))
	origDispatcherFactory = typeCreatorRegistry[configType]
	typeCreatorRegistry[configType] = limitDispatcherFactory
}

func limitDispatcherFactory(ctx context.Context, config interface{}) (interface{}, error) {
	orig, err := origDispatcherFactory(ctx, config)
	if err != nil {
		return nil, err
	}
	inner, ok := orig.(routing.Dispatcher)
	if !ok {
		return orig, nil
	}
	ld := &LimitDispatcher{
		inner:      orig,
		innerDisp:  inner,
		limitedIPs: make(map[string]map[string]int),
		globalIPs:  make(map[string]map[string]struct{}),
	}
	globalLimitDispatcher.Store(ld)
	nlog.Core().Debug("xray: limit dispatcher installed")
	return ld, nil
}

// deviceLimitGracePercent is how far past the configured device_limit a user
// may go before new source IPs are rejected. Existing IPs always stay allowed.
// Example: limit=2 → hard cap 3; limit=10 → hard cap 13.
const deviceLimitGracePercent = 30

// LimitDispatcher wraps xray's DefaultDispatcher to enforce per-user
// admission checks before a request is dispatched into xray-core.
//
// It intentionally does NOT mutate transport.Link.Reader/Writer. Xray's
// mux/XUDP close path requires the original concrete *pipe.Reader to remain
// intact, so the dispatcher is limited to gate-keeping and safe connection
// lifecycle bookkeeping.
type LimitDispatcher struct {
	inner     interface{}        // original DefaultDispatcher (Feature + Dispatcher)
	innerDisp routing.Dispatcher // same object, typed as Dispatcher

	// limitedUsers: users with device limit > 0, protected by mu.
	// Needs deterministic IP ordering for kick decisions.
	mu           sync.RWMutex
	limitedIPs   map[string]map[string]int // email → sourceIP → local refcount
	deviceLimits map[string]int            // email → max devices (configured)
	emailToUID   map[string]int            // email → panel user ID
	// globalIPs holds remote (other-node) source IPs reported by the panel via
	// sync.devices. They count toward the hard cap but are never refcounted
	// locally, so delConn cannot erase them.
	globalIPs map[string]map[string]struct{} // email → set of remote IPs

	// unlimitedIPs: users without device limit — sync.Map for lock-free access.
	// Each entry is *ipCounter{ips sync.Map}.
	unlimitedIPs sync.Map // email → *ipCounter

	connCount atomic.Int64 // total active connections tracked by dispatcher
}

// ipCounter tracks IPs for unlimited users without any lock.
type ipCounter struct {
	ips sync.Map // sourceIP → *atomic.Int64 (refcount)
}

// aliveIPs returns a snapshot of distinct IPs.
func (ic *ipCounter) aliveIPs() map[string]bool {
	result := make(map[string]bool)
	ic.ips.Range(func(key, value interface{}) bool {
		if value.(*atomic.Int64).Load() > 0 {
			result[key.(string)] = true
		}
		return true
	})
	return result
}

// ─── routing.Dispatcher ──────────────────────────────────────────────────────

func (d *LimitDispatcher) Dispatch(ctx context.Context, dest net.Destination) (*transport.Link, error) {
	email, sourceIP, isTCP, err := d.identifyAndCheck(ctx, dest)
	if err != nil {
		return nil, err
	}

	link, err := d.innerDisp.Dispatch(ctx, dest)
	if err != nil {
		if email != "" && isTCP {
			d.delConn(email, sourceIP)
		}
		return nil, err
	}

	if email != "" {
		d.trackLink(link, email, sourceIP, isTCP)
	}
	return link, nil
}

func (d *LimitDispatcher) DispatchLink(ctx context.Context, dest net.Destination, link *transport.Link) error {
	email, sourceIP, isTCP, err := d.identifyAndCheck(ctx, dest)
	if err != nil {
		return err
	}

	if email != "" {
		d.trackLink(link, email, sourceIP, isTCP)
	}
	return d.innerDisp.DispatchLink(ctx, dest, link)
}

// identifyAndCheck extracts user identity from the session context, enforces
// device limits, and returns the user's email, source IP, and TCP flag.
// Returns a non-nil error only when the connection should be rejected.
func (d *LimitDispatcher) identifyAndCheck(ctx context.Context, dest net.Destination) (email, sourceIP string, isTCP bool, err error) {
	si := session.InboundFromContext(ctx)
	if si == nil || si.User == nil || len(si.User.Email) == 0 {
		return "", "", false, nil
	}
	email = si.User.Email
	sourceIP = si.Source.Address.IP().String()
	isTCP = dest.Network == net.Network_TCP

	if d.checkDeviceLimit(email, sourceIP, isTCP) {
		nlog.Core().Debug("xray: device limit exceeded", "email", email, "ip", sourceIP)
		return "", "", false, errors.New("device limit exceeded for " + email)
	}
	return email, sourceIP, isTCP, nil
}

// trackLink records connection lifecycle without mutating xray-core owned
// transport primitives. This keeps mux/XUDP compatible while still allowing
// the dispatcher to release device-limit state when the link closes.
//
// Fields are stored on the wrapper instead of a capturing closure so each
// connection only pays for one small object allocation (not object + func).
func (d *LimitDispatcher) trackLink(link *transport.Link, email, sourceIP string, isTCP bool) {
	d.connCount.Add(1)
	link.Writer = &closeTrackingWriter{
		Writer:     link.Writer,
		dispatcher: d,
		email:      email,
		sourceIP:   sourceIP,
		isTCP:      isTCP,
	}
}

// ─── features.Feature (delegated) ───────────────────────────────────────────

func (d *LimitDispatcher) Type() interface{} { return routing.DispatcherType() }

func (d *LimitDispatcher) Start() error {
	if s, ok := d.inner.(interface{ Start() error }); ok {
		return s.Start()
	}
	return nil
}

func (d *LimitDispatcher) Close() error {
	if c, ok := d.inner.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

// ─── Limit management (called by Xray kernel) ──────────────────────────────

func (d *LimitDispatcher) UpdateLimits(emailToUID map[string]int, deviceLimits, _ map[string]int) {
	d.mu.Lock()
	d.emailToUID = emailToUID
	d.deviceLimits = deviceLimits
	d.mu.Unlock()
}

// UpdateGlobalDevices replaces the remote device snapshot from the panel.
// users is panel userID → list of source IPs currently seen across the fleet.
// Local connections keep their own refcounts; only non-local IPs are stored
// here so they still contribute to the hard cap.
func (d *LimitDispatcher) UpdateGlobalDevices(users map[int][]string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Build uid → email reverse index once under the same lock.
	// Prefer the stats email (user@N) over raw UUID keys that share the same
	// uid, so remote IPs land under the same key as local limitedIPs.
	uidToEmail := make(map[int]string, len(d.emailToUID))
	for email, uid := range d.emailToUID {
		if existing, ok := uidToEmail[uid]; ok {
			// Prefer stats email (user@N) over raw UUID aliases.
			if isUserEmail(email) && !isUserEmail(existing) {
				uidToEmail[uid] = email
			}
			continue
		}
		uidToEmail[uid] = email
	}

	next := make(map[string]map[string]struct{}, len(users))
	for uid, ips := range users {
		email := uidToEmail[uid]
		if email == "" {
			// User not present on this node yet — keep a synthetic key so a
			// later limit refresh still has the remote set if needed. Using
			// userEmail keeps it consistent with local keys.
			email = userEmail(uid)
		}
		local := d.limitedIPs[email]
		set := make(map[string]struct{}, len(ips))
		for _, ip := range ips {
			if ip == "" {
				continue
			}
			// Skip IPs that already have a local connection — they are
			// tracked via limitedIPs refcounts and must not be double-counted.
			if local != nil && local[ip] > 0 {
				continue
			}
			set[ip] = struct{}{}
		}
		if len(set) > 0 {
			next[email] = set
		}
	}
	d.globalIPs = next
}

// ClearGlobalDevices drops the remote device snapshot (e.g. on WS disconnect).
// Local connection tracking is left intact.
func (d *LimitDispatcher) ClearGlobalDevices() {
	d.mu.Lock()
	d.globalIPs = make(map[string]map[string]struct{})
	d.mu.Unlock()
}

// snapshotGlobalDevices returns a deep copy of the remote IP set so a new
// LimitDispatcher can inherit fleet state across kernel restarts.
func (d *LimitDispatcher) snapshotGlobalDevices() map[string]map[string]struct{} {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.globalIPs) == 0 {
		return nil
	}
	out := make(map[string]map[string]struct{}, len(d.globalIPs))
	for email, ips := range d.globalIPs {
		cp := make(map[string]struct{}, len(ips))
		for ip := range ips {
			cp[ip] = struct{}{}
		}
		out[email] = cp
	}
	return out
}

// restoreGlobalDevices replaces the remote IP set with a previously taken
// snapshot. Used when the xray instance is rebuilt and a fresh dispatcher
// would otherwise forget multi-node occupancy.
func (d *LimitDispatcher) restoreGlobalDevices(snapshot map[string]map[string]struct{}) {
	if snapshot == nil {
		return
	}
	d.mu.Lock()
	d.globalIPs = snapshot
	d.mu.Unlock()
}

func (d *LimitDispatcher) ResetConns() {
	d.mu.Lock()
	d.limitedIPs = make(map[string]map[string]int)
	// Keep globalIPs: they come from the panel and are independent of local
	// connection lifecycle. Clearing them here would briefly open a window
	// where multi-node limits stop working after a kernel restart.
	d.mu.Unlock()

	// Clear unlimited IPs
	d.unlimitedIPs.Range(func(key, _ interface{}) bool {
		d.unlimitedIPs.Delete(key)
		return true
	})

	d.connCount.Store(0)
}

// GetConnectionState returns dispatcher-tracked alive IPs and connection count.
// Traffic bytes are intentionally left to xray's built-in stats pipeline.
//
// The returned maps are owned by the caller. Empty result is a non-nil empty
// map so trackers can distinguish "no online users" from "not running".
func (d *LimitDispatcher) GetConnectionState() (aliveIPs map[int]map[string]bool, connCount int) {
	d.mu.RLock()
	emailToUID := d.emailToUID
	aliveIPs = make(map[int]map[string]bool, len(d.limitedIPs))

	// Collect IPs from limited users while holding RLock so the nested
	// maps are not mutated by delConn mid-iteration.
	for email, ipsMap := range d.limitedIPs {
		uid := emailToUID[email]
		if uid == 0 || len(ipsMap) == 0 {
			continue
		}
		ipSet := make(map[string]bool, len(ipsMap))
		for ip := range ipsMap {
			ipSet[ip] = true
		}
		aliveIPs[uid] = ipSet
	}
	d.mu.RUnlock()

	// Collect IPs from unlimited users (lock-free sync.Map).
	d.unlimitedIPs.Range(func(key, value interface{}) bool {
		email := key.(string)
		uid := emailToUID[email]
		if uid == 0 {
			return true
		}
		ic := value.(*ipCounter)
		if ips := ic.aliveIPs(); len(ips) > 0 {
			if existing, ok := aliveIPs[uid]; ok {
				for ip := range ips {
					existing[ip] = true
				}
			} else {
				aliveIPs[uid] = ips
			}
		}
		return true
	})

	connCount = int(d.connCount.Load())
	return
}

// ─── Internal helpers ───────────────────────────────────────────────────────

// isUserEmail reports whether email is the stats-tracking form "user@<id>".
func isUserEmail(email string) bool {
	return len(email) > 5 && email[:5] == "user@"
}

// hardDeviceLimit returns the admission ceiling after applying the grace
// percentage. limit<=0 means unlimited (caller should not invoke this).
func hardDeviceLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	// ceil(limit * (100+grace) / 100) without float, then at least `limit`.
	hard := (limit*(100+deviceLimitGracePercent) + 99) / 100
	if hard < limit {
		return limit
	}
	return hard
}

// distinctDeviceCount returns how many unique source IPs the user currently
// occupies, merging local connections with remote (panel-synced) IPs.
// Must be called with d.mu held (read or write).
func (d *LimitDispatcher) distinctDeviceCount(email, extraIP string) int {
	seen := make(map[string]struct{}, 8)
	if local := d.limitedIPs[email]; local != nil {
		for ip, n := range local {
			if n > 0 {
				seen[ip] = struct{}{}
			}
		}
	}
	if remote := d.globalIPs[email]; remote != nil {
		for ip := range remote {
			seen[ip] = struct{}{}
		}
	}
	if extraIP != "" {
		seen[extraIP] = struct{}{}
	}
	return len(seen)
}

// checkDeviceLimit enforces per-user device limits across local + remote IPs.
// Returns true when the connection must be rejected.
//
// Policy:
//   - device_limit <= 0 → unlimited (lock-free path)
//   - already-known local IP → always allowed (reconnect / multi-conn)
//   - new IP allowed while distinct count (local∪remote∪new) <= hard cap
//     where hard cap = ceil(device_limit * 1.30)
//   - when over hard cap, only the lexicographically lowest hard-cap IPs win
//     (deterministic, shared across nodes for the same IP set)
func (d *LimitDispatcher) checkDeviceLimit(email, sourceIP string, isTCP bool) bool {
	d.mu.RLock()
	limit, hasLimit := d.deviceLimits[email]
	d.mu.RUnlock()

	// Fast path: no device limit — use lock-free sync.Map.
	if !hasLimit || limit <= 0 {
		if isTCP {
			v, _ := d.unlimitedIPs.LoadOrStore(email, &ipCounter{})
			ic := v.(*ipCounter)

			// Increment IP refcount atomically.
			rv, _ := ic.ips.LoadOrStore(sourceIP, &atomic.Int64{})
			rv.(*atomic.Int64).Add(1)
		}
		return false
	}

	hard := hardDeviceLimit(limit)

	// Fast path: already-known local IP — always allow (and bump refcount).
	d.mu.RLock()
	if ips := d.limitedIPs[email]; ips != nil && ips[sourceIP] > 0 {
		d.mu.RUnlock()
		if isTCP {
			d.mu.Lock()
			if d.limitedIPs[email] == nil {
				d.limitedIPs[email] = make(map[string]int)
			}
			d.limitedIPs[email][sourceIP]++
			// IP moved local — drop from remote set to avoid double-count.
			if remote := d.globalIPs[email]; remote != nil {
				delete(remote, sourceIP)
			}
			d.mu.Unlock()
		}
		return false
	}

	// Under hard cap with room for this new IP?
	if d.distinctDeviceCount(email, sourceIP) <= hard {
		d.mu.RUnlock()
		if isTCP {
			d.mu.Lock()
			// Re-check under write lock to close the TOCTOU window.
			if d.limitedIPs[email] == nil {
				d.limitedIPs[email] = make(map[string]int)
			}
			if d.limitedIPs[email][sourceIP] > 0 || d.distinctDeviceCount(email, sourceIP) <= hard {
				d.limitedIPs[email][sourceIP]++
				if remote := d.globalIPs[email]; remote != nil {
					delete(remote, sourceIP)
				}
			} else {
				// Lost the race — fall through to deterministic reject under
				// the same write lock by re-running the slow path inline.
				d.mu.Unlock()
				return d.checkDeviceLimitSlow(email, sourceIP, isTCP, hard)
			}
			d.mu.Unlock()
		}
		return false
	}
	d.mu.RUnlock()

	return d.checkDeviceLimitSlow(email, sourceIP, isTCP, hard)
}

// checkDeviceLimitSlow is the write-locked admission path used when the fast
// path believes we are at/over the hard cap (or lost a race).
func (d *LimitDispatcher) checkDeviceLimitSlow(email, sourceIP string, isTCP bool, hard int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	ips := d.limitedIPs[email]
	if ips == nil {
		ips = make(map[string]int)
		d.limitedIPs[email] = ips
	}

	// Already local.
	if ips[sourceIP] > 0 {
		if isTCP {
			ips[sourceIP]++
		}
		return false
	}

	// Room under hard cap (local ∪ remote ∪ new).
	if d.distinctDeviceCount(email, sourceIP) <= hard {
		if isTCP {
			ips[sourceIP]++
			if remote := d.globalIPs[email]; remote != nil {
				delete(remote, sourceIP)
			}
		}
		return false
	}

	// Over hard cap — deterministic: keep the lowest `hard` IPs.
	ipSet := make(map[string]struct{}, hard+1)
	for ip, n := range ips {
		if n > 0 {
			ipSet[ip] = struct{}{}
		}
	}
	if remote := d.globalIPs[email]; remote != nil {
		for ip := range remote {
			ipSet[ip] = struct{}{}
		}
	}
	ipSet[sourceIP] = struct{}{}

	ipList := make([]string, 0, len(ipSet))
	for ip := range ipSet {
		ipList = append(ipList, ip)
	}
	sort.Strings(ipList)

	for i := 0; i < hard && i < len(ipList); i++ {
		if ipList[i] == sourceIP {
			if isTCP {
				ips[sourceIP]++
				if remote := d.globalIPs[email]; remote != nil {
					delete(remote, sourceIP)
				}
			}
			return false
		}
	}
	return true
}

// delConn decrements the IP refcount when a connection closes.
func (d *LimitDispatcher) delConn(email, sourceIP string) {
	// Check if this is an unlimited user first (lock-free).
	if v, ok := d.unlimitedIPs.Load(email); ok {
		ic := v.(*ipCounter)
		if rv, ok := ic.ips.Load(sourceIP); ok {
			counter := rv.(*atomic.Int64)
			if counter.Add(-1) <= 0 {
				ic.ips.Delete(sourceIP)
			}
		}
		// Clean up empty ipCounter to prevent sync.Map bloat.
		empty := true
		ic.ips.Range(func(_, _ interface{}) bool { empty = false; return false })
		if empty {
			d.unlimitedIPs.Delete(email)
		}
		return
	}

	// Limited user — use write lock.
	d.mu.Lock()
	defer d.mu.Unlock()
	if ips, ok := d.limitedIPs[email]; ok {
		ips[sourceIP]--
		if ips[sourceIP] <= 0 {
			delete(ips, sourceIP)
		}
		if len(ips) == 0 {
			delete(d.limitedIPs, email)
		}
	}
}

// closeTrackingWriter wraps a link writer so we observe Close/Interrupt and
// release device-limit state without a capturing closure.
type closeTrackingWriter struct {
	buf.Writer
	dispatcher *LimitDispatcher
	email      string
	sourceIP   string
	isTCP      bool
	closed     atomic.Bool
}

func (w *closeTrackingWriter) onClose() {
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	if w.isTCP {
		w.dispatcher.delConn(w.email, w.sourceIP)
	}
	w.dispatcher.connCount.Add(-1)
}

func (w *closeTrackingWriter) Close() error {
	w.onClose()
	return common.Close(w.Writer)
}

func (w *closeTrackingWriter) Interrupt() {
	w.onClose()
	common.Interrupt(w.Writer)
}
