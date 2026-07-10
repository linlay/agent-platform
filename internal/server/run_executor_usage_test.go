package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

func TestRunEventProcessorDecoratesTerminalUsage(t *testing.T) {
	eventTypes := []string{"run.complete", "run.error", "run.cancel"}
	for _, eventType := range eventTypes {
		t.Run(eventType, func(t *testing.T) {
			runUsage := chat.UsageData{}
			processor := &runEventProcessor{
				chatUsage: chat.UsageData{
					PromptTokens:           100,
					CompletionTokens:       50,
					TotalTokens:            150,
					CachedTokens:           20,
					ReasoningTokens:        10,
					PromptCacheHitTokens:   20,
					PromptCacheMissTokens:  80,
					LlmChatCompletionCount: 4,
					ToolCallCount:          6,
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
							"cacheHitTokens":  5,
							"cacheMissTokens": 2,
						},
						"completionTokensDetails": map[string]any{
							"reasoningTokens": 2,
						},
						"llmChatCompletionCount": 1,
						"toolCallCount":          2,
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
			if AnyIntNode(runPromptDetails["cacheHitTokens"]) != 5 || AnyIntNode(runPromptDetails["cacheMissTokens"]) != 2 ||
				AnyIntNode(runCompletionDetails["reasoningTokens"]) != 2 {
				t.Fatalf("unexpected run detailed usage %#v", usage)
			}
			if AnyIntNode(run["llmChatCompletionCount"]) != 1 {
				t.Fatalf("unexpected run llm chat completion count %#v", usage)
			}
			if AnyIntNode(run["toolCallCount"]) != 2 {
				t.Fatalf("unexpected run tool call count %#v", usage)
			}
			chatUsage, _ := usage["chat"].(map[string]any)
			if AnyIntNode(chatUsage["promptTokens"]) != 107 || AnyIntNode(chatUsage["completionTokens"]) != 53 || AnyIntNode(chatUsage["totalTokens"]) != 160 {
				t.Fatalf("unexpected chat usage %#v", usage)
			}
			chatPromptDetails, _ := chatUsage["promptTokensDetails"].(map[string]any)
			chatCompletionDetails, _ := chatUsage["completionTokensDetails"].(map[string]any)
			if AnyIntNode(chatPromptDetails["cacheHitTokens"]) != 25 || AnyIntNode(chatPromptDetails["cacheMissTokens"]) != 82 ||
				AnyIntNode(chatCompletionDetails["reasoningTokens"]) != 12 {
				t.Fatalf("unexpected chat detailed usage %#v", usage)
			}
			if AnyIntNode(chatUsage["llmChatCompletionCount"]) != 5 {
				t.Fatalf("unexpected chat llm chat completion count %#v", usage)
			}
			if AnyIntNode(chatUsage["toolCallCount"]) != 8 {
				t.Fatalf("unexpected chat tool call count %#v", usage)
			}
		})
	}
}

func TestSystemInitQueryIsNotPublishedToClients(t *testing.T) {
	event := stream.EventData{
		Type: "request.query",
		Payload: map[string]any{
			"kind":   "system-init",
			"hidden": true,
			"system": map[string]any{"agentKey": "agent", "cacheKey": "react:main", "fingerprint": "sha256:test"},
		},
	}
	if shouldPublishClientEvent(event) {
		t.Fatalf("system-init query must remain storage-only: %#v", event)
	}
	visible := clientVisibleEventData(stream.EventData{
		Type: "request.query",
		Payload: map[string]any{
			"message": "hello",
			"system":  map[string]any{"secret": true},
		},
	})
	if _, ok := visible.Payload["system"]; ok || visible.String("message") != "hello" {
		t.Fatalf("client query filtering failed: %#v", visible)
	}
}

func TestRunEventProcessorKeepsTerminalUsageWhenOnlyLLMChatCompletionCountKnown(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		runUsage: &runUsage,
	}
	data := &stream.EventData{
		Type: "run.error",
		Payload: map[string]any{
			"runId": "run-usage",
			"usage": map[string]any{
				"llmChatCompletionCount": 1,
			},
		},
	}

	processor.decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	if AnyIntNode(run["llmChatCompletionCount"]) != 1 {
		t.Fatalf("expected terminal usage with llmChatCompletionCount, got %#v", data.Payload)
	}
}

func TestRunEventProcessorKeepsTerminalUsageWhenOnlyToolCallCountKnown(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		runUsage: &runUsage,
	}
	data := &stream.EventData{
		Type: "run.error",
		Payload: map[string]any{
			"runId": "run-usage",
			"usage": map[string]any{
				"toolCallCount": 2,
			},
		},
	}

	processor.decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	if AnyIntNode(run["toolCallCount"]) != 2 {
		t.Fatalf("expected terminal usage with toolCallCount, got %#v", data.Payload)
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

func TestRunEventProcessorDecoratesUsageSnapshotWithChatUsage(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		chatUsage: chat.UsageData{
			PromptTokens:           100,
			CompletionTokens:       50,
			TotalTokens:            150,
			CachedTokens:           20,
			ReasoningTokens:        10,
			PromptCacheHitTokens:   20,
			PromptCacheMissTokens:  80,
			LlmChatCompletionCount: 4,
			ToolCallCount:          6,
		},
		runUsage: &runUsage,
	}
	data := &stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"runId":  "run-usage",
			"chatId": "chat-usage",
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     7,
					"completionTokens": 3,
					"totalTokens":      10,
				},
				"run": map[string]any{
					"promptTokens":     7,
					"completionTokens": 3,
					"totalTokens":      10,
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  5,
						"cacheMissTokens": 2,
					},
					"completionTokensDetails": map[string]any{
						"reasoningTokens": 2,
					},
					"llmChatCompletionCount": 1,
					"toolCallCount":          2,
				},
			},
		},
	}

	processor.decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	chatUsage, _ := usage["chat"].(map[string]any)
	if AnyIntNode(chatUsage["promptTokens"]) != 107 || AnyIntNode(chatUsage["completionTokens"]) != 53 || AnyIntNode(chatUsage["totalTokens"]) != 160 {
		t.Fatalf("unexpected chat usage %#v", usage)
	}
	chatPromptDetails, _ := chatUsage["promptTokensDetails"].(map[string]any)
	chatCompletionDetails, _ := chatUsage["completionTokensDetails"].(map[string]any)
	if AnyIntNode(chatPromptDetails["cacheHitTokens"]) != 25 || AnyIntNode(chatPromptDetails["cacheMissTokens"]) != 82 ||
		AnyIntNode(chatCompletionDetails["reasoningTokens"]) != 12 {
		t.Fatalf("unexpected detailed chat usage %#v", usage)
	}
	if AnyIntNode(chatUsage["llmChatCompletionCount"]) != 5 {
		t.Fatalf("unexpected chat llm completion count %#v", usage)
	}
	if AnyIntNode(chatUsage["toolCallCount"]) != 8 {
		t.Fatalf("unexpected chat tool call count %#v", usage)
	}
}

func TestRunEventProcessorKeepsZeroDetailedUsageInSnapshotAggregates(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		runUsage: &runUsage,
	}
	data := &stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"runId":  "run-zero-details",
			"chatId": "chat-zero-details",
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     10,
					"completionTokens": 2,
					"totalTokens":      12,
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  0,
						"cacheMissTokens": 10,
					},
					"completionTokensDetails": map[string]any{
						"reasoningTokens": 0,
					},
					"llmChatCompletionCount": 1,
				},
			},
		},
	}

	processor.decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	for _, key := range []string{"run", "chat"} {
		stats, _ := usage[key].(map[string]any)
		promptDetails, _ := stats["promptTokensDetails"].(map[string]any)
		if _, ok := promptDetails["cacheHitTokens"]; !ok || AnyIntNode(promptDetails["cacheHitTokens"]) != 0 {
			t.Fatalf("expected %s cacheHitTokens=0, got %#v", key, stats)
		}
		if AnyIntNode(promptDetails["cacheMissTokens"]) != 10 {
			t.Fatalf("expected %s cacheMissTokens=10, got %#v", key, stats)
		}
		completionDetails, _ := stats["completionTokensDetails"].(map[string]any)
		if _, ok := completionDetails["reasoningTokens"]; !ok || AnyIntNode(completionDetails["reasoningTokens"]) != 0 {
			t.Fatalf("expected %s reasoningTokens=0, got %#v", key, stats)
		}
	}
}

func TestRunEventProcessorNormalizesCumulativeUsageSnapshotCacheMissTokens(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		runUsage: &runUsage,
	}
	data := &stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"runId":  "run-minimax-usage",
			"chatId": "chat-minimax-usage",
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     8751,
					"completionTokens": 1461,
					"totalTokens":      10212,
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  8059,
						"cacheMissTokens": 692,
					},
				},
				"run": map[string]any{
					"promptTokens":     16929,
					"completionTokens": 1670,
					"totalTokens":      18599,
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  8059,
						"cacheMissTokens": 692,
					},
				},
			},
		},
	}

	processor.decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	current, _ := usage["current"].(map[string]any)
	currentPromptDetails, _ := current["promptTokensDetails"].(map[string]any)
	if AnyIntNode(currentPromptDetails["cacheHitTokens"]) != 8059 || AnyIntNode(currentPromptDetails["cacheMissTokens"]) != 692 {
		t.Fatalf("expected current usage details to remain unchanged, got %#v", usage)
	}
	run, _ := usage["run"].(map[string]any)
	runPromptDetails, _ := run["promptTokensDetails"].(map[string]any)
	if AnyIntNode(run["promptTokens"]) != 16929 || AnyIntNode(runPromptDetails["cacheHitTokens"]) != 8059 ||
		AnyIntNode(runPromptDetails["cacheMissTokens"]) != 8870 {
		t.Fatalf("expected run cache miss to be normalized from cumulative prompt tokens, got %#v", usage)
	}
	chatUsage, _ := usage["chat"].(map[string]any)
	chatPromptDetails, _ := chatUsage["promptTokensDetails"].(map[string]any)
	if AnyIntNode(chatUsage["promptTokens"]) != 16929 || AnyIntNode(chatPromptDetails["cacheHitTokens"]) != 8059 ||
		AnyIntNode(chatPromptDetails["cacheMissTokens"]) != 8870 {
		t.Fatalf("expected chat cache miss to be normalized from cumulative prompt tokens, got %#v", usage)
	}
}

func TestRunEventProcessorDecoratesUsageSnapshotWithEstimatedCost(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		billing:  config.BillingConfig{Currency: "CNY"},
		models:   writeUsageCostRegistry(t),
		runUsage: &runUsage,
	}
	data := &stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"modelKey":         "mock-model",
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  200_000,
						"cacheMissTokens": 800_000,
					},
				},
				"run": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
				},
			},
		},
	}

	processor.decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	current, _ := usage["current"].(map[string]any)
	if current["modelKey"] != "mock-model" {
		t.Fatalf("expected current modelKey, got %#v", current)
	}
	currentCost, _ := current["estimatedCost"].(map[string]any)
	if currentCost["currency"] != "CNY" || floatValue(currentCost["inputCacheHit"]) != 0.005 ||
		floatValue(currentCost["inputCacheMiss"]) != 2.4 || floatValue(currentCost["output"]) != 6 ||
		floatValue(currentCost["total"]) != 8.405 {
		t.Fatalf("unexpected current estimated cost %#v", currentCost)
	}
	run, _ := usage["run"].(map[string]any)
	if _, exists := run["modelKey"]; exists {
		t.Fatalf("did not expect run modelKey, got %#v", run)
	}
	runCost, _ := run["estimatedCost"].(map[string]any)
	if runCost["currency"] != "CNY" || floatValue(runCost["inputCacheHit"]) != 0.005 ||
		floatValue(runCost["inputCacheMiss"]) != 2.4 || floatValue(runCost["output"]) != 6 ||
		floatValue(runCost["total"]) != 8.405 {
		t.Fatalf("expected run cost to accumulate from current usage, got %#v", runCost)
	}
	if runUsage.EstimatedCostCurrency != "CNY" || runUsage.EstimatedCostTotal != 8.405 {
		t.Fatalf("expected run usage cost to be captured, got %#v", runUsage)
	}
	if runUsage.ModelKey != "mock-model" {
		t.Fatalf("expected run usage modelKey to be captured, got %#v", runUsage)
	}
}

func TestRunEventProcessorPersistsDebugLLMChatEstimatedCostToJSONL(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStore(root)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-debug-cost", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	stepWriter := chat.NewStepWriter(store, "chat-debug-cost", "run-debug-cost", "REACT")
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		stepWriter: stepWriter,
		billing:    config.BillingConfig{Currency: "CNY"},
		models:     writeUsageCostRegistry(t),
		runUsage:   &runUsage,
	}

	processor.Consume(stream.NewEvent("content.snapshot", map[string]any{
		"contentId": "content-1",
		"text":      "answer",
	}))
	processor.Consume(stream.NewEvent("debug.llmChat", map[string]any{
		"data": map[string]any{
			"model": map[string]any{"key": "mock-model"},
			"contextWindow": map[string]any{
				"maxSize":               128000,
				"estimatedNextCallSize": 200,
			},
			"usage": map[string]any{
				"llmReturnUsage": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  200_000,
						"cacheMissTokens": 800_000,
					},
					"llmChatCompletionCount": 1,
				},
				"runUsage": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
				},
			},
		},
	}))
	processor.Consume(stream.NewEvent("run.complete", map[string]any{"runId": "run-debug-cost"}))
	stepWriter.Flush()

	raw, err := os.ReadFile(filepath.Join(root, "chat-debug-cost.jsonl"))
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one persisted step line, got %d lines: %s", len(lines), string(raw))
	}
	var step map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &step); err != nil {
		t.Fatalf("decode step line: %v", err)
	}
	usage, _ := step["usage"].(map[string]any)
	estimatedCost, _ := usage["estimatedCost"].(map[string]any)
	if estimatedCost["currency"] != "CNY" || floatValue(estimatedCost["inputCacheHit"]) != 0.005 ||
		floatValue(estimatedCost["inputCacheMiss"]) != 2.4 || floatValue(estimatedCost["output"]) != 6 ||
		floatValue(estimatedCost["total"]) != 8.405 {
		t.Fatalf("expected step usage estimated cost, got %#v in step %#v", estimatedCost, step)
	}

	detail, err := store.LoadChat("chat-debug-cost")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostCurrency != "CNY" || detail.ReplayUsage.LastRun.EstimatedCostTotal != 8.405 {
		t.Fatalf("expected replay lastRun cost from debug llmChat step usage, got %#v", detail.ReplayUsage.LastRun)
	}
	if detail.ReplayUsage.Chat.EstimatedCostCurrency != "CNY" || detail.ReplayUsage.Chat.EstimatedCostTotal != 8.405 {
		t.Fatalf("expected replay chat cost from debug llmChat step usage, got %#v", detail.ReplayUsage.Chat)
	}
	if runUsage.EstimatedCostCurrency != "" || runUsage.EstimatedCostTotal != 0 {
		t.Fatalf("did not expect debug llmChat runUsage merge to estimate cumulative cost, got %#v", runUsage)
	}
}

func TestRunEventProcessorOmitsDebugLLMChatEstimatedCostWithoutPricing(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		billing:  config.BillingConfig{Currency: "CNY"},
		models:   writeUsageCostRegistry(t),
		runUsage: &runUsage,
	}
	data := &stream.EventData{
		Type: "debug.llmChat",
		Payload: map[string]any{
			"data": map[string]any{
				"model": map[string]any{"key": "no-pricing-model"},
				"usage": map[string]any{
					"llmReturnUsage": map[string]any{
						"promptTokens":     100,
						"completionTokens": 50,
						"totalTokens":      150,
					},
				},
			},
		},
	}

	processor.decorate(data)

	inner, _ := data.Payload["data"].(map[string]any)
	usage, _ := inner["usage"].(map[string]any)
	llmReturnUsage, _ := usage["llmReturnUsage"].(map[string]any)
	if _, exists := llmReturnUsage["estimatedCost"]; exists {
		t.Fatalf("did not expect estimatedCost without model pricing, got %#v", llmReturnUsage)
	}
}

func TestRunEventProcessorPreservesEstimatedCostOnTerminalUsage(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		billing:  config.BillingConfig{Currency: "CNY"},
		models:   writeUsageCostRegistry(t),
		runUsage: &runUsage,
	}
	processor.decorate(&stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"modelKey":         "mock-model",
				},
				"run": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
				},
			},
		},
	})
	processor.decorate(&stream.EventData{
		Type: "debug.llmChat",
		Payload: map[string]any{
			"data": map[string]any{
				"usage": map[string]any{
					"runUsage": map[string]any{
						"promptTokens":     1_000_000,
						"completionTokens": 1_000_000,
						"totalTokens":      2_000_000,
					},
				},
			},
		},
	})
	data := &stream.EventData{
		Type: "run.complete",
		Payload: map[string]any{
			"runId": "run-usage",
			"usage": map[string]any{
				"promptTokens":     1_000_000,
				"completionTokens": 1_000_000,
				"totalTokens":      2_000_000,
			},
		},
	}

	processor.decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	runCost, _ := run["estimatedCost"].(map[string]any)
	if _, exists := run["modelKey"]; exists {
		t.Fatalf("did not expect terminal usage to expose modelKey, got %#v", run)
	}
	if floatValue(runCost["total"]) != 9 {
		t.Fatalf("expected terminal usage to preserve cost, got %#v", run)
	}
}

func TestRunEventProcessorAccumulatesCurrentCostAcrossModels(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := &runEventProcessor{
		billing:  config.BillingConfig{Currency: "CNY"},
		models:   writeUsageCostRegistry(t),
		runUsage: &runUsage,
	}
	for _, event := range []stream.EventData{
		{
			Type: "usage.snapshot",
			Payload: map[string]any{
				"model": map[string]any{"key": "mock-model"},
				"usage": map[string]any{
					"current": map[string]any{"promptTokens": 1_000_000, "totalTokens": 1_000_000},
					"run":     map[string]any{"promptTokens": 1_000_000, "totalTokens": 1_000_000},
				},
			},
		},
		{
			Type: "usage.snapshot",
			Payload: map[string]any{
				"model": map[string]any{"key": "expensive-model"},
				"usage": map[string]any{
					"current": map[string]any{"promptTokens": 1_000_000, "totalTokens": 1_000_000},
					"run":     map[string]any{"promptTokens": 2_000_000, "totalTokens": 2_000_000},
				},
			},
		},
	} {
		current := event
		processor.decorate(&current)
	}

	if runUsage.ModelKey != "" {
		t.Fatalf("expected mixed-model run to omit modelKey, got %#v", runUsage)
	}
	if runUsage.EstimatedCostCurrency != "CNY" || runUsage.EstimatedCostTotal != 13 {
		t.Fatalf("expected cost to sum per current model, got %#v", runUsage)
	}
}

func TestProxyUsageTrackerDecoratesUsageSnapshotWithEstimatedCost(t *testing.T) {
	runUsage := chat.UsageData{}
	tracker := newProxyUsageTracker(
		chat.UsageData{
			PromptTokens:             10,
			CompletionTokens:         5,
			TotalTokens:              15,
			LlmChatCompletionCount:   1,
			FirstTokenLatencyTotalMs: 500,
			FirstTokenLatencyCount:   1,
			GenerationDurationMs:     500,
		},
		&runUsage,
		writeUsageCostRegistry(t),
		config.BillingConfig{Currency: "CNY"},
	)
	event := &stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"modelKey":         "mock-model",
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  200_000,
						"cacheMissTokens": 800_000,
					},
				},
				"run": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"timing": map[string]any{
						"firstTokenLatencyMs":  2000,
						"generationDurationMs": 2500,
					},
				},
			},
		},
	}

	tracker.Decorate(event)

	usage, _ := event.Payload["usage"].(map[string]any)
	current, _ := usage["current"].(map[string]any)
	currentCost, _ := current["estimatedCost"].(map[string]any)
	if current["modelKey"] != "mock-model" || floatValue(currentCost["total"]) != 8.405 {
		t.Fatalf("expected proxy current cost decoration, got %#v", current)
	}
	run, _ := usage["run"].(map[string]any)
	runCost, _ := run["estimatedCost"].(map[string]any)
	if floatValue(runCost["total"]) != 8.405 {
		t.Fatalf("expected proxy run cost from current usage, got %#v", run)
	}
	runTiming, _ := run["timing"].(map[string]any)
	if AnyIntNode(runTiming["firstTokenLatencyTotalMs"]) != 2000 ||
		AnyIntNode(runTiming["firstTokenLatencyCount"]) != 1 ||
		AnyIntNode(runTiming["generationDurationMs"]) != 2500 {
		t.Fatalf("expected proxy run cumulative timing, got %#v", run)
	}
	if _, ok := runTiming["firstTokenLatencyMs"]; ok {
		t.Fatalf("did not expect proxy run average first token latency, got %#v", run)
	}
	if _, ok := runTiming["outputTokensPerSecond"]; ok {
		t.Fatalf("did not expect proxy run output speed in timing, got %#v", run)
	}
	chatUsage, _ := usage["chat"].(map[string]any)
	chatCost, _ := chatUsage["estimatedCost"].(map[string]any)
	if AnyIntNode(chatUsage["totalTokens"]) != 2_000_015 || floatValue(chatCost["total"]) != 8.405 {
		t.Fatalf("expected proxy chat usage to include base tokens and run cost, got %#v", chatUsage)
	}
	chatTiming, _ := chatUsage["timing"].(map[string]any)
	if AnyIntNode(chatTiming["firstTokenLatencyTotalMs"]) != 2500 ||
		AnyIntNode(chatTiming["firstTokenLatencyCount"]) != 2 ||
		AnyIntNode(chatTiming["generationDurationMs"]) != 3000 {
		t.Fatalf("expected proxy chat cumulative timing, got %#v", chatUsage)
	}
	if _, ok := chatTiming["firstTokenLatencyMs"]; ok {
		t.Fatalf("did not expect proxy chat average first token latency, got %#v", chatUsage)
	}
	if _, ok := chatTiming["outputTokensPerSecond"]; ok {
		t.Fatalf("did not expect proxy chat output speed in timing, got %#v", chatUsage)
	}
	if runUsage.EstimatedCostCurrency != "CNY" || runUsage.EstimatedCostTotal != 8.405 {
		t.Fatalf("expected proxy run usage to capture cost, got %#v", runUsage)
	}
}

func TestProxyEventRecorderPersistsDecoratedUsageSnapshotCost(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-proxy-cost", "proxy-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	stepWriter := chat.NewStepWriter(store, "chat-proxy-cost", "run-proxy-cost", "PROXY")
	recorder := newProxyEventRecorder(
		api.QueryRequest{ChatID: "chat-proxy-cost", RunID: "run-proxy-cost", AgentKey: "proxy-agent", Message: "hello"},
		catalog.AgentDefinition{Key: "proxy-agent", Mode: "PROXY"},
		store,
		stepWriter,
		nil,
		nil,
		chat.UsageData{},
		writeUsageCostRegistry(t),
		config.BillingConfig{Currency: "CNY"},
	)
	recorder.OnEvent(stream.EventData{
		Type:      "content.start",
		Timestamp: 1,
		Payload:   map[string]any{"contentId": "content-1", "runId": "run-proxy-cost"},
	})
	recorder.OnEvent(stream.EventData{
		Type:      "content.delta",
		Timestamp: 2,
		Payload:   map[string]any{"contentId": "content-1", "delta": "answer"},
	})
	recorder.OnEvent(stream.EventData{
		Type:      "content.end",
		Timestamp: 3,
		Payload:   map[string]any{"contentId": "content-1"},
	})
	usageEvent := stream.EventData{
		Type:      "usage.snapshot",
		Timestamp: 4,
		Payload: map[string]any{
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"modelKey":         "mock-model",
				},
				"run": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
				},
			},
		},
	}
	recorder.DecorateEvent(&usageEvent)
	recorder.OnEvent(usageEvent)
	terminalEvent := stream.EventData{
		Type:      "run.complete",
		Timestamp: 5,
		Payload:   map[string]any{"runId": "run-proxy-cost"},
	}
	recorder.DecorateEvent(&terminalEvent)
	recorder.OnEvent(terminalEvent)

	persisted, completion := recorder.Finish()
	if !persisted {
		t.Fatalf("expected proxy completion to persist")
	}
	if completion.Usage.EstimatedCostCurrency != "CNY" || completion.Usage.EstimatedCostTotal != 9 {
		t.Fatalf("expected completion usage cost from decorated snapshot, got %#v", completion.Usage)
	}
	detail, err := store.LoadChat("chat-proxy-cost")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostCurrency != "CNY" || detail.ReplayUsage.LastRun.EstimatedCostTotal != 9 {
		t.Fatalf("expected replay lastRun cost from proxy step usage, got %#v", detail.ReplayUsage.LastRun)
	}
	if detail.ReplayUsage.Chat.EstimatedCostCurrency != "CNY" || detail.ReplayUsage.Chat.EstimatedCostTotal != 9 {
		t.Fatalf("expected replay chat cost from proxy step usage, got %#v", detail.ReplayUsage.Chat)
	}
}

func writeUsageCostRegistry(t *testing.T) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "providers"), filepath.Join(root, "models")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir registry dir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "providers", "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: https://example.com",
		"apiKey: test",
		"defaultModel: mock-model",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "mock.yml"), []byte(strings.Join([]string{
		"key: mock-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: mock-model-id",
		"pricing:",
		"  currency: CNY",
		"  unit: per_1m_tokens",
		"  inputCacheHit: 0.025",
		"  inputCacheMiss: 3.00",
		"  output: 6.00",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "expensive.yml"), []byte(strings.Join([]string{
		"key: expensive-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: expensive-model-id",
		"pricing:",
		"  currency: CNY",
		"  unit: per_1m_tokens",
		"  inputCacheHit: 0.00",
		"  inputCacheMiss: 10.00",
		"  output: 20.00",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write expensive model: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "no-pricing.yml"), []byte(strings.Join([]string{
		"key: no-pricing-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: no-pricing-model-id",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write no-pricing model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return registry
}
