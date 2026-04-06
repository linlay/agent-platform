package stream

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"agent-platform-runner-go/internal/config"
)

const DoneSentinel = "[DONE]"

type Options struct {
	SSE            config.SSEConfig
	Render         config.H2ARenderConfig
	LoggingEnabled bool
}

type Writer struct {
	responseWriter http.ResponseWriter
	flusher        http.Flusher
	opts           Options

	mu            sync.Mutex
	pending       []frame
	bufferedChars int
	timer         *time.Timer
	heartbeatStop chan struct{}
	closed        bool
}

type frame struct {
	raw       string
	eventType string
	runID     string
	chatID    string
	heartbeat bool
	terminal  bool
	length    int
}

func NewWriter(w http.ResponseWriter, opts Options) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	return &Writer{
		responseWriter: w,
		flusher:        flusher,
		opts:           opts,
		heartbeatStop:  make(chan struct{}),
	}, nil
}

func (w *Writer) StartHeartbeat() {
	if w.opts.SSE.HeartbeatIntervalMs <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Duration(w.opts.SSE.HeartbeatIntervalMs) * time.Millisecond)
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

func (w *Writer) Close() error {
	w.stopHeartbeat()
	if err := w.flushPending(); err != nil {
		return err
	}
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return nil
}

func (w *Writer) WriteJSON(eventName string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := frame{
		raw:       fmt.Sprintf("event: %s\ndata: %s\n\n", eventName, data),
		eventType: eventTypeFromPayload(payload),
		runID:     stringField(payload, "runId"),
		chatID:    stringField(payload, "chatId"),
		terminal:  isTerminalEvent(payload),
		length:    len(data),
	}
	return w.writeFrame(frame)
}

func (w *Writer) WriteComment(comment string) error {
	frame := frame{
		raw:       fmt.Sprintf(": %s\n\n", comment),
		eventType: "heartbeat",
		heartbeat: true,
		length:    len(comment),
	}
	return w.writeFrame(frame)
}

func (w *Writer) WriteDone() error {
	return w.writeFrame(frame{
		raw:       fmt.Sprintf("event: message\ndata: %s\n\n", DoneSentinel),
		eventType: DoneSentinel,
		terminal:  true,
		length:    len(DoneSentinel),
	})
}

func (w *Writer) writeFrame(next frame) error {
	var toWrite []frame

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
		return w.writeFrames([]frame{next})
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

func (w *Writer) bufferingEnabled() bool {
	return w.opts.Render.FlushIntervalMs > 0 || w.opts.Render.MaxBufferedChars > 0 || w.opts.Render.MaxBufferedEvents > 0
}

func (w *Writer) shouldFlushLocked(latest frame) bool {
	if latest.terminal {
		return true
	}
	if w.opts.Render.MaxBufferedEvents > 0 && len(w.pending) >= w.opts.Render.MaxBufferedEvents {
		return true
	}
	return w.opts.Render.MaxBufferedChars > 0 && w.bufferedChars >= w.opts.Render.MaxBufferedChars
}

func (w *Writer) scheduleFlushLocked() {
	if w.opts.Render.FlushIntervalMs <= 0 || w.timer != nil {
		return
	}
	w.timer = time.AfterFunc(time.Duration(w.opts.Render.FlushIntervalMs)*time.Millisecond, func() {
		_ = w.flushPending()
	})
}

func (w *Writer) flushPending() error {
	w.mu.Lock()
	toWrite := w.drainPendingLocked()
	w.stopTimerLocked()
	w.mu.Unlock()
	if len(toWrite) == 0 {
		return nil
	}
	return w.writeFrames(toWrite)
}

func (w *Writer) drainPendingLocked() []frame {
	if len(w.pending) == 0 {
		return nil
	}
	drained := append([]frame(nil), w.pending...)
	w.pending = nil
	w.bufferedChars = 0
	return drained
}

func (w *Writer) stopTimerLocked() {
	if w.timer == nil {
		return
	}
	w.timer.Stop()
	w.timer = nil
}

func (w *Writer) stopHeartbeat() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.heartbeatStop == nil {
		return
	}
	close(w.heartbeatStop)
	w.heartbeatStop = nil
}

func (w *Writer) writeFrames(frames []frame) error {
	for _, frame := range frames {
		if _, err := fmt.Fprint(w.responseWriter, frame.raw); err != nil {
			return err
		}
		if w.opts.LoggingEnabled {
			log.Printf(
				"[sse][run:%s][chat:%s] event=%s heartbeat=%t terminal=%t size=%d",
				frame.runID,
				frame.chatID,
				frame.eventType,
				frame.heartbeat,
				frame.terminal,
				frame.length,
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
	valueMap, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := valueMap[key].(string)
	return value
}
