package mieru

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common"
	"github.com/fearless743/fboard-node/internal/xraycore/transport/internet"
)

const protocolName = "mieru"

func init() {
	common.Must(internet.RegisterProtocolConfigCreator(protocolName, func() interface{} {
		return new(Config)
	}))
}
