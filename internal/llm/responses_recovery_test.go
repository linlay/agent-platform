package llm

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/modelclient"
	"strings"
	"testing"
)

func TestResponsesRecoveryOnlyCompletedPrefix(t *testing.T) {
	rejection := modelclient.ResponseError(400, []byte(`{"error":{"code":"invalid_encrypted_content","message":"invalid state"}}`))
	for _, pending := range []bool{false, true} {
		_, s := responseTestStream()
		s.provider.BaseURL = "https://example.test"
		s.provider.APIKey = "test"
		s.protocolConfig.EndpointPath = "/v1/responses"
		m := openAIMessage{Role: "assistant", Content: "completed", EncryptedReasoning: []contracts.ReasoningPart{{Type: "encrypted_text", ID: "rs", EncryptedText: "cipher"}}}
		if pending {
			m.Content = nil
			m.ToolCalls = []openAIToolCall{{ID: "call", Function: openAIFunctionCall{Name: "f", Arguments: "{}"}}}
		}
		s.messages = []openAIMessage{m, {Role: "user", Content: "continue"}}
		recovered := s.recoverResponsesState(rejection)
		if recovered == pending {
			t.Fatalf("pending=%v recovered=%v", pending, recovered)
		}
		if pending {
			if len(s.messages[0].EncryptedReasoning) != 1 {
				t.Fatal("active group lost state")
			}
		} else {
			if strings.Contains(string(s.modelCall.prepared.RequestBodyJSON), "cipher") || len(s.messages[0].EncryptedReasoning) != 0 {
				t.Fatal("invalid state retained")
			}
			if len(m.EncryptedReasoning) != 1 {
				t.Fatal("original message mutated")
			}
			if s.recoverResponsesState(rejection) {
				t.Fatal("recovery repeated")
			}
		}
	}
	_, s := responseTestStream()
	if s.recoverResponsesState(modelclient.ResponseError(400, []byte(`{"error":{"code":"invalid_request_error"}}`))) {
		t.Fatal("generic error recovered")
	}
}
