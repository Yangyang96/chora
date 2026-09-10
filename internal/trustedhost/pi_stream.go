package trustedhost

import (
	"bytes"
	"encoding/json"
	"io"
)

// Pi sends transient tool and model streaming progress. Drop only complete, valid transient frames
// before persistence; all authority-bearing and malformed bytes remain exact.
// Oversized records pass through rather than allocating an unbounded buffer.
const piProgressRecordLimit = 1 << 20

type piProgressWriter struct {
	target      io.Writer
	pending     []byte
	passthrough bool
}

func (w *piProgressWriter) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 {
		n := bytes.IndexByte(data, '\n')
		complete := n >= 0
		if !complete {
			n = len(data)
		} else {
			n++
		}
		part := data[:n]
		data = data[n:]
		if w.passthrough || len(w.pending)+len(part) > piProgressRecordLimit {
			if err := w.Flush(); err != nil {
				return 0, err
			}
			if _, err := w.target.Write(part); err != nil {
				return 0, err
			}
			w.passthrough = !complete
			continue
		}
		w.pending = append(w.pending, part...)
		if complete {
			var frame map[string]json.RawMessage
			var frameType string
			if !bytes.ContainsRune(w.pending, '\r') && json.Unmarshal(w.pending, &frame) == nil && json.Unmarshal(frame["type"], &frameType) == nil && (frameType == "tool_execution_update" || frameType == "message_update") {
				w.pending = w.pending[:0]
			} else if err := w.Flush(); err != nil {
				return 0, err
			}
		}
	}
	return size, nil
}

func (w *piProgressWriter) Flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	_, err := w.target.Write(w.pending)
	w.pending = w.pending[:0]
	return err
}
