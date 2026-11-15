package controlplane

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fearless743/fboard-node/internal/config"
	"github.com/fearless743/fboard-node/internal/model"
	"github.com/fearless743/fboard-node/internal/nlog"
	"github.com/fearless743/fboard-node/internal/panel"
)

// MachinePanelControlPlane implements ControlPlane for a single node running
// under the machine orchestrator.
//
// REST is the baseline: initial config/users and periodic reconciliation come
// from /api/v2/server/{config,user}; reports go to /api/v2/server/report.
// WebSocket remains the realtime/control path (deltas, devices, remote ops).
// Reporting never uses both transports for the same payload: REST is the only
// traffic sink, so a retry cannot double-count user quota.
type MachinePanelControlPlane struct {
	kcfg         config.KernelConfig
	push         PushClient // shared machine WS adapter (may be nil)
	rest         *panel.Client
	pushInterval int
	pullInterval int
	registerFn   func(statuses chan<- StatusChange) *NodeMailbox

	// restReportDisabled latches when the panel reports 404/405 for the report
	// endpoint (older panel); reporting then falls back to WS once.
	restReportDisabled atomic.Bool
}

// NewMachinePanelControlPlane creates a per-node control plane for machine mode.
// push may be nil when WS is unavailable; rest may be nil for tests/compat.
// registerFn is called during Initial() to wire machine mailbox access and
// status updates into the service loop.
func NewMachinePanelControlPlane(
	kcfg config.KernelConfig,
	push PushClient,
	rest *panel.Client,
	pushInterval int,
	pullInterval int,
	registerFn func(statuses chan<- StatusChange) *NodeMailbox,
) *MachinePanelControlPlane {
	return &MachinePanelControlPlane{
		kcfg:         kcfg,
		push:         push,
		rest:         rest,
		pushInterval: pushInterval,
		pullInterval: pullInterval,
		registerFn:   registerFn,
	}
}

func (p *MachinePanelControlPlane) SupportsPolling() bool { return p.rest != nil }

func (p *MachinePanelControlPlane) SupportsReporting() bool {
	return (p.rest != nil && !p.restReportDisabled.Load()) || p.push != nil
}

func (p *MachinePanelControlPlane) SupportsDeviceReports() bool { return p.push != nil }

func (p *MachinePanelControlPlane) Initial(
	ctx context.Context,
	metricsFn func() map[string]interface{},
	events chan<- Event,
	statuses chan<- StatusChange,
) (Bootstrap, error) {
	bootstrap := Bootstrap{
		PushInterval: p.pushInterval,
		PullInterval: p.pullInterval,
	}

	// Register mailbox first so WS events that arrive during the REST fetch
	// are buffered instead of being dropped.
	var mb *NodeMailbox
	var mailboxCh <-chan struct{}
	if p.push != nil && p.registerFn != nil {
		mb = p.registerFn(statuses)
		bootstrap.Mailbox = mb
		bootstrap.Push = p.push
		mailboxCh = mb.NotifyCh()
		mb.MarkReady()
	}

	// Preferred path: REST snapshot. This works even when the panel's WS
	// server is disabled or still reconnecting.
	if p.rest != nil {
		restCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		cfg, cfgErr := p.rest.GetConfig(restCtx)
		users, usersErr := p.rest.GetUsers(restCtx)
		cancel()

		if cfgErr == nil && usersErr == nil && cfg != nil && users != nil {
			spec, err := model.NodeSpecFromPanelValidated(cfg, p.kcfg)
			if err != nil {
				nlog.Core().Warn("rest initial config rejected; falling back to ws", "error", err)
			} else {
				bootstrap.Config = spec
				bootstrap.Users = model.UserSpecsFromPanel(users)
				bootstrap.Maintenance = cfg.MaintenanceMode
				return bootstrap, nil
			}
		} else {
			nlog.Core().Warn("rest initial fetch failed; falling back to ws full-sync",
				"config_error", errString(cfgErr),
				"users_error", errString(usersErr),
			)
		}
	}

	// Fallback: use a WS full-sync already buffered in the mailbox.
	if mb != nil {
		if state := mb.DrainIfReady(); state.HasConfig && state.HasUsers {
			bootstrap.Config = state.Config
			bootstrap.Users = state.Users
			return bootstrap, nil
		}
		// Re-request full-sync: covers nodes added via sync.nodes (the panel
		// only auto-pushes on connect) and races where the connect-time push
		// arrived before this mailbox was registered.
		if p.push != nil {
			p.push.RequestFullSync()
		}
	} else {
		return Bootstrap{}, fmt.Errorf("initial sync unavailable: REST fetch failed and no ws push configured")
	}

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
			return Bootstrap{}, fmt.Errorf("machine initial sync timeout: waiting for REST or WS full-sync")
		case <-mailboxCh:
		}
	}
}

// Poll fetches config and users. Unchanged sections are reported with their
// Changed flag false; errors return any successfully fetched part alongside
// the error so the service can apply what is safe and retry the rest.
func (p *MachinePanelControlPlane) Poll(ctx context.Context) (Snapshot, error) {
	snap := Snapshot{PolledAt: time.Now()}
	if p.rest == nil {
		return snap, fmt.Errorf("polling disabled: no rest client")
	}

	var errs []string

	cfg, err := p.rest.GetConfig(ctx)
	if err != nil {
		errs = append(errs, "config: "+err.Error())
	} else if cfg != nil {
		spec, err := model.NodeSpecFromPanelValidated(cfg, p.kcfg)
		if err != nil {
			errs = append(errs, "config validation: "+err.Error())
		} else {
			snap.Config = spec
			snap.ConfigChanged = true
			snap.Maintenance = cfg.MaintenanceMode
		}
	}

	users, err := p.rest.GetUsers(ctx)
	if err != nil {
		errs = append(errs, "users: "+err.Error())
	} else if users != nil {
		snap.Users = model.UserSpecsFromPanel(users)
		snap.UsersChanged = true
	} else if p.rest.UsersKnownEmpty() {
		// A 304 after an explicit empty list still means "still empty". Keep
		// reporting the empty state so the service can count its confirmation
		// polls instead of seeing empty exactly once.
		snap.Users = []model.UserSpec{}
		snap.UsersChanged = true
	}

	if len(errs) > 0 {
		return snap, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return snap, nil
}

// Report sends one consolidated payload. REST is the only traffic sink: on
// failure the service restores its tracker and retries with the same report
// id. We intentionally do NOT fall back to WS here, because a request that
// reached the panel but lost its response would otherwise be counted twice.
func (p *MachinePanelControlPlane) Report(ctx context.Context, payload ReportPayload) error {
	if p.rest != nil && !p.restReportDisabled.Load() {
		err := p.rest.Report(ctx, panel.NodeReport{
			ReportID: payload.ReportID,
			Traffic:  payload.Traffic,
			Alive:    payload.Alive,
			Online:   payload.Online,
			CPU:      payload.CPU,
			Mem:      payload.Mem,
			Swap:     payload.Swap,
			Disk:     payload.Disk,
			Metrics:  payload.Metrics,
		})
		if err == nil {
			return nil
		}
		if panel.IsEndpointUnsupported(err) {
			p.restReportDisabled.Store(true)
			nlog.Core().Warn("panel does not support REST report endpoint; falling back to websocket", "error", err)
		} else {
			return err
		}
	}
	if p.push != nil {
		return p.push.SendReport(payload)
	}
	return fmt.Errorf("reporting disabled: no rest client and no ws push")
}

func (p *MachinePanelControlPlane) ReportDevices(push PushClient, devices map[int][]string) {
	if push != nil {
		push.SendDeviceReport(devices)
	}
}

func (p *MachinePanelControlPlane) Metrics() APIMetrics {
	if p.rest == nil {
		return APIMetrics{}
	}
	m := p.rest.SnapshotMetrics()
	return APIMetrics{Success: m.Success, Failure: m.Failure}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
