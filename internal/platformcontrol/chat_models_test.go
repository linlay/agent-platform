package platformcontrol

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/models"
)

func TestChatQueryModelsWithOnlyTaskControl(t *testing.T) {
	root := t.TempDir()
	discoveryWrite(t, filepath.Join(root, "providers", "p.yml"), "key: p\nbaseUrl: https://private.invalid\napiKey: secret-token\n")
	discoveryWrite(t, filepath.Join(root, "models", "chat.yml"), "key: chat\nname: Chat Model\nprovider: p\nmodelId: provider-chat\ntype: chat\nisReasoner: true\nreasoningEfforts:\n  - LOW\n  - HIGH\nisVision: true\nmaxInputTokens: 32000\n")
	discoveryWrite(t, filepath.Join(root, "models", "embed.yml"), "key: embed\nprovider: p\nmodelId: provider-embed\ntype: embedding\n")
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	h := NewToolHandler(config.Config{}, nil, nil).ConfigureControl(nil, nil, registry)
	exec := controlExecution()
	delete(exec.Session.ConnectorDirs, connector.PlatformControlConnectorID)
	for tool, owner := range exec.Session.NativeConnectorTools {
		if owner != connector.TaskControlConnectorID {
			delete(exec.Session.NativeConnectorTools, tool)
		}
	}
	result, err := h.Invoke(context.Background(), "chat_query", map[string]any{"action": "models"}, exec)
	if err != nil || result.Error != "" {
		t.Fatalf("%+v %v", result, err)
	}
	items := result.Structured["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items=%#v", items)
	}
	item := items[0].(map[string]any)
	for key, want := range map[string]any{"modelKey": "chat", "name": "Chat Model", "provider": "p", "modelId": "provider-chat", "isReasoner": true, "isVision": true, "contextWindow": float64(32000)} {
		if item[key] != want {
			t.Fatalf("%s=%v want %v", key, item[key], want)
		}
	}
	efforts := item["reasoningEfforts"].([]any)
	if len(efforts) != 2 || efforts[0] != "LOW" || efforts[1] != "HIGH" {
		t.Fatalf("efforts=%v", efforts)
	}
	raw, _ := json.Marshal(result.Structured)
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "private.invalid") {
		t.Fatalf("private model configuration leaked: %s", raw)
	}
}
