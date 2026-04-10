package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
)

// handleProxyQuery forwards the /api/query request to a remote AGW-compatible
// service (e.g. claude-code relay-server) and pipes the SSE response back to
// the client unchanged. No protocol conversion needed — both sides speak AGW.
func (s *Server) handleProxyQuery(w http.ResponseWriter, r *http.Request, req api.QueryRequest, agentDef catalog.AgentDefinition) {
	proxy := agentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		writeJSON(w, http.StatusBadGateway, api.Failure(http.StatusBadGateway, "PROXY agent missing proxyConfig.baseUrl"))
		return
	}

	baseURL := strings.TrimRight(proxy.BaseURL, "/")
	targetURL := baseURL + "/api/query"

	// Build request body — pass through the original fields.
	body, err := json.Marshal(map[string]any{
		"requestId":  req.RequestID,
		"chatId":     req.ChatID,
		"agentKey":   req.AgentKey,
		"role":       req.Role,
		"message":    req.Message,
		"references": req.References,
		"params":     req.Params,
		"scene":      req.Scene,
		"stream":     true,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	timeout := time.Duration(proxy.TimeoutMs) * time.Millisecond
	client := &http.Client{Timeout: timeout}

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, api.Failure(http.StatusBadGateway, "failed to create proxy request: "+err.Error()))
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "text/event-stream")
	if proxy.Token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+proxy.Token)
	}

	log.Printf("[proxy] forwarding query to %s (agent=%s, chatId=%s)", targetURL, agentDef.Key, req.ChatID)

	resp, err := client.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, api.Failure(http.StatusBadGateway, "proxy request failed: "+err.Error()))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		writeJSON(w, resp.StatusCode, api.Failure(resp.StatusCode, fmt.Sprintf("upstream returned %d: %s", resp.StatusCode, string(data))))
		return
	}

	// Set SSE headers and pipe the upstream SSE stream to the client.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "streaming not supported"))
		return
	}

	// Pipe SSE lines from upstream to client line-by-line.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		// Flush on empty line (end of SSE event) or data line.
		if line == "" || strings.HasPrefix(line, "data:") {
			flusher.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[proxy] stream read error: %v", err)
	}

	log.Printf("[proxy] stream completed (agent=%s, chatId=%s)", agentDef.Key, req.ChatID)
}
