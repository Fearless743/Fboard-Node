package model

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fearless743/fboard-node/internal/config"
)

// maxHysteriaListenPorts caps how many UDP sockets Hy2 multi-port listen may open.
// Matches the panel-side cap in ServerService::hysteriaListenPorts.
const maxHysteriaListenPorts = 1024

// ValidateNodeSpec validates the node spec against the fixed xray kernel capabilities.
func ValidateNodeSpec(n *NodeSpec, kcfg config.KernelConfig) error {
	if n == nil {
		return nil
	}

	if err := validateHysteriaListenPorts(n); err != nil {
		return err
	}

	additionalOutboundSources, err := collectAdditionalOutboundTagSources(kcfg.CustomConfig, kcfg.CustomOutbound)
	if err != nil {
		return fmt.Errorf("collect additional outbound tags: %w", err)
	}
	if err := validateOutboundTagCollisions(n.CustomOutbounds, additionalOutboundSources); err != nil {
		return fmt.Errorf("validate outbound tags: %w", err)
	}
	additionalTags := additionalTagNames(additionalOutboundSources)
	availableTags := buildAvailableOutboundTags(n.CustomOutbounds, additionalTags)
	if err := ValidateCustomOutboundsWithTags(n.CustomOutbounds, additionalTags); err != nil {
		return fmt.Errorf("validate custom outbounds: %w", err)
	}
	if err := ValidateCustomRouteRules(n.CustomRouteRules, availableTags); err != nil {
		return fmt.Errorf("validate custom route rules: %w", err)
	}
	return nil
}

// validateHysteriaListenPorts checks optional Hy2 multi-port listen range.
// Empty is fine (single server_port). Invalid ranges or spans > 1024 fail.
func validateHysteriaListenPorts(n *NodeSpec) error {
	raw := strings.TrimSpace(n.ListenPorts)
	if raw == "" {
		return nil
	}
	if !strings.EqualFold(n.Protocol, "hysteria") {
		// Ignore on non-Hy2; panel only sets this for hysteria.
		return nil
	}
	// Realms uses punched path; multi-port listen is unused (warn via empty apply).
	if strings.TrimSpace(n.Realm) != "" {
		return nil
	}

	parts := strings.Split(raw, "-")
	if len(parts) != 2 {
		return fmt.Errorf("listen_ports must be a range like \"10000-20000\", got %q", raw)
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || start < 1 || start > 65535 || end < 1 || end > 65535 || end < start {
		return fmt.Errorf("listen_ports is invalid: %q", raw)
	}
	span := end - start + 1
	if span > maxHysteriaListenPorts {
		return fmt.Errorf("listen_ports span %d exceeds max %d", span, maxHysteriaListenPorts)
	}
	return nil
}

func buildAvailableOutboundTags(structured []OutboundConfig, rawTags []string) map[string]struct{} {
	available := map[string]struct{}{
		"direct": {},
		"block":  {},
	}
	for _, outbound := range structured {
		tag := strings.ToLower(strings.TrimSpace(outbound.Tag))
		if tag != "" {
			available[tag] = struct{}{}
		}
	}
	for _, tag := range rawTags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" {
			available[tag] = struct{}{}
		}
	}
	return available
}
