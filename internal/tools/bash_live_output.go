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
)

type bashOutputFollower struct {
	file *os.File
	live *bashLiveOutputWriter

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	offset   int64
}

func newBashOutputFollower(ctx context.Context, file *os.File, sink ToolOutputSink, stream string) *bashOutputFollower {
	if file == nil || sink == nil {
		return nil
	}
	follower := &bashOutputFollower{
		file: file,
		live: &bashLiveOutputWriter{
			ctx:    ctx,
			sink:   sink,
			stream: stream,
		},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go follower.run()
	return follower
}

func (f *bashOutputFollower) run() {
	defer close(f.done)
	ticker := time.NewTicker(bashLiveOutputFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			f.readAvailable(false)
		case <-f.stop:
			f.readAvailable(true)
			f.live.Close()
			return
		}
	}
}

func (f *bashOutputFollower) readAvailable(final bool) {
	if f == nil || f.file == nil || f.live == nil {
		return
	}
	for {
		info, err := f.file.Stat()
		if err != nil || info.Size() <= f.offset {
			break
		}
		remaining := info.Size() - f.offset
		readSize := int64(bashLiveOutputMaxChunkBytes)
		if remaining < readSize {
			readSize = remaining
		}
		buffer := make([]byte, int(readSize))
		n, readErr := f.file.ReadAt(buffer, f.offset)
		if n > 0 {
			f.offset += int64(n)
			_ = f.live.Write(buffer[:n])
		}
		if readErr != nil && readErr != io.EOF {
			break
		}
		if n == 0 {
			break
		}
	}
	f.live.Flush(final)
}

func (f *bashOutputFollower) Close() {
	if f == nil {
		return
	}
	f.stopOnce.Do(func() { close(f.stop) })
	<-f.done
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
