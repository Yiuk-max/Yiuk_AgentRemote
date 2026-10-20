package server

import (
	"bytes"
	"io"
	"sync"
)

// bufferedLogWriter keeps complete slog records in memory and writes them as
// one batch when the threshold is reached. Flush is called during shutdown.
type bufferedLogWriter struct {
	mu        sync.Mutex
	dst       io.Writer
	buf       bytes.Buffer
	threshold int
}

func (w *bufferedLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if err != nil {
		return n, err
	}
	if w.buf.Len() >= w.threshold {
		if err := w.flushLocked(); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (w *bufferedLogWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *bufferedLogWriter) flushLocked() error {
	for w.buf.Len() > 0 {
		chunk := w.buf.Bytes()
		n, err := w.dst.Write(chunk)
		if n > 0 {
			w.buf.Next(n)
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
