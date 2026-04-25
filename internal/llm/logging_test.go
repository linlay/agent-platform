package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/observability"
)

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
