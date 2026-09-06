package tools

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	. "agent-platform/internal/contracts"
)

const (
	bashLiveOutputMaxChunkBytes = 16 * 1024
	bashLiveOutputFlushInterval = 50 * time.Millisecond
	bashOutputPipeWaitDelay     = 250 * time.Millisecond
)

type bashOutputCapture struct {
	file *os.File
	live *bashLiveOutputWriter

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	errMu sync.Mutex
	err   error
}

func newBashOutputCapture(ctx context.Context, file *os.File, sink ToolOutputSink, stream string) *bashOutputCapture {
	if file == nil {
		return nil
	}
	capture := &bashOutputCapture{file: file}
	if sink == nil {
		return capture
	}
	capture.live = &bashLiveOutputWriter{
		ctx:    ctx,
		sink:   sink,
		stream: stream,
	}
	capture.stop = make(chan struct{})
	capture.done = make(chan struct{})
	go capture.run()
	return capture
}

func (c *bashOutputCapture) run() {
	defer close(c.done)
	ticker := time.NewTicker(bashLiveOutputFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.live.Flush(false)
		case <-c.stop:
			c.live.Close()
			return
		}
	}
}

func (c *bashOutputCapture) Write(p []byte) (int, error) {
	if c == nil || c.file == nil || len(p) == 0 {
		return len(p), nil
	}
	n, err := c.file.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		c.recordError(err)
		return n, err
	}
	if c.live != nil {
		// The file is the final fact source. A canceled live stream must not
		// corrupt it or turn an otherwise successful command into a failure.
		_ = c.live.Write(p)
	}
	return n, nil
}

func (c *bashOutputCapture) recordError(err error) {
	if c == nil || err == nil {
		return
	}
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

func (c *bashOutputCapture) Err() error {
	if c == nil {
		return nil
	}
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *bashOutputCapture) Close() {
	if c == nil || c.live == nil {
		return
	}
	c.stopOnce.Do(func() { close(c.stop) })
	<-c.done
}

type bashLiveOutputWriter struct {
	ctx    context.Context
	sink   ToolOutputSink
	stream string

	mu      sync.Mutex
	pending []byte
	closed  bool
}

func (w *bashLiveOutputWriter) Write(p []byte) error {
	if w == nil || len(p) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.pending = append(w.pending, p...)
	for len(w.pending) >= bashLiveOutputMaxChunkBytes {
		if err := w.flushOneLocked(false); err != nil {
			return err
		}
	}
	return nil
}

func (w *bashLiveOutputWriter) Flush(final bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	for len(w.pending) > 0 {
		before := len(w.pending)
		if err := w.flushOneLocked(final); err != nil || len(w.pending) == before {
			return
		}
	}
}

func (w *bashLiveOutputWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	for len(w.pending) > 0 {
		if err := w.flushOneLocked(true); err != nil {
			return
		}
	}
}
func (w *bashLiveOutputWriter) flushOneLocked(final bool) error {
	delta, consumed := encodeBashLiveOutputPrefix(w.pending, final, bashLiveOutputMaxChunkBytes)
	if consumed == 0 {
		return nil
	}
	w.pending = w.pending[consumed:]
	if delta == "" {
		return nil
	}
	return w.sink.EmitToolOutput(w.ctx, ToolOutput{Stream: w.stream, Delta: delta})
}

func encodeBashLiveOutputPrefix(input []byte, final bool, maxBytes int) (string, int) {
	if len(input) == 0 || maxBytes <= 0 {
		return "", 0
	}
	var out strings.Builder
	consumed := 0
	for consumed < len(input) {
		remainder := input[consumed:]
		r, size := utf8.DecodeRune(remainder)
		piece := remainder[:size]
		if r == utf8.RuneError && size == 1 {
			if !utf8.FullRune(remainder) && !final {
				break
			}
			piece = []byte("\uFFFD")
		}
		if out.Len()+len(piece) > maxBytes && out.Len() > 0 {
			break
		}
		out.Write(piece)
		consumed += size
		if out.Len() >= maxBytes {
			break
		}
	}
	return out.String(), consumed
}
