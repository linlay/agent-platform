package kbx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/operationstate"
)

func awaitRefresh(t *testing.T, m *Manager, id string) refreshReceipt {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var r refreshReceipt
		if operationstate.Read(m.receiptRoot(), id, &r) == nil && r.Result.Status != "pending" && r.Result.Status != "running" {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("refresh did not terminate")
	return refreshReceipt{}
}
func stopManager(t *testing.T, m *Manager) {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if e := m.Close(ctx); e != nil {
		t.Error(e)
	}
}

type maintenanceFake struct {
	mu               sync.Mutex
	collection       map[string]any
	calls            []string
	started, release chan struct{}
	once             sync.Once
}

func (f *maintenanceFake) Run(ctx context.Context, db string, cfg []byte, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	f.mu.Unlock()
	if args[0] == "capabilities" {
		return []byte(`{"schemaVersion":1,"type":"kbx.capabilities","maintenance":{"schemaVersion":1,"structuredErrors":true,"registerWithoutIndex":true,"jsonCommands":["collection.add","collection.list","collection.show","status","update","embed"],"includes":{"expression":"globset","multiple":"braceAlternation","caseSensitive":true},"ignore":{"multiple":true,"caseSensitive":true,"builtinsOverrideIncludes":true},"pathUpdates":{"schemaVersion":1,"singleCollection":true,"filesOnly":true}}}`), nil
	}
	op := args[0]
	var data any
	if op == "collection" {
		op += "." + args[1]
		switch args[1] {
		case "list":
			items := []any{}
			if f.collection != nil {
				items = append(items, f.collection)
			}
			data = map[string]any{"collections": items}
		case "add":
			f.collection = map[string]any{"name": "workspace", "path": args[2]}
		case "set-pattern":
			f.collection["pattern"] = args[3]
		case "set-ignore":
			f.collection["ignore"] = args[3:]
		case "set-chunking":
			f.collection["chunking"] = map[string]any{"strategy": "window", "max_chars": 3600, "overlap_chars": 540}
		case "show":
			data = f.collection
		}
	}
	if op == "update" && f.started != nil {
		f.once.Do(func() {
			close(f.started)
			select {
			case <-f.release:
			case <-ctx.Done():
			}
		})
	}
	r := map[string]any{"schemaVersion": 1, "type": "kbx.maintenance.response", "operation": op, "status": "complete", "exitCode": 0, "data": data, "index": map[string]any{"selected": map[string]any{"documents": 1, "fullText": map[string]any{"ready": true}, "vector": map[string]any{"state": "absent"}}}}
	return json.Marshal(r)
}
func TestRefreshReceiptSurvivesWaitCancellationAndQueuedForce(t *testing.T) {
	m, l := newTestManager(t)
	m.options.Debounce = time.Hour
	f := &maintenanceFake{started: make(chan struct{}), release: make(chan struct{})}
	m.runner = f
	m.Start(context.Background())
	defer stopManager(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	first, e := m.Refresh(ctx, "docs", knowledge.RefreshOptions{})
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	next, e := m.Refresh(context.Background(), "docs", knowledge.RefreshOptions{Force: true})
	if e != nil {
		t.Fatal(e)
	}
	if next.RefreshID == first.RefreshID {
		t.Fatal("force merged into running ordinary refresh")
	}
	var persisted refreshReceipt
	if operationstate.Read(m.receiptRoot(), next.RefreshID, &persisted) != nil || !persisted.Force || persisted.Database != l.database {
		t.Fatal("acknowledged request not durable/scope bound")
	}
	close(f.release)
	for _, id := range []string{first.RefreshID, next.RefreshID} {
		if r := awaitRefresh(t, m, id); r.Result.Status != "completed" {
			t.Fatalf("%+v", r)
		}
	}
	if _, e = m.RefreshOperationStatus("other", first.RefreshID); e == nil {
		t.Fatal("foreign receipt accepted")
	}
	if state, e := m.RefreshOperationStatus("docs", first.RefreshID); e != nil || state != "completed" {
		t.Fatalf("history overwritten %s %v", state, e)
	}
}
func TestRestartInterruptsOnlyUnfinishedReceipts(t *testing.T) {
	m, _ := newTestManager(t)
	m.runner = &maintenanceFake{}
	m.options.Debounce = time.Hour
	for _, state := range []string{"running", "completed"} {
		r := refreshReceipt{Result: knowledge.RefreshResult{AgentKey: "docs", RefreshID: state, Status: state}}
		if e := operationstate.Write(m.receiptRoot(), state, r); e != nil {
			t.Fatal(e)
		}
	}
	m.Start(context.Background())
	defer stopManager(t, m)
	for id, want := range map[string]string{"running": "interrupted", "completed": "completed"} {
		got, e := m.RefreshOperationStatus("docs", id)
		if e != nil || got != want {
			t.Fatalf("%s %s %v", id, got, e)
		}
	}
	other := NewManager(m.options, m.agents, nil)
	other.Start(context.Background())
	defer stopManager(t, other)
	if other.startError == nil {
		t.Fatal("second scheduler acquired same runtime lock")
	}
}
func TestMaintenancePartialAndNonzeroJSONAreNotSuccess(t *testing.T) {
	for _, status := range []string{"partial", "failed"} {
		t.Run(status, func(t *testing.T) {
			m, l := newTestManager(t)
			m.runner = runFunc(func(context.Context, string, []byte, ...string) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"schemaVersion":1,"type":"kbx.maintenance.response","operation":"update","status":%q,"exitCode":1,"error":{"code":"SOURCE_NOT_FOUND","message":"missing source"}}`, status)), fmt.Errorf("process exited 1")
			})
			r, e := m.maintenance(context.Background(), l, []byte("{}"), "update", "update")
			if e == nil || r.Error == nil || r.Error.Code != "SOURCE_NOT_FOUND" {
				t.Fatalf("lost JSON failure: %+v %v", r, e)
			}
		})
	}
}
func TestMaintenanceGlobsFailClosed(t *testing.T) {
	for _, p := range []string{"**/*.md", "docs/**/*.txt", "private/**", "file,one.md"} {
		if _, e := maintenancePattern(p); e != nil {
			t.Fatal(e)
		}
	}
	for _, p := range []string{"*.md", "docs/*.md", "a?b.md", "**/private*.txt"} {
		if _, e := maintenancePattern(p); e == nil {
			t.Fatal("unsafe glob accepted", p)
		}
	}
}

func TestLivePlatformLifecycle(t *testing.T) {
	bin := os.Getenv("KBX_ACCEPTANCE_BIN")
	if bin == "" {
		t.Skip("set KBX_ACCEPTANCE_BIN")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, e := builtins.ConfigureProcessPath(); e != nil {
		t.Fatal(e)
	}
	m, l := newTestManager(t)
	// Remove the empty fake DB, allowing the production lifecycle to create it.
	if e := os.Remove(l.database); e != nil {
		t.Fatal(e)
	}
	m.options.Debounce = 30 * time.Millisecond
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(l.spec.WorkspaceRoot, name)
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("public.md", "# Orchard\nOrchard approval requires two reviewers.")
	write(".private/secret.md", "Never index hidden secrets.")
	m.Start(context.Background())
	defer stopManager(t, m)
	waitFiles := func(n int) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			s, e := m.Status("docs")
			if e == nil && s.Files == n && !s.Indexing && s.LastIndexedAt != nil && s.Error == "" {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		s, _ := m.Status("docs")
		t.Fatalf("files want %d: %+v", n, s)
	}
	waitFiles(1)
	hits, e := m.Search(context.Background(), "docs", "Orchard", knowledge.SearchOptions{})
	if e != nil || len(hits.Results) == 0 {
		t.Fatalf("search %+v %v", hits, e)
	}
	evidence, e := m.Read("docs", knowledge.ReadOptions{ChunkID: hits.Results[0].ChunkID})
	if e != nil || !strings.Contains(evidence.Content, "two reviewers") {
		t.Fatalf("evidence %+v %v", evidence, e)
	}
	write("new.md", "Orchard knowledge added.")
	waitFiles(2)
	if e = os.Remove(filepath.Join(l.spec.WorkspaceRoot, "public.md")); e != nil {
		t.Fatal(e)
	}
	waitFiles(1)
	r, e := m.Refresh(context.Background(), "docs", knowledge.RefreshOptions{Force: true})
	if e != nil {
		t.Fatal(e)
	}
	if receipt := awaitRefresh(t, m, r.RefreshID); receipt.Result.Status != "completed" {
		t.Fatalf("%+v", receipt)
	}
	files, e := m.Files("docs", knowledge.FilesOptions{HeadLimit: 0})
	if e != nil || len(files.Results) != 1 || files.Results[0].Path != "new.md" {
		t.Fatalf("inventory %+v %v", files, e)
	}
}

func TestLivePlatformEmbeddingExcludesAndFailure(t *testing.T) {
	bin := os.Getenv("KBX_ACCEPTANCE_BIN")
	if bin == "" {
		t.Skip("set KBX_ACCEPTANCE_BIN")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, e := builtins.ConfigureProcessPath(); e != nil {
		t.Fatal(e)
	}
	t.Setenv("NO_PROXY", "127.0.0.1,localhost,::1")
	t.Setenv("no_proxy", "127.0.0.1,localhost,::1")
	var broken atomic.Bool
	var requests atomic.Int32
	var leaked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input json.RawMessage }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.Contains(string(in.Input), "EXCLUDED_SECRET") {
			leaked.Store(true)
		}
		requests.Add(1)
		if broken.Load() {
			http.Error(w, "fixture failure", 400)
			return
		}
		var texts []string
		if json.Unmarshal(in.Input, &texts) != nil {
			var s string
			_ = json.Unmarshal(in.Input, &s)
			texts = []string{s}
		}
		data := []any{}
		for i := range texts {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0, 0, 0, 0, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	m, _ := newTestManager(t)
	m.options.Debounce = time.Hour
	m.options.DefaultEmbeddingModelKey = "fixture"
	m.models = fixtureModels{server.URL}
	source := m.agents.(testSource)
	spec := source["docs"]
	spec.Config.Exclude = append(spec.Config.Exclude, "private/**")
	source["docs"] = spec
	root := spec.WorkspaceRoot
	if e := os.MkdirAll(filepath.Join(root, "private"), 0700); e != nil {
		t.Fatal(e)
	}
	for name, body := range map[string]string{"public.md": "Orchard approvals require two reviewers.", "private/secret.md": "EXCLUDED_SECRET"} {
		if e := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	m.Start(context.Background())
	defer stopManager(t, m)
	refresh := func(force bool) refreshReceipt {
		t.Helper()
		r, e := m.Refresh(context.Background(), "docs", knowledge.RefreshOptions{Force: force})
		if e != nil {
			t.Fatal(e)
		}
		return awaitRefresh(t, m, r.RefreshID)
	}
	if r := refresh(false); r.Result.Status != "completed" {
		t.Fatalf("initial: %+v", r)
	}
	if requests.Load() == 0 || leaked.Load() {
		t.Fatal("embedding absent or exclusion leaked")
	}
	s, _ := m.Status("docs")
	if s.Indexes == nil || !s.Indexes.Vector.Ready || s.Files != 1 {
		t.Fatalf("vector not ready: %+v", s)
	}
	broken.Store(true)
	if e := os.WriteFile(filepath.Join(root, "public.md"), []byte("Orchard now requires three reviewers."), 0600); e != nil {
		t.Fatal(e)
	}
	if r := refresh(false); r.Result.Status != "failed" || !strings.Contains(r.Result.Error, "MODEL_FAILED") {
		t.Fatalf("model failure claimed success: %+v", r)
	}
	s, _ = m.Status("docs")
	if !s.Degraded || s.Indexes == nil || !s.Indexes.FTS.Ready {
		t.Fatalf("fulltext lost after embedding failure: %+v", s)
	}
	broken.Store(false)
	result, e := m.Search(context.Background(), "docs", "Orchard", knowledge.SearchOptions{})
	if e != nil || len(result.Results) == 0 {
		t.Fatalf("lexical fallback: %+v %v", result, e)
	}
	if r := refresh(true); r.Result.Status != "completed" {
		t.Fatalf("force: %+v", r)
	}
	if leaked.Load() {
		t.Fatal("excluded document reached embedding")
	}
}

func TestChangesDuringRefreshRemainForNextBatch(t *testing.T) {
	m, _ := newTestManager(t)
	m.options.Debounce = 20 * time.Millisecond
	f := &maintenanceFake{started: make(chan struct{}), release: make(chan struct{})}
	m.runner = f
	m.Start(context.Background())
	defer stopManager(t, m)
	r, e := m.Refresh(context.Background(), "docs", knowledge.RefreshOptions{})
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.started:
	case <-time.After(3 * time.Second):
		t.Fatal("no first batch")
	}
	m.mu.Lock()
	w := m.workers["docs"]
	m.mu.Unlock()
	w.changed("during.md", false)
	close(f.release)
	if receipt := awaitRefresh(t, m, r.RefreshID); receipt.Result.Status != "completed" {
		t.Fatalf("%+v", receipt)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		calls := append([]string{}, f.calls...)
		f.mu.Unlock()
		for _, call := range calls {
			if strings.Contains(call, "--paths-from") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("change observed during running batch was lost")
}
