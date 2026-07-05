package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTailSteerQueuedBeforeFinalDoneContinuesSameRun(t *testing.T) {
	const (
		chatID       = "chat-tail-steer"
		steerMessage = "Please answer this tail steer before finishing."
	)

	var providerCallCount atomic.Int32
	releaseFirstDone := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirstDone) }) }
	t.Cleanup(release)
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"draft answer"}}]}`)
			<-releaseFirstDone
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"tail steer handled"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})
	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"chatId":"`+chatID+`","message":"start"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if id, _ := payload["runId"].(string); id != "" {
				runID = id
			}
			if payload["type"] == "content.delta" && payload["delta"] == "draft answer" {
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before steer: %v", readErr)
		}
	}
	if runID == "" {
		t.Fatalf("expected runId before steer, stream=%s", streamBody.String())
	}

	steerRec := httptest.NewRecorder()
	steerReq := httptest.NewRequest(http.MethodPost, "/api/steer", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+runID+`","chatId":"`+chatID+`","message":"`+steerMessage+`"}`))
	steerReq.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(steerRec, steerReq)
	if steerRec.Code != http.StatusOK {
		t.Fatalf("steer expected 200, got %d: %s", steerRec.Code, steerRec.Body.String())
	}
	var steerResp apiSteerResponse
	if err := json.Unmarshal(steerRec.Body.Bytes(), &steerResp); err != nil {
		t.Fatalf("decode steer response: %v", err)
	}
	if !steerResp.Data.Accepted || steerResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted steer, got %#v", steerResp.Data)
	}

	release()
	seenSteer := false
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "request.steer":
				seenSteer = true
			case "run.complete":
				if !seenSteer {
					t.Fatalf("run.complete arrived before request.steer: %s", streamBody.String())
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after steer: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"request.steer"`) {
		t.Fatalf("expected request.steer in stream, got %s", body)
	}
	if !strings.Contains(body, "tail steer handled") {
		t.Fatalf("expected second turn answer in stream, got %s", body)
	}
	assertEventOrder(t, body, "content.delta", "request.steer", "run.complete")

	select {
	case messages := <-secondTurnMessages:
		if len(messages) == 0 {
			t.Fatalf("expected second provider request messages")
		}
		last := messages[len(messages)-1]
		if last["role"] != "user" || last["content"] != steerMessage {
			t.Fatalf("expected tail steer as final second-turn user message, got %#v", messages)
		}
	default:
		t.Fatalf("expected second provider request")
	}

	jsonlContent, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load chat jsonl: %v", err)
	}
	assertSteerPersistedWithoutInputMessage(t, jsonlContent, steerMessage)
}

func TestLateSteerAfterRunCompleteIsUnmatched(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"final answer"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"finish normally"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	runID := ""
	for {
		line, readErr := reader.ReadString('\n')
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if id, _ := payload["runId"].(string); id != "" {
				runID = id
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream: %v", readErr)
		}
	}
	if runID == "" {
		t.Fatalf("expected runId in completed stream")
	}

	steerRec := httptest.NewRecorder()
	steerReq := httptest.NewRequest(http.MethodPost, "/api/steer", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+runID+`","message":"too late"}`))
	steerReq.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(steerRec, steerReq)
	if steerRec.Code != http.StatusOK {
		t.Fatalf("late steer expected 200, got %d: %s", steerRec.Code, steerRec.Body.String())
	}
	var steerResp apiSteerResponse
	if err := json.Unmarshal(steerRec.Body.Bytes(), &steerResp); err != nil {
		t.Fatalf("decode late steer response: %v", err)
	}
	if steerResp.Data.Accepted || steerResp.Data.Status != "unmatched" {
		t.Fatalf("expected late steer to be unmatched, got %#v", steerResp.Data)
	}
}

type apiSteerResponse struct {
	Data struct {
		Accepted bool   `json:"accepted"`
		Status   string `json:"status"`
	} `json:"data"`
}

func assertSteerPersistedWithoutInputMessage(t *testing.T, jsonlContent string, steerMessage string) {
	t.Helper()
	foundSteer := false
	for _, line := range strings.Split(jsonlContent, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode jsonl line %q: %v", line, err)
		}
		if entry["_type"] == "steer" {
			foundSteer = true
		}
		if inputMessages, ok := entry["inputMessages"]; ok {
			encoded, err := json.Marshal(inputMessages)
			if err != nil {
				t.Fatalf("marshal inputMessages: %v", err)
			}
			if strings.Contains(string(encoded), steerMessage) {
				t.Fatalf("did not expect steer in inputMessages line: %s", line)
			}
		}
	}
	if !foundSteer {
		t.Fatalf("expected persisted steer line in:\n%s", jsonlContent)
	}
}
