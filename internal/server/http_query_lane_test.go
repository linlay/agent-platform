package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/chat"
)

func TestHTTPQueryMainLane(t *testing.T) {
	for _, lane := range []string{"", "main"} {
		t.Run("lane_"+lane, func(t *testing.T) {
			fixture := newTestFixture(t)
			payload := map[string]any{"chatId": "chat-http-main", "agentKey": "mock-agent", "message": "main question", "stream": false}
			if lane != "" {
				payload["lane"] = lane
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(marshalPayload(payload))))
			if rec.Code != http.StatusOK || rec.Header().Get("X-Btw-Id") != "" {
				t.Fatalf("main response: %d %s", rec.Code, rec.Body.String())
			}
			summary, err := fixture.chats.Summary("chat-http-main")
			if err != nil || summary == nil || summary.LastRunID == "" {
				t.Fatalf("main did not update chat: %#v %v", summary, err)
			}
		})
	}
}

func TestHTTPQueryBTWLanePreservesHiddenBranch(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		name := "json"
		if streaming {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newTestFixture(t)
			const chatID = "chat-http-btw"
			serveJSONRequestForBTWTest(t, fixture.server, "/api/query", `{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"parent","stream":false}`)
			parentPath := filepath.Join(fixture.cfg.Paths.ChatsDir, chatID+".jsonl")
			before, err := os.ReadFile(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			summaryBefore, err := fixture.chats.Summary(chatID)
			if err != nil {
				t.Fatal(err)
			}
			var btwID, previousRunID string
			for _, message := range []string{"first side question", "continued side question"} {
				payload := map[string]any{"lane": "btw", "chatId": chatID, "btwId": btwID, "message": message, "stream": streaming,
					"references": []any{map[string]any{"type": "selection", "text": "selected source"}}}
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(marshalPayload(payload))))
				if rec.Code != http.StatusOK {
					t.Fatalf("side response: %d %s", rec.Code, rec.Body.String())
				}
				id, runID := rec.Header().Get("X-Btw-Id"), rec.Header().Get("X-Run-Id")
				if !chat.ValidBTWID(id) || (btwID != "" && id != btwID) || runID == "" || runID == previousRunID {
					t.Fatalf("bad identity btw=%q run=%q", id, runID)
				}
				btwID, previousRunID = id, runID
				if streaming {
					if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
						t.Fatal("expected SSE")
					}
					event := findSSEMessageByType(t, decodeSSEMessages(t, rec.Body.String()), "request.query")
					if event["hidden"] != true || event["btwId"] != btwID {
						t.Fatalf("not hidden: %#v", event)
					}
				} else {
					var response struct {
						Data struct {
							BTWID string `json:"btwId"`
							RunID string `json:"runId"`
						} `json:"data"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if response.Data.BTWID != btwID || response.Data.RunID != runID {
						t.Fatalf("bad JSON: %s", rec.Body.String())
					}
				}
			}
			after, err := os.ReadFile(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			summaryAfter, err := fixture.chats.Summary(chatID)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !reflect.DeepEqual(summaryBefore, summaryAfter) {
				t.Fatal("side query changed main history or summary")
			}
			branch, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.ChatsDir, chatID, chat.BTWRootDirName, btwID+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"first side question", "continued side question", "selected source"} {
				if !bytes.Contains(branch, []byte(text)) {
					t.Fatalf("branch lost %q", text)
				}
			}
		})
	}
}

func TestHTTPQueryRejectsUnsupportedLaneBeforeCreatingChat(t *testing.T) {
	for _, tc := range []struct {
		lane   string
		status int
		code   string
	}{
		{"explain", http.StatusForbidden, "explain_ws_required"},
		{"unknown", http.StatusBadRequest, "invalid_lane"},
	} {
		t.Run(tc.lane, func(t *testing.T) {
			fixture := newTestFixture(t)
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(marshalPayload(map[string]any{"lane": tc.lane, "chatId": "chat-rejected-lane", "agentKey": "mock-agent", "message": "reject me"}))))
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
			}
			summary, err := fixture.chats.Summary("chat-rejected-lane")
			if err != nil || summary != nil {
				t.Fatalf("rejected lane created chat: %#v %v", summary, err)
			}
		})
	}
}
