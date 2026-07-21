package model

import (
	"strings"
	"testing"

	"github.com/fearless743/fboard-node/internal/config"
	"github.com/fearless743/fboard-node/internal/panel"
)

func TestNodeSpecFromPanelValidated(t *testing.T) {
	t.Run("valid custom outbounds and route targets", func(t *testing.T) {
		node, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
			Protocol:   "shadowsocks",
			ServerPort: 8388,
			CustomOutbounds: []panel.OutboundConfig{
				{Tag: "warp", Protocol: "wireguard", Settings: map[string]any{"server": "1.1.1.1", "server_port": 2408, "private_key": "pk"}},
				{Tag: "proxy", Protocol: "socks", ProxyTag: "warp", Settings: map[string]any{"server": "2.2.2.2", "server_port": 1080}},
			},
			CustomRouteRules: []panel.CustomRouteRule{{
				Action: panel.RouteAction{Type: "route", Target: "proxy"},
				Match:  panel.RouteMatch{DomainSuffixes: []string{"example.com"}},
			}},
		}, config.KernelConfig{})
		if err != nil {
			t.Fatalf("NodeSpecFromPanelValidated: %v", err)
		}
		if node == nil || len(node.CustomOutbounds) != 2 {
			t.Fatalf("unexpected node spec: %#v", node)
		}
	})

	t.Run("invalid custom outbounds", func(t *testing.T) {
		_, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
			Protocol:   "shadowsocks",
			ServerPort: 8388,
			CustomOutbounds: []panel.OutboundConfig{{
				Tag: "proxy", Protocol: "socks", ProxyTag: "missing", Settings: map[string]any{"server": "2.2.2.2", "server_port": 1080},
			}},
		}, config.KernelConfig{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), `custom_outbounds[0].proxy_tag references unknown outbound "missing"`) {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("reject unknown route target", func(t *testing.T) {
		_, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
			Protocol:   "shadowsocks",
			ServerPort: 8388,
			CustomOutbounds: []panel.OutboundConfig{{
				Tag: "warp", Protocol: "wireguard", Settings: map[string]any{"server": "1.1.1.1", "server_port": 2408, "private_key": "pk"},
			}},
			CustomRouteRules: []panel.CustomRouteRule{{
				Action: panel.RouteAction{Type: "route", Target: "missing"},
				Match:  panel.RouteMatch{DomainSuffixes: []string{"example.com"}},
			}},
		}, config.KernelConfig{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), `custom_route_rules[0].action.target references unknown outbound "missing"`) {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("hysteria listen_ports round-trip and validation", func(t *testing.T) {
		node, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
			Protocol:    "hysteria",
			Version:     2,
			ServerPort:  443,
			ListenPorts: "10000-10100",
		}, config.KernelConfig{})
		if err != nil {
			t.Fatalf("valid listen_ports: %v", err)
		}
		if node.ListenPorts != "10000-10100" {
			t.Fatalf("ListenPorts not mapped: got %q", node.ListenPorts)
		}
		back := node.ToPanel()
		if back == nil || back.ListenPorts != "10000-10100" {
			t.Fatalf("ToPanel ListenPorts: %#v", back)
		}

		_, err = NodeSpecFromPanelValidated(&panel.NodeConfig{
			Protocol:    "hysteria",
			Version:     2,
			ServerPort:  443,
			ListenPorts: "10000-20000", // span 10001 > 1024
		}, config.KernelConfig{})
		if err == nil {
			t.Fatal("expected span error, got nil")
		}
		if !strings.Contains(err.Error(), "exceeds max") {
			t.Fatalf("unexpected error: %v", err)
		}

		_, err = NodeSpecFromPanelValidated(&panel.NodeConfig{
			Protocol:    "hysteria",
			Version:     2,
			ServerPort:  443,
			ListenPorts: "not-a-range",
		}, config.KernelConfig{})
		if err == nil {
			t.Fatal("expected invalid range error, got nil")
		}
	})
}
