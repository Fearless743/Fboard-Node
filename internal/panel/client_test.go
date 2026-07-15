package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fearless743/fboard-node/internal/config"
)

func newTestServer(handler http.HandlerFunc) (*httptest.Server, *Client) {
	ts := httptest.NewServer(handler)
	client := NewClient(config.PanelConfig{
		URL:       ts.URL,
		Token:     "test-token",
		NodeID:    1,
		MachineID: 9,
	})
	return ts, client
}

func TestGetConfig_Success(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/config" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("token") != "test-token" {
			t.Errorf("missing token in query")
		}
		if r.URL.Query().Get("machine_id") != "9" {
			t.Errorf("missing machine_id in query")
		}
		if r.URL.Query().Get("node_id") != "1" {
			t.Errorf("missing node_id in query")
		}
		w.Header().Set("ETag", `"etag-1"`)
		json.NewEncoder(w).Encode(NodeConfig{
			Protocol:   "shadowsocks",
			ServerPort: 111,
			Cipher:     "aes-128-gcm",
		})
	})
	defer ts.Close()

	cfg, err := client.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Protocol != "shadowsocks" {
		t.Errorf("protocol: got %q", cfg.Protocol)
	}
	if cfg.ServerPort != 111 {
		t.Errorf("server_port: got %d", cfg.ServerPort)
	}
}

func TestGetConfig_NotModified(t *testing.T) {
	callCount := 0
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("ETag", `"etag-1"`)
			json.NewEncoder(w).Encode(NodeConfig{Protocol: "shadowsocks"})
			return
		}
		if r.Header.Get("If-None-Match") != `"etag-1"` {
			t.Errorf("expected If-None-Match header, got %q", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	})
	defer ts.Close()

	cfg, err := client.GetConfig()
	if err != nil || cfg == nil {
		t.Fatalf("first GetConfig: err=%v cfg=%v", err, cfg)
	}

	cfg, err = client.GetConfig()
	if err != nil {
		t.Fatalf("second GetConfig: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for 304")
	}
}

func TestGetConfig_ServerError(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})
	defer ts.Close()

	_, err := client.GetConfig()
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestGetUsers_Success(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/user" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(UsersResponse{Users: []User{{ID: 1, UUID: "u1"}}})
	})
	defer ts.Close()

	users, err := client.GetUsers()
	if err != nil {
		t.Fatalf("GetUsers: %v", err)
	}
	if len(users) != 1 || users[0].UUID != "u1" {
		t.Fatalf("users: %+v", users)
	}
}

func TestGetUsers_NotModified(t *testing.T) {
	callCount := 0
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("ETag", `"u-1"`)
			json.NewEncoder(w).Encode(UsersResponse{Users: []User{{ID: 1, UUID: "u1"}}})
			return
		}
		w.WriteHeader(http.StatusNotModified)
	})
	defer ts.Close()

	if _, err := client.GetUsers(); err != nil {
		t.Fatalf("first GetUsers: %v", err)
	}
	users, err := client.GetUsers()
	if err != nil {
		t.Fatalf("second GetUsers: %v", err)
	}
	if users != nil {
		t.Error("expected nil users for 304")
	}
}

func TestPushTraffic_Success(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/push" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["token"] != "test-token" {
			t.Errorf("token: %v", body["token"])
		}
		if body["machine_id"] != float64(9) {
			t.Errorf("machine_id: %v", body["machine_id"])
		}
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()

	if err := client.PushTraffic(map[int][2]int64{1: {10, 20}}); err != nil {
		t.Fatalf("PushTraffic: %v", err)
	}
}

func TestPushTraffic_Empty(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not call server for empty traffic")
	})
	defer ts.Close()
	if err := client.PushTraffic(nil); err != nil {
		t.Fatalf("PushTraffic empty: %v", err)
	}
}

func TestPushAlive_Success(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/alive" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	if err := client.PushAlive(map[int][]string{1: {"1.1.1.1"}}); err != nil {
		t.Fatalf("PushAlive: %v", err)
	}
}

func TestPushStatus_Success(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	if err := client.PushStatus(1.0, [2]uint64{100, 50}, [2]uint64{0, 0}, [2]uint64{1000, 100}); err != nil {
		t.Fatalf("PushStatus: %v", err)
	}
}

func TestResetETags(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"e1"`)
		json.NewEncoder(w).Encode(NodeConfig{Protocol: "vless"})
	})
	defer ts.Close()
	if _, err := client.GetConfig(); err != nil {
		t.Fatal(err)
	}
	if client.configETag == "" {
		t.Fatal("expected etag")
	}
	client.ResetETags()
	if client.configETag != "" || client.userETag != "" {
		t.Fatal("etags should be cleared")
	}
}

func TestPushTraffic_ServerError(t *testing.T) {
	ts, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer ts.Close()
	if err := client.PushTraffic(map[int][2]int64{1: {1, 2}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestStringOrArrayDecodeHook(t *testing.T) {
	raw := map[string]interface{}{
		"protocol":       "vless",
		"padding_scheme": []interface{}{"a", "b"},
	}
	var cfg NodeConfig
	if err := decodeWeakRaw(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if string(cfg.PaddingScheme) != "a\nb" {
		t.Fatalf("padding_scheme: %q", cfg.PaddingScheme)
	}
}

func TestStringOrArrayDecodeHook_String(t *testing.T) {
	raw := map[string]interface{}{
		"protocol":       "vless",
		"padding_scheme": "plain",
	}
	var cfg NodeConfig
	if err := decodeWeakRaw(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if string(cfg.PaddingScheme) != "plain" {
		t.Fatalf("padding_scheme: %q", cfg.PaddingScheme)
	}
}
