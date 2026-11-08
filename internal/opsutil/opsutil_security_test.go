package opsutil

import (
	"strings"
	"testing"
)

func TestValidateUpgradeVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantErr string
	}{
		{name: "tag ok", version: "v1.2.3"},
		{name: "latest ok", version: "latest"},
		{name: "empty rejected", version: "", wantErr: "empty"},
		{name: "too long rejected", version: strings.Repeat("a", 65), wantErr: "exceeds"},
		{name: "semicolon rejected", version: "v1.0;rm -rf /", wantErr: "illegal"},
		{name: "slash rejected", version: "../evil", wantErr: "illegal"},
		{name: "space rejected", version: "v1 2", wantErr: "illegal"},
		{name: "dollar rejected", version: "$(id)", wantErr: "illegal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUpgradeVersion(tc.version)
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

func TestNormalizeChecksum(t *testing.T) {
	valid := strings.Repeat("ab12", 16) // 64 hex chars
	got, err := NormalizeChecksum(valid)
	if err != nil || got != valid {
		t.Fatalf("expected %q, got %q err %v", valid, got, err)
	}
	got, err = NormalizeChecksum("")
	if err != nil || got != "" {
		t.Fatalf("expected empty, got %q err %v", got, err)
	}
	for _, bad := range []string{"xyz", strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		if _, err := NormalizeChecksum(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}
