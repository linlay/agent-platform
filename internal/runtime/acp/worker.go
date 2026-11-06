package acp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"
)

// An engine worker owns one initialized connection. Chat handles own separate
// ACP sessions. Workers never cross an engine/profile or working-directory boundary.
type engineWorker struct {
	owner       *Sessions
	key         string
	setup       chan struct{}
	mu          sync.Mutex
	process     *Process
	initialized json.RawMessage
	reusable    bool
	handles     map[*sessionProcess]struct{}
	preparing   *sessionProcess
	closing     bool
	idleSince   time.Time
}

type sessionProcess struct {
	*Process
	worker     *engineWorker
	entry      *session
	generation string
	done       chan struct{}
	once       sync.Once
	closeErr   error
}

func (p *sessionProcess) Generation() string    { return p.generation }
func (p *sessionProcess) Done() <-chan struct{} { return p.done }
func (p *sessionProcess) Close() error          { return p.close(false) }
func (p *sessionProcess) close(keepWarm bool) error {
	p.once.Do(func() {
		defer func() { <-p.worker.owner.slots }()
		w := p.worker
		p.entry.mu.Lock()
		id := p.entry.sessionID
		p.entry.mu.Unlock()
		select {
		case <-p.Process.Done():
		default:
			if w.reusable && id != "" {
				ctx, cancel := context.WithTimeout(w.owner.host.ctx, 5*time.Second)
				_, p.closeErr = controlRequest(ctx, p.Process, "session/close", map[string]any{"sessionId": id})
				cancel()
				if p.closeErr != nil {
					p.Process.stop()
				}
			}
		}
		w.mu.Lock()
		delete(w.handles, p)
		empty := len(w.handles) == 0
		closeWorker := !w.reusable || (empty && (!keepWarm || w.closing))
		if closeWorker {
			w.closing = true
		}
		if empty {
			w.idleSince = time.Now()
		}
		if w.preparing == p {
			w.preparing = nil
		}
		w.mu.Unlock()
		close(p.done)
		if closeWorker {
			err := p.Process.Close()
			if p.closeErr == nil {
				p.closeErr = err
			}
			w.owner.forgetWorker(w)
		}
	})
	return p.closeErr
}

// A fresh connection reloads process-scoped credentials. Existing Chats drain
// independently; a failed login in one Chat must not kill a sibling prompt.
func (w *engineWorker) invalidate() {
	w.mu.Lock()
	w.closing = true
	w.mu.Unlock()
	w.owner.forgetWorker(w)
}
func (w *engineWorker) draining() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closing
}

// A malformed agent must not bind two Chats to the same upstream session.
func (p *sessionProcess) uniqueSessionID(id string) bool {
	w := p.worker
	w.mu.Lock()
	defer w.mu.Unlock()
	for other := range w.handles {
		if other == p {
			continue
		}
		other.entry.mu.Lock()
		duplicate := other.entry.sessionID == id || other.entry.observedSessionID == id
		other.entry.mu.Unlock()
		if duplicate {
			return false
		}
	}
	return true
}

func (s *Sessions) forgetWorker(w *engineWorker) {
	s.workersMu.Lock()
	if s.workers[w.key] == w {
		delete(s.workers, w.key)
	}
	s.workersMu.Unlock()
}

func (s *Sessions) startSessionProcess(ctx context.Context, chatID, engineID, workspace string, e *session) (*sessionProcess, json.RawMessage, func(), error) {
	select {
	case s.slots <- struct{}{}:
	default:
		if e.preview {
			return nil, nil, nil, ErrBusy
		}
		if err := s.releaseIdleSession(time.Time{}); err != nil {
			return nil, nil, nil, err
		}
		select {
		case s.slots <- struct{}{}:
		default:
			return nil, nil, nil, ErrBusy
		}
	}
	attached := false
	defer func() {
		if !attached {
			<-s.slots
		}
	}()
	key := engineID + "\x00" + workspace
	if e.childDefinition != nil && !e.preview {
		key += "\x00" + chatID
	}
	previewKey := key + "\x00config-preview"
	for {
		s.workersMu.Lock()
		w := s.workers[key]
		if w == nil {
			w = &engineWorker{owner: s, key: key, setup: make(chan struct{}, 1), handles: map[*sessionProcess]struct{}{}}
			s.workers[key] = w
		}
		s.workersMu.Unlock()
		if e.preview {
			select {
			case w.setup <- struct{}{}:
			default:
				if key != previewKey {
					key = previewKey
					continue
				}
				return nil, nil, nil, ErrBusy
			}
		} else {
			select {
			case w.setup <- struct{}{}:
			case <-ctx.Done():
				return nil, nil, nil, ctx.Err()
			}
		}
		unlock := func() { w.mu.Lock(); w.preparing = nil; w.mu.Unlock(); <-w.setup }
		if e.preview {
			w.mu.Lock()
			occupied := len(w.handles) != 0
			w.mu.Unlock()
			if occupied {
				unlock()
				// Discovery cleanup must not close a connection serving real Chats.
				// Use spare host capacity instead of treating an occupied worker as full.
				if key != previewKey {
					key = previewKey
					continue
				}
				return nil, nil, nil, ErrBusy
			}
		}
		if w.process != nil {
			w.mu.Lock()
			unusable := !w.reusable || w.closing
			w.mu.Unlock()
			select {
			case <-w.process.Done():
				unusable = true
			default:
			}
			select {
			case <-w.process.retired:
				unusable = true
			default:
			}
			if unusable {
				s.forgetWorker(w)
				unlock()
				continue
			}
		} else {
			p, err := s.host.Start(s.host.ctx, "worker-"+rand.Text(), engineID, workspace, w.incoming)
			if err != nil {
				s.forgetWorker(w)
				unlock()
				return nil, nil, nil, err
			}
			w.process = p
			raw, err := controlRequest(ctx, p, "initialize", s.initializeParams())
			if err != nil {
				p.Close()
				s.forgetWorker(w)
				unlock()
				return nil, nil, nil, err
			}
			w.initialized = raw
			var init struct {
				AgentCapabilities struct {
					LoadSession         bool                                    `json:"loadSession"`
					SessionCapabilities struct{ Close, Resume json.RawMessage } `json:"sessionCapabilities"`
				} `json:"agentCapabilities"`
			}
			json.Unmarshal(raw, &init)
			var closeCapability, resumeCapability map[string]json.RawMessage
			json.Unmarshal(init.AgentCapabilities.SessionCapabilities.Close, &closeCapability)
			json.Unmarshal(init.AgentCapabilities.SessionCapabilities.Resume, &resumeCapability)
			w.reusable = closeCapability != nil && (init.AgentCapabilities.LoadSession || resumeCapability != nil)
			s.workerTasks.Add(1)
			go func() { defer s.workerTasks.Done(); w.retire() }()
		}
		p := &sessionProcess{Process: w.process, worker: w, entry: e, generation: rand.Text(), done: make(chan struct{})}
		w.mu.Lock()
		if w.closing {
			w.mu.Unlock()
			s.forgetWorker(w)
			unlock()
			continue
		}
		w.handles[p] = struct{}{}
		w.idleSince = time.Time{}
		w.preparing = p
		w.mu.Unlock()
		attached = true
		return p, w.initialized, unlock, nil
	}
}

func (w *engineWorker) incoming(ctx context.Context, in Incoming) (any, *sdk.RequestError) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(in.Params, &params) != nil || params.SessionID == "" {
		return nil, sdk.NewInvalidParams(nil)
	}
	w.mu.Lock()
	var target *sessionProcess
	for p := range w.handles {
		p.entry.mu.Lock()
		id, observed := p.entry.sessionID, p.entry.observedSessionID
		p.entry.mu.Unlock()
		if id == params.SessionID || observed == params.SessionID {
			target = p
			break
		}
	}
	if target == nil && w.preparing != nil {
		e := w.preparing.entry
		e.mu.Lock()
		unknown := e.sessionID == "" && e.observedSessionID == ""
		e.mu.Unlock()
		if unknown {
			target = w.preparing
		}
	}
	w.mu.Unlock()
	if target == nil {
		return nil, sdk.NewInvalidParams(nil)
	}
	in.Generation = target.Generation()
	return w.owner.incoming(ctx, target.entry, in)
}

func (w *engineWorker) retire() {
	p := w.process
	select {
	case <-p.retired:
	case <-p.Done():
	}
	w.owner.forgetWorker(w)
	w.setup <- struct{}{}
	w.mu.Lock()
	w.closing = true
	handles := make([]*sessionProcess, 0, len(w.handles))
	for h := range w.handles {
		handles = append(handles, h)
	}
	w.mu.Unlock()
	<-w.setup
	// Drain every Chat operation before retiring this version. Closing one handle
	// must never terminate a sibling Chat's prompt or permission request.
	for _, h := range handles {
		h.entry.op.Lock()
		h.Close()
		w.owner.mu.Lock()
		h.entry.mu.Lock()
		if h.entry.process == h {
			h.entry.process = nil
			h.entry.readyState = false
			if w.owner.entries[h.entry.binding.ChatID] == h.entry {
				delete(w.owner.entries, h.entry.binding.ChatID)
			}
		}
		h.entry.mu.Unlock()
		w.owner.mu.Unlock()
		h.entry.op.Unlock()
	}
	p.Close()
}
