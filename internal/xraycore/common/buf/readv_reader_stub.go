//go:build wasm || openbsd
// +build wasm openbsd

package buf

import (
	"io"
	"syscall"

	"github.com/fearless743/fboard-node/internal/xraycore/features/stats"
)

const useReadv = false

func NewReadVReader(reader io.Reader, rawConn syscall.RawConn, counter stats.Counter) Reader {
	panic("not implemented")
}
