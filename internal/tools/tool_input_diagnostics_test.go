package tools

import (
	"context"
	"strings"
	"testing"
)

func TestWebInputDiagnostics(t *testing.T) {
	_, r, bad := webControlString(map[string]any{"url": true}, "url", true)
	if !bad || !strings.Contains(r.Output, "boolean") || !strings.Contains(r.Output, "expected") {
		t.Fatal(r.Output)
	}
	_, _, r, bad = webControlBool(map[string]any{"visible": "secret-value"}, "visible")
	if !bad || strings.Contains(r.Output, "secret-value") || !strings.Contains(r.Output, "visible") || !strings.Contains(r.Output, "JSON boolean") || !strings.Contains(r.Output, "string") {
		t.Fatal(r.Output)
	}
	r, bad = webControlFields(map[string]any{"secret-key": "secret-value"}, "url")
	if !bad || strings.Contains(r.Output, "secret-") || !strings.Contains(r.Output, "url") {
		t.Fatal(r.Output)
	}
}
func TestDesktopEnvelopeRejectsBeforeDispatch(t *testing.T) {
	executor, exec, invoker := webControlTestRuntime(t, nil)
	for _, args := range []map[string]any{{"action": "secret-value"}, {"action": "kanban.updateIssue", "args": []any{}}, {"action": "kanban.updateIssue", "secret-key": "secret-value"}} {
		r, err := executor.invokeDesktopDomain(context.Background(), "desktop_kanban", args, exec)
		if err != nil || r.Error == "" || !strings.Contains(r.Output, "expected") || strings.Contains(r.Output, "secret-") {
			t.Fatalf("%s %v", r.Output, err)
		}
	}
	if len(invoker.requests) != 0 {
		t.Fatal("invalid input dispatched")
	}
}
