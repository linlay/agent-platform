package llm

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/observability"
)

func TestLLMConsoleDefaultLogsRequestSummaryButNotBodyOrChunks(t *testing.T) {
	engine := testLLMConsoleEngine(true, []string{"request", "usage"})

	content := captureStandardLog(t, func() {
		engine.logOutgoingRequest("run-1", models.ProviderDefinition{Key: "provider-a"}, models.ModelDefinition{ModelID: "model-a"}, "https://example.test/v1", []openAIMessage{{Role: "user", Content: "hello"}}, nil, "", []byte(`{"messages":[]}`))
		engine.logRawChunk("run-1", `{"delta":"raw"}`)
		engine.logParsedDelta("run-1", "content", "hello")
	})

	if !strings.Contains(content, "[request_summary]") {
		t.Fatalf("expected request summary by default, got %s", content)
	}
	if engine.llmConsoleEnabled(llmConsoleHitl) {
		t.Fatalf("did not expect hitl category enabled by default")
	}
	for _, unwanted := range []string{"[request_body]", "[raw_chunk]", "[parsed_content]"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("did not expect %s by default, got %s", unwanted, content)
		}
	}
}

func TestLLMConsoleBodyCategoryLogsRequestBody(t *testing.T) {
	engine := testLLMConsoleEngine(true, []string{"body"})

	content := captureStandardLog(t, func() {
		engine.logOutgoingRequest("run-1", models.ProviderDefinition{Key: "provider-a"}, models.ModelDefinition{ModelID: "model-a"}, "https://example.test/v1", []openAIMessage{{Role: "user", Content: "hello"}}, nil, "", []byte(`{"messages":[]}`))
	})

	if !strings.Contains(content, "[request_body]") {
		t.Fatalf("expected request body with body category, got %s", content)
	}
	if strings.Contains(content, "[request_summary]") {
		t.Fatalf("did not expect request summary with body-only category, got %s", content)
	}
}

func TestLLMConsoleRawAndParsedCategoriesLogChunks(t *testing.T) {
	engine := testLLMConsoleEngine(true, []string{"raw", "parsed"})

	content := captureStandardLog(t, func() {
		engine.logRawChunk("run-1", `{"delta":"raw"}`)
		engine.logParsedDelta("run-1", "content", "hello")
		engine.logParsedToolDelta("run-1", openAIStreamToolDelta{
			Index: 1,
			ID:    "tool-1",
			Type:  "function",
			Function: openAIStreamFunctionDelta{
				Name:      "bash",
				Arguments: `{"command":"pwd"}`,
			},
		})
	})

	for _, want := range []string{"[raw_chunk]", "[parsed_content]", "[parsed_tool_call]"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %s in chunk logs, got %s", want, content)
		}
	}
}

func TestLLMConsoleDefaultLogsUsage(t *testing.T) {
	engine := testLLMConsoleEngine(true, []string{"request", "usage"})
	stream := &llmRunStream{
		engine:  engine,
		session: QuerySession{RunID: "run-1"},
	}

	content := captureStandardLog(t, func() {
		stream.commitUsage(&openAIUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3})
	})

	if !strings.Contains(content, "[usage]") {
		t.Fatalf("expected usage log by default, got %s", content)
	}
}

func TestLLMConsoleNoneDisablesOptionalLogs(t *testing.T) {
	engine := testLLMConsoleEngine(true, []string{"none"})
	stream := &llmRunStream{
		engine:  engine,
		session: QuerySession{RunID: "run-1"},
	}

	content := captureStandardLog(t, func() {
		engine.logOutgoingRequest("run-1", models.ProviderDefinition{Key: "provider-a"}, models.ModelDefinition{ModelID: "model-a"}, "https://example.test/v1", []openAIMessage{{Role: "user", Content: "hello"}}, nil, "", []byte(`{"messages":[]}`))
		engine.logRawChunk("run-1", `{"delta":"raw"}`)
		engine.logParsedDelta("run-1", "content", "hello")
		stream.commitUsage(&openAIUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3})
	})

	if content != "" {
		t.Fatalf("expected optional llm console logs disabled with none, got %s", content)
	}
}

func TestLLMWarningsAlwaysLog(t *testing.T) {
	engine := testLLMConsoleEngine(false, []string{"none"})

	content := captureStandardLog(t, func() {
		engine.logMissingToolSpecsWarning("run-1", []string{"bash"})
	})

	if !strings.Contains(content, "[warning]") {
		t.Fatalf("expected warning even when optional console logging is disabled, got %s", content)
	}
}

func TestLLMConsoleAllCategoryEnablesOptionalCategories(t *testing.T) {
	for _, category := range []string{llmConsoleRequest, llmConsoleBody, llmConsoleRaw, llmConsoleParsed, llmConsoleUsage, llmConsoleHitl, llmConsolePrompt, llmConsoleSystem, llmConsoleMedia, llmConsoleTrace} {
		if !llmConsoleCategoryEnabled([]string{"all"}, category) {
			t.Fatalf("expected all to enable %s", category)
		}
	}
	if llmConsoleCategoryEnabled([]string{"all", "none"}, llmConsoleRaw) {
		t.Fatalf("expected none to override all")
	}
	if llmConsoleCategoryEnabled([]string{"raw", "none"}, llmConsoleRaw) {
		t.Fatalf("expected none to override explicit categories")
	}
}

func TestLogPromptMemoryWritesDedicatedMemoryLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "memory.log")
	if err := observability.InitMemoryLogger(true, logPath); err != nil {
		t.Fatalf("init memory logger: %v", err)
	}
	defer func() {
		if err := observability.CloseMemoryLogger(); err != nil {
			t.Fatalf("close memory logger: %v", err)
		}
	}()

	engine := &LLMAgentEngine{
		cfg: config.Config{
			Logging: config.LoggingConfig{
				LLMInteraction: config.LLMInteractionLoggingConfig{
					Enabled:       true,
					MaskSensitive: false,
				},
			},
		},
	}
	engine.logPromptMemory("run-1", "react", api.QueryRequest{}, QuerySession{
		RequestID:            "req-1",
		ChatID:               "chat-1",
		AgentKey:             "agent-a",
		AgentHasMemoryConfig: true,
		StableMemoryContext:  "偏好：每周工时要保证 40h",
	})

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read memory log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "\"operation\":\"llm_prompt_memory\"") {
		t.Fatalf("expected llm prompt memory operation, got %s", content)
	}
	if !strings.Contains(content, "\"source\":\"llm\"") {
		t.Fatalf("expected llm source, got %s", content)
	}
	if !strings.Contains(content, "\"memoryPrompt\"") {
		t.Fatalf("expected memory prompt payload, got %s", content)
	}
	if !strings.Contains(content, "每周工时要保证 40h") {
		t.Fatalf("expected memory prompt content, got %s", content)
	}
	if !strings.Contains(content, "\"stableMemoryChars\"") {
		t.Fatalf("expected stableMemoryChars field, got %s", content)
	}
	if !strings.Contains(content, "\"observationMemoryChars\"") {
		t.Fatalf("expected observationMemoryChars field, got %s", content)
	}
}

func captureStandardLog(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	oldOutput := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(oldOutput)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	}()

	fn()
	return buf.String()
}

func testLLMConsoleEngine(enabled bool, categories []string) *LLMAgentEngine {
	return &LLMAgentEngine{
		cfg: config.Config{
			Logging: config.LoggingConfig{
				LLMInteraction: config.LLMInteractionLoggingConfig{
					Enabled:           enabled,
					ConsoleCategories: append([]string(nil), categories...),
				},
			},
		},
	}
}
