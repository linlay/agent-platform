package chat

import "testing"

func TestStoredDefaultAccessKeepsFrozenApprovalWithoutChangingCallerInput(t *testing.T) {
	for _, frozen := range []string{"default", "ask_for_approval", "full_access"} {
		input := map[string]any{"accessLevel": "default", "message": "task"}
		line := queryLineFromMap(map[string]any{"accessLevel": frozen, "query": input}, "chat")
		want := frozen
		if frozen == "default" {
			want = "ask_for_approval"
		}
		if line.AccessLevel != want || line.LegacyAccessLevel != (frozen == "default") {
			t.Fatalf("frozen %q decoded as %+v", frozen, line)
		}
		if line.Query["accessLevel"] != "default" || input["accessLevel"] != "default" {
			t.Fatal("historical caller input was rewritten")
		}
	}
}
