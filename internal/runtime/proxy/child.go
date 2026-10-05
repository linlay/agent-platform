package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/httpclient"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type ChildResult struct{ Status, Text, Error, ErrorCode string }

func RunChild(ctx context.Context, runID string, taskID string, subReq runtimetypes.QueryCommand, workspaceRoot string, config *catalog.ProxyConfig, route func(stream.StreamInput)) *ChildResult {
	result := &ChildResult{Status: "completed"}
	proxy := config
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		result.Status = "failed"
		result.Text = "PROXY sub-agent missing proxyConfig.baseUrl"
		result.Error = result.Text
		return result
	}

	targetURL := strings.TrimRight(proxy.BaseURL, "/") + "/api/query"
	payload := map[string]any{
		"agentKey":   AgentKey(proxy, subReq.AgentKey),
		"message":    subReq.Message,
		"references": subReq.References,
	}
	if chatID := strings.TrimSpace(proxy.ChatID); chatID != "" {
		payload["chatId"] = chatID
	}
	if params := ForwardParams(subReq, proxy, workspaceRoot); params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}

	client := httpclient.NewClient(RequestTimeout(proxy))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if strings.TrimSpace(proxy.Token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(proxy.Token))
	}

	resp, err := client.Do(req)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		result.Status = "failed"
		result.Text = fmt.Sprintf("PROXY sub-agent returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		result.Error = result.Text
		return result
	}

	var assistantText strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		event, ok, decodeErr := parseProxySSEDataLineAt(scanner.Text())
		if decodeErr != nil {
			result.Status = "failed"
			result.Text = "time contract violation"
			result.Error = decodeErr.Error()
			result.ErrorCode = "time_contract_violation"
			return result
		}
		if !ok {
			continue
		}
		switch event.Type {
		case "content.delta":
			delta, _ := event.Payload["delta"].(string)
			if delta == "" {
				continue
			}
			assistantText.WriteString(delta)
			contentID, _ := event.Payload["contentId"].(string)
			if strings.TrimSpace(contentID) == "" {
				contentID = taskID + ":proxy"
			} else {
				contentID = taskID + ":" + strings.TrimSpace(contentID)
			}
			route(stream.ContentDelta{
				ContentID: contentID,
				TaskID:    taskID,
				Delta:     delta,
			})
		case "run.error":
			result.Status = "failed"
			result.Text = childErrorMessage(event.Payload)
			result.Error = result.Text
			return result
		case "run.cancel":
			result.Status = "cancelled"
			result.Text = "sub-agent cancelled"
			return result
		case "run.complete":
			result.Text = strings.TrimSpace(assistantText.String())
			if result.Text == "" {
				result.Status = "failed"
				result.Text = "PROXY sub-agent returned run.complete without assistant content"
				result.Error = result.Text
			}
			return result
		}
	}
	if err := scanner.Err(); err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	result.Text = strings.TrimSpace(assistantText.String())
	if result.Text == "" {
		result.Status = "failed"
		result.Text = "PROXY sub-agent returned an empty SSE stream"
		result.Error = result.Text
	}
	return result
}
func parseProxySSEDataLineAt(line string) (stream.EventData, bool, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return stream.EventData{}, false, nil
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == stream.DoneSentinel {
		return stream.EventData{}, false, nil
	}
	return DecodeEventAt([]byte(payload), "proxy.child.sse.event")
}
func RequestTimeout(proxy *catalog.ProxyConfig) time.Duration {
	if proxy != nil && proxy.TimeoutMS > 0 {
		return time.Duration(proxy.TimeoutMS) * time.Millisecond
	}
	if proxy != nil && proxy.Timeout > 0 {
		return time.Duration(proxy.Timeout) * time.Second
	}
	return 5 * time.Minute
}
func childErrorMessage(payload map[string]any) string {
	if payload == nil {
		return "sub-agent failed"
	}
	if message := firstPayloadString(payload, "message", "error", "reason", "detail", "msg"); message != "" {
		return message
	}
	for _, key := range []string{"error", "rawEvent"} {
		if nested, ok := payload[key].(map[string]any); ok {
			if message := firstPayloadString(nested, "message", "error", "reason", "detail", "msg"); message != "" {
				return message
			}
		}
	}
	if data, err := json.Marshal(payload); err == nil && len(data) > 0 {
		return "sub-agent failed: " + string(data)
	}
	return "sub-agent failed"
}
func firstPayloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, _ := payload[key].(string)
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
