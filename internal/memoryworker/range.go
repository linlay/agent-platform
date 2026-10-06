package memoryworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

var ErrRangeInvalid = errors.New("invalid memory range")
var ErrBusy = errors.New("memory maintenance already running")

type RangeRequest struct {
	StartDate       string `json:"startDate"`
	EndDate         string `json:"endDate"`
	IncludeArchived bool   `json:"includeArchived,omitempty"`
}

type RangeStatus struct {
	RangeRequest
	ID               string `json:"id"`
	State            string `json:"state"`
	ModelKey         string `json:"modelKey"`
	Timezone         string `json:"timezone"`
	StartedAt        int64  `json:"startedAt"`
	FinishedAt       int64  `json:"finishedAt,omitempty"`
	ScannedChats     int    `json:"scannedChats"`
	SelectedRuns     int    `json:"selectedRuns"`
	SkippedRuns      int    `json:"skippedRuns"`
	ProcessedBatches int    `json:"processedBatches"`
	ReusedBatches    int    `json:"reusedBatches"`
	EmptyRuns        int    `json:"emptyRuns"`
	NewFacts         *int   `json:"newFacts,omitempty"`
	Error            string `json:"error,omitempty"`
	// Persisted cursors are not part of the public status.
	Source int    `json:"-"`
	After  int64  `json:"-"`
	ChatID string `json:"-"`
	Until  int64  `json:"-"`
}

type rangeCheckpoint struct {
	RangeStatus
	Source int    `json:"source"`
	After  int64  `json:"after"`
	ChatID string `json:"chatId,omitempty"`
	Until  int64  `json:"until"`
}

// WithArchives is called during assembly, before Start.
func (w *Worker) WithArchives(archives Chats) *Worker { w.archives = archives; return w }
func (w *Worker) statusLocked() Status {
	s := w.status
	if w.manual != nil {
		copy := *w.manual
		if copy.NewFacts != nil {
			n := *copy.NewFacts
			copy.NewFacts = &n
		}
		s.Manual = &copy
	}
	return s
}
func (w *Worker) hasPendingRange() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.manual != nil && (w.manual.State == "queued" || w.manual.State == "canceling")
}
func (w *Worker) rangeBounds(r RangeRequest, now time.Time) (time.Time, time.Time, error) {
	start, e1 := time.ParseInLocation(time.DateOnly, r.StartDate, w.location)
	end, e2 := time.ParseInLocation(time.DateOnly, r.EndDate, w.location)
	if e1 != nil || e2 != nil || start.After(end) || end.Format(time.DateOnly) > now.In(w.location).Format(time.DateOnly) || start.UnixMilli() <= 0 {
		return time.Time{}, time.Time{}, ErrRangeInvalid
	}
	return start, end.AddDate(0, 0, 1), nil
}
func (w *Worker) TriggerRange(r RangeRequest) (Status, error) {
	now := time.Now()
	_, end, err := w.rangeBounds(r, now)
	if err != nil {
		return w.Status(), err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.cfg.Enabled || !w.started || w.ctx.Err() != nil {
		return w.statusLocked(), fmt.Errorf("memory worker unavailable")
	}
	if r.IncludeArchived && w.archives == nil {
		return w.statusLocked(), fmt.Errorf("archive memory unavailable")
	}
	if w.manual != nil && (w.manual.State == "queued" || w.manual.State == "running" || w.manual.State == "canceling") {
		if w.manual.RangeRequest == r {
			return w.statusLocked(), nil
		}
		return w.statusLocked(), ErrBusy
	}
	if w.status.State == "running" || w.status.State == "queued" {
		return w.statusLocked(), ErrBusy
	}
	unlock, err := w.acquireRangeLock()
	if err != nil {
		return w.statusLocked(), err
	}
	defer unlock()
	// The shared lock also protects task submission across Platform processes.
	disk, readErr := w.readRangeCheckpoint()
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return w.statusLocked(), readErr
	}
	if disk != nil && (disk.State == "queued" || disk.State == "running" || disk.State == "canceling") && !(w.manual != nil && w.manual.ID == disk.ID && (w.manual.State == "failed" || w.manual.State == "canceled")) {
		return w.statusLocked(), ErrBusy
	}
	until := end.UnixMilli()
	if until > now.UnixMilli() {
		until = now.UnixMilli()
	}
	next := &RangeStatus{RangeRequest: r, ID: fmt.Sprintf("memory-%d", now.UnixNano()), State: "queued", ModelKey: w.cfg.Worker.ModelKey, Timezone: w.location.String(), StartedAt: now.UnixMilli(), Until: until}
	w.cancelRange = false
	old := w.manual
	w.manual = next
	if err = w.saveRangeLocked(); err != nil {
		w.manual = old
		return w.statusLocked(), err
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return w.statusLocked(), nil
}
func (w *Worker) acquireRangeLock() (func(), error) {
	if err := os.MkdirAll(w.stateDir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(w.stateDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if info, e := root.Lstat("worker.lock"); e == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid worker lock")
	}
	f, err := root.OpenFile("worker.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockWorker(f); err != nil {
		f.Close()
		return nil, ErrBusy
	}
	return func() { _ = f.Close() }, nil
}
func (w *Worker) saveRangeLocked() error {
	root, err := os.OpenRoot(w.stateDir)
	if err != nil {
		return err
	}
	defer root.Close()
	b, err := json.Marshal(rangeCheckpoint{RangeStatus: *w.manual, Source: w.manual.Source, After: w.manual.After, ChatID: w.manual.ChatID, Until: w.manual.Until})
	if err != nil {
		return err
	}
	if err = root.Remove("range.tmp"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile("range.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	return root.Rename("range.tmp", "range.json")
}
func (w *Worker) restoreRangeLocked() {
	saved, err := w.readRangeCheckpoint()
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		w.status.State = "failed"
		w.status.Error = "invalid memory range checkpoint"
		return
	}
	job := saved.RangeStatus
	job.Source = saved.Source
	job.After = saved.After
	job.ChatID = saved.ChatID
	job.Until = saved.Until
	if job.Source < 0 || job.Source > 2 || job.After < 0 || job.Until <= 0 {
		w.status.State = "failed"
		w.status.Error = "invalid memory range checkpoint"
		return
	}
	if job.State == "canceling" {
		job.State = "canceled"
	}
	if job.State == "running" {
		job.State = "queued"
	}
	if job.ModelKey != w.cfg.Worker.ModelKey || job.Timezone != w.location.String() {
		if job.State == "queued" {
			job.State = "failed"
			job.Error = "memory configuration changed; submit the date range again"
		}
		w.manual = &job
		return
	}
	start, end, boundsErr := w.rangeBounds(job.RangeRequest, time.Now())
	if boundsErr != nil || job.Until <= start.UnixMilli() || job.Until > end.UnixMilli() {
		w.status.State = "failed"
		w.status.Error = "invalid memory range checkpoint"
		return
	}
	w.manual = &job
}
func (w *Worker) executeRange(ctx context.Context) {
	unlock, err := w.acquireRangeLock()
	if err != nil {
		w.mu.Lock()
		w.manual.State = "failed"
		w.manual.Error = err.Error()
		w.mu.Unlock()
		return
	}
	defer unlock()
	disk, readErr := w.readRangeCheckpoint()
	w.mu.Lock()
	if readErr != nil || disk.ID != w.manual.ID || (disk.State != "queued" && disk.State != "running" && disk.State != "canceling") {
		w.manual.State = "failed"
		w.manual.Error = "memory range changed in another process"
		w.mu.Unlock()
		return
	}
	w.manual.State = "running"
	w.manual.Error = ""
	job := *w.manual
	runCtx, cancel := context.WithCancel(ctx)
	w.manualCancel = cancel
	if w.cancelRange {
		cancel()
	}
	err = w.saveRangeLocked()
	w.mu.Unlock()
	defer cancel()
	if err == nil {
		err = w.runRange(runCtx, &job)
	}
	w.mu.Lock()
	w.manualCancel = nil
	if ctx.Err() != nil {
		w.manual.State = "queued"
		if w.cancelRange {
			w.manual.State = "canceled"
		}
		w.manual.Error = ""
		_ = w.saveRangeLocked()
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.finishRange(err)
}
func (w *Worker) finishRange(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.manual.State = "completed"
	w.manual.FinishedAt = time.Now().UnixMilli()
	if err != nil {
		w.manual.State = "failed"
		w.manual.Error = err.Error()
		if errors.Is(err, context.Canceled) {
			w.manual.State = "canceled"
			w.manual.Error = ""
		}
	}
	if e := w.saveRangeLocked(); e != nil {
		w.manual.State = "failed"
		w.manual.Error = "unable to save memory range progress"
	}
}
func (w *Worker) publishRange(job *RangeStatus) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	copy := *job
	if w.cancelRange {
		copy.State = "canceling"
	}
	w.manual = &copy
	return w.saveRangeLocked()
}
func (w *Worker) runRange(ctx context.Context, job *RangeStatus) error {
	start, _, err := w.rangeBounds(job.RangeRequest, time.Now())
	if err != nil {
		return err
	}
	if err = w.syncModels(ctx); err != nil {
		return err
	}
	stores := []Chats{w.chats}
	if job.IncludeArchived {
		if w.archives == nil {
			return fmt.Errorf("archive memory unavailable")
		}
		stores = append(stores, w.archives)
	}
	for job.Source < len(stores) {
		store := stores[job.Source]
		items, err := store.MemoryChats(ctx, start.UnixMilli(), job.After, job.ChatID, 200)
		if err != nil {
			return err
		}
		for _, c := range items {
			if err = ctx.Err(); err != nil {
				return err
			}
			runs, err := store.ListRuns(c.ChatID)
			if err != nil {
				return err
			}
			for i := len(runs) - 1; i >= 0; i-- {
				r := runs[i]
				if r.CompletedAt < start.UnixMilli() || r.CompletedAt >= job.Until || r.TeamID != "" {
					continue
				}
				project, allowed := w.eligible(r.AgentKey)
				if !allowed {
					job.SkippedRuns++
					continue
				}
				job.SelectedRuns++
				messages, err := store.MemoryMessages(c.ChatID, r.RunID)
				if err != nil {
					return err
				}
				batches := makeBatches(r, project, messages)
				if len(batches) == 0 {
					job.EmptyRuns++
				}
				for _, batch := range batches {
					if err = ctx.Err(); err != nil {
						return err
					}
					var receipt struct {
						Processed bool `json:"processed"`
					}
					if err = w.call(ctx, "receipt", batch, &receipt); err != nil {
						return err
					}
					if receipt.Processed {
						job.ReusedBatches++
						continue
					}
					if _, ok := w.eligible(r.AgentKey); !ok {
						return fmt.Errorf("memory authorization changed")
					}
					var result struct {
						FactCount *int `json:"factCount"`
					}
					if err = w.call(ctx, "update", map[string]any{"batch": batch}, &result); err != nil {
						return err
					}
					job.ProcessedBatches++
					if result.FactCount != nil {
						n := *result.FactCount
						if job.NewFacts != nil {
							n += *job.NewFacts
						}
						job.NewFacts = &n
					}
					// Publish live counters. Durable cursor advances only after the whole chat.
					w.mu.Lock()
					w.manual.ProcessedBatches = job.ProcessedBatches
					w.manual.NewFacts = job.NewFacts
					w.mu.Unlock()
				}
			}
			job.ScannedChats++
			job.After = c.CompletedAt
			job.ChatID = c.ChatID
			if err = w.publishRange(job); err != nil {
				return err
			}
		}
		if len(items) < 200 {
			job.Source++
			job.After = 0
			job.ChatID = ""
			if err = w.publishRange(job); err != nil {
				return err
			}
		}
	}
	now := time.Now().In(w.location)
	through := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, w.location).AddDate(0, 0, -1).Format(time.DateOnly)
	return w.call(ctx, "summarize", map[string]any{"through": through, "maxChars": w.cfg.Worker.SummaryMaxChars}, nil)
}

// CancelRange cancels only the named manual task, never automatic maintenance.
func (w *Worker) CancelRange(id string) (Status, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.manual == nil || id == "" || w.manual.ID != id {
		return w.statusLocked(), ErrRangeInvalid
	}
	if w.manual.State != "queued" && w.manual.State != "running" && w.manual.State != "canceling" {
		return w.statusLocked(), nil
	}
	w.cancelRange = true
	w.manual.State = "canceling"
	if w.manualCancel != nil {
		w.manualCancel()
	}
	if err := w.saveRangeLocked(); err != nil {
		return w.statusLocked(), err
	}
	return w.statusLocked(), nil
}

func (w *Worker) readRangeCheckpoint() (*rangeCheckpoint, error) {
	root, err := os.OpenRoot(w.stateDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("range.json")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("invalid memory range checkpoint")
	}
	b, err := root.ReadFile("range.json")
	if err != nil {
		return nil, err
	}
	var disk rangeCheckpoint
	if json.Unmarshal(b, &disk) != nil || disk.ID == "" {
		return nil, fmt.Errorf("invalid memory range checkpoint")
	}
	return &disk, nil
}
