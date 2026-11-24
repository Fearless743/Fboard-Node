package task

import "github.com/fearless743/fboard-node/internal/xraycore/common"

// Close returns a func() that closes v.
func Close(v interface{}) func() error {
	return func() error {
		return common.Close(v)
	}
}
