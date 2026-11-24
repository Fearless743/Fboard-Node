package outbound

import (

	"github.com/fearless743/fboard-node/internal/xraycore/common/net"
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
)

// As a stub command consumer.
func (h *Handler) handleCommand(dest net.Destination, cmd protocol.ResponseCommand) {
	switch cmd.(type) {
	default:
	}
}
