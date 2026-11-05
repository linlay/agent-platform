package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/observability"
	"agent-platform/internal/runtime/controlscope"
	rt "agent-platform/internal/runtime/types"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type turn struct {
	number   int
	ctx      context.Context
	cancel   context.CancelFunc
	audio    chan []byte
	phase    string
	awaiting bool
	text     string
	tailSeen bool
	speech   *speechTask
	started  time.Time
	pcmBytes int
	heard    bool
	asrTrace *taskTrace
}

type speechTask struct {
	ctx       context.Context
	cancel    context.CancelFunc
	hint      bool
	started   time.Time
	pcmBytes  int
	textChars int
}

const (
	interruptIdle int32 = iota
	interruptPending
	interruptAccepted
	interruptFailed
)

// ownedRun records only this session's one blocking call and explicit cancel
// intention. Admission, Chat exclusion and HITL remain owned by Runtime.
type ownedRun struct {
	ref            atomic.Pointer[rt.RunRef]
	cancelled      atomic.Bool
	interruptState atomic.Int32
}

type sessionEvent struct {
	kind             string
	turn             *turn
	run              *ownedRun
	speech           *speechTask
	text             string
	stopping         bool
	awaitingID, mode string
	result           rt.QueryResult
	err              error
	pcm              []byte
	ack              chan error
}

type inbound struct {
	kind int
	data []byte
	err  error
}

type session struct {
	service                 *Service
	conn                    *websocket.Conn
	caller                  Caller
	ctx                     context.Context
	cancel                  context.CancelFunc
	chatID, agentKey, voice string
	id                      string
	primary                 bool
	primaryIfMissing        bool
	sampleRate              int
	asr                     *ASRClient
	tts                     *TTSClient
	events                  chan sessionEvent
	current                 *turn
	running                 *ownedRun
	pending                 *turn
	waitTimer               *time.Timer
	waited                  <-chan time.Time
	nextTurn                int
}

func runSession(service *Service, conn *websocket.Conn, caller Caller) {
	ctx, cancel := context.WithCancel(service.ctx)
	defer cancel()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if caller.Identity != nil && caller.Identity.ExpiresAt != "" {
		if expiry, err := time.Parse(time.RFC3339, caller.Identity.ExpiresAt); err == nil {
			timer := time.AfterFunc(max(time.Until(expiry), 0), cancel)
			defer timer.Stop()
		}
	}
	s := &session{service: service, conn: conn, caller: caller, ctx: ctx, cancel: cancel, events: make(chan sessionEvent, 16), id: uuid.NewString()}
	conn.SetReadLimit(16 << 10)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	kind, data, err := conn.ReadMessage()
	if err != nil {
		return
	}
	hello, err := decodeMessage(data)
	if kind != websocket.TextMessage || err != nil {
		_ = s.fail("invalid_request", "请先发送语音 hello")
		return
	}
	s.chatID, s.primary, s.voice, err = service.resolveHello(hello)
	if err != nil {
		_ = s.fail("invalid_request", err.Error())
		return
	}
	s.agentKey, s.sampleRate = hello.AgentKey, hello.Audio.Output.SampleRate
	s.primaryIfMissing = s.primary && !hello.NewChat
	am, ap, tm, tp, err := service.speechModels()
	if err != nil {
		_ = s.fail("voice_unavailable", "语音模型不可用")
		return
	}
	s.asr, err = NewASRClient(am, ap, service.deps.Config.MaxSentenceSilenceMS)
	if err != nil {
		_ = s.fail("voice_unavailable", "语音识别配置不可用")
		return
	}
	defer s.asr.Close()
	s.tts, err = NewTTSClient(tm, tp)
	if err != nil {
		_ = s.fail("voice_unavailable", "语音合成配置不可用")
		return
	}
	defer s.tts.Close()
	if s.write(map[string]any{"type": "hello", "version": 1, "sessionId": s.id, "agentKey": s.agentKey, "chatId": s.chatID, "audio": map[string]any{"output": audioFormat{Codec: "pcm_s16le", SampleRate: s.sampleRate, Channels: 1, FrameMS: 20}}}) != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	in := make(chan inbound)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			kind, data, err := conn.ReadMessage()
			select {
			case in <- inbound{kind, data, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { cancel(); _ = conn.Close(); s.invalidate(); s.clearPending(); <-readerDone }()
	for {
		select {
		case <-ctx.Done():
			return
		case input := <-in:
			if input.err != nil {
				return
			}
			if input.kind == websocket.BinaryMessage {
				err = s.audio(input.data)
			} else if input.kind == websocket.TextMessage {
				var msg clientMessage
				msg, err = decodeMessage(input.data)
				if err == nil {
					err = s.control(msg)
				} else {
					err = s.fail("invalid_request", "语音消息格式错误")
				}
			} else {
				err = s.fail("invalid_request", "语音消息类型错误")
			}
			if err != nil {
				return
			}
		case e := <-s.events:
			if s.event(e) != nil {
				return
			}
		case <-s.waited:
			s.clearPending()
			if s.current != nil && s.failTurn(s.current, "run_busy_timeout", "旧任务尚未结束，请稍后重试") != nil {
				return
			}
		}
	}
}

func (s *session) write(value any) error {
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return s.conn.WriteJSON(value)
}

func (s *session) fail(code, msg string) error {
	observability.Log("voice", map[string]any{"sessionId": s.id, "turn": s.nextTurn, "stage": "error", "code": code})
	return s.write(map[string]any{"type": "error", "turn": s.nextTurn, "code": code, "msg": msg})
}

// failTurn ends media only; Runtime retains ownership of any accepted Run.
func (s *session) failTurn(t *turn, code, message string) error {
	t.cancel()
	s.stopListening(t)
	t.phase, t.text, t.awaiting = "done", "", false
	if s.pending == t {
		s.clearPending()
	}
	return s.fail(code, message)
}

func (s *session) post(ctx context.Context, e sessionEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.ctx.Done():
		return false
	case s.events <- e:
		return true
	}
}

func (s *session) stopListening(t *turn) {
	if t.audio != nil {
		close(t.audio)
		t.audio = nil
	}
}

func (s *session) invalidate() {
	if s.current != nil {
		s.current.cancel()
		s.stopListening(s.current)
		s.current = nil
	}
}

func (s *session) clearPending() {
	if s.waitTimer != nil {
		s.waitTimer.Stop()
	}
	s.waitTimer, s.waited, s.pending = nil, nil, nil
}

func (s *session) control(msg clientMessage) error {
	switch msg.Type {
	case "abort":
		number := s.nextTurn
		s.invalidate()
		s.clearPending()
		if s.running != nil {
			s.running.cancelled.Store(true)
			s.interrupt(s.running)
		}
		// This event loop is the sole writer. Any prior in-flight write has
		// completed or failed before this confirmation can cross the boundary.
		return s.write(map[string]any{"type": "abort", "turn": number})
	case "listen":
		if msg.State == "stop" {
			if s.current != nil {
				s.stopListening(s.current)
			}
			return nil
		}
		if msg.State != "start" || (msg.Mode != "auto" && msg.Mode != "manual") {
			return s.fail("invalid_request", "listen 参数无效")
		}
		if s.current != nil && s.current.phase == "listening" {
			return nil // A repeated start while still listening is idempotent.
		}
		if s.current != nil && s.current.phase != "done" {
			// Silently dropping it would leave the client counting a turn the
			// server never opened.
			return s.fail("turn_busy", "当前轮尚未结束，请等待播放结束或先打断")
		}
		s.invalidate()
		s.nextTurn++
		ctx, cancel := context.WithCancel(s.ctx)
		t := &turn{number: s.nextTurn, ctx: ctx, cancel: cancel, audio: make(chan []byte, maxStartupPCM/640), phase: "listening", started: time.Now(), asrTrace: &taskTrace{}}
		s.current = t
		if s.running != nil && s.running.interruptState.Load() == interruptFailed {
			return s.reportInterruptFailure()
		}
		input := t.audio
		s.service.start(func() {
			text, err := s.asr.Recognize(withTaskTrace(ctx, t.asrTrace), input, msg.Mode, func(text string, stopping bool) {
				ack := make(chan error, 1)
				if s.post(ctx, sessionEvent{kind: "partial", turn: t, text: text, stopping: stopping, ack: ack}) {
					select {
					case <-ack:
					case <-ctx.Done():
					}
				}
			})
			s.post(ctx, sessionEvent{kind: "recognized", turn: t, text: text, err: err})
		})
		return nil
	default:
		return s.fail("invalid_request", "不支持的语音控制消息")
	}
}

func (s *session) audio(pcm []byte) error {
	t := s.current
	if t == nil || t.audio == nil || t.phase != "listening" {
		return nil
	}
	if t.tailSeen || len(pcm) == 0 || len(pcm) > 640 || len(pcm)%2 != 0 {
		s.invalidate()
		return s.fail("invalid_audio", "需要采样对齐的 16kHz 单声道 PCM")
	}
	t.tailSeen = len(pcm) < 640
	select {
	case t.audio <- pcm:
		t.pcmBytes += len(pcm)
		return nil
	default:
		// The queue covers provider startup, so name the stalled stage.
		observability.Log("voice", map[string]any{"sessionId": s.id, "turn": t.number, "stage": "asr_overflow", "asrPhase": t.asrTrace.phase(), "elapsedMs": time.Since(t.started).Milliseconds(), "audioMs": t.pcmBytes / 32, "queuedMs": len(t.audio) * 20})
		s.invalidate()
		return s.fail("audio_overflow", "语音输入缓冲已满")
	}
}

func (s *session) interrupt(run *ownedRun) {
	ref := run.ref.Load()
	if ref == nil || !run.cancelled.Load() {
		return
	}
	if !run.interruptState.CompareAndSwap(interruptIdle, interruptPending) &&
		!run.interruptState.CompareAndSwap(interruptFailed, interruptPending) {
		return
	}
	work := func() {
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()
		source, reason, detail := contracts.InterruptSourceWSAPI, contracts.InterruptReasonUserCancelled, "voice abort"
		if s.service.ctx.Err() != nil {
			source, reason, detail = contracts.InterruptSourceServerSetup, contracts.InterruptReasonRunInterrupted, "voice shutdown"
		}
		result, err := s.service.deps.Runtime.Interrupt(ctx, rt.InterruptCommand{RunRef: *ref, Source: source, Reason: reason, Detail: detail})
		accepted := err == nil && result.Accepted
		if err == nil && !accepted {
			active, found, lookupErr := s.service.deps.Runs.ActiveRunForChat(ref.ChatID)
			accepted = lookupErr == nil && (!found || active.RunID != ref.RunID)
		}
		if accepted {
			run.interruptState.Store(interruptAccepted)
			return
		}
		run.interruptState.Store(interruptFailed)
		s.post(s.ctx, sessionEvent{kind: "interruptFailed", run: run})
	}
	if !s.service.start(work) {
		work()
	}
}

func (s *session) reportInterruptFailure() error {
	if s.current == nil || s.current.phase == "done" {
		return nil // Retain the failure on ownedRun until the next listen.
	}
	return s.failTurn(s.current, "run_failed", "任务中断失败，请再次点击打断或在聊天界面停止任务")
}

func (s *session) submit(t *turn) error {
	primary := s.primary
	if primary {
		summary, err := s.service.deps.Chats.Summary(s.chatID)
		if err != nil {
			return s.failTurn(t, "run_failed", "聊天状态不可用")
		}
		if summary != nil {
			primary = false
			s.primary = false
		}
	}
	if primary && s.primaryIfMissing {
		current, err := s.service.deps.Chats.PrimaryChatID(s.agentKey)
		if err != nil || current != "" && current != s.chatID {
			return s.failTurn(t, "chat_changed", "主要聊天已变化，请重新连接语音")
		}
	}
	planning := false
	input := map[string]any{"agentKey": s.agentKey, "chatId": s.chatID, "message": t.text, "planningMode": false}
	if primary {
		input["primary"] = true
	}
	original, _ := json.Marshal(input)
	cmd := rt.QueryCommand{AgentKey: s.agentKey, ChatID: s.chatID, Message: t.text, PlanningMode: &planning, Primary: primary, PrimaryIfMissing: primary && s.primaryIfMissing, OriginalQuery: original, Identity: s.caller.Identity, Locale: s.caller.Locale, ResourceBaseURL: s.caller.ResourceBaseURL}
	if s.caller.Identity != nil {
		cmd.Caller = rt.Caller{Subject: s.caller.Identity.Subject, DeviceID: s.caller.Identity.DeviceID, Scope: s.caller.Identity.Scope}
	}
	run := &ownedRun{}
	s.running = run
	t.phase = "running"
	t.text = ""
	runCtx := controlscope.WithContext(s.service.ctx, controlscope.Scope{Transport: "voice", Subject: cmd.Caller.Subject})
	s.service.start(func() {
		shutdownDone := make(chan struct{})
		stopShutdown := context.AfterFunc(s.service.ctx, func() { run.cancelled.Store(true); s.interrupt(run); close(shutdownDone) })
		defer func() {
			if !stopShutdown() {
				<-shutdownDone
			}
		}()
		var observed <-chan struct{}
		observationCtx, stopObservation := context.WithCancel(s.ctx)
		defer stopObservation()
		result, err := s.service.deps.Runtime.ExecuteQueryWithHooks(runCtx, cmd, rt.QueryHooks{OnRunStarted: func(start chat.RunStart) {
			ref := &rt.RunRef{RunID: start.RunID, ChatID: start.ChatID, AgentKey: start.AgentKey, Caller: cmd.Caller}
			run.ref.Store(ref)
			s.interrupt(run)
			if !s.post(s.ctx, sessionEvent{kind: "runStarted", run: run, turn: t}) {
				return
			}
			sub, attachErr := s.service.deps.Runtime.AttachRun(runCtx, *ref, 0)
			if attachErr != nil {
				s.post(s.ctx, sessionEvent{kind: "observationFailed", turn: t, run: run})
				return
			}
			done := make(chan struct{})
			observed = done
			started := s.service.start(func() {
				defer close(done)
				defer sub.Close()
				for {
					select {
					case <-observationCtx.Done():
						return
					case event, ok := <-sub.Events:
						if !ok {
							return
						}
						if event.Type == "awaiting.ask" || event.Type == "awaiting.answer" {
							if !s.post(observationCtx, sessionEvent{kind: event.Type, run: run, turn: t, awaitingID: event.String("awaitingId"), mode: event.String("mode")}) {
								return
							}
						}
					}
				}
			})
			if !started {
				sub.Close()
				close(done)
			}
		}})
		// One deadline bounds both subscription drain and Chat release.
		deadline := time.NewTimer(finishTimeout)
		defer deadline.Stop()
		if observed != nil {
			select {
			case <-observed:
			case <-s.ctx.Done():
				stopObservation()
			case <-deadline.C:
				stopObservation()
				<-observed
				s.post(s.ctx, sessionEvent{kind: "releaseFailed", turn: t, run: run})
				return
			}
		}
		// A cancel acknowledgment alone never releases Chat admission.
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		for {
			active, found, activeErr := s.service.deps.Runs.ActiveRunForChat(s.chatID)
			ref := run.ref.Load()
			if activeErr == nil && (!found || ref == nil || active.RunID != ref.RunID) {
				break
			}
			select {
			case <-s.service.ctx.Done():
				return
			case <-deadline.C:
				s.post(s.ctx, sessionEvent{kind: "releaseFailed", turn: t, run: run})
				return
			case <-poll.C:
			}
		}
		s.post(s.ctx, sessionEvent{kind: "runResult", run: run, turn: t, result: result, err: err})
	})
	return nil
}

func (s *session) event(e sessionEvent) error {
	if e.kind == "interruptFailed" {
		if e.run == s.running && e.run.interruptState.Load() == interruptFailed {
			return s.reportInterruptFailure()
		}
		return nil
	}
	if e.kind == "releaseFailed" {
		if err := s.fail("run_busy_timeout", "任务收尾超时，请重新连接语音"); err != nil {
			return err
		}
		return errors.New("voice Run release timeout")
	}
	if (e.kind == "runStarted" || e.kind == "runResult" && e.result.RunID != "") && e.run == s.running {
		s.primary = false
	}
	valid := e.turn != nil && e.turn == s.current && e.turn.ctx.Err() == nil
	if e.ack != nil {
		defer func() { e.ack <- nil }()
	}
	if e.kind == "runResult" {
		if e.run != s.running {
			return nil
		}
		s.running = nil
		if valid {
			result := e.result
			if result.RunID != "" {
				s.primary = false
			}
			if result.RunID != "" {
				if err := s.write(map[string]any{"type": "run", "turn": e.turn.number, "state": "end", "runId": result.RunID, "chatId": s.chatID, "finishReason": result.FinishReason}); err != nil {
					return err
				}
			}
			if e.err != nil || !result.Persisted || result.Completion == nil || result.FinishReason != "complete" {
				code := "run_failed"
				var request *rt.RequestError
				if errors.As(e.err, &request) {
					switch request.Code {
					case "active_run_conflict", "primary_chat_busy", "awaiting_pending":
						code = "chat_busy"
					case "chat_changed":
						code = "chat_changed"
					}
				}
				if err := s.failTurn(e.turn, code, "当前问题未完成，请查看聊天界面"); err != nil {
					return err
				}
			} else {
				summary, err := s.service.deps.Chats.Summary(s.chatID)
				if err != nil {
					return s.failTurn(e.turn, "run_failed", "无法核对聊天完成状态")
				}
				if summary != nil && summary.PendingAwaiting != nil {
					// A later Run continues this item and this session does not
					// observe it; end the turn instead of waiting silently.
					if err := s.failTurn(e.turn, "awaiting_pending", "请在聊天界面继续处理待确认项"); err != nil {
						return err
					}
				} else if err := s.speak(e.turn, speechText(result.Completion.AssistantText), false); err != nil {
					return err
				}
			}
		}
		if s.pending != nil {
			t := s.pending
			s.clearPending()
			if t == s.current && t.ctx.Err() == nil {
				return s.submit(t)
			}
		}
		return nil
	}
	if !valid {
		return nil
	}
	t := e.turn
	switch e.kind {
	case "observationFailed":
		if err := s.fail("run_failed", "无法观察当前任务，请查看聊天界面"); err != nil {
			return err
		}
		return errors.New("voice Run observation unavailable")
	case "partial":
		t.heard = t.heard || strings.TrimSpace(e.text) != ""
		if e.stopping {
			s.stopListening(t)
		}
		return s.write(map[string]any{"type": "stt", "turn": t.number, "text": e.text, "final": false})
	case "recognized":
		s.stopListening(t)
		// Either 60s bound can win: buffered PCM can hit its byte limit before
		// the wall-clock deadline. Both end a silent turn without a Query.
		atRecordingLimit := errors.Is(e.err, context.DeadlineExceeded) || errors.Is(e.err, errRecordingLimit)
		if atRecordingLimit && !t.heard && t.pcmBytes > 0 {
			e.err = nil
		}
		entry := map[string]any{"sessionId": s.id, "turn": t.number, "stage": "asr_end", "elapsedMs": time.Since(t.started).Milliseconds(), "audioMs": t.pcmBytes / 32, "heard": t.heard, "failed": e.err != nil, "asrPhase": t.asrTrace.phase()}
		if started := t.asrTrace.started.Load(); started != 0 {
			entry["startMs"] = time.Unix(0, started).Sub(t.started).Milliseconds()
		}
		if e.err != nil {
			entry["error"] = e.err.Error()
		}
		observability.Log("voice", entry)
		if e.err != nil {
			code := "asr_failed"
			for text, stable := range map[string]string{"recording limit": "recording_limit", "transcript limit": "transcript_limit", "startup audio": "audio_overflow"} {
				if strings.Contains(e.err.Error(), text) {
					code = stable
				}
			}
			return s.failTurn(t, code, "语音识别未完成，请重试")
		}
		if strings.TrimSpace(e.text) == "" {
			t.phase = "done"
			return s.write(map[string]any{"type": "stt", "turn": t.number, "text": "", "final": true})
		}
		t.text = e.text
		t.phase = "running"
		if err := s.write(map[string]any{"type": "stt", "turn": t.number, "text": e.text, "final": true}); err != nil {
			return err
		}
		if s.running != nil {
			if !s.running.cancelled.Load() {
				return s.failTurn(t, "chat_busy", "当前聊天已有运行中的任务")
			}
			s.pending = t
			s.waitTimer = time.NewTimer(finishTimeout)
			s.waited = s.waitTimer.C
			return nil
		}
		return s.submit(t)
	case "runStarted":
		ref := e.run.ref.Load()
		if ref != nil {
			s.primary = false
			return s.write(map[string]any{"type": "run", "turn": t.number, "state": "start", "runId": ref.RunID, "chatId": ref.ChatID})
		}
	case "awaiting.ask":
		ref := e.run.ref.Load()
		if ref == nil {
			return nil
		}
		wasAwaiting := t.awaiting
		t.awaiting = true
		t.phase = "awaiting"
		if err := s.write(map[string]any{"type": "awaiting", "turn": t.number, "runId": ref.RunID, "awaitingId": e.awaitingID, "mode": e.mode}); err != nil {
			return err
		}
		if !wasAwaiting {
			return s.speak(t, "需要你在界面上确认后继续。", true)
		}
	case "awaiting.answer":
		t.awaiting = false
	case "pcm":
		if t.speech != e.speech || e.speech.ctx.Err() != nil {
			return nil
		}
		_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		e.speech.pcmBytes += len(e.pcm)
		return s.conn.WriteMessage(websocket.BinaryMessage, e.pcm)
	case "spoken":
		if t.speech != e.speech || e.speech.ctx.Err() != nil {
			return nil
		}
		reason := "done"
		if e.err != nil {
			reason = "error"
		}
		entry := map[string]any{"sessionId": s.id, "turn": t.number, "stage": "tts_end", "reason": reason, "elapsedMs": time.Since(e.speech.started).Milliseconds(), "audioMs": e.speech.pcmBytes * 1000 / (s.sampleRate * 2), "textChars": e.speech.textChars}
		if e.err != nil {
			entry["error"] = e.err.Error()
		}
		observability.Log("voice", entry)
		if err := s.write(map[string]any{"type": "tts", "turn": t.number, "state": "stop", "reason": reason}); err != nil {
			return err
		}
		e.speech.cancel()
		if !e.speech.hint {
			t.phase = "done"
		}
		// An error ends the client's turn. The awaiting hint is advisory and its
		// Run is still waiting, so only stop(reason=error) reports it.
		if e.err != nil && !e.speech.hint {
			return s.fail("tts_failed", "语音播放未完成，请查看聊天界面")
		}
	}
	return nil
}
