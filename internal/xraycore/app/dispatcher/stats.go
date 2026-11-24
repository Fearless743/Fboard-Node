package dispatcher

import (
	"github.com/fearless743/fboard-node/internal/xraycore/common"
	"github.com/fearless743/fboard-node/internal/xraycore/common/buf"
	"github.com/fearless743/fboard-node/internal/xraycore/features/stats"
)

type SizeStatWriter struct {
	Counter stats.Counter
	Writer  buf.Writer
}

func (w *SizeStatWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	w.Counter.Add(int64(mb.Len()))
	return w.Writer.WriteMultiBuffer(mb)
}

func (w *SizeStatWriter) Close() error {
	return common.Close(w.Writer)
}

func (w *SizeStatWriter) Interrupt() {
	common.Interrupt(w.Writer)
}
