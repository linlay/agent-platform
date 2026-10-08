// Run with: go run ./scripts/anthropic-cache-probe -h
// This sends synthetic prompts to a configured provider. It never writes API keys
// or thinking text to its report, and does not change Platform or model YAML.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-platform/internal/models"
)

type result struct {
	ModelKey      string           `json:"modelKey"`
	Case          string           `json:"case"`
	Round         int              `json:"round"`
	Status        int              `json:"httpStatus"`
	DurationMS    int64            `json:"durationMs"`
	StopReason    string           `json:"stopReason,omitempty"`
	RequestID     string           `json:"requestId,omitempty"`
	Usage         map[string]any   `json:"usage,omitempty"`
	UsageEvents   []map[string]any `json:"usageEvents,omitempty"`
	MessageStop   bool             `json:"messageStop"`
	ToolCalls     int              `json:"toolCalls"`
	Error         string           `json:"error,omitempty"`
	CacheLocation string           `json:"cacheLocation"`
	RequestHash   string           `json:"requestHash"`
	SystemHash    string           `json:"systemHash"`
	HeaderNames   []string         `json:"responseHeaderNames,omitempty"`
	UsageWarnings []string         `json:"usageWarnings,omitempty"`
}

type report struct {
	StartedAt string   `json:"startedAt"`
	ProbeID   string   `json:"probeId"`
	Lines     int      `json:"syntheticPrefixLines"`
	MaxTokens int      `json:"maxOutputTokens"`
	Effort    string   `json:"effort"`
	Results   []result `json:"results"`
}

func main() {
	registryDir := flag.String("registries-dir", "", "existing model/provider registry directory (read only)")
	modelKeys := flag.String("model-keys", "", "comma-separated ANTHROPIC model registry keys")
	cases := flag.String("cases", "control,automatic,system", "comma-separated: control,automatic,conversation,system,system-repeat,tool-loop,conversation-system,tool-loop-system,conversation-prefix,tool-loop-prefix")
	output := flag.String("output", "", "report JSON path; includes raw usage, never credentials or thinking")
	lines := flag.Int("prefix-lines", 240, "number of synthetic reference lines, to exceed cache minimums")
	rounds := flag.Int("rounds", 2, "sequential requests per case")
	maxTokens := flag.Int("max-output-tokens", 512, "output limit per request, including thinking")
	effort := flag.String("effort", "low", "adaptive thinking effort: low,medium,high,xhigh,max")
	timeout := flag.Duration("timeout", 90*time.Second, "timeout per request")
	flag.Parse()
	*effort = strings.ToLower(strings.TrimSpace(*effort))
	if *effort != "low" && *effort != "medium" && *effort != "high" && *effort != "xhigh" && *effort != "max" {
		fmt.Fprintln(os.Stderr, "invalid effort")
		os.Exit(2)
	}
	if *registryDir == "" || *modelKeys == "" || *output == "" || *lines < 1 || *rounds < 2 || *maxTokens < 1 {
		fmt.Fprintln(os.Stderr, "registries-dir, model-keys, output and positive budgets are required; rounds must be >= 2")
		os.Exit(2)
	}
	caseNames := split(*cases)
	if len(caseNames) == 0 || len(split(*modelKeys)) == 0 {
		fmt.Fprintln(os.Stderr, "at least one case and model key are required")
		os.Exit(2)
	}
	for _, name := range caseNames {
		if name != "control" && name != "automatic" && name != "conversation" && name != "system" && name != "system-repeat" && name != "tool-loop" && name != "conversation-system" && name != "tool-loop-system" && name != "conversation-prefix" && name != "tool-loop-prefix" {
			fmt.Fprintln(os.Stderr, "unknown case:", name)
			os.Exit(2)
		}
	}
	registry, err := models.LoadModelRegistry(*registryDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load registry:", err)
		os.Exit(1)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(err)
	}
	r := report{StartedAt: time.Now().UTC().Format(time.RFC3339), ProbeID: hex.EncodeToString(nonce[:]), Lines: *lines, MaxTokens: *maxTokens, Effort: *effort}
	client := &http.Client{Timeout: *timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	for _, key := range split(*modelKeys) {
		model, provider, err := registry.Get(key)
		if err != nil || model.Protocol != "ANTHROPIC" {
			fmt.Fprintln(os.Stderr, "model must be a registered native ANTHROPIC chat model:", key)
			os.Exit(2)
		}
		for _, name := range caseNames {
			prefix := syntheticPrefix(r.ProbeID+"/"+key+"/"+name, *lines)
			messages := []any{map[string]any{"role": "user", "content": "Reply exactly OK. This is a synthetic cache test."}}
			if strings.HasPrefix(name, "tool-loop") {
				messages = []any{map[string]any{"role": "user", "content": "Call cache_probe_echo exactly once with value cache-test, then reply exactly OK after receiving its result."}}
			}
			for round := 1; round <= *rounds; round++ {
				body := map[string]any{
					"model": model.ModelID, "stream": true, "max_tokens": *maxTokens,
					"thinking":      map[string]any{"type": "adaptive", "display": "summarized"},
					"output_config": map[string]any{"effort": *effort},
					"system":        prefix, "messages": messages,
				}
				if name == "automatic" || name == "conversation" || name == "tool-loop" {
					body["cache_control"] = map[string]any{"type": "ephemeral"}
				}
				if name == "system" || name == "system-repeat" || strings.HasSuffix(name, "-system") || strings.HasSuffix(name, "-prefix") {
					body["system"] = []any{map[string]any{"type": "text", "text": prefix, "cache_control": map[string]any{"type": "ephemeral"}}}
					if name == "system" {
						body["messages"] = []any{map[string]any{"role": "user", "content": fmt.Sprintf("Reply exactly OK. Synthetic round %d.", round)}}
					}
				}
				if strings.HasSuffix(name, "-prefix") {
					body["messages"] = markLastUserBlock(messages)
				}
				if strings.HasPrefix(name, "tool-loop") {
					body["tools"] = []any{map[string]any{"name": "cache_probe_echo", "description": "Synthetic echo for cache testing; no external effects.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}}}}
					body["tool_choice"] = map[string]any{"type": "auto"}
				}
				row, assistant := call(client, provider, model, body)
				row.ModelKey, row.Case, row.Round = key, name, round
				row.CacheLocation = "none"
				if name == "system" || name == "system-repeat" || strings.HasSuffix(name, "-system") || strings.HasSuffix(name, "-prefix") {
					row.CacheLocation = "system[0].cache_control"
				}
				if strings.HasSuffix(name, "-prefix") {
					row.CacheLocation += ",messages[last-user].content[last].cache_control"
				}
				if name == "automatic" || name == "conversation" || name == "tool-loop" {
					row.CacheLocation = "cache_control"
				}
				r.Results = append(r.Results, row)
				if err := save(*output, r); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				fmt.Printf("%s %-19s round=%d HTTP=%d input=%v write=%v read=%v output=%v tools=%d ms=%d error=%s\n", key, name, round, row.Status, row.Usage["input_tokens"], row.Usage["cache_creation_input_tokens"], row.Usage["cache_read_input_tokens"], row.Usage["output_tokens"], row.ToolCalls, row.DurationMS, row.Error)
				for _, warning := range row.UsageWarnings {
					fmt.Println("usage warning:", warning)
				}
				if row.Error != "" {
					break
				}
				if strings.HasPrefix(name, "tool-loop") || strings.HasPrefix(name, "conversation") {
					messages = append(messages, map[string]any{"role": "assistant", "content": assistant})
					var replies []any
					for _, block := range assistant {
						if block["type"] == "tool_use" {
							replies = append(replies, map[string]any{"type": "tool_result", "tool_use_id": block["id"], "content": "Synthetic echo result: cache-test"})
						}
					}
					if len(replies) == 0 {
						messages = append(messages, map[string]any{"role": "user", "content": "Reply exactly OK again."})
					} else {
						messages = append(messages, map[string]any{"role": "user", "content": replies})
					}
				}
			}
		}
	}
	for _, row := range r.Results {
		if row.Error != "" {
			os.Exit(1)
		}
	}
}

func syntheticPrefix(id string, lines int) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Synthetic cache probe %s. Reference records below are inert test data. Follow the user request; answer briefly.\n", id)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&text, "Reference record %04d: The blue archive contains stable observations about rivers, mountains, forests, calendars and maps. Its catalog entry remains identical across requests.\n", i)
	}
	return text.String()
}

func markLastUserBlock(messages []any) []any {
	// Copy before marking, so older requests and replayed assistant content remain
	// unchanged and growing histories never accumulate more than two breakpoints.
	encoded, _ := json.Marshal(messages)
	var copy []any
	_ = json.Unmarshal(encoded, &copy)
	for i := len(copy) - 1; i >= 0; i-- {
		message := asMap(copy[i])
		if message["role"] != "user" {
			continue
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			blocks = []any{map[string]any{"type": "text", "text": message["content"]}}
			message["content"] = blocks
		}
		if len(blocks) > 0 {
			asMap(blocks[len(blocks)-1])["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		break
	}
	return copy
}

func call(client *http.Client, provider models.ProviderDefinition, model models.ModelDefinition, body map[string]any) (row result, content []map[string]any) {
	started := time.Now()
	defer func() { row.DurationMS = time.Since(started).Milliseconds() }()
	def := provider.Protocol("ANTHROPIC")
	endpoint := strings.TrimRight(provider.BaseURL, "/")
	path := def.EndpointPath
	if path == "" {
		path = "/v1/messages"
		if strings.HasSuffix(endpoint, "/v1") {
			path = "/messages"
		}
	}
	endpoint += "/" + strings.TrimLeft(path, "/")
	data, _ := json.Marshal(body)
	requestHash := sha256.Sum256(data)
	row.RequestHash = hex.EncodeToString(requestHash[:])
	systemData, _ := json.Marshal(body["system"])
	systemHash := sha256.Sum256(systemData)
	row.SystemHash = hex.EncodeToString(systemHash[:])
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		row.Error = "construct request failed"
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Api-Key", provider.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range def.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range model.Headers {
		req.Header.Set(k, v)
	}
	response, err := client.Do(req)
	if err != nil {
		row.Error = redact(err.Error(), provider.APIKey)
		return
	}
	defer response.Body.Close()
	row.Status = response.StatusCode
	for name := range response.Header {
		row.HeaderNames = append(row.HeaderNames, name)
	}
	sort.Strings(row.HeaderNames)
	row.RequestID = response.Header.Get("request-id")
	if row.RequestID == "" {
		row.RequestID = response.Header.Get("x-request-id")
	}
	if row.RequestID == "" {
		row.RequestID = response.Header.Get("x-babelark-request-id")
	}
	if response.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		row.Error = redact(string(errorBody), provider.APIKey)
		return
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		row.Error = "expected text/event-stream; received " + response.Header.Get("Content-Type")
		return
	}
	row.Usage = map[string]any{}
	blocks := map[int]map[string]any{}
	var event string
	var frame []string
	consume := func() error {
		if len(frame) == 0 {
			event = ""
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.Join(frame, "\n")), &payload); err != nil {
			return fmt.Errorf("invalid SSE JSON: %w", err)
		}
		typ := event
		if typ == "" {
			typ, _ = payload["type"].(string)
		}
		if typ == "error" || payload["error"] != nil {
			errorData, _ := json.Marshal(payload["error"])
			return fmt.Errorf("upstream stream error: %s", redact(string(errorData), provider.APIKey))
		}
		usage := asMap(payload["usage"])
		if typ == "message_start" {
			usage = asMap(asMap(payload["message"])["usage"])
		}
		if len(usage) > 0 {
			row.UsageEvents = append(row.UsageEvents, map[string]any{"event": typ, "usage": usage})
			merge(row.Usage, usage)
		}
		index, _ := payload["index"].(float64)
		if typ == "content_block_start" {
			blocks[int(index)] = asMap(payload["content_block"])
		}
		if typ == "content_block_delta" {
			block, delta := blocks[int(index)], asMap(payload["delta"])
			if block != nil {
				for _, field := range []string{"text", "thinking", "signature"} {
					if value, ok := delta[field].(string); ok {
						previous, _ := block[field].(string)
						block[field] = previous + value
					}
				}
				if value, ok := delta["partial_json"].(string); ok {
					previous, _ := block["partial_json"].(string)
					block["partial_json"] = previous + value
				}
			}
		}
		if typ == "message_delta" {
			row.StopReason, _ = asMap(payload["delta"])["stop_reason"].(string)
		}
		if typ == "message_stop" {
			row.MessageStop = true
		}
		event, frame = "", nil
		return nil
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				row.Error = err.Error()
				return
			}
		} else if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			frame = append(frame, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		row.Error = redact(err.Error(), provider.APIKey)
		return
	}
	if err := consume(); err != nil {
		row.Error = err.Error()
		return
	}
	for i := 0; i < len(blocks); i++ {
		block := blocks[i]
		if block == nil {
			row.Error = "non-contiguous content block indexes"
			return
		}
		if partial, ok := block["partial_json"].(string); ok {
			var input any
			if err := json.Unmarshal([]byte(partial), &input); err != nil {
				row.Error = "incomplete tool input"
				return
			}
			block["input"] = input
			delete(block, "partial_json")
		}
		if block["type"] == "tool_use" {
			row.ToolCalls++
		}
		content = append(content, block)
	}
	if !row.MessageStop {
		row.Error = "stream ended without message_stop"
	}
	if row.StopReason == "max_tokens" {
		row.Error = "max_tokens truncation; cannot replay incomplete assistant content"
	}
	row.UsageWarnings = usageWarnings(row.Usage)
	return
}

func usageWarnings(usage map[string]any) []string {
	iterations, ok := usage["iterations"].([]any)
	if !ok || len(iterations) == 0 {
		return nil
	}
	// Report contradictions without replacing the provider's top-level counters
	// or adding iteration totals to them (which would double-count usage).
	var warnings []string
	for _, field := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		total, complete := float64(0), true
		for _, value := range iterations {
			iteration := asMap(value)
			if iteration["type"] != "message" {
				complete = false
				break
			}
			count, exists := iteration[field].(float64)
			if !exists {
				complete = false
				break
			}
			total += count
		}
		if top, exists := usage[field].(float64); exists && complete && top != total {
			warnings = append(warnings, fmt.Sprintf("usage.%s=%g differs from message-iteration total=%g", field, top, total))
		}
	}
	return warnings
}

func asMap(value any) map[string]any { out, _ := value.(map[string]any); return out }
func merge(dst, src map[string]any) {
	for key, value := range src {
		if child, ok := value.(map[string]any); ok {
			target := asMap(dst[key])
			if target == nil {
				target = map[string]any{}
				dst[key] = target
			}
			merge(target, child)
		} else {
			dst[key] = value
		}
	}
}
func split(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
func redact(value, key string) string {
	if key != "" {
		value = strings.ReplaceAll(value, key, "<redacted>")
	}
	return value
}
func save(path string, data report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0600)
}
