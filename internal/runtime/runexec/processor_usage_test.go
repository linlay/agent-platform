package runexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

func TestRunEventProcessorFirstTerminalErrorWins(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-terminal-error")
	processor := NewProcessor(ProcessorOptions{
		RunControl: control,
		RunID:      "run-terminal-error",
		ChatID:     "chat-terminal-error",
		AgentKey:   "agent-terminal-error",
	})
	errorPayload := map[string]any{
		"code": "tool_calls_exceeded",
		"diagnostics": map[string]any{
			"toolCalls":  61,
			"limitValue": 60,
			"limitName":  "budget.tool.maxCalls",
			"toolName":   "bash",
		},
	}
	if _, _, err := processor.Consume(stream.NewEvent("run.error", map[string]any{
		"runId": "run-terminal-error",
		"error": errorPayload,
	})); err != nil {
		t.Fatalf("consume run.error: %v", err)
	}
	if _, _, err := processor.Consume(stream.NewEvent("run.cancel", map[string]any{
		"runId": "run-terminal-error",
	})); err != nil {
		t.Fatalf("consume late run.cancel: %v", err)
	}

	if processor.TerminalFinishReason() != "error" {
		t.Fatalf("terminal reason = %q, want error", processor.TerminalFinishReason())
	}
	if contracts.AnyStringNode(processor.TerminalErrorPayload()["code"]) != "tool_calls_exceeded" {
		t.Fatalf("terminal payload was not retained: %#v", processor.TerminalErrorPayload())
	}
	if control.State() != contracts.RunLoopStateFailed {
		t.Fatalf("run state = %s, want %s", control.State(), contracts.RunLoopStateFailed)
	}
	if control.Interrupt(contracts.InterruptInfo{Source: contracts.InterruptSourceHTTPAPI, Reason: contracts.InterruptReasonUserCancelled}) {
		t.Fatal("late interrupt must be unmatched after terminal error")
	}
}

func TestRunEventProcessorDecoratesTerminalUsage(t *testing.T) {
	eventTypes := []string{"run.complete", "run.error", "run.cancel"}
	for _, eventType := range eventTypes {
		t.Run(eventType, func(t *testing.T) {
			runUsage := chat.UsageData{}
			processor := NewProcessor(ProcessorOptions{
				ChatUsage: chat.UsageData{
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
				RunUsage: &runUsage,
			})
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

			processor.Decorate(data)

			if _, ok := data.Payload["chatUsage"]; ok {
				t.Fatalf("terminal event should not carry top-level chatUsage: %#v", data.Payload)
			}
			usage, _ := data.Payload["usage"].(map[string]any)
			if usage == nil {
				t.Fatalf("expected nested usage payload, got %#v", data.Payload)
			}
			run, _ := usage["run"].(map[string]any)
			if contracts.AnyIntNode(run["promptTokens"]) != 7 || contracts.AnyIntNode(run["completionTokens"]) != 3 || contracts.AnyIntNode(run["totalTokens"]) != 10 {
				t.Fatalf("unexpected run usage %#v", usage)
			}
			runPromptDetails, _ := run["promptTokensDetails"].(map[string]any)
			runCompletionDetails, _ := run["completionTokensDetails"].(map[string]any)
			if contracts.AnyIntNode(runPromptDetails["cacheHitTokens"]) != 5 || contracts.AnyIntNode(runPromptDetails["cacheMissTokens"]) != 2 ||
				contracts.AnyIntNode(runCompletionDetails["reasoningTokens"]) != 2 {
				t.Fatalf("unexpected run detailed usage %#v", usage)
			}
			if contracts.AnyIntNode(run["llmChatCompletionCount"]) != 1 {
				t.Fatalf("unexpected run llm chat completion count %#v", usage)
			}
			if contracts.AnyIntNode(run["toolCallCount"]) != 2 {
				t.Fatalf("unexpected run tool call count %#v", usage)
			}
			chatUsage, _ := usage["chat"].(map[string]any)
			if contracts.AnyIntNode(chatUsage["promptTokens"]) != 107 || contracts.AnyIntNode(chatUsage["completionTokens"]) != 53 || contracts.AnyIntNode(chatUsage["totalTokens"]) != 160 {
				t.Fatalf("unexpected chat usage %#v", usage)
			}
			chatPromptDetails, _ := chatUsage["promptTokensDetails"].(map[string]any)
			chatCompletionDetails, _ := chatUsage["completionTokensDetails"].(map[string]any)
			if contracts.AnyIntNode(chatPromptDetails["cacheHitTokens"]) != 25 || contracts.AnyIntNode(chatPromptDetails["cacheMissTokens"]) != 82 ||
				contracts.AnyIntNode(chatCompletionDetails["reasoningTokens"]) != 12 {
				t.Fatalf("unexpected chat detailed usage %#v", usage)
			}
			if contracts.AnyIntNode(chatUsage["llmChatCompletionCount"]) != 5 {
				t.Fatalf("unexpected chat llm chat completion count %#v", usage)
			}
			if contracts.AnyIntNode(chatUsage["toolCallCount"]) != 8 {
				t.Fatalf("unexpected chat tool call count %#v", usage)
			}
		})
	}
}

func TestRunEventProcessorKeepsTerminalUsageWhenOnlyLLMChatCompletionCountKnown(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		RunUsage: &runUsage,
	})
	data := &stream.EventData{
		Type: "run.error",
		Payload: map[string]any{
			"runId": "run-usage",
			"usage": map[string]any{
				"llmChatCompletionCount": 1,
			},
		},
	}

	processor.Decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	if contracts.AnyIntNode(run["llmChatCompletionCount"]) != 1 {
		t.Fatalf("expected terminal usage with llmChatCompletionCount, got %#v", data.Payload)
	}
}

func TestRunEventProcessorKeepsTerminalUsageWhenOnlyToolCallCountKnown(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		RunUsage: &runUsage,
	})
	data := &stream.EventData{
		Type: "run.error",
		Payload: map[string]any{
			"runId": "run-usage",
			"usage": map[string]any{
				"toolCallCount": 2,
			},
		},
	}

	processor.Decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	if contracts.AnyIntNode(run["toolCallCount"]) != 2 {
		t.Fatalf("expected terminal usage with toolCallCount, got %#v", data.Payload)
	}
}

func TestRunEventProcessorOmitsTerminalUsageWhenUnknown(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		ChatUsage: chat.UsageData{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
		RunUsage: &runUsage,
	})
	data := &stream.EventData{
		Type: "run.complete",
		Payload: map[string]any{
			"runId":     "run-usage",
			"chatUsage": map[string]any{"totalTokens": 150},
		},
	}

	processor.Decorate(data)

	if _, ok := data.Payload["usage"]; ok {
		t.Fatalf("did not expect usage without known run tokens: %#v", data.Payload)
	}
	if _, ok := data.Payload["chatUsage"]; ok {
		t.Fatalf("terminal event should not carry top-level chatUsage: %#v", data.Payload)
	}
}

func TestRunEventProcessorDecoratesUsageSnapshotWithChatUsage(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		ChatUsage: chat.UsageData{
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
		RunUsage: &runUsage,
	})
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

	processor.Decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	chatUsage, _ := usage["chat"].(map[string]any)
	if contracts.AnyIntNode(chatUsage["promptTokens"]) != 107 || contracts.AnyIntNode(chatUsage["completionTokens"]) != 53 || contracts.AnyIntNode(chatUsage["totalTokens"]) != 160 {
		t.Fatalf("unexpected chat usage %#v", usage)
	}
	chatPromptDetails, _ := chatUsage["promptTokensDetails"].(map[string]any)
	chatCompletionDetails, _ := chatUsage["completionTokensDetails"].(map[string]any)
	if contracts.AnyIntNode(chatPromptDetails["cacheHitTokens"]) != 25 || contracts.AnyIntNode(chatPromptDetails["cacheMissTokens"]) != 82 ||
		contracts.AnyIntNode(chatCompletionDetails["reasoningTokens"]) != 12 {
		t.Fatalf("unexpected detailed chat usage %#v", usage)
	}
	if contracts.AnyIntNode(chatUsage["llmChatCompletionCount"]) != 5 {
		t.Fatalf("unexpected chat llm completion count %#v", usage)
	}
	if contracts.AnyIntNode(chatUsage["toolCallCount"]) != 8 {
		t.Fatalf("unexpected chat tool call count %#v", usage)
	}
}

func TestRunEventProcessorKeepsZeroDetailedUsageInSnapshotAggregates(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		RunUsage: &runUsage,
	})
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

	processor.Decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	for _, key := range []string{"run", "chat"} {
		stats, _ := usage[key].(map[string]any)
		promptDetails, _ := stats["promptTokensDetails"].(map[string]any)
		if _, ok := promptDetails["cacheHitTokens"]; !ok || contracts.AnyIntNode(promptDetails["cacheHitTokens"]) != 0 {
			t.Fatalf("expected %s cacheHitTokens=0, got %#v", key, stats)
		}
		if contracts.AnyIntNode(promptDetails["cacheMissTokens"]) != 10 {
			t.Fatalf("expected %s cacheMissTokens=10, got %#v", key, stats)
		}
		completionDetails, _ := stats["completionTokensDetails"].(map[string]any)
		if _, ok := completionDetails["reasoningTokens"]; !ok || contracts.AnyIntNode(completionDetails["reasoningTokens"]) != 0 {
			t.Fatalf("expected %s reasoningTokens=0, got %#v", key, stats)
		}
	}
}

func TestRunEventProcessorNormalizesCumulativeUsageSnapshotCacheMissTokens(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		RunUsage: &runUsage,
	})
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

	processor.Decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	current, _ := usage["current"].(map[string]any)
	currentPromptDetails, _ := current["promptTokensDetails"].(map[string]any)
	if contracts.AnyIntNode(currentPromptDetails["cacheHitTokens"]) != 8059 || contracts.AnyIntNode(currentPromptDetails["cacheMissTokens"]) != 692 {
		t.Fatalf("expected current usage details to remain unchanged, got %#v", usage)
	}
	run, _ := usage["run"].(map[string]any)
	runPromptDetails, _ := run["promptTokensDetails"].(map[string]any)
	if contracts.AnyIntNode(run["promptTokens"]) != 16929 || contracts.AnyIntNode(runPromptDetails["cacheHitTokens"]) != 8059 ||
		contracts.AnyIntNode(runPromptDetails["cacheMissTokens"]) != 8870 {
		t.Fatalf("expected run cache miss to be normalized from cumulative prompt tokens, got %#v", usage)
	}
	chatUsage, _ := usage["chat"].(map[string]any)
	chatPromptDetails, _ := chatUsage["promptTokensDetails"].(map[string]any)
	if contracts.AnyIntNode(chatUsage["promptTokens"]) != 16929 || contracts.AnyIntNode(chatPromptDetails["cacheHitTokens"]) != 8059 ||
		contracts.AnyIntNode(chatPromptDetails["cacheMissTokens"]) != 8870 {
		t.Fatalf("expected chat cache miss to be normalized from cumulative prompt tokens, got %#v", usage)
	}
}

func TestRunEventProcessorDecoratesUsageSnapshotWithEstimatedCost(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		Billing:  config.BillingConfig{Currency: "CNY"},
		Models:   writeUsageCostRegistry(t),
		RunUsage: &runUsage,
	})
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

	processor.Decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	current, _ := usage["current"].(map[string]any)
	if current["modelKey"] != "mock-model" {
		t.Fatalf("expected current modelKey, got %#v", current)
	}
	currentCost, _ := current["estimatedCost"].(map[string]any)
	if currentCost["currency"] != "CNY" || FloatValue(currentCost["inputCacheHit"]) != 0.005 ||
		FloatValue(currentCost["inputCacheMiss"]) != 2.4 || FloatValue(currentCost["output"]) != 6 ||
		FloatValue(currentCost["total"]) != 8.405 {
		t.Fatalf("unexpected current estimated cost %#v", currentCost)
	}
	run, _ := usage["run"].(map[string]any)
	if _, exists := run["modelKey"]; exists {
		t.Fatalf("did not expect run modelKey, got %#v", run)
	}
	runCost, _ := run["estimatedCost"].(map[string]any)
	if runCost["currency"] != "CNY" || FloatValue(runCost["inputCacheHit"]) != 0.005 ||
		FloatValue(runCost["inputCacheMiss"]) != 2.4 || FloatValue(runCost["output"]) != 6 ||
		FloatValue(runCost["total"]) != 8.405 {
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
	store, err := chat.NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-debug-cost", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	defer store.Close()
	if err := store.OnRunStarted(chat.RunStart{
		ChatID: "chat-debug-cost", RunID: "run-debug-cost", StartedAtMillis: 1_700_000_000_000,
	}); err != nil {
		t.Fatalf("record run start: %v", err)
	}
	stepWriter := chat.NewStepWriter(store, "chat-debug-cost", "run-debug-cost", "REACT")
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		StepWriter: stepWriter,
		Billing:    config.BillingConfig{Currency: "CNY"},
		Models:     writeUsageCostRegistry(t),
		RunUsage:   &runUsage,
	})

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
	if estimatedCost["currency"] != "CNY" || FloatValue(estimatedCost["inputCacheHit"]) != 0.005 ||
		FloatValue(estimatedCost["inputCacheMiss"]) != 2.4 || FloatValue(estimatedCost["output"]) != 6 ||
		FloatValue(estimatedCost["total"]) != 8.405 {
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
	processor := NewProcessor(ProcessorOptions{
		Billing:  config.BillingConfig{Currency: "CNY"},
		Models:   writeUsageCostRegistry(t),
		RunUsage: &runUsage,
	})
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

	processor.Decorate(data)

	inner, _ := data.Payload["data"].(map[string]any)
	usage, _ := inner["usage"].(map[string]any)
	llmReturnUsage, _ := usage["llmReturnUsage"].(map[string]any)
	if _, exists := llmReturnUsage["estimatedCost"]; exists {
		t.Fatalf("did not expect estimatedCost without model pricing, got %#v", llmReturnUsage)
	}
}

func TestRunEventProcessorPreservesEstimatedCostOnTerminalUsage(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		Billing:  config.BillingConfig{Currency: "CNY"},
		Models:   writeUsageCostRegistry(t),
		RunUsage: &runUsage,
	})
	processor.Decorate(&stream.EventData{
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
	processor.Decorate(&stream.EventData{
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

	processor.Decorate(data)

	usage, _ := data.Payload["usage"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	runCost, _ := run["estimatedCost"].(map[string]any)
	if _, exists := run["modelKey"]; exists {
		t.Fatalf("did not expect terminal usage to expose modelKey, got %#v", run)
	}
	if FloatValue(runCost["total"]) != 9 {
		t.Fatalf("expected terminal usage to preserve cost, got %#v", run)
	}
}

func TestRunEventProcessorAccumulatesCurrentCostAcrossModels(t *testing.T) {
	runUsage := chat.UsageData{}
	processor := NewProcessor(ProcessorOptions{
		Billing:  config.BillingConfig{Currency: "CNY"},
		Models:   writeUsageCostRegistry(t),
		RunUsage: &runUsage,
	})
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
		processor.Decorate(&current)
	}

	if runUsage.ModelKey != "" {
		t.Fatalf("expected mixed-model run to omit modelKey, got %#v", runUsage)
	}
	if runUsage.EstimatedCostCurrency != "CNY" || runUsage.EstimatedCostTotal != 13 {
		t.Fatalf("expected cost to sum per current model, got %#v", runUsage)
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
