package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fearless743/fboard-node/internal/config"
)

func TestClientGetConfigETagAndBodyHash(t *testing.T) {
	var calls atomic.Int32
	var sawIfNoneMatch atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			sawIfNoneMatch.Store(true)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = io.WriteString(w, `{"protocol":"vless","server_port":443,"network":"tcp"}`)
	}))
	defer srv.Close()

	c := NewClient(config.PanelConfig{URL: srv.URL, Token: "token", MachineID: 1}).ForNode(7)
	cfg, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg == nil || cfg.Protocol != "vless" || cfg.ServerPort != 443 {
		t.Fatalf("unexpected config: %+v", cfg)
	}

	cfg, err = c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig second: %v", err)
	}
	if cfg == nil || cfg.ServerPort != 443 {
		t.Fatalf("expected cached config on 304, got %+v", cfg)
	}
	if !sawIfNoneMatch.Load() {
		t.Fatal("expected If-None-Match to be sent")
	}
}

func TestClientGetConfigBodyHashWithoutETag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No ETag header: body hash must suppress the duplicate.
		_, _ = io.WriteString(w, `{"protocol":"vless","server_port":443}`)
	}))
	defer srv.Close()

	c := NewClient(config.PanelConfig{URL: srv.URL, Token: "token", MachineID: 1}).ForNode(7)
	if _, err := c.GetConfig(context.Background()); err != nil {
		t.Fatalf("first GetConfig: %v", err)
	}
	cfg, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("second GetConfig: %v", err)
	}
	if cfg == nil || cfg.ServerPort != 443 {
		t.Fatalf("expected cached config for unchanged body, got %+v", cfg)
	}
}

func TestClientGetUsersEmptyIsExplicitButMissingIsError(t *testing.T) {
	mode := "empty"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "missing" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		_, _ = io.WriteString(w, `{"users":[]}`)
	}))
	defer srv.Close()

	c := NewClient(config.PanelConfig{URL: srv.URL, Token: "token", MachineID: 1}).ForNode(7)
	users, err := c.GetUsers(context.Background())
	if err != nil {
		t.Fatalf("GetUsers: %v", err)
	}
	if users == nil || len(users) != 0 {
		t.Fatalf("expected explicit empty slice, got %#v", users)
	}

	mode = "missing"
	c.ResetETags()
	if _, err := c.GetUsers(context.Background()); err == nil {
		t.Fatal("expected missing users array to be an error")
	}
}

func TestClientGetMachineNodesRequiresNodesArray(t *testing.T) {
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "missing" {
			_, _ = io.WriteString(w, `{"base_config":{"pull_interval":60}}`)
			return
		}
		_, _ = io.WriteString(w, `{"nodes":[{"id":1,"type":"vless","name":"n1"}],"base_config":{"push_interval":30,"pull_interval":60}}`)
	}))
	defer srv.Close()

	c := NewClient(config.PanelConfig{URL: srv.URL, Token: "token", MachineID: 1})
	resp, err := c.GetMachineNodes(context.Background())
	if err != nil {
		t.Fatalf("GetMachineNodes: %v", err)
	}
	if len(resp.Nodes) != 1 || resp.BaseConfig.PullInterval != 60 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	mode = "missing"
	if _, err := c.GetMachineNodes(context.Background()); err == nil {
		t.Fatal("expected missing nodes array to be an error")
	}
}

func TestClientReportIncludesReportID(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/report" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = io.WriteString(w, `{"data":true}`)
	}))
	defer srv.Close()

	c := NewClient(config.PanelConfig{URL: srv.URL, Token: "token", MachineID: 1}).ForNode(7)
	err := c.Report(context.Background(), NodeReport{
		ReportID: "rid-1",
		Traffic:  map[int][2]int64{1: {10, 20}},
		Online:   map[int]int{1: 1},
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if body["report_id"] != "rid-1" {
		t.Fatalf("report_id = %v, want rid-1", body["report_id"])
	}
	if _, ok := body["online"]; !ok {
		t.Fatal("expected online to be present even for a non-empty map")
	}
	if _, ok := body["node_id"]; !ok {
		t.Fatal("expected node_id auth field")
	}
}

func TestClientEndpointUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(config.PanelConfig{URL: srv.URL, Token: "token", MachineID: 1}).ForNode(7)
	_, err := c.GetConfig(context.Background())
	if err == nil || !IsEndpointUnsupported(err) {
		t.Fatalf("expected ErrEndpointUnsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected status in error, got %v", err)
	}
}

func TestClientUsersKnownEmptySurvives304(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"empty-v1"`)
		_, _ = io.WriteString(w, `{"users":[]}`)
	}))
	defer srv.Close()

	c := NewClient(config.PanelConfig{URL: srv.URL, Token: "t", MachineID: 1}).ForNode(1)
	users, err := c.GetUsers(context.Background())
	if err != nil || users == nil || len(users) != 0 {
		t.Fatalf("first GetUsers = %#v, %v", users, err)
	}
	users, err = c.GetUsers(context.Background())
	if err != nil || users == nil || len(users) != 0 {
		t.Fatalf("second GetUsers = %#v, %v (want cached empty slice)", users, err)
	}
	if !c.UsersKnownEmpty() {
		t.Fatal("expected UsersKnownEmpty to remain true after 304")
	}
}
