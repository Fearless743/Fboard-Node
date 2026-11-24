package conf

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/errors"
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
	"github.com/fearless743/fboard-node/internal/xraycore/common/serial"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/hysteria"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/hysteria/account"
	"google.golang.org/protobuf/proto"
)

type HysteriaClientConfig struct {
	Version int32    `json:"version"`
	Address *Address `json:"address"`
	Port    uint16   `json:"port"`
}

func (c *HysteriaClientConfig) Build() (proto.Message, error) {
	if c.Version != 2 {
		return nil, errors.New("version != 2")
	}

	config := &hysteria.ClientConfig{}
	config.Version = c.Version
	config.Server = &protocol.ServerEndpoint{
		Address: c.Address.Build(),
		Port:    uint32(c.Port),
	}

	return config, nil
}

type HysteriaUserConfig struct {
	Auth  string `json:"auth"`
	Level uint32 `json:"level"`
	Email string `json:"email"`
}

type HysteriaServerConfig struct {
	Version int32                 `json:"version"`
	Users   []*HysteriaUserConfig `json:"clients"`
}

func (c *HysteriaServerConfig) Build() (proto.Message, error) {
	config := new(hysteria.ServerConfig)

	if c.Users != nil {
		for _, user := range c.Users {
			account := &account.Account{
				Auth: user.Auth,
			}
			config.Users = append(config.Users, &protocol.User{
				Email:   user.Email,
				Level:   user.Level,
				Account: serial.ToTypedMessage(account),
			})
		}
	}

	return config, nil
}
