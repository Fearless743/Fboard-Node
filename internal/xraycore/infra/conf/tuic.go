package conf

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
	"github.com/fearless743/fboard-node/internal/xraycore/common/serial"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/tuic"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/tuic/account"
	"google.golang.org/protobuf/proto"
)

type TUICUserConfig struct {
	UUID     string `json:"uuid"`
	Password string `json:"password"`
	Level    byte   `json:"level"`
	Email    string `json:"email"`
}

type TUICServerConfig struct {
	Users             []*TUICUserConfig `json:"clients"`
	CongestionControl string            `json:"congestion_control"`
}

func (c *TUICServerConfig) Build() (proto.Message, error) {
	config := &tuic.ServerConfig{
		CongestionControl: c.CongestionControl,
	}
	for _, user := range c.Users {
		uuid := user.UUID
		if uuid == "" {
			uuid = user.Password // Fboard: password field carries UUID
		}
		password := user.Password
		if password == "" {
			password = uuid
		}
		config.Users = append(config.Users, &protocol.User{
			Level: uint32(user.Level),
			Email: user.Email,
			Account: serial.ToTypedMessage(&account.Account{
				Uuid:     uuid,
				Password: password,
			}),
		})
	}
	return config, nil
}
