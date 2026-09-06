package conversationexport

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"agent-platform/internal/chat"
)

// Input is the complete pre-refactor history detail, not a reduced export model.
// Keeping both projections fixed makes their deliberately different filters visible.
func TestHistoricalDetailExportCompatibility(t *testing.T) {
	for _, owner := range []string{"agent", "team"} {
		t.Run(owner, func(t *testing.T) {
			prefix := "../chat/testdata/replay/" + owner
			raw, err := os.ReadFile(prefix + ".detail.json")
			if err != nil {
				t.Fatal(err)
			}
			var detail chat.Detail
			if err := json.Unmarshal(raw, &detail); err != nil {
				t.Fatal(err)
			}
			document, err := BuildSnapshotDocument(&chat.Summary{ChatID: detail.ChatID, ChatName: detail.ChatName, CreatedAt: testEpoch}, detail.Events, testEpoch+1000)
			if err != nil {
				t.Fatal(err)
			}
			markdown, err := RenderMarkdown(document.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for suffix, got := range map[string][]byte{".snapshot.json": document.JSON, ".md": markdown} {
				path := prefix + suffix
				if os.Getenv("UPDATE_REPLAY_GOLDEN") == "1" {
					if err := os.WriteFile(path, got, 0600); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("export differs from %s", path)
				}
			}
		})
	}
}
