package httpupgrade

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common"
	"github.com/fearless743/fboard-node/internal/xraycore/transport/internet"
)

func (c *Config) GetNormalizedPath() string {
	path := c.Path
	if path == "" {
		return "/"
	}
	if path[0] != '/' {
		return "/" + path
	}
	return path
}

func init() {
	common.Must(internet.RegisterProtocolConfigCreator(protocolName, func() interface{} {
		return new(Config)
	}))
}
