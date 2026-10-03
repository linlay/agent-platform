package toolpolicy

import (
	"agent-platform/internal/connector"
	"strings"
	"testing"
)

func TestDesktopControlScheduling(t *testing.T) {
	for _, a := range connector.ControlActions() {
		if !strings.HasPrefix(a.Tool, "desktop_") {
			continue
		}
		op, ok := LookupOperation(a.Tool, a.Action)
		if !ok || !op.Barrier || op.AllowsExecutionPolicy("read_only") {
			t.Fatalf("unsafe policy: %+v", op)
		}
	}
}
