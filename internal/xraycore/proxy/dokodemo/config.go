package dokodemo

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/net"
)

// GetPredefinedAddress returns the defined address from proto config. Null if address is not valid.
func (v *Config) GetPredefinedAddress() net.Address {
	addr := v.Address.AsAddress()
	if addr == nil {
		return nil
	}
	return addr
}
