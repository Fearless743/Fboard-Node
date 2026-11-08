package model

import (
	"testing"

	"github.com/fearless743/fboard-node/internal/config"
)

func TestValidateMasque(t *testing.T) {
	ok := &NodeSpec{Protocol: "masque", MasqueEndpointPrivateKey: "key", MasqueAddresses: []string{"10.99.0.1/24"}}
	if err := ValidateNodeSpec(ok, config.KernelConfig{}); err != nil {
		t.Fatalf("valid masque rejected: %v", err)
	}
	noKey := &NodeSpec{Protocol: "masque", MasqueAddresses: []string{"10.99.0.1/24"}}
	if err := ValidateNodeSpec(noKey, config.KernelConfig{}); err == nil {
		t.Fatal("masque without endpoint key accepted")
	}
	noAddr := &NodeSpec{Protocol: "masque", MasqueEndpointPrivateKey: "key"}
	if err := ValidateNodeSpec(noAddr, config.KernelConfig{}); err == nil {
		t.Fatal("masque without addresses accepted")
	}
	other := &NodeSpec{Protocol: "vmess"}
	if err := ValidateNodeSpec(other, config.KernelConfig{}); err != nil {
		t.Fatalf("non-masque affected: %v", err)
	}
}
