package server

import (
	"testing"

	"agent-platform-runner-go/internal/chat"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/stream"
)

func TestRunEventProcessorDecoratesTerminalUsage(t *testing.T) {
	eventTypes := []string{"run.complete", "run.error", "run.cancel"}
	for _, eventType := range eventTypes {
		t.Run(eventType, func(t *testing.T) {
			runUsage := chat.UsageData{}
			processor := &runEventProcessor{
				chatUsage: chat.UsageData{
					PromptTokens:          100,
					CompletionTokens:      50,
					TotalTokens:           150,
					CachedTokens:          20,
					ReasoningTokens:       10,
					PromptCacheHitTokens:  20,
					PromptCacheMissTokens: 80,
				},
				runUsage: &runUsage,
			}
			data := &stream.EventData{
				Type: eventType,
				Payload: map[string]any{
					"runId": "run-usage",
					"usage": map[string]any{
						"promptTokens":     7,
						"completionTokens": 3,
						"totalTokens":      10,
						"promptTokensDetails": map[string]any{
							"cachedTokens": 5,
						},
						"completionTokensDetails": map[string]any{
							"reasoningTokens": 2,
						},
						"promptCacheHitTokens":  5,
						"promptCacheMissTokens": 2,
					},
					"chatUsage": map[string]any{
						"promptTokens":     100,
						"completionTokens": 50,
						"totalTokens":      150,
					},
				},
			}

			processor.decorate(data)

			if _, ok := data.Payload["chatUsage"]; ok {
				t.Fatalf("terminal event should not carry top-level chatUsage: %#v", data.Payload)
			}
			usage, _ := data.Payload["usage"].(map[string]any)
			if usage == nil {
				t.Fatalf("expected nested usage payload, got %#v", data.Payload)
			}
			run, _ := usage["run"].(map[string]any)
			if AnyIntNode(run["promptTokens"]) != 7 || AnyIntNode(run["completionTokens"]) != 3 || AnyIntNode(run["totalTokens"]) != 10 {
				t.Fatalf("unexpected run usage %#v", usage)
			}
			runPromptDetails, _ := run["promptTokensDetails"].(map[string]any)
			runCompletionDetails, _ := run["completionTokensDetails"].(map[string]any)
			if AnyIntNode(runPromptDetails["cachedTokens"]) != 5 || AnyIntNode(runCompletionDetails["reasoningTokens"]) != 2 ||
				AnyIntNode(run["promptCacheHitTokens"]) != 5 || AnyIntNode(run["promptCacheMissTokens"]) != 2 {
				t.Fatalf("unexpected run detailed usage %#v", usage)
			}
			chatUsage, _ := usage["chat"].(map[string]any)
			if AnyIntNode(chatUsage["promptTokens"]) != 107 || AnyIntNode(chatUsage["completionTokens"]) != 53 || AnyIntNode(chatUsage["totalTokens"]) != 160 {
				t.Fatalf("unexpected chat usage %#v", usage)
			}
			chatPromptDetails, _ := chatUsage["promptTokensDetails"].(map[string]any)
			chatCompletionDetails, _ := chatUsage["completionTokensDetails"].(map[string]any)
			if AnyIntNode(chatPromptDetails["cachedTokens"]) != 25 || AnyIntNode(chatCompletionDetails["reasoningTokens"]) != 12 ||
				AnyIntNode(chatUsage["promptCacheHitTokens"]) != 25 || AnyIntNode(chatUsage["promptCacheMissTokens"]) != 82 {
				t.Fatalf("unexpected chat detailed usage %#v", usage)
			}
		})
	}
}

func TestRunEventProcessorOmitsTerminalUsageWhenUnknown(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		chatUsage: chat.UsageData{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
		runUsage: &runUsage,
	}
	data := &stream.EventData{
		Type: "run.complete",
		Payload: map[string]any{
			"runId":     "run-usage",
			"chatUsage": map[string]any{"totalTokens": 150},
		},
	}

	processor.decorate(data)

	if _, ok := data.Payload["usage"]; ok {
		t.Fatalf("did not expect usage without known run tokens: %#v", data.Payload)
	}
	if _, ok := data.Payload["chatUsage"]; ok {
		t.Fatalf("terminal event should not carry top-level chatUsage: %#v", data.Payload)
	}
}
