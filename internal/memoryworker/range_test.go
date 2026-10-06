package memoryworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/chat"
)

type rangeChats struct{ runs []chat.RunSummary }

func (f rangeChats) MemoryChats(_ context.Context, since, after int64, id string, limit int) ([]chat.MemoryChat, error) {
	var out []chat.MemoryChat
	for _, r := range f.runs {
		if r.CompletedAt >= since && (r.CompletedAt > after || r.CompletedAt == after && r.ChatID > id) {
			out = append(out, chat.MemoryChat{ChatID: r.ChatID, CompletedAt: r.CompletedAt, RunCount: 1})
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (f rangeChats) ListRuns(id string) ([]chat.RunSummary, error) {
	for _, r := range f.runs {
		if r.ChatID == id {
			return []chat.RunSummary{r}, nil
		}
	}
	return nil, errors.New("not found")
}
func (f rangeChats) MemoryMessages(id, run string) ([]chat.MemoryMessage, error) {
	return []chat.MemoryMessage{{ID: run + ":query", Role: "user", Content: "Please use Chinese", At: 1790985600000}}, nil
}
func manualJob(w *Worker, from, to string) *RangeStatus {
	_, end, _ := w.rangeBounds(RangeRequest{StartDate: from, EndDate: to}, time.Now())
	job := &RangeStatus{ID: "test", RangeRequest: RangeRequest{StartDate: from, EndDate: to}, State: "running", Timezone: w.location.String(), ModelKey: w.cfg.Worker.ModelKey, Until: end.UnixMilli()}
	w.manual = job
	return job
}
func TestRangePaginationBoundsReceiptsAndIndependentCheckpoint(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, _, _ := workerFixture(t, cli)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	var runs []chat.RunSummary
	for i := 0; i < 205; i++ {
		runs = append(runs, chat.RunSummary{ChatID: fmt.Sprintf("c%03d", i), RunID: fmt.Sprintf("r%03d", i), AgentKey: "agent", CompletedAt: start + int64(i), FinishReason: "complete"})
	}
	runs = append(runs, chat.RunSummary{ChatID: "outside", RunID: "outside", AgentKey: "agent", CompletedAt: start + 24*3600*1000, FinishReason: "complete"})
	w.chats = rangeChats{runs: runs}
	w.cfg.Worker.MaxBatches = 1
	checkpointPath := filepath.Join(w.stateDir, "checkpoint.json")
	original := []byte(`{"since":123,"seen":{"unrelated":"version"}}`)
	if err := os.WriteFile(checkpointPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	job := manualJob(w, "2026-10-01", "2026-10-01")
	if err := w.runRange(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if cli.updates != 205 || job.SelectedRuns != 205 || job.ProcessedBatches != 205 {
		t.Fatalf("updates=%d job=%+v", cli.updates, job)
	}
	b, _ := os.ReadFile(checkpointPath)
	if string(b) != string(original) {
		t.Fatal("incremental checkpoint changed")
	}
	job = manualJob(w, "2026-10-01", "2026-10-02")
	if err := w.runRange(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if job.ReusedBatches != 205 || job.ProcessedBatches != 1 {
		t.Fatalf("overlap duplicated: %+v", job)
	}
}
func TestRangeIncludesArchivesAndSkipsIneligible(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, _, now := workerFixture(t, cli)
	w.WithArchives(w.chats)
	job := manualJob(w, "2026-10-03", "2026-10-03")
	job.IncludeArchived = true
	if err := w.runRange(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if job.ProcessedBatches != 1 || job.ReusedBatches != 1 {
		t.Fatalf("%+v", job)
	}
	w.eligible = func(string) (string, bool) { return "", false }
	job = manualJob(w, now.Format(time.DateOnly), now.Format(time.DateOnly))
	if err := w.runRange(context.Background(), job); err != nil || job.SkippedRuns != 1 {
		t.Fatal(err, job)
	}
}
func TestRangeRestartAndInvalidDates(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, _, _ := workerFixture(t, cli)
	w.location, _ = time.LoadLocation("America/New_York")
	start, end, err := w.rangeBounds(RangeRequest{StartDate: "2026-03-08", EndDate: "2026-03-08"}, time.Now())
	if err != nil || end.Sub(start) != 23*time.Hour {
		t.Fatal(start, end, err)
	}
	for _, r := range []RangeRequest{{}, {StartDate: "2026-02-30", EndDate: "2026-03-01"}, {StartDate: "2026-10-03", EndDate: "2026-10-02"}, {StartDate: "2999-01-01", EndDate: "2999-01-02"}} {
		if _, _, err = w.rangeBounds(r, time.Now()); err == nil {
			t.Fatal(r)
		}
	}
	job := manualJob(w, "2026-10-01", "2026-10-02")
	job.After = 123
	job.ChatID = "c"
	job.ProcessedBatches = 2
	if err = w.saveRangeLocked(); err != nil {
		t.Fatal(err)
	}
	w.manual = nil
	w.restoreRangeLocked()
	if w.manual == nil || w.manual.State != "queued" || w.manual.After != 123 || w.manual.ProcessedBatches != 2 {
		t.Fatalf("%+v", w.manual)
	}
	raw, _ := json.Marshal(w.Status())
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["manual"].(map[string]any)["after"]; ok {
		t.Fatal("internal cursor leaked")
	}
}
func TestRangeManualRunsWithTimerDisabled(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, _, _ := workerFixture(t, cli)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	r := RangeRequest{StartDate: "2026-10-03", EndDate: "2026-10-03"}
	if _, err := w.TriggerRange(r); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := w.Status()
		if s.Manual != nil && (s.Manual.State == "completed" || s.Manual.State == "failed") {
			if s.Manual.State != "completed" || s.Manual.ProcessedBatches != 1 {
				t.Fatalf("%+v", s.Manual)
			}
			cancel()
			if err := w.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("manual request did not execute")
}
func TestRangeFailureIsRetryableAndNotMarkedCompleted(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}, failUpdate: true}
	w, _, _ := workerFixture(t, cli)
	job := manualJob(w, "2026-10-03", "2026-10-03")
	if err := w.runRange(context.Background(), job); err == nil {
		t.Fatal("failure ignored")
	}
	cli.failUpdate = false
	job = manualJob(w, "2026-10-03", "2026-10-03")
	if err := w.runRange(context.Background(), job); err != nil || job.ProcessedBatches != 1 {
		t.Fatal(err, job)
	}
}
func TestCompleteFinishReasonIncludesAssistant(t *testing.T) {
	r := chat.RunSummary{ChatID: "c", RunID: "r", AgentKey: "a", FinishReason: "complete"}
	batches := makeBatches(r, "", []chat.MemoryMessage{{ID: "a", Role: "assistant", Content: "Finished", At: 1790985600000}})
	if len(batches) != 1 || batches[0].Sources[0].Role != "assistant" {
		t.Fatal(batches)
	}
}

func TestRangeCancellationAndRestartResume(t *testing.T) {
	cli := &cancellingCLI{fakeCLI: fakeCLI{receipts: map[string]bool{}}, started: make(chan struct{})}
	w, _, _ := workerFixture(t, cli)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	s, err := w.TriggerRange(RangeRequest{StartDate: "2026-10-03", EndDate: "2026-10-03"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cli.started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	if _, err = w.CancelRange("wrong"); !errors.Is(err, ErrRangeInvalid) {
		t.Fatal(err)
	}
	if _, err = w.CancelRange(s.Manual.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s = w.Status()
		if s.Manual.State == "canceled" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if s.Manual.State != "canceled" {
		t.Fatalf("%+v", s.Manual)
	}
	cancel()
	if err = w.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cli.updates != 0 {
		t.Fatal("canceled extraction committed")
	}
}
func TestRangeShutdownRequeuesAndResumesWithoutTimer(t *testing.T) {
	cli := &cancellingCLI{fakeCLI: fakeCLI{receipts: map[string]bool{}}, started: make(chan struct{})}
	w, _, _ := workerFixture(t, cli)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	if _, err := w.TriggerRange(RangeRequest{StartDate: "2026-10-03", EndDate: "2026-10-03"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cli.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("not started")
	}
	cancel()
	if err := w.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Status().Manual.State != "queued" {
		t.Fatal(w.Status())
	}
	next := New(w.cfg, w.stateDir, w.chats, &fakeCLI{receipts: map[string]bool{}}, &fakeSync{}, w.eligible)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	next.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := next.Status()
		if s.Manual != nil && s.Manual.State == "completed" {
			cancel()
			_ = next.Wait(context.Background())
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(next.Status())
}

func TestRangeChangedConfigurationAllowsExplicitRetry(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, _, _ := workerFixture(t, cli)
	job := manualJob(w, "2026-10-03", "2026-10-03")
	job.ModelKey = "old-config"
	job.State = "running"
	if err := w.saveRangeLocked(); err != nil {
		t.Fatal(err)
	}
	w.restoreRangeLocked()
	if w.manual.State != "failed" {
		t.Fatal(w.manual)
	}
	w.ctx = context.Background()
	w.started = true
	s, err := w.TriggerRange(job.RangeRequest)
	if err != nil || s.Manual.State != "queued" || s.Manual.ModelKey == "old-config" {
		t.Fatal(s, err)
	}
}
