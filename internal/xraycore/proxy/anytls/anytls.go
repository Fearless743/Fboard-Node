package anytls

import (
	"context"

	"github.com/fearless743/fboard-node/internal/xraycore/common"
)

const protocolName = "anytls"

func init() {
	common.Must(common.RegisterConfig((*ServerConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewServer(ctx, config.(*ServerConfig))
	}))
}
