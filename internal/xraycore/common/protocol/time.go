package protocol

import (
	"time"

	"github.com/fearless743/fboard-node/internal/xraycore/common/dice"
)

type Timestamp int64

type TimestampGenerator func() Timestamp

func NowTime() Timestamp {
	return Timestamp(time.Now().Unix())
}

func NewTimestampGenerator(base Timestamp, delta int) TimestampGenerator {
	return func() Timestamp {
		rangeInDelta := dice.Roll(delta*2) - delta
		return base + Timestamp(rangeInDelta)
	}
}
