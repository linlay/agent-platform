package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/models"
)

type RememberSummarizer interface {
	SummarizeRemember(input RememberSynthesisInput) ([]MemoryDraft, error)
	SummarizeLearn(input LearnSynthesisInput) ([]MemoryDraft, error)
}

type RememberSynthesisInput struct {
	Request  api.RememberRequest
	Chat     chat.Detail
	AgentKey string
	History  []api.StoredMemoryResponse
}

type LearnSynthesisInput struct {
	Request  api.LearnRequest
	Trace    chat.RunTrace
	AgentKey string
	TeamID   string
	UserKey  string
	History  []api.StoredMemoryResponse
}

type MemoryDraft struct {
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Category   string   `json:"category"`
	Importance int      `json:"importance"`
	Confidence float64  `json:"confidence"`
	Tags       []string `json:"tags"`
}

type LLMMemorySummarizer struct {
	client   *http.Client
	registry *models.ModelRegistry
	modelKey string
	timeout  time.Duration
}

func NewLLMMemorySummarizer(registry *models.ModelRegistry, modelKey string, timeoutMs int64) *LLMMemorySummarizer {
	if registry == nil || strings.TrimSpace(modelKey) == "" {
		return nil
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &LLMMemorySummarizer{
		client:   &http.Client{Timeout: timeout},
		registry: registry,
		modelKey: strings.TrimSpace(modelKey),
		timeout:  timeout,
	}
}

func (s *LLMMemorySummarizer) SummarizeRemember(input RememberSynthesisInput) ([]MemoryDraft, error) {
	if s == nil {
		return nil, nil
	}
	source := rememberSourceText(input.Chat)
	if strings.TrimSpace(source) == "" {
		return nil, nil
	}
	return s.complete(memoryPrompt{
		Task:        "remember",
		AgentKey:    input.AgentKey,
		ChatID:      input.Request.ChatID,
		History:     input.History,
		SourceText:  source,
		UserRequest: firstRawMessage(input.Chat.RawMessages),
	})
}

func (s *LLMMemorySummarizer) SummarizeLearn(input LearnSynthesisInput) ([]MemoryDraft, error) {
	if s == nil {
		return nil, nil
	}
	source := learnSourceText(input.Trace)
	if strings.TrimSpace(source) == "" {
		return nil, nil
	}
	userRequest := ""
	if input.Trace.Query != nil {
		userRequest = AnyString(input.Trace.Query.Query["message"])
	}
	return s.complete(memoryPrompt{
		Task:        "learn",
		AgentKey:    input.AgentKey,
		ChatID:      input.Request.ChatID,
		History:     input.History,
		SourceText:  source,
		UserRequest: userRequest,
	})
}

type memoryPrompt struct {
	Task        string
	AgentKey    string
	ChatID      string
	History     []api.StoredMemoryResponse
	SourceText  string
	UserRequest string
}

func (s *LLMMemorySummarizer) complete(prompt memoryPrompt) ([]MemoryDraft, error) {
	model, provider, err := s.registry.Get(s.modelKey)
	if err != nil {
		return nil, err
	}
	systemPrompt := buildMemorySummarizerSystemPrompt(prompt.Task)
	userPrompt := buildMemorySummarizerUserPrompt(prompt)
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	switch strings.ToUpper(strings.TrimSpace(model.Protocol)) {
	case "ANTHROPIC":
		return s.completeAnthropic(ctx, model, provider, systemPrompt, userPrompt)
	default:
		return s.completeOpenAI(ctx, model, provider, systemPrompt, userPrompt)
	}
}

func (s *LLMMemorySummarizer) completeOpenAI(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, systemPrompt string, userPrompt string) ([]MemoryDraft, error) {
	endpoint := strings.TrimRight(provider.BaseURL, "/") + provider.Protocol(model.Protocol).EndpointPath
	body := map[string]any{
		"model": model.ModelID,
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0,
		"stream":      false,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	for key, value := range provider.Protocol(model.Protocol).Headers {
		req.Header.Set(key, value)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("memory summarizer request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("memory summarizer returned no choices")
	}
	return decodeMemoryDrafts(extractOpenAIContent(decoded.Choices[0].Message.Content))
}

func (s *LLMMemorySummarizer) completeAnthropic(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, systemPrompt string, userPrompt string) ([]MemoryDraft, error) {
	endpoint := strings.TrimRight(provider.BaseURL, "/") + provider.Protocol(model.Protocol).EndpointPath
	body := map[string]any{
		"model":      model.ModelID,
		"max_tokens": 1200,
		"system":     systemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{{"type": "text", "text": userPrompt}}},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", provider.APIKey)
	for key, value := range provider.Protocol(model.Protocol).Headers {
		req.Header.Set(key, value)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("memory summarizer request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(decoded.Content))
	for _, item := range decoded.Content {
		if strings.TrimSpace(item.Type) == "text" && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, item.Text)
		}
	}
	return decodeMemoryDrafts(strings.Join(parts, "\n"))
}

func decodeMemoryDrafts(raw string) ([]MemoryDraft, error) {
	raw = extractJSONPayload(raw)
	var payload struct {
		Items []MemoryDraft `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	out := make([]MemoryDraft, 0, len(payload.Items))
	for _, item := range payload.Items {
		item.Title = strings.TrimSpace(sanitizeMemoryText(item.Title))
		item.Summary = strings.TrimSpace(sanitizeMemoryText(item.Summary))
		item.Category = normalizeCategory(item.Category)
		item.Importance = normalizeImportance(item.Importance)
		item.Confidence = normalizeMemoryConfidence(item.Confidence, KindFact)
		item.Tags = normalizeTags(item.Tags)
		if item.Summary == "" {
			continue
		}
		if item.Title == "" {
			item.Title = normalizeMemoryTitle("", item.Summary)
		}
		out = append(out, item)
	}
	return out, nil
}

func extractJSONPayload(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end >= start {
		return raw[start : end+1]
	}
	return raw
}

func extractOpenAIContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			node, _ := item.(map[string]any)
			if strings.TrimSpace(AnyString(node["type"])) == "text" {
				parts = append(parts, AnyString(node["text"]))
				continue
			}
			if text := AnyString(node["content"]); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func buildMemorySummarizerSystemPrompt(task string) string {
	base := []string{
		"You are a memory curator for an agent system.",
		"Return strict JSON only: {\"items\":[...]} with no markdown.",
		"Decide what is worth storing as durable memory. Do not copy the full dialogue.",
		"Use history to merge overlapping memories into a better consolidated summary instead of repeating near-duplicates.",
		"Skip transient chatter, raw step-by-step reasoning, tool noise, and low-value one-off details.",
		"Each item must be concise, factual, and safe to inject into future prompts.",
		"Allowed categories include: general, preference, constraint, decision, bugfix, workflow, profile, project, remember.",
	}
	if task == "learn" {
		base = append(base, "For learn mode, extract only reusable outcomes or durable observations from the run.")
	} else {
		base = append(base, "For remember mode, extract only the stable facts that should remain after this conversation is forgotten.")
	}
	return strings.Join(base, "\n")
}

func buildMemorySummarizerUserPrompt(prompt memoryPrompt) string {
	lines := []string{
		"task: " + prompt.Task,
		"agent_key: " + strings.TrimSpace(prompt.AgentKey),
		"chat_id: " + strings.TrimSpace(prompt.ChatID),
	}
	if strings.TrimSpace(prompt.UserRequest) != "" {
		lines = append(lines, "user_request:", sanitizeMemoryText(prompt.UserRequest))
	}
	lines = append(lines, "historical_memory:")
	lines = append(lines, renderHistoricalMemory(prompt.History)...)
	lines = append(lines, "source_text:")
	lines = append(lines, sanitizeMemoryText(prompt.SourceText))
	lines = append(lines, "output_schema:")
	lines = append(lines, `{"items":[{"title":"...","summary":"...","category":"general","importance":1,"confidence":0.9,"tags":["tag"]}]}`)
	return strings.Join(lines, "\n")
}

func rememberSourceText(detail chat.Detail) string {
	lines := []string{}
	if strings.TrimSpace(detail.ChatName) != "" {
		lines = append(lines, "chat_name: "+strings.TrimSpace(detail.ChatName))
	}
	for _, sample := range sampleMessages(detail.RawMessages) {
		lines = append(lines, sample)
	}
	summary := extractRememberSummary(detail)
	if strings.TrimSpace(summary) != "" {
		lines = append(lines, "assistant_summary: "+summary)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func learnSourceText(trace chat.RunTrace) string {
	lines := []string{}
	if trace.Query != nil {
		if text := AnyString(trace.Query.Query["message"]); strings.TrimSpace(text) != "" {
			lines = append(lines, "user_message: "+text)
		}
	}
	if strings.TrimSpace(trace.AssistantText) != "" {
		lines = append(lines, "assistant_result: "+strings.TrimSpace(trace.AssistantText))
	}
	for _, step := range trace.Steps {
		for _, message := range step.Messages {
			if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
				continue
			}
			text := extractMessageText(message)
			if strings.TrimSpace(text) != "" {
				lines = append(lines, "assistant_step: "+text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func renderHistoricalMemory(items []api.StoredMemoryResponse) []string {
	if len(items) == 0 {
		return []string{"- (none)"}
	}
	filtered := pickHistoricalMemory(items, 12)
	lines := make([]string, 0, len(filtered))
	for _, item := range filtered {
		lines = append(lines, fmt.Sprintf("- [%s/%s] %s :: %s", item.Kind, item.Category, item.Title, sanitizeMemoryText(item.Summary)))
	}
	return lines
}

func pickHistoricalMemory(items []api.StoredMemoryResponse, limit int) []api.StoredMemoryResponse {
	filtered := make([]api.StoredMemoryResponse, 0, len(items))
	for _, item := range items {
		if normalizeMemoryStatus(item.Status, item.Kind) == StatusArchived || normalizeMemoryStatus(item.Status, item.Kind) == StatusSuperseded {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Importance != filtered[j].Importance {
			return filtered[i].Importance > filtered[j].Importance
		}
		return filtered[i].UpdatedAt > filtered[j].UpdatedAt
	})
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

func buildRememberStoredItems(request api.RememberRequest, chatDetail chat.Detail, agentKey string, drafts []MemoryDraft) []api.StoredMemoryResponse {
	now := time.Now().UnixMilli()
	out := make([]api.StoredMemoryResponse, 0, len(drafts))
	for _, draft := range drafts {
		item := api.StoredMemoryResponse{
			ID:         generateMemoryID(),
			RequestID:  request.RequestID,
			ChatID:     request.ChatID,
			AgentKey:   agentKey,
			SubjectKey: chatDetail.ChatID,
			Kind:       KindFact,
			RefID:      request.ChatID,
			ScopeType:  ScopeAgent,
			ScopeKey:   normalizeScopeKey(ScopeAgent, "", agentKey, "", request.ChatID, ""),
			Title:      draft.Title,
			Summary:    draft.Summary,
			SourceType: "remember",
			Category:   nonEmptyCategory(draft.Category, "remember"),
			Importance: draft.Importance,
			Confidence: draft.Confidence,
			Status:     StatusActive,
			Tags:       append([]string{"remember"}, draft.Tags...),
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		out = append(out, normalizeStoredItem(item))
	}
	return out
}

func buildLearnedMemoriesFromDrafts(input LearnInput, drafts []MemoryDraft) []api.StoredMemoryResponse {
	now := time.Now().UnixMilli()
	out := make([]api.StoredMemoryResponse, 0, len(drafts))
	for _, draft := range drafts {
		item := api.StoredMemoryResponse{
			ID:         generateMemoryID(),
			RequestID:  input.Request.RequestID,
			ChatID:     input.Request.ChatID,
			AgentKey:   strings.TrimSpace(input.AgentKey),
			SubjectKey: normalizeSubjectKey("", input.Request.ChatID, input.AgentKey),
			Kind:       KindObservation,
			RefID:      strings.TrimSpace(input.Trace.RunID),
			ScopeType:  ScopeChat,
			ScopeKey:   observationScopeKey(input),
			Title:      draft.Title,
			Summary:    draft.Summary,
			SourceType: "learn",
			Category:   nonEmptyCategory(draft.Category, classifyObservationCategory(draft.Summary)),
			Importance: draft.Importance,
			Confidence: draft.Confidence,
			Status:     StatusOpen,
			Tags:       append([]string{"learned"}, draft.Tags...),
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		out = append(out, normalizeStoredItem(item))
	}
	return out
}

func summarizeRememberWithFallback(summarizer RememberSummarizer, input RememberSynthesisInput) []MemoryDraft {
	if summarizer != nil {
		drafts, err := summarizer.SummarizeRemember(input)
		if err == nil {
			return drafts
		}
		log.Printf("[memory][remember] summarizer failed, fallback to heuristic (chatId=%s agentKey=%s): %v", input.Request.ChatID, input.AgentKey, err)
	}
	summary := extractRememberSummary(input.Chat)
	if strings.TrimSpace(summary) == "" {
		return nil
	}
	return []MemoryDraft{{
		Summary:    summary,
		Category:   "remember",
		Importance: rememberImportance,
		Confidence: 0.9,
		Tags:       []string{"remember"},
	}}
}

func summarizeLearnWithFallback(summarizer RememberSummarizer, input LearnSynthesisInput) []MemoryDraft {
	if summarizer != nil {
		drafts, err := summarizer.SummarizeLearn(input)
		if err == nil {
			return drafts
		}
		log.Printf("[memory][learn] summarizer failed, fallback to heuristic (chatId=%s agentKey=%s): %v", input.Request.ChatID, input.AgentKey, err)
	}
	stored := extractLearnedMemories(LearnInput{
		Request:  input.Request,
		Trace:    input.Trace,
		AgentKey: input.AgentKey,
		TeamID:   input.TeamID,
		UserKey:  input.UserKey,
	})
	drafts := make([]MemoryDraft, 0, len(stored))
	for _, item := range stored {
		drafts = append(drafts, MemoryDraft{
			Title:      item.Title,
			Summary:    item.Summary,
			Category:   item.Category,
			Importance: item.Importance,
			Confidence: item.Confidence,
			Tags:       item.Tags,
		})
	}
	return drafts
}

func nonEmptyCategory(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func AnyString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
