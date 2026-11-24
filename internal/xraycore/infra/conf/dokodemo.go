package conf

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/errors"
	"github.com/fearless743/fboard-node/internal/xraycore/common/net"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy/dokodemo"
	"google.golang.org/protobuf/proto"
)

type DokodemoConfig struct {
	Address        *Address          `json:"address"`
	Port           uint16            `json:"port"`
	PortMap        map[string]string `json:"portMap"`
	Network        *NetworkList      `json:"network"`
	FollowRedirect bool              `json:"followRedirect"`
	UserLevel      uint32            `json:"userLevel"`
}

func (v *DokodemoConfig) Build() (proto.Message, error) {
	config := new(dokodemo.Config)
	if v.Address != nil {
		config.Address = v.Address.Build()
	}
	config.Port = uint32(v.Port)
	config.PortMap = v.PortMap
	for _, v := range config.PortMap {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return nil, errors.New("invalid portMap: ", v).Base(err)
		}
	}
	config.Networks = v.Network.Build()
	config.FollowRedirect = v.FollowRedirect
	config.UserLevel = v.UserLevel
	return config, nil
}
