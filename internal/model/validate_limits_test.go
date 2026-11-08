package model

import (
	"strings"
	"testing"

	"github.com/fearless743/fboard-node/internal/config"
)

func TestValidateNodeSpecCustomLimits(t *testing.T) {
	mkOutbounds := func(n int) []OutboundConfig {
		out := make([]OutboundConfig, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, OutboundConfig{
				Tag:      "ob" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
				Protocol: "socks",
				Settings: map[string]any{"server": "2.2.2.2", "server_port": 1080},
			})
		}
		return out
	}
	mkRoutes := func(n int) []map[string]any {
		out := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{"type": "field", "outboundTag": "block"})
		}
		return out
	}

	if err := ValidateNodeSpec(&NodeSpec{CustomOutbounds: mkOutbounds(20)}, config.KernelConfig{}); err != nil {
		t.Fatalf("20 outbounds should pass: %v", err)
	}
	if err := ValidateNodeSpec(&NodeSpec{CustomOutbounds: mkOutbounds(21)}, config.KernelConfig{}); err == nil {
		t.Fatal("21 outbounds should fail")
	} else if !strings.Contains(err.Error(), "exceeds max 20") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidateNodeSpec(&NodeSpec{CustomRoutes: mkRoutes(200)}, config.KernelConfig{}); err != nil {
		t.Fatalf("200 routes should pass: %v", err)
	}
	if err := ValidateNodeSpec(&NodeSpec{CustomRoutes: mkRoutes(201)}, config.KernelConfig{}); err == nil {
		t.Fatal("201 routes should fail")
	} else if !strings.Contains(err.Error(), "exceeds max 200") {
		t.Fatalf("unexpected error: %v", err)
	}
}
