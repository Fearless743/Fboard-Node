package protocol // import "github.com/fearless743/fboard-node/internal/xraycore/common/protocol"

import (
	"errors"
)

var ErrProtoNeedMoreData = errors.New("protocol matches, but need more data to complete sniffing")
