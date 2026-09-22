package trustedhost

import (
	"io"

	"github.com/Yangyang96/chora/internal/pistream"
)

const piProgressRecordLimit = pistream.RecordLimit

type piProgressWriter struct {
	target io.Writer
	writer *pistream.Writer
}

func (w *piProgressWriter) Write(data []byte) (int, error) {
	if w.writer == nil {
		w.writer = &pistream.Writer{Target: w.target}
	}
	return w.writer.Write(data)
}
func (w *piProgressWriter) Flush() error {
	if w.writer == nil {
		return nil
	}
	return w.writer.Flush()
}
