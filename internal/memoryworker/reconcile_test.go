package memoryworker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type reconciliationCLI struct {
	calls            []string
	failAgent        bool
	failGlobal       bool
	reportAgentError bool
	cancel           context.CancelFunc
}

func (c *reconciliationCLI) Call(_ context.Context, method string, in, out any) error {
	data := map[string]any{}
	switch method {
	case "agents":
		c.calls = append(c.calls, method)
		data["agents"] = []map[string]string{{"agentKey": "first"}, {"agentKey": "second"}}
	case "summarize":
		params := in.(map[string]any)
		key := params["agentKey"].(string)
		c.calls = append(c.calls, method+":"+key)
		if len(params) != 4 || params["maxTokens"] != 1500 || params["maxLines"] != 150 || params["through"] != "2026-10-03" {
			return errors.New("wrong agent budget or cutoff")
		}
		if c.cancel != nil {
			c.cancel()
		}
		if c.failAgent && key == "first" {
			return errors.New("revision_conflict")
		}
	case "consolidate":
		c.calls = append(c.calls, method)
		params := in.(map[string]any)
		if len(params) != 5 || params["globalMaxTokens"] != 2000 || params["globalMaxLines"] != 200 || params["agentMaxTokens"] != 1500 || params["agentMaxLines"] != 150 || params["through"] != "2026-10-03" {
			return errors.New("wrong global budget or cutoff")
		}
		if c.failGlobal {
			return errors.New("model_error")
		}
		if c.reportAgentError {
			data["agentErrors"] = []map[string]string{{"agentKey": "second", "code": "over_budget"}}
		}
	default:
		return errors.New("unexpected method")
	}
	if out != nil {
		b, _ := json.Marshal(data)
		return json.Unmarshal(b, out)
	}
	return nil
}

func TestReconcileAllAgentsWithoutNewEvidence(t *testing.T) {
	cli := &reconciliationCLI{}
	w, _, _ := workerFixture(t, cli)
	for range 2 {
		if err := w.reconcile(context.Background(), "2026-10-03"); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"agents", "summarize:first", "summarize:second", "consolidate", "agents", "summarize:first", "summarize:second", "consolidate"}
	if !reflect.DeepEqual(cli.calls, want) {
		t.Fatal(cli.calls)
	}
}
func TestReconcileContinuesAfterAgentFailureAndReportsAllErrors(t *testing.T) {
	cli := &reconciliationCLI{failAgent: true, failGlobal: true}
	w, _, _ := workerFixture(t, cli)
	err := w.reconcile(context.Background(), "2026-10-03")
	if err == nil || !strings.Contains(err.Error(), "first: revision_conflict") || !strings.Contains(err.Error(), "model_error") {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cli.calls, []string{"agents", "summarize:first", "summarize:second", "consolidate"}) {
		t.Fatal(cli.calls)
	}
	cli.failAgent = false
	cli.failGlobal = false
	cli.reportAgentError = true
	if err = w.reconcile(context.Background(), "2026-10-03"); err == nil || !strings.Contains(err.Error(), "second: over_budget") {
		t.Fatal(err)
	}
}
func TestReconcileCancellationStopsFurtherCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cli := &reconciliationCLI{cancel: cancel}
	w, _, _ := workerFixture(t, cli)
	if err := w.reconcile(ctx, "2026-10-03"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cli.calls, []string{"agents", "summarize:first"}) {
		t.Fatal(cli.calls)
	}
}
func TestRequireMemxRejectsWrongProtocolAndTokenUnits(t *testing.T) {
	for _, values := range [][2]int{{0, 1}, {1, 1}, {2, 0}, {2, 2}} {
		err := requireMemx(context.Background(), func(_ context.Context, _ string, _ any, out any) error {
			b, _ := json.Marshal(map[string]any{"version": "0.2.0", "maintenanceVersion": 2, "configDirEnv": true, "protocolVersion": values[0], "tokenUnitVersion": values[1]})
			return json.Unmarshal(b, out)
		})
		if err == nil {
			t.Fatalf("accepted incompatible capabilities %v", values)
		}
	}
}
