package extension

import (
	"context"

	"github.com/fearless743/fboard-node/internal/xraycore/features"
	"google.golang.org/protobuf/proto"
)

type Observatory interface {
	features.Feature

	GetObservation(ctx context.Context) (proto.Message, error)
}

type BurstObservatory interface {
	Observatory
	Check(tag []string)
}

func ObservatoryType() interface{} {
	return (*Observatory)(nil)
}
