package tools

import (
	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"context"
	"testing"
)

func TestKanbanToolsRequireTheirOwnConnectorAtRouter(t *testing.T) {
	names := (connector.Package{Manifest: connector.Manifest{ID: connector.KanbanControlConnectorID, Type: "native"}}).NativeTools()
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			backend := &recordingPolicyBackend{defs: []api.ToolDetailResponse{{Name: name}}}
			router := mustNewToolRouter(t, backend, nil, nil, nil)
			handler := &captureNamedToolHandler{names: []string{name}}
			if err := router.RegisterHandler(handler); err != nil {
				t.Fatal(err)
			}
			for _, owner := range []string{"", connector.PlatformControlConnectorID, connector.KanbanControlConnectorID} {
				e := &contracts.ExecutionContext{}
				e.Session.ToolNames = []string{name}
				e.Session.NativeConnectorTools = map[string]string{name: owner}
				e.Session.ConnectorDirs = map[string]string{owner: "/mounted"}
				handler.invoked = ""
				result, err := router.Invoke(context.Background(), name, map[string]any{}, e)
				if err != nil {
					t.Fatal(err)
				}
				if owner != connector.KanbanControlConnectorID {
					if result.Error != "connector_not_mounted" || handler.invoked != "" {
						t.Fatalf("wrong mount accepted: %+v", result)
					}
				} else if handler.invoked != name {
					t.Fatalf("task mount did not dispatch: %+v", result)
				}
			}
		})
	}
}
