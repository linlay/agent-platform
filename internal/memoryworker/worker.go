package memoryworker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

type Chats interface {
	MemoryChats(context.Context, int64, int64, string, int) ([]chat.MemoryChat, error)
	ListRuns(string) ([]chat.RunSummary, error)
	MemoryMessages(string, string) ([]chat.MemoryMessage, error)
}
type Eligibility func(string) (project string, enabled bool)
type Status struct {
	Enabled             bool         `json:"enabled"`
	Automatic           bool         `json:"automatic"`
	PollIntervalSeconds int          `json:"pollIntervalSeconds"`
	State               string       `json:"state"`
	StartedAt           int64        `json:"startedAt,omitempty"`
	FinishedAt          int64        `json:"finishedAt,omitempty"`
	Processed           int          `json:"processedBatches"`
	Error               string       `json:"error,omitempty"`
	ModelKey            string       `json:"modelKey"`
	Timezone            string       `json:"timezone"`
	Manual              *RangeStatus `json:"manual,omitempty"`
}
type Worker struct {
	cfg          config.MemoryConfig
	stateDir     string
	location     *time.Location
	chats        Chats
	archives     Chats
	manual       *RangeStatus
	manualCancel context.CancelFunc
	cancelRange  bool
	cli          CLI
	syncConfig   ConfigSync
	eligible     Eligibility
	mu           sync.Mutex
	status       Status
	wake         chan struct{}
	done         chan struct{}
	started      bool
	ctx          context.Context
}

func New(cfg config.MemoryConfig, stateDir string, chats Chats, cli CLI, syncConfig ConfigSync, eligible Eligibility) *Worker {
	if cfg.Summary == (config.MemorySummaryConfig{}) {
		cfg.Summary = config.DefaultMemorySummaryConfig()
	}
	loc, _ := time.LoadLocation(cfg.Timezone)
	if loc == nil {
		loc = time.Local
	}
	return &Worker{cfg: cfg, stateDir: stateDir, location: loc, chats: chats, cli: cli, syncConfig: syncConfig, eligible: eligible, wake: make(chan struct{}, 1), done: make(chan struct{}), status: Status{Enabled: cfg.Enabled, Automatic: cfg.Worker.Enabled, PollIntervalSeconds: cfg.Worker.PollIntervalSeconds, ModelKey: cfg.Worker.ModelKey, Timezone: loc.String(), State: "idle"}}
}
func (w *Worker) Status() Status { w.mu.Lock(); defer w.mu.Unlock(); return w.statusLocked() }
func (w *Worker) Trigger() (Status, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.cfg.Enabled || !w.started || w.ctx.Err() != nil {
		return w.statusLocked(), fmt.Errorf("memory worker unavailable")
	}
	if w.status.State == "running" || w.status.State == "queued" {
		return w.statusLocked(), nil
	}
	w.status.State = "queued"
	w.status.Error = ""
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return w.statusLocked(), nil
}
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.ctx = ctx
	w.restoreRangeLocked()
	w.mu.Unlock()
	go func() {
		defer close(w.done)
		interval := time.Duration(w.cfg.Worker.PollIntervalSeconds) * time.Second
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if w.cfg.Enabled && w.hasPendingRange() {
			select {
			case w.wake <- struct{}{}:
			default:
			}
		} else if w.cfg.Enabled && w.cfg.Worker.Enabled {
			_, _ = w.Trigger()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !w.cfg.Enabled || !w.cfg.Worker.Enabled {
					continue
				}
			case <-w.wake:
			}
			if ctx.Err() != nil {
				return
			}
			if w.hasPendingRange() {
				w.executeRange(ctx)
				continue
			}
			w.mu.Lock()
			w.status.State = "running"
			w.status.Error = ""
			w.status.Processed = 0
			w.status.StartedAt = time.Now().UnixMilli()
			w.mu.Unlock()
			n, err := w.run(ctx, time.Now())
			w.mu.Lock()
			w.status.Processed = n
			w.status.FinishedAt = time.Now().UnixMilli()
			w.status.State = "idle"
			if err != nil {
				w.status.State = "failed"
				w.status.Error = err.Error()
			}
			w.mu.Unlock()
			if err != nil && ctx.Err() == nil {
				log.Printf("memory worker failed: %v", err)
			}
		}
	}()
}
func (w *Worker) Wait(ctx context.Context) error {
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type checkpoint struct {
	Since  int64             `json:"since"`
	After  int64             `json:"after"`
	ChatID string            `json:"chatId"`
	Seen   map[string]string `json:"seen,omitempty"`
}

func digest(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }
func (w *Worker) syncModels(ctx context.Context) error {
	if w.syncConfig == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(w.cfg.Worker.TimeoutSeconds)*time.Second)
	defer cancel()
	return w.syncConfig.Sync(ctx)
}
func (w *Worker) call(ctx context.Context, method string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(w.cfg.Worker.TimeoutSeconds)*time.Second)
	defer cancel()
	return w.cli.Call(ctx, method, in, out)
}
func (w *Worker) run(ctx context.Context, now time.Time) (int, error) {
	if err := os.MkdirAll(w.stateDir, 0700); err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(w.stateDir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	if info, e := root.Lstat("worker.lock"); e == nil && !info.Mode().IsRegular() {
		return 0, fmt.Errorf("invalid worker lock")
	}
	lock, err := root.OpenFile("worker.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	if err = lockWorker(lock); err != nil {
		return 0, fmt.Errorf("memory worker busy")
	}
	local := now.In(w.location)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, w.location)
	cp := checkpoint{Since: midnight.UnixMilli()}
	if b, e := root.ReadFile("checkpoint.json"); e == nil {
		if json.Unmarshal(b, &cp) != nil || cp.Since <= 0 {
			return 0, fmt.Errorf("invalid memory checkpoint")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return 0, e
	}
	if cp.Seen == nil {
		cp.Seen = map[string]string{}
	}
	save := func() error {
		b, _ := json.Marshal(cp)
		if len(b) > 8<<20 {
			return fmt.Errorf("memory checkpoint exceeds 8 MiB")
		}
		if e := root.Remove("checkpoint.tmp"); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		f, e := root.OpenFile("checkpoint.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(b)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		return root.Rename("checkpoint.tmp", "checkpoint.json")
	}
	if err = save(); err != nil {
		return 0, err
	}
	if err = requireMemx(ctx, w.call); err != nil {
		return 0, err
	}
	if err = w.syncModels(ctx); err != nil {
		return 0, err
	}
	processed := 0
	attempted := 0
	var firstError error
	candidates, err := w.chats.MemoryChats(ctx, cp.Since, cp.After, cp.ChatID, 200)
	if err != nil {
		return 0, err
	}
	completePage := true
	for _, c := range candidates {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		e := func() error {
			chatVersion := fmt.Sprintf("%d:%d", c.CompletedAt, c.RunCount)
			if cp.Seen[c.ChatID] == chatVersion {
				return nil
			}
			runs, e := w.chats.ListRuns(c.ChatID)
			if e != nil {
				return e
			}
			// Oldest first; one malformed chat must not starve later conversations.
			for i := len(runs) - 1; i >= 0; i-- {
				r := runs[i]
				if r.CompletedAt < cp.Since || r.TeamID != "" {
					continue
				}
				project, allowed := w.eligible(r.AgentKey)
				if !allowed {
					continue
				}
				messages, e := w.chats.MemoryMessages(c.ChatID, r.RunID)
				if e != nil {
					return e
				}
				for _, batch := range makeBatches(r, project, messages) {
					var receipt struct {
						Processed bool `json:"processed"`
					}
					if e = w.call(ctx, "receipt", batch, &receipt); e != nil {
						return e
					}
					if receipt.Processed {
						continue
					}
					if attempted >= w.cfg.Worker.MaxBatches {
						completePage = false
						return nil
					}
					attempted++
					if _, ok := w.eligible(r.AgentKey); !ok {
						return fmt.Errorf("memory authorization changed")
					}
					if e = w.call(ctx, "update", map[string]any{"batch": batch}, nil); e != nil {
						return e
					}
					processed++
				}
			}
			cp.Seen[c.ChatID] = chatVersion
			return nil
		}()
		if e != nil && firstError == nil {
			firstError = e
		}
		if !completePage {
			break
		}
		cp.After = c.CompletedAt
		cp.ChatID = c.ChatID
		if err = save(); err != nil {
			return processed, err
		}
	}
	if completePage && len(candidates) < 200 {
		cp.After = 0
		cp.ChatID = ""
		if err = save(); err != nil {
			return processed, err
		}
	}
	// Idempotent reconciliation also handles late daily facts and manual deletions.
	through := midnight.AddDate(0, 0, -1).Format(time.DateOnly)
	return processed, errors.Join(firstError, w.reconcile(ctx, through))
}
func makeBatches(r chat.RunSummary, project string, messages []chat.MemoryMessage) []Batch {
	var batches []Batch
	var sources []Source
	size := 0
	seen := map[string]bool{}
	flush := func() {
		if len(sources) == 0 {
			return
		}
		b, _ := json.Marshal(sources)
		batches = append(batches, Batch{SchemaVersion: 1, BatchID: digest(string(b)), Sources: sources})
		sources = nil
		size = 0
	}
	for _, m := range messages {
		if m.At <= 0 || strings.TrimSpace(m.Content) == "" {
			continue
		}
		// A failed/cancelled run can still contain explicit user corrections, but its
		// assistant output must never be summarized as completed work.
		if m.Role == "assistant" && r.FinishReason != "complete" && r.FinishReason != "stop" && r.FinishReason != "end_turn" {
			continue
		}
		runes := []rune(m.Content)
		for part := 0; len(runes) > 0; part++ {
			n := min(len(runes), 4000)
			text := string(runes[:n])
			runes = runes[n:]
			h := digest(text)
			id := digest(fmt.Sprintf("%s:%d:%s", m.ID, part, h))
			if seen[id] {
				continue
			}
			seen[id] = true
			if size+len(text) > 24000 {
				flush()
			}
			sources = append(sources, Source{ID: id, ChatID: r.ChatID, RunID: r.RunID, AgentKey: r.AgentKey, ProjectKey: project, Role: m.Role, OccurredAt: time.UnixMilli(m.At).UTC().Format(time.RFC3339Nano), Content: text, ContentHash: h})
			size += len(text)
		}
	}
	flush()
	return batches
}

// StateRoot is deliberately separate from Markdown content and chat storage.
func StateRoot(stateDir string) string { return filepath.Join(stateDir, "memory-worker") }
