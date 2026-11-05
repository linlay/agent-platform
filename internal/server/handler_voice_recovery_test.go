package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/chat"
	rt "agent-platform/internal/runtime/types"
	"agent-platform/internal/voice"
)

type voiceFailingSummary struct {
	voice.ChatReader
	fail atomic.Bool
}

func (c *voiceFailingSummary) Summary(id string) (*chat.Summary, error) {
	if c.fail.Swap(false) {
		return nil, errors.New("storage temporarily unavailable")
	}
	return c.ChatReader.Summary(id)
}

type voiceSummaryAfterRun struct {
	voice.Runtime
	chats *voiceFailingSummary
	fail  atomic.Bool
}

func (r *voiceSummaryAfterRun) ExecuteQueryWithHooks(ctx context.Context, cmd rt.QueryCommand, hooks rt.QueryHooks) (rt.QueryResult, error) {
	result, err := r.Runtime.ExecuteQueryWithHooks(ctx, cmd, hooks)
	if r.fail.Swap(false) {
		r.chats.fail.Store(true)
	}
	return result, err
}
func TestVoiceSummaryFailureEndsTurnAndAllowsNextQuery(t *testing.T) {
	for _, afterRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_query", true: "after_run"}[afterRun], func(t *testing.T) {
			f := newTestFixture(t)
			chats := &voiceFailingSummary{ChatReader: f.chats.(voice.ChatReader)}
			runtime := &voiceSummaryAfterRun{Runtime: f.server.deps.Runtime, chats: chats}
			srv, _ := enableVoiceFixtureWithChats(t, f, runtime, chats)
			c, chatID := dialVoice(t, srv, "", 16000)
			if afterRun {
				runtime.fail.Store(true)
			} else {
				chats.fail.Store(true)
			}
			speakVoice(t, c)
			for {
				msg, _ := readVoice(t, c)
				if msg["type"] == "error" {
					if msg["code"] != "run_failed" {
						t.Fatal(msg)
					}
					break
				}
			}
			speakVoice(t, c)
			start, _ := voiceUntil(t, c, "run", "start")
			if start["turn"] != float64(2) {
				t.Fatal(start)
			}
			voiceUntil(t, c, "tts", "stop")
			want := 1
			if afterRun {
				want = 2
			}
			if got := len(queryLines(t, f, chatID)); got != want {
				t.Fatalf("queries=%d want=%d", got, want)
			}
		})
	}
}

type voiceRetryInterruptRuntime struct {
	voice.Runtime
	calls         atomic.Int32
	entered, fail chan struct{}
}

func (r *voiceRetryInterruptRuntime) Interrupt(ctx context.Context, cmd rt.InterruptCommand) (rt.InterruptResult, error) {
	if r.calls.Add(1) == 1 {
		close(r.entered)
		<-r.fail
		return rt.InterruptResult{}, errors.New("proxy temporarily unavailable")
	}
	return r.Runtime.Interrupt(ctx, cmd)
}
func TestVoiceInterruptFailureCanRetry(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "next_listen", true: "pending_transcript"}[pending], func(t *testing.T) {
			f := newTestFixture(t)
			engine := &voiceCancelEngine{first: &gatedQueryEngine{entered: make(chan struct{}), release: make(chan struct{})}}
			defer close(engine.first.release)
			f.server.deps.Agent = engine
			bindTestRuntime(f.server)
			runtime := &voiceRetryInterruptRuntime{Runtime: f.server.deps.Runtime, entered: make(chan struct{}), fail: make(chan struct{})}
			srv, _ := enableVoiceFixture(t, f, runtime)
			var release sync.Once
			defer release.Do(func() { close(runtime.fail) })
			c, chatID := dialVoice(t, srv, "", 16000)
			speakVoice(t, c)
			voiceUntil(t, c, "run", "start")
			_ = c.WriteJSON(map[string]any{"type": "abort"})
			voiceUntil(t, c, "abort", "")
			select {
			case <-runtime.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("interrupt was not attempted")
			}
			if pending {
				speakVoice(t, c)
				for {
					msg, _ := readVoice(t, c)
					if msg["type"] == "stt" && msg["final"] == true {
						break
					}
					if msg["type"] == "error" {
						t.Fatal(msg)
					}
				}
			}
			release.Do(func() { close(runtime.fail) })
			if !pending {
				_ = c.WriteJSON(map[string]any{"type": "listen", "state": "start", "mode": "manual"})
			}
			msg, _ := readVoice(t, c)
			if msg["type"] != "error" || msg["code"] != "run_failed" || msg["turn"] != float64(2) {
				t.Fatal(msg)
			}
			_ = c.WriteJSON(map[string]any{"type": "abort"})
			voiceUntil(t, c, "abort", "")
			speakVoice(t, c)
			start, _ := voiceUntil(t, c, "run", "start")
			if start["turn"] != float64(3) {
				t.Fatal(start)
			}
			voiceUntil(t, c, "tts", "stop")
			if runtime.calls.Load() != 2 || len(queryLines(t, f, chatID)) != 2 {
				t.Fatalf("interrupts=%d queries=%d", runtime.calls.Load(), len(queryLines(t, f, chatID)))
			}
		})
	}
}
