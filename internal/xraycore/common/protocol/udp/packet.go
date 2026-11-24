package udp

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/buf"
	"github.com/fearless743/fboard-node/internal/xraycore/common/net"
)

// Packet is a UDP packet together with its source and destination address.
type Packet struct {
	Payload *buf.Buffer
	Source  net.Destination
	Target  net.Destination
}
