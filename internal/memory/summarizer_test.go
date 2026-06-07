package memory

import (
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/models"
)

func TestMemorySummarizerTimeoutUsesSeconds(t *testing.T) {
	summarizer := NewLLMMemorySummarizer(&models.ModelRegistry{}, "mock-model", 0, config.MemoryPromptsConfig{})
	if summarizer.timeout != 60*time.Second || summarizer.client.Timeout != 60*time.Second {
		t.Fatalf("expected default summarizer timeout 60s, got timeout=%s client=%s", summarizer.timeout, summarizer.client.Timeout)
	}

	summarizer = NewLLMMemorySummarizer(&models.ModelRegistry{}, "mock-model", 2, config.MemoryPromptsConfig{})
	if summarizer.timeout != 2*time.Second || summarizer.client.Timeout != 2*time.Second {
		t.Fatalf("expected configured summarizer timeout 2s, got timeout=%s client=%s", summarizer.timeout, summarizer.client.Timeout)
	}
}

func TestMemorySummarizerDefaultPrompts(t *testing.T) {
	summarizer := &LLMMemorySummarizer{}
	systemPrompt := summarizer.buildMemorySummarizerSystemPrompt("learn")
	if !strings.Contains(systemPrompt, "You are a memory curator for an agent system.") {
		t.Fatalf("expected default system prompt, got %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "For learn mode") {
		t.Fatalf("expected learn task instruction, got %q", systemPrompt)
	}

	userPrompt := summarizer.buildMemorySummarizerUserPrompt(memoryPrompt{
		Task:       "remember",
		AgentKey:   "agent-1",
		ChatID:     "chat-1",
		SourceText: "source",
	})
	if !strings.Contains(userPrompt, "task: remember") || !strings.Contains(userPrompt, "source_text:\nsource") {
		t.Fatalf("expected default user prompt, got %q", userPrompt)
	}
}

func TestMemorySummarizerUsesConfiguredPromptTemplates(t *testing.T) {
	summarizer := &LLMMemorySummarizer{
		prompts: config.MemoryPromptsConfig{
			SystemPromptTemplate: "custom system {{task}} {{task_instruction}}",
			UserPromptTemplate:   "custom user {{agent_key}} {{chat_id}} {{user_request}} {{source_text}} {{output_schema}}",
		},
	}
	systemPrompt := summarizer.buildMemorySummarizerSystemPrompt("learn")
	if !strings.Contains(systemPrompt, "custom system learn For learn mode") {
		t.Fatalf("expected configured system prompt, got %q", systemPrompt)
	}
	userPrompt := summarizer.buildMemorySummarizerUserPrompt(memoryPrompt{
		AgentKey:    "agent-1",
		ChatID:      "chat-1",
		UserRequest: "hello",
		SourceText:  "source",
	})
	if !strings.Contains(userPrompt, "custom user agent-1 chat-1 hello source") {
		t.Fatalf("expected configured user prompt, got %q", userPrompt)
	}
	if !strings.Contains(userPrompt, `"items"`) {
		t.Fatalf("expected output schema in configured user prompt, got %q", userPrompt)
	}
}
