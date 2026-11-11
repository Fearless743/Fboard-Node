package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/fearless743/fboard-node/internal/config"
)

// MachinePanelControlPlane implements ControlPlane for a single node running
// under the machine orchestrator. WS-only: initial config/users come from the
// shared WS mailbox (panel pushFullSync on connect); reports go out via the
// virtual PushClient backed by the shared machine WS connection.
type MachinePanelControlPlane struct {
	kcfg         config.KernelConfig
	push         PushClient // virtual push client from WS mux (may be nil)
	pushInterval int

	registerFn func(statuses chan<- StatusChange) *NodeMailbox
}

// NewMachinePanelControlPlane creates a per-node control plane for machine mode.
// push may be nil if WS is unavailable.
// registerFn is called during Initial() to wire machine mailbox access and status
// updates into the service loop.
func NewMachinePanelControlPlane(
	kcfg config.KernelConfig,
	push PushClient,
	pushInterval int,
	registerFn func(statuses chan<- StatusChange) *NodeMailbox,
) *MachinePanelControlPlane {
	return &MachinePanelControlPlane{
		kcfg:         kcfg,
		push:         push,
		pushInterval: pushInterval,
		registerFn:   registerFn,
	}
}

func (p *MachinePanelControlPlane) SupportsPolling() bool       { return false }
func (p *MachinePanelControlPlane) SupportsReporting() bool     { return p.push != nil }
func (p *MachinePanelControlPlane) SupportsDeviceReports() bool { return p.push != nil }

func (p *MachinePanelControlPlane) Initial(
	ctx context.Context,
	metricsFn func() map[string]interface{},
	events chan<- Event,
	statuses chan<- StatusChange,
) (Bootstrap, error) {
	bootstrap := Bootstrap{}
	if p.pushInterval > 0 {
		bootstrap.PushInterval = p.pushInterval
	}

	// Register mailbox access so WS events have somewhere to land while we wait.
	var mb *NodeMailbox
	var mailboxCh <-chan struct{}
	if p.push != nil && p.registerFn != nil {
		mb = p.registerFn(statuses)
		bootstrap.Mailbox = mb
		bootstrap.Push = p.push
		mailboxCh = mb.NotifyCh()
		// Mark ready immediately: WS full-sync may have arrived before the
		// service started; DrainIfReady requires ready to emit.
		mb.MarkReady()
		// Re-request full-sync: covers nodes added via sync.nodes (panel only
		// auto-pushes on connect) and races where the connect-time push
		// arrived before this mailbox was registered.
		p.push.RequestFullSync()
	}

	// WS-only bootstrap: wait for the panel full-sync (sync.config + sync.users)
	// instead of REST polling. The panel pushes both on every (re)connect.
	timeout := 60 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d < timeout {
			timeout = d
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		if mb != nil {
			state := mb.DrainIfReady()
			if state.HasConfig {
				bootstrap.Config = state.Config
			}
			if state.HasUsers {
				bootstrap.Users = state.Users
			}
			if bootstrap.Config != nil && bootstrap.Users != nil {
				return bootstrap, nil
			}
		}
		select {
		case <-ctx.Done():
			return Bootstrap{}, ctx.Err()
		case <-timer.C:
			if bootstrap.Config != nil && bootstrap.Users != nil {
				return bootstrap, nil
			}
			return Bootstrap{}, fmt.Errorf("machine initial sync timeout: waiting for WS full-sync")
		case <-mailboxCh:
		}
	}
}

func (p *MachinePanelControlPlane) Poll(ctx context.Context) (Snapshot, error) {
	return Snapshot{}, fmt.Errorf("polling disabled in WS-only mode")
}

func (p *MachinePanelControlPlane) Report(payload ReportPayload) error {
	if p.push == nil {
		return fmt.Errorf("ws not connected")
	}
	return p.push.SendReport(payload)
}

func (p *MachinePanelControlPlane) ReportDevices(push PushClient, devices map[int][]string) {
	if push != nil {
		push.SendDeviceReport(devices)
	}
}

func (p *MachinePanelControlPlane) Metrics() APIMetrics {
	return APIMetrics{}
}
