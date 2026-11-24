//go:build !linux && !freebsd && !darwin
// +build !linux,!freebsd,!darwin

package tcp

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/net"
	"github.com/fearless743/fboard-node/internal/xraycore/transport/internet/stat"
)

func GetOriginalDestination(conn stat.Connection) (net.Destination, error) {
	return net.Destination{}, nil
}
