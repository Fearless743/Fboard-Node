package tagged

import (
	"context"

	"github.com/fearless743/fboard-node/internal/xraycore/common/net"
	"github.com/fearless743/fboard-node/internal/xraycore/features/routing"
)

type DialFunc func(ctx context.Context, dispatcher routing.Dispatcher, dest net.Destination, tag string) (net.Conn, error)

var Dialer DialFunc
