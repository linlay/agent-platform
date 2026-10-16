package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/ws"
)

func TestQueryPersistsOriginalJSONAcrossTransports(t *testing.T) {
	for _, transport := range []string{"sse", "json", "ws", "detached"} {
		t.Run(transport, func(t *testing.T) {
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
			}, testFixtureOptions{notifications: ws.NewHub()})
			// Omit all IDs/agent/access/editing defaults in the minimal case.
			raw := `{"message":"hello"}`
			if transport != "sse" {
				raw = `{"message":"hello","agentKey":"mock-agent","accessLevel":" default ","model":{"reasoningEffort":"low"},"includeUsage":false,"includeFullText":false,"planningMode":false,"editingMode":false,"hidden":false,"references":[],"params":{"id":9007199254740993},"scene":null,"sourceUser":"caller","stream":false,"detached":false,"lane":"main","sideId":""}`
			}
			if transport == "detached" {
				raw = strings.Replace(raw, `"detached":false`, `"detached":true`, 1)
			}
			if transport == "sse" || transport == "json" {
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(raw)))
				if rec.Code != http.StatusOK {
					t.Fatalf("query failed: %d %s", rec.Code, rec.Body.String())
				}
			} else {
				conn := dialTestWS(t, fixture.server)
				defer conn.Close()
				if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/query", ID: "original", Payload: json.RawMessage(raw)}); err != nil {
					t.Fatal(err)
				}
				if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				for {
					var frame map[string]any
					if err := conn.ReadJSON(&frame); err != nil {
						t.Fatal(err)
					}
					if frame["frame"] == "error" {
						t.Fatalf("query failed: %#v", frame)
					}
					if frame["id"] == "original" && (frame["frame"] == "response" || frame["reason"] != nil) {
						break
					}
				}
			}
			var line *chat.QueryLine
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				summaries, err := fixture.chats.ListChats("", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(summaries) == 1 && summaries[0].LastRunID != "" {
					line, err = fixture.chats.LoadRunQuery(summaries[0].ChatID, summaries[0].LastRunID)
					if err != nil {
						t.Fatal(err)
					}
					if line != nil {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if line == nil {
				t.Fatal("query was not persisted")
			}
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			var want map[string]any
			if err := decoder.Decode(&want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(line.Query, want) {
				t.Fatalf("incoming query changed: got %#v, want %#v", line.Query, want)
			}
			if line.AccessLevel != "default" || line.AgentKey != "mock-agent" || line.RunID == "" || line.RequestID == "" ||
				line.Origin == nil || line.Origin.Type != "user" || line.Lane != "" {
				t.Fatalf("platform decisions lost beside the query: %#v", line)
			}
			if transport != "sse" {
				model, _ := line.Model.(map[string]any)
				if model["reasoningEffort"] != "LOW" || line.EditingMode == nil || *line.EditingMode {
					t.Fatalf("model normalization or explicit editing intent lost: %#v", line)
				}
			}
		})
	}
}

func TestQueryPreservesReferencesBeforeNormalization(t *testing.T) {
	fixture := newTestFixture(t)
	const raw = `{"message":"read quote","agentKey":"mock-agent","references":[{"type":"selection","id":"quote","text":"quoted text","name":"client name","path":"/client/path","meta":{"clientOnly":true}}]}`
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("query failed: %d %s", rec.Code, rec.Body.String())
	}
	event := findSSEMessageByType(t, decodeSSEMessages(t, rec.Body.String()), "request.query")
	line, err := fixture.chats.LoadRunQuery(event["chatId"].(string), event["runId"].(string))
	if err != nil || line == nil {
		t.Fatalf("load query: %#v, %v", line, err)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(line.Query, want) {
		t.Fatalf("reference normalized inside original query: %#v", line.Query)
	}
	refs, _ := line.References.([]any)
	if len(refs) != 1 {
		t.Fatalf("execution references lost: %#v", line)
	}
	ref, _ := refs[0].(map[string]any)
	if ref["path"] != nil || ref["name"] != nil || ref["meta"] != nil || ref["text"] != "quoted text" {
		t.Fatalf("execution reference must still be normalized: %#v", ref)
	}
}

func TestQueryIgnoresRoleInRequestBody(t *testing.T) {
	fixture := newTestFixture(t)
	const raw = `{"message":"hi","agentKey":"mock-agent","role":"automation"}`
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("role must be ignored, got %d %s", rec.Code, rec.Body.String())
	}
	event := findSSEMessageByType(t, decodeSSEMessages(t, rec.Body.String()), "request.query")
	line, err := fixture.chats.LoadRunQuery(event["chatId"].(string), event["runId"].(string))
	if err != nil || line == nil {
		t.Fatalf("load query: %#v, %v", line, err)
	}
	// Still recorded as received, but it does not change who sent the input.
	if line.Query["role"] != "automation" || line.Origin == nil || line.Origin.Type != "user" || event["role"] != nil {
		t.Fatalf("role must be kept only in the original query: %#v event=%#v", line, event)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"message":"hi","btwId":"btw_1"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("removed btwId stays an unknown field, got %d %s", rec.Code, rec.Body.String())
	}
}
