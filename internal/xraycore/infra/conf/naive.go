package conf

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
	"github.com/fearless743/fboard-node/internal/xraycore/common/serial"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/naive"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/naive/account"
	"google.golang.org/protobuf/proto"
)

type NaiveUserConfig struct {
	User     string `json:"user"`
	Pass     string `json:"pass"`
	Password string `json:"password"`
	Level    byte   `json:"level"`
	Email    string `json:"email"`
}

type NaiveServerConfig struct {
	Users []*NaiveUserConfig `json:"accounts"`
}

func (c *NaiveServerConfig) Build() (proto.Message, error) {
	config := &naive.ServerConfig{}
	for _, user := range c.Users {
		password := user.Pass
		if password == "" {
			password = user.Password
		}
		config.Users = append(config.Users, &protocol.User{
			Level: uint32(user.Level),
			Email: user.Email,
			Account: serial.ToTypedMessage(&account.Account{
				Password: password,
			}),
		})
	}
	return config, nil
}
