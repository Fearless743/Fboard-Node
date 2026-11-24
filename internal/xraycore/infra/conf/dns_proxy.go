package conf

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/errors"
	"github.com/fearless743/fboard-node/internal/xraycore/common/net"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/dns"
	"google.golang.org/protobuf/proto"
)

type DNSOutboundConfig struct {
	Network    Network  `json:"network"`
	Address    *Address `json:"address"`
	Port       uint16   `json:"port"`
	UserLevel  uint32   `json:"userLevel"`
	NonIPQuery string   `json:"nonIPQuery"`
	BlockTypes []int32  `json:"blockTypes"`
}

func (c *DNSOutboundConfig) Build() (proto.Message, error) {
	config := &dns.Config{
		Server: &net.Endpoint{
			Network: c.Network.Build(),
			Port:    uint32(c.Port),
		},
		UserLevel: c.UserLevel,
	}
	if c.Address != nil {
		config.Server.Address = c.Address.Build()
	}
	switch c.NonIPQuery {
	case "", "reject", "drop", "skip":
	default:
		return nil, errors.New(`unknown "nonIPQuery": `, c.NonIPQuery)
	}
	config.Non_IPQuery = c.NonIPQuery
	config.BlockTypes = c.BlockTypes
	return config, nil
}
