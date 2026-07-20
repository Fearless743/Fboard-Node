// Package machine implements the machine-mode orchestrator that dynamically
// discovers nodes from the panel's machine API and manages their lifecycles.
package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
//   - discovers nodes via GET /machine/nodes
//   - starts / stops Service instances as nodes are added / removed
//   - maintains a shared WS connection that demuxes events by node_id
//   - reports machine-level load via POST /machine/status
type Orchestrator struct {
	cfg    *config.Config
	client *panel.Client // machine-level client (no node_id)

	mu    sync.Mutex
	nodes map[int]*nodeHandle // node_id → handle

	// Per-node mailbox keyed by node_id. Shared WS events are aggregated here
	// and each node service drains the latest state when ready.
	eventsMu  sync.RWMutex
	mailboxes map[int]*controlplane.NodeMailbox
	statuses  map[int]chan<- controlplane.StatusChange

	// Shared WS client (nil when WS is disabled).
	ws       *panel.WSClient
	wsCancel context.CancelFunc

	// runCtx is stored from Run() so that onWSEvent can trigger rediscover
	// for sync.nodes events without blocking the main loop.
	runCtx context.Context

	pullInterval time.Duration
	pushInterval time.Duration
}

// New creates a machine orchestrator from the given config.
func New(cfg *config.Config) *Orchestrator {
	panelCfg := config.PanelConfig{
		URL:       cfg.Panel.URL,
		Token:     cfg.Machine.Token,
		MachineID: cfg.Machine.MachineID,
	}
	return &Orchestrator{
		cfg:       cfg,
		client:    panel.NewClient(panelCfg),
		nodes:     make(map[int]*nodeHandle),
		mailboxes: make(map[int]*controlplane.NodeMailbox),
		statuses:  make(map[int]chan<- controlplane.StatusChange),
	}
}

// log returns a logger scoped to this machine so multi-instance rings never mix.
func (o *Orchestrator) log() *nlog.NodeLog {
	if o.cfg != nil && o.cfg.Machine != nil && o.cfg.Machine.MachineID > 0 {
		return nlog.ForMachine(o.cfg.Machine.MachineID)
	}
	return nlog.Core()
}

func (o *Orchestrator) machineID() int {
	if o.cfg != nil && o.cfg.Machine != nil {
		return o.cfg.Machine.MachineID
	}
	return 0
}

// Run is the main loop. It blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.runCtx = ctx
	nodesResp, err := o.client.GetMachineNodes()
	if err != nil {
		return fmt.Errorf("initial node discovery: %w", err)
	}

	o.applyIntervals(nodesResp.BaseConfig)
	o.log().Info(fmt.Sprintf("machine %d: discovered %d nodes",
		o.cfg.Machine.MachineID, len(nodesResp.Nodes)))

	// Start machine-level WS as early as possible so sync.nodes can reach an
	// empty machine before the first node is attached.
	o.tryStartWS(ctx)

	// Start initial nodes.
	for _, n := range nodesResp.Nodes {
		o.startNode(ctx, n)
	}

	discoveryTicker := time.NewTicker(o.pullInterval)
	statusTicker := time.NewTicker(o.pushInterval)
	defer discoveryTicker.Stop()
	defer statusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.stopAll()
			return nil

		case <-discoveryTicker.C:
			o.rediscover(ctx)

		case <-statusTicker.C:
			o.reportMachineStatus()
		}
	}
}

// ─── Node lifecycle ──────────────────────────────────────────────────────

func (o *Orchestrator) startNode(ctx context.Context, mn panel.MachineNode) {
	o.mu.Lock()
	if _, exists := o.nodes[mn.ID]; exists {
		o.mu.Unlock()
		return
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

	perNodeClient := o.client.ForNode(mn.ID)
	// Reset cached ETag so the subsequent GetConfig in Initial() gets a full response.
	perNodeClient.ResetConfigETag()

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

	cp := controlplane.NewMachinePanelControlPlane(perNodeClient, nodeCfg.Kernel, push, registerFn)
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

// ─── Node discovery ──────────────────────────────────────────────────────

func (o *Orchestrator) rediscover(ctx context.Context) {
	nodesResp, err := o.client.GetMachineNodes()
	if err != nil {
		o.log().Warn("machine node discovery failed", "error", err)
		return
	}

	wanted := make(map[int]panel.MachineNode, len(nodesResp.Nodes))
	for _, n := range nodesResp.Nodes {
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

	for _, n := range nodesResp.Nodes {
		o.startNode(ctx, n) // no-op if already running
	}
}

// ─── Machine status reporting ────────────────────────────────────────────

func (o *Orchestrator) reportMachineStatus() {
	s := monitor.Collect()
	if err := o.client.ReportMachineStatus(
		s.CPU,
		[2]uint64{s.MemTotal, s.MemUsed},
		[2]uint64{s.SwapTotal, s.SwapUsed},
		[2]uint64{s.DiskTotal, s.DiskUsed},
		s.NetInSpeed, s.NetOutSpeed,
		buildinfo.Version,
	); err != nil {
		o.log().Warn("machine status report failed", "error", err)
	}
}

// ─── WS mux ─────────────────────────────────────────────────────────────

func (o *Orchestrator) tryStartWS(ctx context.Context) {
	hs, err := o.client.Handshake()
	if err != nil {
		o.log().Warn("machine ws handshake failed, REST only", "error", err)
		return
	}
	if !hs.WebSocket.Enabled || hs.WebSocket.WSURL == "" {
		o.log().Info("machine: ws disabled by panel, REST only")
		return
	}

	wsCfg := panel.WSClientConfig{
		StatusInterval:   time.Duration(o.cfg.WS.StatusInterval) * time.Second,
		HandshakeTimeout: time.Duration(o.cfg.WS.HandshakeTimeout) * time.Second,
		BackoffInitial:   time.Duration(o.cfg.WS.BackoffInitial) * time.Second,
		BackoffMax:       time.Duration(o.cfg.WS.BackoffMax) * time.Second,
		MachineID:        o.cfg.Machine.MachineID,
	}

	o.ws = panel.NewWSClient(
		hs.WebSocket.WSURL,
		o.cfg.Machine.Token,
		0, // no single node_id
		wsCfg,
		o.onWSEvent,
		o.onWSStatus,
		nil, // per-node status is sent via machineNodePush
	)

	wsCtx, wsCancel := context.WithCancel(ctx)
	o.wsCancel = wsCancel
	go o.ws.Run(wsCtx)

	o.log().Info("machine: ws mux started")
}

// onWSEvent routes a WS event to the correct node's channel.
// Machine-level events (sync.nodes / sync.logs / upgrade / kernel / legacy restart) are handled here.
func (o *Orchestrator) onWSEvent(event panel.WSEvent) {
	switch event.Type {
	case panel.WSEventSyncNodes:
		o.log().Info("machine received sync.nodes, triggering immediate rediscovery")
		go o.rediscover(o.runCtx)
		return

	case panel.WSEventSyncLogs:
		o.handleSyncLogs(event)
		return

	case panel.WSEventSyncUpgrade:
		o.handleRemoteUpgrade(event.DeltaAction)
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
	lines := nlog.Recent(o.machineID(), limit)
	if lines == nil {
		lines = []string{}
	}
	o.log().Debug("machine replying sync.logs", "machine_id", o.machineID(), "lines", len(lines), "req_id", event.LogReqID)
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

func detectInit() opsutil.InitSystem   { return opsutil.DetectInit(fboardServiceName) }
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
//     (systemd / openrc / sysvinit / supervisor / launchd) so the new binary
//     replaces this one. If no manager is registered, falls back to spawning
//     a detached copy of ourselves and exiting.
//   - Sends WS ops ack to the panel (best-effort) with `ok`/`failed` + detail.
func (o *Orchestrator) handleRemoteUpgrade(version string) {
	if version == "" {
		version = "latest"
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
		out, err := exec.Command(fbctl, "upgrade", "--version", version).CombinedOutput()
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
	o.eventsMu.Lock()
	delete(o.mailboxes, nodeID)
	delete(o.statuses, nodeID)
	o.eventsMu.Unlock()
}

func (o *Orchestrator) applyIntervals(bc panel.MachineBaseConfig) {
	o.pullInterval = time.Duration(bc.PullInterval) * time.Second
	if o.pullInterval < 30*time.Second {
		o.pullInterval = 60 * time.Second
	}
	o.pushInterval = time.Duration(bc.PushInterval) * time.Second
	if o.pushInterval < 10*time.Second {
		o.pushInterval = 60 * time.Second
	}
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
