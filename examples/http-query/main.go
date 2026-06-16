package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type queryRequest struct {
	Message  string `json:"message"`
	AgentKey string `json:"agentKey,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
	RunID    string `json:"runId,omitempty"`
	Stream   *bool  `json:"stream,omitempty"`
}

type apiResponse struct {
	Code int           `json:"code"`
	Msg  string        `json:"msg"`
	Data queryResponse `json:"data"`
}

type queryResponse struct {
	RequestID     string `json:"requestId"`
	RunID         string `json:"runId"`
	ChatID        string `json:"chatId"`
	AgentKey      string `json:"agentKey"`
	AssistantText string `json:"assistantText"`
	FinishReason  string `json:"finishReason"`
}

func main() {
	defaultBaseURL := firstNonBlank(os.Getenv("AGENT_PLATFORM_BASE_URL"), "http://127.0.0.1:11949")
	baseURL := flag.String("base-url", defaultBaseURL, "agent-platform base URL")
	agentKey := flag.String("agent", "default_agent", "agent key")
	message := flag.String("message", "Describe agent-platform in one sentence.", "query message")
	chatID := flag.String("chat-id", "", "optional chat id for continuing a conversation")
	runID := flag.String("run-id", "", "optional run id")
	stream := flag.Bool("stream", false, "use SSE streaming response")
	timeout := flag.Duration("timeout", 2*time.Minute, "HTTP request timeout")
	flag.Parse()

	if strings.TrimSpace(*message) == "" {
		exitf("message must not be empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	reqBody := queryRequest{
		Message:  strings.TrimSpace(*message),
		AgentKey: strings.TrimSpace(*agentKey),
		ChatID:   strings.TrimSpace(*chatID),
		RunID:    strings.TrimSpace(*runID),
	}
	if *stream {
		v := true
		reqBody.Stream = &v
		if err := queryStream(ctx, *baseURL, reqBody); err != nil {
			exitf("%v", err)
		}
		return
	}

	v := false
	reqBody.Stream = &v
	resp, err := queryJSON(ctx, *baseURL, reqBody)
	if err != nil {
		exitf("%v", err)
	}
	fmt.Printf("chatId: %s\n", resp.ChatID)
	fmt.Printf("runId: %s\n", resp.RunID)
	fmt.Printf("agentKey: %s\n", resp.AgentKey)
	fmt.Printf("finishReason: %s\n\n", resp.FinishReason)
	fmt.Println(resp.AssistantText)
}

func queryJSON(ctx context.Context, baseURL string, body queryRequest) (queryResponse, error) {
	httpResp, err := postQuery(ctx, baseURL, body, "application/json")
	if err != nil {
		return queryResponse{}, err
	}
	defer httpResp.Body.Close()

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return queryResponse{}, fmt.Errorf("read response: %w", err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return queryResponse{}, fmt.Errorf("query failed: status=%d body=%s", httpResp.StatusCode, strings.TrimSpace(string(data)))
	}

	var decoded apiResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return queryResponse{}, fmt.Errorf("decode response: %w\nbody=%s", err, strings.TrimSpace(string(data)))
	}
	if decoded.Code != 0 {
		return queryResponse{}, fmt.Errorf("query failed: code=%d msg=%s", decoded.Code, decoded.Msg)
	}
	return decoded.Data, nil
}

func queryStream(ctx context.Context, baseURL string, body queryRequest) error {
	httpResp, err := postQuery(ctx, baseURL, body, "text/event-stream")
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("query failed: status=%d body=%s", httpResp.StatusCode, strings.TrimSpace(string(data)))
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			fmt.Println()
			return nil
		}
		printStreamData(data)
	}
	return scanner.Err()
}

func printStreamData(data string) {
	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		fmt.Println(data)
		return
	}
	eventType, _ := event["type"].(string)
	payload, _ := event["payload"].(map[string]any)
	if payload == nil {
		payload = event
	}
	if eventType == "content.delta" {
		if delta, _ := payload["delta"].(string); delta != "" {
			fmt.Print(delta)
			return
		}
	}
	if eventType == "content.end" || eventType == "run.complete" {
		return
	}
	fmt.Printf("\n[%s] %s\n", eventType, data)
}

func postQuery(ctx context.Context, baseURL string, body queryRequest, accept string) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	url := strings.TrimRight(baseURL, "/") + "/api/query"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", accept)
	return http.DefaultClient.Do(httpReq)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
