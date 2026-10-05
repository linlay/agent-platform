package memoryworker

import (
	"agent-platform/internal/models"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectionSnapshot(t *testing.T) {
	for _, p := range []struct{ protocol, path, wire, auth string }{{"OPENAI", "/chat/completions", "openai_chat", "bearer"}, {"OPENAI_RESPONSES", "/responses", "openai_responses", "bearer"}, {"ANTHROPIC", "/messages", "anthropic_messages", "api_key"}} {
		m := models.ModelDefinition{ModelID: "actual", Protocol: p.protocol, Timeout: 30}
		s, e := connectionSnapshot(m, models.ProviderDefinition{BaseURL: "https://example.test/v1", APIKey: "secret"}, 120)
		if e != nil {
			t.Fatal(e)
		}
		role := s["models"].(map[string]any)["extraction"].(map[string]any)
		if role["url"] != "https://example.test/v1"+p.path || role["model"] != "actual" || role["protocol"] != p.wire || role["timeoutSeconds"] != 30 {
			t.Fatal(role)
		}
		if role["auth"].(map[string]any)["type"] != p.auth {
			t.Fatal(role)
		}
	}
}
func TestFailedConfigurationNeverStartsBusinessWork(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, s, now := workerFixture(t, cli)
	s.fail = true
	if _, e := w.run(context.Background(), now); e == nil {
		t.Fatal("sync failure ignored")
	}
	if cli.updates != 0 || cli.summaryThrough != "" {
		t.Fatal("business work after failed sync")
	}
	s.fail = false
	if n, e := w.run(context.Background(), now); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

type cancellingCLI struct {
	fakeCLI
	started chan struct{}
}

func (c *cancellingCLI) Call(ctx context.Context, method string, in, out any) error {
	if method == "update" {
		close(c.started)
		<-ctx.Done()
		return ctx.Err()
	}
	return c.fakeCLI.Call(ctx, method, in, out)
}
func TestShutdownCancelsCLI(t *testing.T) {
	c := &cancellingCLI{fakeCLI: fakeCLI{receipts: map[string]bool{}}, started: make(chan struct{})}
	w, _, _ := workerFixture(t, c)
	b, _ := json.Marshal(checkpoint{Since: 1790899200000})
	if e := os.WriteFile(filepath.Join(w.stateDir, "checkpoint.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	if _, e := w.Trigger(); e != nil {
		t.Fatal(e)
	}
	wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	select {
	case <-c.started:
	case <-wait.Done():
		t.Fatal("CLI not started")
	}
	cancel()
	if e := w.Wait(wait); e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if c.updates != 0 {
		t.Fatal("cancelled update persisted")
	}
}
