package llm

import (
	"strings"
	"testing"

	"agent-platform/internal/contracts"
)

func TestPrepareToolCallRejectsLegacyBeforeApproval(t *testing.T) {
	for _, tc := range []struct{ tool, args string }{
		{"file_write", `{"file_path":"/outside/secret","filePath":"/other","content":"secret-value"}`},
		{"file_grep", `{"pattern":"x","-i":false}`},
		{"image_generate", `{"prompt":"x","mask":{"source_type":"file_path","value":"/outside/mask.png"}}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			// No executor or approval services: rejection must happen before either is needed.
			s := &llmRunStream{engine: &LLMAgentEngine{}, execCtx: &contracts.ExecutionContext{}}
			call := openAIToolCall{ID: "call", Type: "function", Function: openAIFunctionCall{Name: tc.tool, Arguments: tc.args}}
			invocation, _, message := s.prepareToolCall(call)
			if invocation != nil || message == nil || !strings.Contains(contracts.AnyStringNode(message.Content), "unsupported argument") {
				t.Fatalf("not rejected before preparation: %#v %#v", invocation, message)
			}
			if strings.Contains(contracts.AnyStringNode(message.Content), "secret-value") {
				t.Fatal("error leaked argument value")
			}
			if call.Function.Arguments != tc.args {
				t.Fatal("raw arguments changed")
			}
		})
	}
}
