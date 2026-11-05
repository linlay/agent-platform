package toolpolicy

import (
	"agent-platform/internal/api"
	"testing"
)

func TestChatSteerCannotMasqueradeAsReadOnly(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		if AllowsReadOnly(api.ToolDetailResponse{Name: "chat_send", Meta: map[string]any{"sourceCategory": "platform", "readOnly": readOnly}}, true) {
			t.Fatal("BTW permitted task mutation")
		}
	}
}
