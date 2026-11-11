// Package machine implements the machine-mode orchestrator that manages node
// lifecycles over a single shared WebSocket connection (WS-only, no REST).
package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fearless743/fboard-node/internal/buildinfo"
	"github.com/fearless743/fboard-node/internal/config"
	"github.com/fearless743/fboard-node/internal/controlplane"
	"github.com/fearless743/fboard-node/internal/monitor"
	"github.com/fearless743/fboard-node/internal/nlog"
	"github.com/fearless743/fboard-node/internal/opsutil"
	"github.com/fearless743/fboard-node/internal/panel"
	"github.com/fearless743/fboard-node/internal/service"
)

// nodeHandle tracks a running node service.
type nodeHandle struct {
	cancel  context.CancelFunc
	done    chan struct{}
	mailbox *controlplane.NodeMailbox
	svc     *service.Service
}

// Orchestrator manages all nodes bound to a panel machine. It:
//   - learns nodes from WS auth.success / sync.nodes
//   - starts / stops Service instances as nodes are added / removed
//   - maintains a shared WS connection that demuxes events by node_id
//   - reports machine-level load via WS machine.status
type Orchestrator struct {
	cfg       *config.Config
	panelURL  string
	token     string
	machineID int
	wsURL     string

	mu    sync.Mutex
	nodes map[int]*nodeHandle // node_id → handle

	// Per-node mailbox keyed by node_id. Shared WS events are aggregated here
	// and each node service drains the latest state when ready.
	eventsMu  sync.RWMutex
	mailboxes map[int]*controlplane.NodeMailbox
	statuses  map[int]chan<- controlplane.StatusChange

	// Shared WS client.
	ws       *panel.WSClient
	wsCancel context.CancelFunc

	// runCtx is stored from Run() so that onWSEvent can trigger rediscover
	// for sync.nodes events without blocking the main loop.
	runCtx context.Context

	pushInterval time.Duration
}

// New creates a machine orchestrator from the given config.
func New(cfg *config.Config) *Orchestrator {
	machineID := 0
	token := ""
	if cfg.Machine != nil {
		machineID = cfg.Machine.MachineID
		token = cfg.Machine.Token
	}
	return &Orchestrator{
		cfg:       cfg,
		panelURL:  cfg.Panel.URL,
		token:     token,
		machineID: machineID,
		wsURL:     panel.DeriveWSURL(cfg.Panel.URL, cfg.WS.URL),
		nodes:     make(map[int]*nodeHandle),
		mailboxes: make(map[int]*controlplane.NodeMailbox),
		statuses:  make(map[int]chan<- controlplane.StatusChange),
	}
}

// log returns a logger scoped to this machine so multi-instance rings never mix.
func (o *Orchestrator) log() *nlog.NodeLog {
	if o.machineID > 0 {
		return nlog.ForMachine(o.machineID)
	}
	return nlog.Core()
}

// Run is the main loop. It blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.runCtx = ctx
	if o.wsURL == "" {
		return fmt.Errorf("cannot derive WS url from panel.url %q", o.panelURL)
	}
	if o.machineID <= 0 || o.token == "" {
		return fmt.Errorf("machine.machine_id and machine.token are required")
	}

	o.pushInterval = 60 * time.Second
	if o.cfg.Node.PushInterval > 0 {
		o.pushInterval = time.Duration(o.cfg.Node.PushInterval) * time.Second
	}

	// Start machine-level WS; node discovery arrives via auth.success.
	o.startWS(ctx)
	o.log().Info(fmt.Sprintf("machine %d: ws connecting to %s", o.machineID, o.wsURL))

	statusTicker := time.NewTicker(o.pushInterval)
	defer statusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.stopAll()
			return nil

		case <-statusTicker.C:
			o.reportMachineStatus()
		}
	}
}

// ─── Node lifecycle ──────────────────────────────────────────────────────

func (o *Orchestrator) startNode(ctx context.Context, mn panel.MachineNode) {
	o.mu.Lock()
	// A stale handle left behind by a crashed/exited node service must not
	// block rediscovery from restarting it. Drop it and fall through.
	if h, exists := o.nodes[mn.ID]; exists {
		select {
		case <-h.done:
			delete(o.nodes, mn.ID)
		default:
			o.mu.Unlock()
			return
		}
	}

	nodeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	mb := controlplane.NewNodeMailbox()
	o.nodes[mn.ID] = &nodeHandle{cancel: cancel, done: done, mailbox: mb}
	o.mu.Unlock()

	o.eventsMu.Lock()
	o.mailboxes[mn.ID] = mb
	o.eventsMu.Unlock()

	nodeCfg := o.cfg.ExpandMachineNode(mn.ID)

	var push controlplane.PushClient
	if o.ws != nil {
		push = &machineNodePush{
			nodeID: mn.ID,
			ws:     o.ws,
		}
	}

	// The registerFn is called by MachinePanelControlPlane.Initial() to expose
	// the node mailbox + status channel to the Service.
	nodeID := mn.ID
	registerFn := func(st chan<- controlplane.StatusChange) *controlplane.NodeMailbox {
		o.registerNode(nodeID, st)
		return mb
	}

	pushInterval := int(o.pushInterval / time.Second)
	cp := controlplane.NewMachinePanelControlPlane(nodeCfg.Kernel, push, pushInterval, registerFn)
	svc := service.NewWithControlPlane(nodeCfg, cp)

	o.mu.Lock()
	if h, ok := o.nodes[mn.ID]; ok {
		h.svc = svc
	}
	o.mu.Unlock()

	o.log().Info(fmt.Sprintf("machine: starting node %d (%s/%s)",
		mn.ID, mn.Type, mn.Name))

	go func() {
		defer close(done)
		defer o.unregisterNode(mn.ID)
		if err := svc.Run(nodeCtx); err != nil {
			o.log().Error("machine node exited with error",
				"node_id", mn.ID, "error", err)
		}
	}()
}

func (o *Orchestrator) stopNode(nodeID int) {
	o.mu.Lock()
	h, ok := o.nodes[nodeID]
	if !ok {
		o.mu.Unlock()
		return
	}
	delete(o.nodes, nodeID)
	o.mu.Unlock()

	o.eventsMu.Lock()
	delete(o.mailboxes, nodeID)
	delete(o.statuses, nodeID)
	o.eventsMu.Unlock()

	o.log().Info(fmt.Sprintf("machine: stopping node %d", nodeID))
	h.cancel()
	<-h.done
}

func (o *Orchestrator) stopAll() {
	o.mu.Lock()
	handles := make(map[int]*nodeHandle, len(o.nodes))
	for id, h := range o.nodes {
		handles[id] = h
	}
	o.mu.Unlock()

	for id, h := range handles {
		o.log().Info(fmt.Sprintf("machine: stopping node %d", id))
		h.cancel()
	}
	for _, h := range handles {
		<-h.done
	}

	if o.wsCancel != nil {
		o.wsCancel()
	}
}

// ─── Node discovery (WS-driven) ──────────────────────────────────────────

// applyNodes reconciles running nodes with the latest panel-provided list.
func (o *Orchestrator) applyNodes(nodes []panel.MachineNode) {
	wanted := make(map[int]panel.MachineNode, len(nodes))
	for _, n := range nodes {
		wanted[n.ID] = n
	}

	o.mu.Lock()
	var toRemove []int
	for id := range o.nodes {
		if _, ok := wanted[id]; !ok {
			toRemove = append(toRemove, id)
		}
	}
	o.mu.Unlock()

	for _, id := range toRemove {
		o.stopNode(id)
	}

	// Clean up any orphaned mailbox/status entries for nodes that are no
	// longer part of this machine.
	o.eventsMu.Lock()
	for id := range o.mailboxes {
		if _, ok := wanted[id]; !ok {
			delete(o.mailboxes, id)
		}
	}
	for id := range o.statuses {
		if _, ok := wanted[id]; !ok {
			delete(o.statuses, id)
		}
	}
	o.eventsMu.Unlock()

	ctx := o.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	for _, n := range nodes {
		o.startNode(ctx, n) // no-op if already running
	}
}

// ─── Machine status reporting (WS) ───────────────────────────────────────

func (o *Orchestrator) reportMachineStatus() {
	if o.ws == nil || !o.ws.IsConnected() {
		return
	}
	s := monitor.Collect()
	payload := map[string]interface{}{
		"cpu":  s.CPU,
		"mem":  map[string]interface{}{"total": s.MemTotal, "used": s.MemUsed},
		"swap": map[string]interface{}{"total": s.SwapTotal, "used": s.SwapUsed},
		"disk": map[string]interface{}{"total": s.DiskTotal, "used": s.DiskUsed},
	}
	if s.NetInSpeed >= 0 && s.NetOutSpeed >= 0 {
		payload["net"] = map[string]interface{}{"in_speed": s.NetInSpeed, "out_speed": s.NetOutSpeed}
	}
	if buildinfo.Version != "" {
		payload["version"] = buildinfo.Version
	}
	payload["kernel"] = o.aggregateKernelStatus()
	o.ws.SendMachineStatus(payload)
}

// aggregateKernelStatus summarizes embedded-kernel state across all node
// services on this machine for panel load_status.kernel.
//
// status:
//
//	idle     — no node services
//	running  — every node kernel is running
//	stopped  — no node kernel is running
//	partial  — mix of running / stopped
func (o *Orchestrator) aggregateKernelStatus() map[string]interface{} {
	o.mu.Lock()
	defer o.mu.Unlock()

	total := 0
	running := 0
	desired := 0
	for _, h := range o.nodes {
		if h == nil || h.svc == nil {
			// Service not ready yet — still counts as a hosted node slot
			// with unknown (non-running) kernel.
			total++
			continue
		}
		total++
		r, d := h.svc.KernelState()
		if r {
			running++
		}
		if d {
			desired++
		}
	}

	status := "idle"
	switch {
	case total == 0:
		status = "idle"
	case running == total:
		status = "running"
	case running == 0:
		status = "stopped"
	default:
		status = "partial"
	}

	return map[string]interface{}{
		"status":        status,
		"nodes_total":   total,
		"nodes_running": running,
		"nodes_desired": desired,
	}
}

// ─── WS mux ─────────────────────────────────────────────────────────────

func (o *Orchestrator) startWS(ctx context.Context) {
	wsCfg := panel.WSClientConfig{
		StatusInterval:   time.Duration(o.cfg.WS.StatusInterval) * time.Second,
		HandshakeTimeout: time.Duration(o.cfg.WS.HandshakeTimeout) * time.Second,
		BackoffInitial:   time.Duration(o.cfg.WS.BackoffInitial) * time.Second,
		BackoffMax:       time.Duration(o.cfg.WS.BackoffMax) * time.Second,
		MachineID:        o.machineID,
	}

	o.ws = panel.NewWSClient(
		o.wsURL,
		o.token,
		0, // no single node_id
		wsCfg,
		o.onWSEvent,
		o.onWSStatus,
		nil, // per-node status is sent via machineNodePush
	)
	o.ws.SetOnAuth(o.onWSAuth)

	wsCtx, wsCancel := context.WithCancel(ctx)
	o.wsCancel = wsCancel
	go o.ws.Run(wsCtx)

	o.log().Info("machine: ws mux started")
}

// onWSAuth handles the initial node list from WS auth.success.
func (o *Orchestrator) onWSAuth(auth panel.AuthInfo) {
	if auth.PushInterval >= 10 {
		o.pushInterval = time.Duration(auth.PushInterval) * time.Second
	}
	o.log().Info(fmt.Sprintf("machine %d: discovered %d nodes via WS",
		o.machineID, len(auth.Nodes)))
	o.applyNodes(auth.Nodes)
}

// onWSEvent routes a WS event to the correct node's channel.
// Machine-level events (sync.nodes / sync.logs / upgrade / kernel / legacy restart) are handled here.
func (o *Orchestrator) onWSEvent(event panel.WSEvent) {
	switch event.Type {
	case panel.WSEventSyncNodes:
		o.log().Info("machine received sync.nodes, reconciling")
		o.applyNodes(event.Nodes)
		return

	case panel.WSEventSyncLogs:
		o.handleSyncLogs(event)
		return

	case panel.WSEventSyncUpgrade:
		o.handleRemoteUpgrade(event.DeltaAction, event.UpgradeChecksum)
		return

	case panel.WSEventSyncKernel:
		o.handleRemoteKernel(event.DeltaAction)
		return

	case panel.WSEventSyncRestart:
		// Legacy: process restart removed; map to kernel restart.
		o.handleRemoteKernel("restart")
		return
	}

	nodeID := event.NodeID
	if nodeID == 0 {
		o.log().Debug("machine ws event missing node_id, dropping", "type", event.Type)
		return
	}

	translated, err := controlplane.TranslateWSEvent(event, o.cfg.Kernel)
	if err != nil {
		o.log().Warn("machine ws event translation failed",
			"type", event.Type, "node_id", nodeID, "error", err)
		return
	}

	o.eventsMu.RLock()
	mailbox, ok := o.mailboxes[nodeID]
	o.eventsMu.RUnlock()
	if !ok {
		o.log().Debug("machine ws event for unknown node", "node_id", nodeID, "type", event.Type)
		return
	}
	mailbox.Apply(translated)
}

// handleSyncLogs replies to the panel with recent in-memory process logs.
func (o *Orchestrator) handleSyncLogs(event panel.WSEvent) {
	if o.ws == nil {
		o.log().Warn("machine sync.logs requested but ws is nil")
		return
	}
	limit := event.LogLimit
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}
	lines := nlog.Recent(o.machineID, limit)
	if lines == nil {
		lines = []string{}
	}
	o.log().Debug("machine replying sync.logs", "machine_id", o.machineID, "lines", len(lines), "req_id", event.LogReqID)
	o.ws.SendReportLogs(lines, event.LogReqID)
}

// resolveFbctl locates the fbctl binary via opsutil (PATH + common install paths).
// Returns an empty string if no executable candidate is found.
//
// Kept as a thin wrapper so existing call-sites don't need to change.
func resolveFbctl() string { return opsutil.ResolveFbctl() }

// detectInit / isActiveService / restartService thin wrappers over opsutil so
// call-sites read well. Defaults to the install.sh-canonical service name.
const fboardServiceName = opsutil.DefaultServiceName

func detectInit() opsutil.InitSystem { return opsutil.DetectInit(fboardServiceName) }
func isActiveService(sys opsutil.InitSystem) bool {
	return opsutil.IsActive(sys, fboardServiceName)
}
func restartService(sys opsutil.InitSystem) error {
	return opsutil.RestartService(sys, fboardServiceName)
}

// handleRemoteUpgrade runs fbctl upgrade for the whole machine process.
//
//   - Resolves fbctl via PATH + known install locations; never silent-fails.
//   - On success, schedules a service restart under the detected init system
//     (systemd / openrc / FreeBSD rc / sysvinit / supervisor / launchd) so the new binary
//     replaces this one. If no manager is registered, falls back to spawning
//     a detached copy of ourselves and exiting.
//   - Sends WS ops ack to the panel (best-effort) with `ok`/`failed` + detail.
func (o *Orchestrator) handleRemoteUpgrade(version, checksum string) {
	if version == "" {
		version = "latest"
	}
	if err := opsutil.ValidateUpgradeVersion(version); err != nil {
		o.log().Warn("remote upgrade rejected: invalid version", "error", err)
		if o.ws != nil {
			o.ws.SendOpAck("upgrade", "failed", err.Error())
		}
		return
	}
	checksum, err := opsutil.NormalizeChecksum(checksum)
	if err != nil {
		o.log().Warn("remote upgrade rejected: invalid checksum", "error", err)
		if o.ws != nil {
			o.ws.SendOpAck("upgrade", "failed", err.Error())
		}
		return
	}
	if checksum == "" {
		o.log().Warn("remote upgrade without checksum: archive will not be verified")
	}
	o.log().Info("remote upgrade requested", "version", version)

	ack := func(status, detail string) {
		if o.ws != nil {
			o.ws.SendOpAck("upgrade", status, detail)
		}
	}

	go func() {
		fbctl := resolveFbctl()
		if fbctl == "" {
			o.log().Error("remote upgrade failed: fbctl not found in PATH or /usr/{local/,}{bin,sbin}/fbctl")
			ack("failed", "fbctl binary not found on host; install via install.sh or `make install`")
			return
		}
		o.log().Info("remote upgrade invoking fbctl", "path", fbctl, "version", version)
		args := []string{"upgrade", "--version", version}
		if checksum != "" {
			args = append(args, "--checksum", checksum)
		}
		out, err := exec.Command(fbctl, args...).CombinedOutput()
		if err != nil {
			detail := strings.TrimSpace(string(out))
			if detail == "" {
				detail = err.Error()
			}
			o.log().Error("remote upgrade failed", "path", fbctl, "error", err, "output", string(out))
			ack("failed", detail)
			return
		}
		o.log().Info("remote upgrade completed", "path", fbctl, "output", string(out))
		ack("ok", "")
		o.triggerServiceRestart("upgrade")
	}()
}

// handleRemoteKernel fans a kernel lifecycle action out to every node service
// on this machine. Process/WS stay alive; only the embedded xray kernel is
// stopped / started / reloaded / force-restarted.
//
// action ∈ stop|start|reload|restart
func (o *Orchestrator) handleRemoteKernel(action string) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "restart"
	}
	o.log().Info("remote kernel event received", "action", action)

	op := "kernel." + action
	ack := func(status, detail string) {
		if o.ws != nil {
			o.ws.SendOpAck(op, status, detail)
		}
	}

	go func() {
		okCount, failCount, detail := o.controlAllKernels(action)
		// Push fresh kernel aggregate so the admin UI can refresh button state
		// without waiting for the next status ticker.
		o.reportMachineStatus()
		if failCount == 0 {
			o.log().Info("remote kernel op completed",
				"action", action, "ok", okCount)
			ack("ok", detail)
			return
		}
		o.log().Error("remote kernel op partially/fully failed",
			"action", action, "ok", okCount, "failed", failCount, "detail", detail)
		ack("failed", detail)
	}()
}

// controlAllKernels invokes Service.ControlKernel on every running node.
// Returns counts and a human-readable detail string for op.ack.
func (o *Orchestrator) controlAllKernels(action string) (okCount, failCount int, detail string) {
	o.mu.Lock()
	handles := make(map[int]*nodeHandle, len(o.nodes))
	for id, h := range o.nodes {
		handles[id] = h
	}
	o.mu.Unlock()

	if len(handles) == 0 {
		return 0, 0, "no nodes running on this machine"
	}

	var parts []string
	for id, h := range handles {
		if h == nil || h.svc == nil {
			failCount++
			parts = append(parts, fmt.Sprintf("node %d: service not ready", id))
			continue
		}
		if err := h.svc.ControlKernel(action); err != nil {
			failCount++
			parts = append(parts, fmt.Sprintf("node %d: %v", id, err))
			continue
		}
		okCount++
		parts = append(parts, fmt.Sprintf("node %d: ok", id))
	}
	detail = strings.Join(parts, "; ")
	return okCount, failCount, detail
}

// triggerServiceRestart fans out the restart command to whichever init system
// this host runs. Returns the dispatch error so the calling handler can decide
// whether to ack.failed. When no manager is present (container / dev shell)
// OR when the manager refuses (Unit not found / unit not installed), attempts
// self-respawn + exit as the final fallback so the binary can still cycle
// under a plain shell, kubelet, dockerd, etc.
//
// For `upgrade`, the supervising process is expected to keep running until we
// exit so that fbctl can finish its own restart sequence if any; we therefore
// don't os.Exit here.
func (o *Orchestrator) triggerServiceRestart(op string) error {
	sys := detectInit()
	if sys != opsutil.InitNone {
		if err := restartService(sys); err == nil {
			o.log().Info("service restart dispatched", "op", op, "init", sys)
			return nil
		} else {
			o.log().Warn("init-based restart failed; will fall back to self-respawn",
				"op", op, "init", sys, "error", err.Error())
		}
	} else {
		o.log().Warn("no service manager detected; attempting self-respawn", "op", op)
	}

	argv0, err := os.Executable()
	if err != nil {
		return fmt.Errorf("self-respawn failed (cannot resolve argv0): %w", err)
	}
	if rerr := opsutil.SelfRespawn(argv0, os.Args); rerr != nil {
		return fmt.Errorf("self-respawn failed: %w", rerr)
	}
	o.log().Info("self-respawn dispatched", "op", op)
	if op == "restart" {
		o.log().Info("self-respawn dispatched for restart; exiting")
		time.Sleep(1 * time.Second)
		o.stopAll()
		os.Exit(0)
	}
	return nil
}

// onWSStatus broadcasts WS connectivity changes to all registered nodes.
func (o *Orchestrator) onWSStatus(status panel.WSStatusChange) {
	change := controlplane.StatusChange{Connected: status.Connected}
	o.eventsMu.RLock()
	defer o.eventsMu.RUnlock()
	for _, ch := range o.statuses {
		select {
		case ch <- change:
		default:
		}
	}
}

func (o *Orchestrator) registerNode(nodeID int, st chan<- controlplane.StatusChange) {
	o.eventsMu.Lock()
	o.statuses[nodeID] = st
	o.eventsMu.Unlock()
}

func (o *Orchestrator) unregisterNode(nodeID int) {
	// Drop the node handle so a later rediscover can restart it if it is still
	// listed by the panel; otherwise stale entries keep dead nodes around and
	// prevent non-existent ones from being cleaned up.
	o.mu.Lock()
	delete(o.nodes, nodeID)
	o.mu.Unlock()

	o.eventsMu.Lock()
	delete(o.mailboxes, nodeID)
	delete(o.statuses, nodeID)
	o.eventsMu.Unlock()
}

// ─── Virtual PushClient ─────────────────────────────────────────────────

// machineNodePush implements controlplane.PushClient for a single node
// backed by the shared machine WS connection. Events are routed by the
// WS mux directly to the Service's channels; this adapter only provides
// connectivity status and send capabilities.
type machineNodePush struct {
	nodeID int
	ws     *panel.WSClient
}

func (p *machineNodePush) Run(ctx context.Context) {
	// The shared WS mux pushes events into our channels; we just wait.
	<-ctx.Done()
}

func (p *machineNodePush) IsConnected() bool {
	return p.ws != nil && p.ws.IsConnected()
}

// SendReport delivers traffic + alive + online + status + metrics via WS node.status.
func (p *machineNodePush) SendReport(payload controlplane.ReportPayload) error {
	if p.ws == nil || !p.ws.IsConnected() {
		return fmt.Errorf("ws not connected")
	}
	data := map[string]interface{}{
		"traffic": strMapTraffic(payload.Traffic),
		"alive":   strMapAlive(payload.Alive),
		"online":  strMapOnline(payload.Online),
		"status": map[string]interface{}{
			"cpu":  payload.CPU,
			"mem":  map[string]interface{}{"total": payload.Mem[0], "used": payload.Mem[1]},
			"swap": map[string]interface{}{"total": payload.Swap[0], "used": payload.Swap[1]},
			"disk": map[string]interface{}{"total": payload.Disk[0], "used": payload.Disk[1]},
		},
		"metrics": payload.Metrics,
	}
	p.ws.SendNodeStatus(p.nodeID, data)
	return nil
}

func strMapTraffic(in map[int][2]int64) map[string][2]int64 {
	out := make(map[string][2]int64, len(in))
	for uid, d := range in {
		out[strconv.Itoa(uid)] = d
	}
	return out
}

func strMapAlive(in map[int][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for uid, ips := range in {
		out[strconv.Itoa(uid)] = ips
	}
	return out
}

func strMapOnline(in map[int]int) map[string]int {
	out := make(map[string]int, len(in))
	for uid, c := range in {
		out[strconv.Itoa(uid)] = c
	}
	return out
}

// RequestFullSync asks the panel to re-push sync.config + sync.users for this node.
func (p *machineNodePush) RequestFullSync() {
	if p.ws == nil {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{"node_id": p.nodeID})
	p.ws.SendRaw("request.sync", data)
}

func (p *machineNodePush) SendDeviceReport(devices map[int][]string) {
	if p.ws == nil {
		return
	}
	payload := map[string]interface{}{
		"node_id": p.nodeID,
	}
	// Flatten into the standard format with node_id wrapper.
	strDevices := make(map[string][]string, len(devices))
	for uid, ips := range devices {
		strDevices[fmt.Sprintf("%d", uid)] = ips
	}
	payload["devices"] = strDevices
	data, _ := json.Marshal(payload)
	p.ws.SendRaw(panel.WSEventReportDevices, data)
}

// SendOpAck forwards a remote-ops ack through the shared machine WS. The
// node_id is wrapped in for panel-side correlation; safe to call when
// the underlying WS is nil (drops the event silently).
func (p *machineNodePush) SendOpAck(op string, status string, detail string) {
	if p.ws == nil {
		return
	}
	p.ws.SendOpAck(op, status, detail)
}
