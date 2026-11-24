package snell

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
	"google.golang.org/protobuf/proto"
)

// MemoryAccount carries the per-user Snell client id (user key).
type MemoryAccount struct {
	UserKey string
}

func (u *Account) AsAccount() (protocol.Account, error) {
	return &MemoryAccount{UserKey: u.GetUserKey()}, nil
}

func (a *MemoryAccount) Equals(another protocol.Account) bool {
	if account, ok := another.(*MemoryAccount); ok {
		return a.UserKey == account.UserKey
	}
	return false
}

func (a *MemoryAccount) ToProto() proto.Message {
	return &Account{UserKey: a.UserKey}
}
