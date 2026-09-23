package llm

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/modelresponses"
	"agent-platform/internal/models"
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in smoke test: credentials are supplied only through the process env.
// Calls a synthetic function; it never reads user files or executes commands.
func TestResponsesLiveStatelessToolRoundTrip(t *testing.T) {
	key := os.Getenv("AP_TEST_RESPONSES_KEY")
	base := os.Getenv("AP_TEST_RESPONSES_URL")
	if key == "" || base == "" {
		t.Skip("set AP_TEST_RESPONSES_KEY/URL for opt-in live test")
	}
	p := &responsesProtocol{engine: &LLMAgentEngine{}}
	params := protocolStreamParams{provider: models.ProviderDefinition{BaseURL: base, APIKey: key}, model: models.ModelDefinition{Key: "live-luna", ModelID: "gpt-6-luna", Protocol: modelresponses.Protocol}, protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/responses"}, stageSettings: contracts.StageSettings{ReasoningEnabled: true, ReasoningEffort: "MEDIUM", MaxOutputTokens: 2048}, modelTimeout: 60 * time.Second, messages: []openAIMessage{{Role: "user", Content: "First solve this puzzle: find the smallest positive integer x such that x mod 7 = 5, x mod 11 = 8, x mod 13 = 3. Think carefully and check the result. Then call lookup_test_value exactly once with that integer as the key string, and finally report the returned value. This is a synthetic connectivity test."}}, toolSpecs: []openAIToolSpec{{Type: "function", Function: openAIToolDefinition{Name: "lookup_test_value", Description: "Return a synthetic test value", Parameters: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []string{"key"}}}}}, toolChoice: "auto"}
	call := func() modelresponses.Response {
		req, err := p.PrepareRequest(params)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		turn, err := p.OpenStream(ctx, params, req)
		if err != nil {
			t.Fatal(err)
		}
		defer turn.body.Close()
		if turn.cancel != nil {
			defer turn.cancel()
		}
		scan := bufio.NewScanner(turn.body)
		scan.Buffer(make([]byte, 4096), 8*1024*1024)
		for scan.Scan() {
			line := scan.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var e struct {
				Type     string                  `json:"type"`
				Response modelresponses.Response `json:"response"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &e) != nil {
				continue
			}
			if e.Type == "response.completed" {
				return e.Response
			}
			if e.Type == "response.failed" || e.Type == "error" {
				t.Fatalf("upstream terminal %s", e.Type)
			}
		}
		t.Fatalf("no completed response (scanner error %v)", scan.Err())
		return modelresponses.Response{}
	}
	first := call()
	for _, item := range first.Output {
		t.Logf("first output type=%s encrypted=%t", item.Type, item.EncryptedContent != "")
	}
	if first.Usage != nil {
		t.Logf("reasoning tokens=%d", first.Usage.OutputDetails.ReasoningTokens)
	}
	message := openAIMessage{Role: "assistant", OriginModelKey: "live-luna"}
	for _, i := range first.Output {
		if i.Type == "reasoning" && i.EncryptedContent != "" {
			message.EncryptedReasoning = append(message.EncryptedReasoning, i.Reasoning())
		}
		if i.Type == "function_call" {
			message.ToolCalls = append(message.ToolCalls, openAIToolCall{ID: i.CallID, Type: "function", Function: openAIFunctionCall{Name: i.Name, Arguments: i.Arguments}})
		}
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("expected one synthetic call, got %d", len(message.ToolCalls))
	}
	// Serialize to the same reasoning_content shape and reconstruct, as on restart.
	persisted := modelMessagesToMaps([]openAIMessage{message})
	message = rawMessageToOpenAI(persisted[0], false)
	params.messages = append(params.messages, message, openAIMessage{Role: "tool", ToolCallID: message.ToolCalls[0].ID, Content: `{"value":"ALPHA-42"}`})
	params.toolChoice = "none"
	second := call()
	if second.Status != "completed" || !strings.Contains(second.Text(), "ALPHA-42") {
		t.Fatalf("unexpected final status/text: %s %q", second.Status, second.Text())
	}
	t.Logf("stateless tool round trip completed: firstID=%s secondID=%s reasoningItems=%d answer=%s", first.ID, second.ID, len(message.EncryptedReasoning), second.Text())
}
