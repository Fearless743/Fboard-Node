package controlplane

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fearless743/fboard-node/internal/config"
	"github.com/fearless743/fboard-node/internal/panel"
)

type fakePush struct {
	reports  int
	devices  int
	fullSync int
}

func (f *fakePush) Run(ctx context.Context) { <-ctx.Done() }
func (f *fakePush) IsConnected() bool       { return true }
func (f *fakePush) SendReport(ReportPayload) error {
	f.reports++
	return nil
}
func (f *fakePush) SendDeviceReport(devices map[int][]string) { f.devices++ }
func (f *fakePush) RequestFullSync()                          { f.fullSync++ }
func (f *fakePush) SendOpAck(string, string, string)          {}

func TestMachineControlPlaneReportNeverUsesBothTransports(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantErr  bool
		wantWS   int
		wantREST bool
	}{
		{name: "rest ok", status: http.StatusOK, wantWS: 0},
		{name: "rest 500 no fallback", status: http.StatusInternalServerError, wantErr: true, wantWS: 0},
		{name: "rest unsupported falls back", status: http.StatusNotFound, wantWS: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{}`)
			}))
			defer srv.Close()

			push := &fakePush{}
			rest := panel.NewClient(config.PanelConfig{URL: srv.URL, Token: "t", MachineID: 1}).ForNode(1)
			cp := NewMachinePanelControlPlane(config.KernelConfig{}, push, rest, 60, 60, nil)

			err := cp.Report(context.Background(), ReportPayload{ReportID: "rid"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if push.reports != tc.wantWS {
				t.Fatalf("ws reports = %d, want %d", push.reports, tc.wantWS)
			}
		})
	}
}

func TestMachineControlPlanePollParsesConfigAndUsers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/server/config":
			_, _ = io.WriteString(w, `{"protocol":"vless","server_port":443,"maintenance_mode":true}`)
		case "/api/v2/server/user":
			_, _ = io.WriteString(w, `{"users":[{"id":1,"uuid":"u1"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rest := panel.NewClient(config.PanelConfig{URL: srv.URL, Token: "t", MachineID: 1}).ForNode(1)
	cp := NewMachinePanelControlPlane(config.KernelConfig{}, nil, rest, 60, 60, nil)
	snap, err := cp.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !snap.ConfigChanged || snap.Config == nil || snap.Config.Protocol != "vless" {
		t.Fatalf("unexpected config snapshot: %+v", snap)
	}
	if !snap.UsersChanged || len(snap.Users) != 1 {
		t.Fatalf("unexpected user snapshot: %+v", snap)
	}
	if !snap.Maintenance {
		t.Fatal("expected maintenance flag from config")
	}
	if snap.PolledAt.IsZero() {
		t.Fatal("expected PolledAt to be set")
	}
}
