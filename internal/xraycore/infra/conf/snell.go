package conf

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
	"github.com/fearless743/fboard-node/internal/xraycore/common/serial"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/snell"
	"google.golang.org/protobuf/proto"
)

type SnellUserConfig struct {
	UserKey string `json:"user_key"`
	Level   byte   `json:"level"`
	Email   string `json:"email"`
}

type SnellServerConfig struct {
	Users    []*SnellUserConfig `json:"clients"`
	Version  int32              `json:"version"`
	PSK      string             `json:"psk"`
	ObfsMode string             `json:"obfs_mode"`
	V6Mode   string             `json:"v6_mode"`
}

func (c *SnellServerConfig) Build() (proto.Message, error) {
	config := &snell.ServerConfig{
		Version:  c.Version,
		Psk:      c.PSK,
		ObfsMode: c.ObfsMode,
		V6Mode:   c.V6Mode,
	}
	for _, user := range c.Users {
		config.Users = append(config.Users, &protocol.User{
			Level:   uint32(user.Level),
			Email:   user.Email,
			Account: serial.ToTypedMessage(&snell.Account{UserKey: user.UserKey}),
		})
	}
	return config, nil
}

type SnellClientConfig struct {
	Server   string   `json:"server"`
	Address  *Address `json:"address"`
	Port     int32    `json:"port"`
	Version  int32    `json:"version"`
	PSK      string   `json:"psk"`
	UserKey  string   `json:"user_key"`
	ObfsMode string   `json:"obfs_mode"`
	ObfsHost string   `json:"obfs_host"`
	V6Mode   string   `json:"v6_mode"`
	Reuse    bool     `json:"reuse"`
}

func (c *SnellClientConfig) Build() (proto.Message, error) {
	server := c.Server
	if server == "" && c.Address != nil && c.Address.Address != nil {
		server = c.Address.String()
	}
	return &snell.ClientConfig{
		Server:   server,
		Port:     c.Port,
		Version:  c.Version,
		Psk:      c.PSK,
		UserKey:  c.UserKey,
		ObfsMode: c.ObfsMode,
		ObfsHost: c.ObfsHost,
		V6Mode:   c.V6Mode,
		Reuse:    c.Reuse,
	}, nil
}
