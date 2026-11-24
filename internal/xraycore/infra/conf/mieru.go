package conf

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
	"github.com/fearless743/fboard-node/internal/xraycore/common/serial"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/mieru"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/mieru/account"
	"google.golang.org/protobuf/proto"
)

type MieruUserConfig struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Level    byte   `json:"level"`
	Email    string `json:"email"`
}

type MieruServerConfig struct {
	Users          []*MieruUserConfig `json:"clients"`
	Transport      string             `json:"transport"`
	TrafficPattern string             `json:"traffic_pattern"`
}

func (c *MieruServerConfig) Build() (proto.Message, error) {
	config := &mieru.ServerConfig{
		Transport:      c.Transport,
		TrafficPattern: c.TrafficPattern,
	}
	for _, user := range c.Users {
		name := user.Name
		pass := user.Password
		if name == "" {
			name = pass
		}
		if pass == "" {
			pass = name
		}
		config.Users = append(config.Users, &protocol.User{
			Level: uint32(user.Level),
			Email: user.Email,
			Account: serial.ToTypedMessage(&account.Account{
				Name:     name,
				Password: pass,
			}),
		})
	}
	return config, nil
}
