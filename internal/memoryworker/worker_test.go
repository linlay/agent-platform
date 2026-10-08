package memoryworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

type fakeChats struct {
	runs     []chat.RunSummary
	messages []chat.MemoryMessage
}

func (f fakeChats) MemoryChats(_ context.Context, since, after int64, id string, _ int) ([]chat.MemoryChat, error) {
	r := f.runs[0]
	if r.CompletedAt < since || r.CompletedAt < after || r.CompletedAt == after && r.ChatID <= id {
		return nil, nil
	}
	return []chat.MemoryChat{{ChatID: r.ChatID, CompletedAt: r.CompletedAt}}, nil
}
func (f fakeChats) ListRuns(string) ([]chat.RunSummary, error) { return f.runs, nil }
func (f fakeChats) MemoryMessages(string, string) ([]chat.MemoryMessage, error) {
	return f.messages, nil
}

type fakeSync struct {
	calls int
	fail  bool
}

func (f *fakeSync) Sync(context.Context) error {
	f.calls++
	if f.fail {
		return errors.New("config sync failed")
	}
	return nil
}

type fakeCLI struct {
	receipts       map[string]bool
	updates        int
	summaryThrough string
	failUpdate     bool
	version        string
}

func (f *fakeCLI) Call(_ context.Context, method string, in, out any) error {
	data := map[string]any{}
	switch method {
	case "ping":
		data["version"] = "0.2.0"
		if f.version != "" {
			data["version"] = f.version
		}
		data["maintenanceVersion"] = 2
		data["configDirEnv"] = true
	case "read":
		data["content"] = ""
	case "receipt":
		data["processed"] = f.receipts[in.(Batch).BatchID]
	case "update":
		if f.failUpdate {
			return errors.New("write failed")
		}
		f.updates++
		f.receipts[in.(map[string]any)["batch"].(Batch).BatchID] = true
	case "summarize":
		f.summaryThrough = in.(map[string]any)["through"].(string)
	default:
		return errors.New("unexpected CLI method")
	}
	if out != nil {
		b, _ := json.Marshal(data)
		return json.Unmarshal(b, out)
	}
	return nil
}
func workerFixture(t *testing.T, cli CLI) (*Worker, *fakeSync, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	cfg := config.MemoryConfig{Enabled: true, Timezone: "UTC", Worker: config.MemoryWorkerConfig{PollIntervalSeconds: 300, TimeoutSeconds: 10, MaxBatches: 20, SummaryMaxChars: 8000}}
	chats := fakeChats{runs: []chat.RunSummary{{ChatID: "chat", RunID: "run", AgentKey: "agent", CompletedAt: now.UnixMilli(), FinishReason: "stop"}}, messages: []chat.MemoryMessage{{ID: "query", Role: "user", Content: "请使用中文", At: now.UnixMilli()}}}
	model := &fakeSync{}
	w := New(cfg, t.TempDir(), chats, cli, model, func(string) (string, bool) { return "project", true })
	return w, model, now
}
func TestWorkerReplayRestartAndDailyCutoff(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, m, now := workerFixture(t, cli)
	n, err := w.run(context.Background(), now)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if cli.summaryThrough != "2026-10-02" {
		t.Fatal(cli.summaryThrough)
	}
	next := New(w.cfg, w.stateDir, w.chats, cli, m, w.eligible)
	n, err = next.run(context.Background(), now.Add(24*time.Hour))
	if err != nil || n != 0 || m.calls != 2 || cli.updates != 1 {
		t.Fatal(n, err, m.calls, cli.updates)
	}
	if cli.summaryThrough != "2026-10-03" {
		t.Fatal(cli.summaryThrough)
	}
}

func TestWorkersRejectBelowMinimumCLI(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.1.1", "0.1.99"} {
		t.Run(version, func(t *testing.T) {
			cli := &fakeCLI{version: version, receipts: map[string]bool{}}
			w, syncer, now := workerFixture(t, cli)
			if _, err := w.run(context.Background(), now); err == nil || !strings.Contains(err.Error(), "0.2.0") {
				t.Fatal(err)
			}
			job := manualJob(w, "2026-10-03", "2026-10-03")
			if err := w.runRange(context.Background(), job); err == nil || !strings.Contains(err.Error(), "0.2.0") {
				t.Fatal(err)
			}
			if syncer.calls != 0 || cli.updates != 0 || cli.summaryThrough != "" {
				t.Fatal("old CLI reached config or maintenance", syncer.calls, cli.updates, cli.summaryThrough)
			}
		})
	}
}
func TestWorkerDoesNotAcknowledgeFailedWrite(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}, failUpdate: true}
	w, m, now := workerFixture(t, cli)
	if _, err := w.run(context.Background(), now); err == nil {
		t.Fatal("write failure ignored")
	}
	cli.failUpdate = false
	n, err := w.run(context.Background(), now)
	if err != nil || n != 1 || m.calls != 2 {
		t.Fatal(n, err, m.calls)
	}
}
func TestWorkerLeaseAndDisabledManualTrigger(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, _, now := workerFixture(t, cli)
	f, err := os.OpenFile(filepath.Join(w.stateDir, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = lockWorker(f); err != nil {
		t.Fatal(err)
	}
	if _, err = w.run(context.Background(), now); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatal(err)
	}
	w.cfg.Enabled = false
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	if _, err = w.Trigger(); err == nil {
		t.Fatal("disabled trigger accepted")
	}
	cancel()
	if err = w.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestEvidenceBatchIDsStableAndFailedAssistantExcluded(t *testing.T) {
	r := chat.RunSummary{ChatID: "c", RunID: "r", AgentKey: "a", FinishReason: "cancelled"}
	messages := []chat.MemoryMessage{{ID: "user", Role: "user", Content: strings.Repeat("中", 5000), At: 1791028800000}, {ID: "assistant", Role: "assistant", Content: "completed", At: 1791028800001}}
	a := makeBatches(r, "p", messages)
	b := makeBatches(r, "p", messages)
	if len(a) != len(b) || a[0].BatchID != b[0].BatchID {
		t.Fatal("unstable identity")
	}
	for _, batch := range a {
		for _, source := range batch.Sources {
			if source.Role != "user" || digest(source.Content) != source.ContentHash {
				t.Fatal(source)
			}
		}
	}
}
