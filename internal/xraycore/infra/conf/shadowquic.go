package conf

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
	"github.com/fearless743/fboard-node/internal/xraycore/common/serial"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/shadowquic"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/shadowquic/account"
	"google.golang.org/protobuf/proto"
)

type ShadowQUICUserConfig struct {
	Name     string `json:"username"`
	Password string `json:"password"`
	Level    byte   `json:"level"`
	Email    string `json:"email"`
}

type ShadowQUICServerConfig struct {
	Users             []*ShadowQUICUserConfig `json:"clients"`
	CongestionControl string                  `json:"congestion_control"`
}

func (c *ShadowQUICServerConfig) Build() (proto.Message, error) {
	config := &shadowquic.ServerConfig{
		CongestionControl: c.CongestionControl,
	}
	for _, user := range c.Users {
		name := user.Name
		if name == "" {
			name = user.Password // Fboard: password field carries UUID as username
		}
		password := user.Password
		if password == "" {
			password = name
		}
		config.Users = append(config.Users, &protocol.User{
			Level: uint32(user.Level),
			Email: user.Email,
			Account: serial.ToTypedMessage(&account.Account{
				Name:     name,
				Password: password,
			}),
		})
	}
	return config, nil
}
