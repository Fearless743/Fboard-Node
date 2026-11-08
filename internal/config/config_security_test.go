package config

import (
	"strings"
	"testing"
)

func TestValidatePanelURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "https ok", url: "https://panel.example.com"},
		{name: "https with path ok", url: "https://panel.example.com/fboard/"},
		{name: "http loopback ipv4 ok", url: "http://127.0.0.1:7001"},
		{name: "http localhost ok", url: "http://localhost:7001"},
		{name: "http ipv6 loopback ok", url: "http://[::1]:7001"},
		{name: "http remote rejected", url: "http://panel.example.com", wantErr: "must use https"},
		{name: "http lan ip rejected", url: "http://192.168.1.10:7001", wantErr: "must use https"},
		{name: "ftp rejected", url: "ftp://panel.example.com", wantErr: "must use https"},
		{name: "missing scheme rejected", url: "panel.example.com", wantErr: "invalid panel.url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePanelURL(tc.url)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.wantErr != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidateCertFilePath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		configDir string
		wantErr   string
	}{
		{name: "abs under allow base", path: "/etc/fboard/certs/node.pem", configDir: "/etc/fboard-node", wantErr: ""},
		{name: "abs elsewhere rejected", path: "/etc/passwd", configDir: "/etc/fboard-node", wantErr: "must be under"},
		{name: "abs tmp rejected", path: "/tmp/evil.pem", configDir: "/etc/fboard-node", wantErr: "must be under"},
		{name: "rel inside config dir", path: "certs/node.pem", configDir: "/etc/fboard-node", wantErr: ""},
		{name: "dotdot rejected", path: "../evil.pem", configDir: "/etc/fboard-node", wantErr: ".."},
		{name: "nested dotdot rejected", path: "a/../../evil.pem", configDir: "/etc/fboard-node", wantErr: ".."},
		{name: "abs config dir rejected (must be relative)", path: "/etc/fboard-node/certs/x.pem", configDir: "/etc/fboard-node", wantErr: "must be under"},
		{name: "empty rejected", path: "", configDir: "/etc/fboard-node", wantErr: "empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCertFilePath(tc.path, tc.configDir)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.wantErr != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}
