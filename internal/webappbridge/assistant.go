package webappbridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/controlscope"
	rt "agent-platform/internal/runtime/types"
	"agent-platform/internal/webapp"
	"github.com/google/uuid"
)

type Runtime interface {
	StartQuery(context.Context, rt.QueryCommand) (rt.RunHandle, error)
	Execute(context.Context, rt.ExecuteCommand) (rt.ExecuteResult, error)
	AttachRun(context.Context, rt.RunRef, int64) (*rt.Subscription, error)
	RunStatus(context.Context, string) (contracts.RunSnapshot, error)
	Interrupt(context.Context, rt.InterruptCommand) (rt.InterruptResult, error)
}
type AdapterOptions struct {
	Apps        *webapp.Service
	Runtime     Runtime
	Registry    ResourceCatalog
	Chats       ChatSummaries
	Artifacts   *chatresource.Service
	Owner, Root string
}
type Adapters struct {
	apps      *webapp.Service
	runtime   Runtime
	registry  ResourceCatalog
	chats     ChatSummaries
	artifacts *chatresource.Service
	owner     string
	ledger    ownership
	mu        sync.Mutex
	runs      map[string]*assistantRun
	active    map[string]int
	total     int
}
type assistantEvent struct {
	Cursor int    `json:"cursor"`
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
}
type assistantRun struct {
	installation, generation string
	ref                      rt.RunRef
	cancel                   context.CancelFunc
	done                     chan struct{}
	expires                  time.Time
	events                   []assistantEvent
	cursor                   int
	terminal, failed         bool
	textOverflow             bool
	text                     string
}

func NewAdapters(o AdapterOptions) *Adapters {
	return &Adapters{apps: o.Apps, runtime: o.Runtime, registry: o.Registry, chats: o.Chats, artifacts: o.Artifacts, owner: o.Owner, ledger: ownership{root: o.Root}, runs: map[string]*assistantRun{}, active: map[string]int{}}
}
func (a *Adapters) Actions() map[string]Handler {
	actions := map[string]Handler{"assistant.chat": a.chat, "assistant.events": a.events, "assistant.stop": a.stop, "assistant.execute": a.execute}
	actions["skill.list"] = func(ctx context.Context, s *webapp.Scope, r json.RawMessage) (any, error) {
		return a.skills(ctx, s, r, false)
	}
	actions["skill.describe"] = func(ctx context.Context, s *webapp.Scope, r json.RawMessage) (any, error) {
		return a.skills(ctx, s, r, true)
	}
	for _, action := range []string{"artifact.list", "artifact.get", "artifact.read"} {
		actions[action] = func(ctx context.Context, s *webapp.Scope, r json.RawMessage) (any, error) {
			return a.artifact(ctx, s, r, action)
		}
	}
	return actions
}
func (a *Adapters) reserve(scope *webapp.Scope) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, r := range a.runs {
		if r.terminal && time.Now().After(r.expires) {
			delete(a.runs, id)
		}
	}
	// Include admissions that have reserved a slot but have not published their
	// run record yet, so concurrent callers cannot overflow the record bound.
	if a.total >= 8 || a.active[scope.InstallationID] >= 2 || len(a.runs)+a.total >= 128 {
		return nil, sdkError("app_capacity_exceeded", 429)
	}
	a.total++
	a.active[scope.InstallationID]++
	return func() { a.mu.Lock(); defer a.mu.Unlock(); a.total--; a.active[scope.InstallationID]-- }, nil
}
func (a *Adapters) control(ctx context.Context) context.Context {
	return controlscope.WithContext(ctx, controlscope.Scope{Transport: "http", Subject: a.owner})
}
func (a *Adapters) caller() rt.Caller { return rt.Caller{Subject: a.owner} }
func (a *Adapters) interrupt(ref rt.RunRef) error {
	ctx, cancel := context.WithTimeout(a.control(context.Background()), 5*time.Second)
	defer cancel()
	_, err := a.runtime.Interrupt(ctx, rt.InterruptCommand{RunRef: ref, Source: contracts.InterruptSourceHTTPAPI, Reason: contracts.InterruptReasonUserCancelled, Detail: "WebApp invocation cancelled"})
	return err
}
func (a *Adapters) append(r *assistantRun, kind, text string) {
	r.cursor++
	r.events = append(r.events, assistantEvent{r.cursor, kind, text})
	if len(r.events) > 256 {
		r.events = r.events[len(r.events)-256:]
	}
}
func (a *Adapters) chat(ctx context.Context, scope *webapp.Scope, raw json.RawMessage) (any, error) {
	var input struct {
		Message    string    `json:"message"`
		SkillIDs   *[]string `json:"skillIds,omitempty"`
		Background bool      `json:"background"`
	}
	if err := decode(raw, &input); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Message) == "" || utf8.RuneCountInString(input.Message) > 12000 {
		return nil, sdkError("invalid_args", 400)
	}
	def, err := a.selected(scope)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, id := range scope.Policy.Skills {
		allowed[id] = true
	}
	ids := []string{}
	if scope.Manifest.Copilot != nil {
		for _, ref := range scope.Manifest.Copilot.References {
			if ref.Type == "skill" && allowed[ref.ID] {
				ids = append(ids, ref.ID)
			}
		}
	}
	if input.SkillIDs != nil {
		if len(*input.SkillIDs) > 16 {
			return nil, sdkError("invalid_args", 400)
		}
		declared := map[string]bool{}
		for _, id := range ids {
			declared[id] = true
		}
		ids = nil
		for _, id := range *input.SkillIDs {
			if !allowed[id] || !declared[id] {
				return nil, sdkError("app_grant_required", 403)
			}
			ids = append(ids, id)
		}
	}
	refs := []rt.Reference{}
	for _, id := range ids {
		refs = append(refs, rt.Reference{Type: "skill", ID: id})
	}
	release, err := a.reserve(scope)
	if err != nil {
		return nil, err
	}
	lease, err := a.apps.AcquireInstance(scope.Entry.ID, scope.Generation, scope.Policy.Revision)
	if err != nil {
		release()
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(a.control(lease.Context), 30*time.Minute)
	ref := rt.RunRef{ChatID: uuid.NewString(), RunID: chat.NewRunID(), AgentKey: def.Key, Caller: a.caller()}
	if err = a.ledger.claim(scope, ref); err != nil {
		cancel()
		lease.Release()
		release()
		return nil, err
	}
	r := &assistantRun{installation: scope.InstallationID, generation: scope.Generation, ref: ref, cancel: cancel, done: make(chan struct{}), expires: time.Now().Add(30 * time.Minute)}
	a.mu.Lock()
	a.runs[ref.RunID] = r
	a.append(r, "run.accepted", "")
	a.mu.Unlock()
	handle, startErr := a.runtime.StartQuery(runCtx, rt.QueryCommand{ChatID: ref.ChatID, RunID: ref.RunID, AgentKey: ref.AgentKey, Message: input.Message, References: refs, AccessLevel: "ask_for_approval", Caller: a.caller(), ChatSource: "webapp"})
	if startErr != nil {
		if _, statusErr := a.runtime.RunStatus(runCtx, ref.RunID); statusErr != nil {
			a.mu.Lock()
			delete(a.runs, ref.RunID)
			a.mu.Unlock()
			cancel()
			lease.Release()
			release()
			return nil, sdkError("assistant_failed", 502)
		}
	} else if handle.RunID != ref.RunID || handle.ChatID != ref.ChatID {
		cancel()
		// A broken Runtime contract must not release an accepted writer's lease.
		r.ref = rt.RunRef{RunID: handle.RunID, ChatID: handle.ChatID, AgentKey: handle.AgentKey, Caller: a.caller()}
		go a.observe(runCtx, r, func() { lease.Release(); release() })
		return nil, sdkError("assistant_failed", 502)
	}
	go a.observe(runCtx, r, func() { cancel(); lease.Release(); release() })
	if input.Background {
		return map[string]any{"chatId": ref.ChatID, "runId": ref.RunID, "accepted": true}, nil
	}
	select {
	case <-ctx.Done():
		cancel()
		return nil, sdkError("cancelled", 499)
	case <-r.done:
	}
	a.mu.Lock()
	text, failed, overflow := r.text, r.failed, r.textOverflow
	a.mu.Unlock()
	if overflow {
		return nil, sdkError("assistant_result_too_large", 413)
	}
	if failed {
		return nil, sdkError("assistant_failed", 502)
	}
	return map[string]any{"chatId": ref.ChatID, "runId": ref.RunID, "agentKey": ref.AgentKey, "text": text}, nil
}
func (a *Adapters) observe(ctx context.Context, r *assistantRun, release func()) {
	defer release()
	defer close(r.done)
	sub, err := a.runtime.AttachRun(ctx, r.ref, 0)
	var events <-chan struct{} // closed when the collector has acknowledged terminal delivery
	if err == nil {
		done := make(chan struct{})
		events = done
		go func() {
			defer close(done)
			defer sub.Close()
			for e := range sub.Events {
				a.mu.Lock()
				switch e.Type {
				case "content.delta":
					text := e.String("delta")
					if text == "" {
						text = e.String("text")
					}
					text = strings.ToValidUTF8(text, "�")
					for len(text) > 0 {
						n := min(len(text), 1024)
						for n < len(text) && !utf8.RuneStart(text[n]) {
							n--
						}
						part := text[:n]
						a.append(r, "text.delta", part)
						if len(r.text)+len(part) <= 128<<10 {
							r.text += part
						} else {
							r.textOverflow = true
						}
						text = text[n:]
					}
				case "run.start", "run.complete", "run.error", "run.interrupted", "artifact.publish", "artifact.published":
					a.append(r, e.Type, "")
					if e.Type == "run.error" || e.Type == "run.interrupted" {
						r.failed = true
					}
				}
				a.mu.Unlock()
			}
		}()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	cancelled := false
	lastInterrupt := time.Time{}
	for {
		if ctx.Err() != nil {
			cancelled = true
			if time.Since(lastInterrupt) > time.Second {
				_ = a.interrupt(r.ref)
				lastInterrupt = time.Now()
			}
		}
		snapshot, e := a.runtime.RunStatus(context.Background(), r.ref.RunID)
		if e == nil && snapshot.CompletedAt > 0 {
			// The observer must acknowledge FreezeAndWait before a generation's
			// lease is released. Unknown outcomes retain the lease and fail closed.
			if events != nil {
				select {
				case <-events:
					events = nil
				default:
				}
			}
			if events == nil {
				a.mu.Lock()
				r.terminal = true
				r.failed = r.failed || cancelled || snapshot.Status == "failed" || snapshot.Status == "interrupted"
				r.expires = time.Now().Add(30 * time.Minute)
				a.append(r, "run.finished", "")
				a.mu.Unlock()
				return
			}
		}
		<-ticker.C
	}
}
func (a *Adapters) ownedRun(scope *webapp.Scope, id string) (*assistantRun, error) {
	r := a.runs[id]
	if r != nil && r.terminal && time.Now().After(r.expires) {
		delete(a.runs, id)
		r = nil
	}
	if r == nil || r.installation != scope.InstallationID || r.generation != scope.Generation {
		return nil, sdkError("app_grant_required", 403)
	}
	return r, nil
}
func (a *Adapters) events(ctx context.Context, scope *webapp.Scope, raw json.RawMessage) (any, error) {
	var input struct {
		RunID  string `json:"runId"`
		Cursor int    `json:"cursor"`
	}
	if err := decode(raw, &input); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.ownedRun(scope, input.RunID)
	if err != nil {
		return nil, err
	}
	if input.Cursor < 0 || input.Cursor > r.cursor {
		return nil, sdkError("invalid_args", 400)
	}
	if len(r.events) > 0 && input.Cursor < r.events[0].Cursor-1 {
		return nil, sdkError("event_cursor_expired", 409)
	}
	out := []assistantEvent{}
	for _, e := range r.events {
		if e.Cursor > input.Cursor {
			out = append(out, e)
		}
	}
	return map[string]any{"chatId": r.ref.ChatID, "runId": r.ref.RunID, "cursor": r.cursor, "events": out, "terminal": r.terminal}, nil
}
func (a *Adapters) stop(ctx context.Context, scope *webapp.Scope, raw json.RawMessage) (any, error) {
	var input struct {
		RunID string `json:"runId"`
	}
	if err := decode(raw, &input); err != nil {
		return nil, err
	}
	a.mu.Lock()
	r, err := a.ownedRun(scope, input.RunID)
	if err == nil {
		r.cancel()
	}
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err = a.interrupt(r.ref); err != nil {
		return nil, sdkError("assistant_stop_failed", 502)
	}
	return map[string]bool{"stopped": true}, nil
}
func validInput(v any, depth int) bool {
	if depth > 64 {
		return false
	}
	switch n := v.(type) {
	case map[string]any:
		for k, c := range n {
			if k == "$file" || !validInput(c, depth+1) {
				return false
			}
		}
	case []any:
		for _, c := range n {
			if !validInput(c, depth+1) {
				return false
			}
		}
	}
	return true
}
func (a *Adapters) execute(ctx context.Context, scope *webapp.Scope, raw json.RawMessage) (any, error) {
	var input struct {
		Message      string          `json:"message"`
		Input        json.RawMessage `json:"input,omitempty"`
		OutputSchema map[string]any  `json:"outputSchema"`
	}
	limit := 65536
	if backend, _ := ctx.Value(backendKey{}).(bool); backend {
		limit = 262144
	}
	if len(raw) > limit || decode(raw, &input) != nil || strings.TrimSpace(input.Message) == "" || utf8.RuneCountInString(input.Message) > 12000 || input.OutputSchema["type"] != "object" {
		return nil, sdkError("invalid_args", 400)
	}
	schema, _ := json.Marshal(input.OutputSchema)
	var value any
	if len(schema) > 65536 || len(input.Input) > 0 && (json.Unmarshal(input.Input, &value) != nil || !validInput(value, 0)) {
		return nil, sdkError("invalid_args", 400)
	}
	def, err := a.selected(scope)
	if err != nil {
		return nil, err
	}
	release, err := a.reserve(scope)
	if err != nil {
		return nil, err
	}
	defer release()
	deadline, cancel := context.WithTimeout(a.control(ctx), 10*time.Minute)
	defer cancel()
	result, err := a.runtime.Execute(deadline, rt.ExecuteCommand{AgentKey: def.Key, Message: input.Message, Input: input.Input, OutputSchema: input.OutputSchema, AccessLevel: "ask_for_approval", Caller: a.caller(), ChatSource: "webapp", OnPrepared: func(ref rt.RunRef) error { return a.ledger.claim(scope, ref) }})
	if err != nil {
		return nil, sdkError("assistant_failed", 502)
	}
	return result, nil
}
