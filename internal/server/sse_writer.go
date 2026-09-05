package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/stream"
)

type sseWriterOptions struct {
	SSE            config.SSEConfig
	Render         stream.RenderConfig
	LoggingEnabled bool
}

type sseWriter struct {
	responseWriter http.ResponseWriter
	flusher        http.Flusher
	opts           sseWriterOptions

	mu            sync.Mutex
	writeMu       sync.Mutex
	pending       []sseFrame
	bufferedChars int
	timer         *time.Timer
	heartbeatStop chan struct{}
	closed        bool
}

type sseFrame struct {
	raw       string
	eventType string
	runID     string
	chatID    string
	heartbeat bool
	terminal  bool
	length    int
}

func newSSEWriter(w http.ResponseWriter, opts sseWriterOptions) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	return &sseWriter{
		responseWriter: w,
		flusher:        flusher,
		opts:           opts,
		heartbeatStop:  make(chan struct{}),
	}, nil
}

func (w *sseWriter) StartHeartbeat() {
	if w.opts.SSE.HeartbeatInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Duration(w.opts.SSE.HeartbeatInterval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := w.WriteComment("heartbeat"); err != nil {
					return
				}
			case <-w.heartbeatStop:
				return
			}
		}
	}()
}

func (w *sseWriter) Close() error {
	w.stopHeartbeat()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	toWrite := w.drainPendingLocked()
	w.stopTimerLocked()
	w.mu.Unlock()
	if len(toWrite) > 0 {
		if err := w.writeFrames(toWrite); err != nil {
			return err
		}
	}
	// A timer or heartbeat may already have passed the state lock. Wait until
	// its underlying ResponseWriter call completes before Close returns.
	w.writeMu.Lock()
	w.writeMu.Unlock()
	return nil
}

func (w *sseWriter) WriteJSON(eventName string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sseFrame := sseFrame{
		raw:       fmt.Sprintf("event: %s\ndata: %s\n\n", eventName, data),
		eventType: eventTypeFromPayload(payload),
		runID:     stringField(payload, "runId"),
		chatID:    stringField(payload, "chatId"),
		terminal:  isTerminalEvent(payload),
		length:    len(data),
	}
	return w.writeFrame(sseFrame)
}

func (w *sseWriter) WriteComment(comment string) error {
	sseFrame := sseFrame{
		raw:       fmt.Sprintf(": %s\n\n", comment),
		eventType: "heartbeat",
		heartbeat: true,
		length:    len(comment),
	}
	return w.writeFrame(sseFrame)
}

func (w *sseWriter) WriteDone() error {
	return w.writeFrame(sseFrame{
		raw:       fmt.Sprintf("event: message\ndata: %s\n\n", stream.DoneSentinel),
		eventType: stream.DoneSentinel,
		terminal:  true,
		length:    len(stream.DoneSentinel),
	})
}

func (w *sseWriter) writeFrame(next sseFrame) error {
	var toWrite []sseFrame

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return fmt.Errorf("sse writer closed")
	}

	if !w.bufferingEnabled() || next.terminal || (next.heartbeat && w.opts.Render.HeartbeatPassThrough) {
		toWrite = w.drainPendingLocked()
		w.stopTimerLocked()
		w.mu.Unlock()
		if len(toWrite) > 0 {
			if err := w.writeFrames(toWrite); err != nil {
				return err
			}
		}
		return w.writeFrames([]sseFrame{next})
	}

	w.pending = append(w.pending, next)
	w.bufferedChars += next.length
	shouldFlush := w.shouldFlushLocked(next)
	if shouldFlush {
		toWrite = w.drainPendingLocked()
		w.stopTimerLocked()
		w.mu.Unlock()
		return w.writeFrames(toWrite)
	}
	w.scheduleFlushLocked()
	w.mu.Unlock()
	return nil
}

func (w *sseWriter) bufferingEnabled() bool {
	return w.opts.Render.FlushInterval > 0 || w.opts.Render.MaxBufferedChars > 0 || w.opts.Render.MaxBufferedEvents > 0
}

func (w *sseWriter) shouldFlushLocked(latest sseFrame) bool {
	if latest.terminal {
		return true
	}
	if w.opts.Render.MaxBufferedEvents > 0 && len(w.pending) >= w.opts.Render.MaxBufferedEvents {
		return true
	}
	return w.opts.Render.MaxBufferedChars > 0 && w.bufferedChars >= w.opts.Render.MaxBufferedChars
}

func (w *sseWriter) scheduleFlushLocked() {
	if w.opts.Render.FlushInterval <= 0 || w.timer != nil {
		return
	}
	w.timer = time.AfterFunc(time.Duration(w.opts.Render.FlushInterval)*time.Second, func() {
		_ = w.flushPending()
	})
}

func (w *sseWriter) flushPending() error {
	w.mu.Lock()
	toWrite := w.drainPendingLocked()
	w.stopTimerLocked()
	w.mu.Unlock()
	if len(toWrite) == 0 {
		return nil
	}
	return w.writeFrames(toWrite)
}

func (w *sseWriter) drainPendingLocked() []sseFrame {
	if len(w.pending) == 0 {
		return nil
	}
	drained := append([]sseFrame(nil), w.pending...)
	w.pending = nil
	w.bufferedChars = 0
	return drained
}

func (w *sseWriter) stopTimerLocked() {
	if w.timer == nil {
		return
	}
	w.timer.Stop()
	w.timer = nil
}

func (w *sseWriter) stopHeartbeat() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.heartbeatStop == nil {
		return
	}
	close(w.heartbeatStop)
	w.heartbeatStop = nil
}

func (w *sseWriter) writeFrames(frames []sseFrame) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	for _, sseFrame := range frames {
		if _, err := fmt.Fprint(w.responseWriter, sseFrame.raw); err != nil {
			return err
		}
		if w.opts.LoggingEnabled {
			log.Printf(
				"[sse][run:%s][chat:%s] event=%s heartbeat=%t terminal=%t size=%d",
				sseFrame.runID,
				sseFrame.chatID,
				sseFrame.eventType,
				sseFrame.heartbeat,
				sseFrame.terminal,
				sseFrame.length,
			)
		}
	}
	w.flusher.Flush()
	if w.opts.LoggingEnabled {
		log.Printf("[sse] flush events=%d", len(frames))
	}
	return nil
}

func eventTypeFromPayload(payload any) string {
	if value := stringField(payload, "type"); value != "" {
		return value
	}
	return "message"
}

func isTerminalEvent(payload any) bool {
	switch eventTypeFromPayload(payload) {
	case "run.complete", "run.cancel", "run.error":
		return true
	default:
		return false
	}
}

func stringField(payload any, key string) string {
	switch value := payload.(type) {
	case stream.EventData:
		return value.String(key)
	case *stream.EventData:
		if value == nil {
			return ""
		}
		return value.String(key)
	}
	valueMap, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := valueMap[key].(string)
	return value
}
