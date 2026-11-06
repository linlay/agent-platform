package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	rt "agent-platform/internal/runtime/types"
)

func TestVoiceExplicitNewChatIsolatedFromPrimary(t *testing.T) {
	for _, primary := range []bool{false, true} {
		t.Run(fmt.Sprintf("primary=%t", primary), func(t *testing.T) {
			const oldID = "old-primary"
			const oldMessage = "old conversation context marker"
			const newMessage = "new voice question marker"
			var mu sync.Mutex
			var requests []string
			f := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				raw, _ := json.Marshal(body)
				mu.Lock()
				requests = append(requests, string(raw))
				mu.Unlock()
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"response"},"finish_reason":"stop"}]}`, "[DONE]")
			})
			if response := postPrimaryQuery(t, f.server, map[string]any{"agentKey": "mock-agent", "chatId": oldID, "message": oldMessage, "primary": true}); response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			srv, provider := enableVoiceFixture(t, f, nil)
			provider.transcript.Store(newMessage)
			hello := voiceHello("", 16000)
			hello["newChat"] = true
			if primary {
				hello["primary"] = true
			}
			c, freshID := dialVoiceHello(t, srv, hello)
			if freshID == oldID {
				t.Fatal("new conversation reused the old primary")
			}
			if summary, err := f.chats.Summary(freshID); err != nil || summary != nil {
				t.Fatal("hello created an empty Chat")
			}
			for turn := 1; turn <= 2; turn++ {
				speakVoice(t, c)
				started, _ := voiceUntil(t, c, "run", "start")
				if started["chatId"] != freshID {
					t.Fatal("Run changed the reserved Chat", started)
				}
				voiceUntil(t, c, "tts", "stop")
				if len(queryLines(t, f, oldID)) != 1 || len(queryLines(t, f, freshID)) != turn {
					t.Fatal("voice appended to the old Chat or created another Chat")
				}
			}
			rows := queryLines(t, f, freshID)
			first := rows[0]["query"].(map[string]any)
			second := rows[1]["query"].(map[string]any)
			if (first["primary"] == true) != primary || second["primary"] != nil {
				t.Fatal("primary creation intent was lost or replayed", first, second)
			}
			wantPrimary := oldID
			if primary {
				wantPrimary = freshID
			}
			if got := agentPrimaryChatID(t, f.server, "mock-agent"); got != wantPrimary {
				t.Fatalf("primary=%q want %q", got, wantPrimary)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 3 {
				t.Fatalf("unexpected model requests: %d", len(requests))
			}
			for _, request := range requests[1:] {
				if strings.Contains(request, oldMessage) || !strings.Contains(request, newMessage) {
					t.Fatal("new Chat inherited old conversation context or lost its own question")
				}
			}
		})
	}
}

func TestVoiceExplicitExistingChatIgnoresPrimary(t *testing.T) {
	f := newTestFixture(t)
	for _, id := range []string{"history-chat", "current-primary"} {
		if response := postPrimaryQuery(t, f.server, map[string]any{"agentKey": "mock-agent", "chatId": id, "message": "seed", "primary": true}); response.Code != http.StatusOK {
			t.Fatal(response.Body.String())
		}
	}
	srv, _ := enableVoiceFixture(t, f, nil)
	c, id := dialVoice(t, srv, "history-chat", 16000)
	speakVoice(t, c)
	voiceUntil(t, c, "tts", "stop")
	if id != "history-chat" || len(queryLines(t, f, id)) != 2 || len(queryLines(t, f, "current-primary")) != 1 || agentPrimaryChatID(t, f.server, "mock-agent") != "current-primary" {
		t.Fatal("explicit Chat binding changed")
	}
}

func TestVoiceExplicitNewPrimaryRespectsBusyPrimary(t *testing.T) {
	f := newTestFixture(t)
	engine := &gatedQueryEngine{entered: make(chan struct{}), release: make(chan struct{})}
	f.server.deps.Agent = engine
	bindTestRuntime(f.server)
	srv, provider := enableVoiceFixture(t, f, nil)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(engine.release) }) })
	handle, err := f.server.deps.Runtime.StartQuery(context.Background(), rt.QueryCommand{AgentKey: "mock-agent", ChatID: "busy-primary", Message: "running", Primary: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("primary Run did not enter")
	}
	hello := voiceHello("", 16000)
	hello["newChat"], hello["primary"] = true, true
	c, reserved := dialVoiceHello(t, srv, hello)
	speakVoice(t, c)
	for {
		msg, _ := readVoice(t, c)
		if msg["type"] == "error" {
			if msg["code"] != "chat_busy" {
				t.Fatal("expected existing primary busy rejection", msg)
			}
			break
		}
	}
	if summary, err := f.chats.Summary(reserved); err != nil || summary != nil {
		t.Fatal("rejected Query left an empty Chat")
	}
	active, found, err := f.runs.ActiveRunForChat("busy-primary")
	if err != nil || !found || active.RunID != handle.RunID || provider.tts.Load() != 0 || agentPrimaryChatID(t, f.server, "mock-agent") != "busy-primary" {
		t.Fatal("voice replaced or interrupted the busy primary")
	}
}

func TestVoiceExplicitNewChatWaitsForValidTranscript(t *testing.T) {
	for _, primary := range []bool{false, true} {
		for _, input := range []string{"empty", "failed", "hangup"} {
			t.Run(fmt.Sprintf("primary=%t/%s", primary, input), func(t *testing.T) {
				f := newTestFixture(t)
				srv, provider := enableVoiceFixture(t, f, nil)
				hello := voiceHello("", 16000)
				hello["newChat"], hello["primary"] = true, primary
				c, reserved := dialVoiceHello(t, srv, hello)
				switch input {
				case "empty":
					provider.transcript.Store("")
					speakVoice(t, c)
					for {
						msg, _ := readVoice(t, c)
						if msg["type"] == "error" {
							t.Fatal(msg)
						}
						if msg["type"] == "stt" && msg["final"] == true {
							break
						}
					}
				case "failed":
					provider.transcript.Store(strings.Repeat("字", 4001))
					speakVoice(t, c)
					for {
						msg, _ := readVoice(t, c)
						if msg["type"] == "error" {
							if msg["code"] != "transcript_limit" {
								t.Fatal(msg)
							}
							break
						}
					}
				case "hangup":
					c.Close()
					f.server.deps.Voice.Close()
				}
				if summary, err := f.chats.Summary(reserved); err != nil || summary != nil || provider.tts.Load() != 0 || agentPrimaryChatID(t, f.server, "mock-agent") != "" {
					t.Fatal("unsuccessful first turn created a Chat or changed primary")
				}
				if input != "hangup" {
					provider.transcript.Store("valid question")
					speakVoice(t, c)
					voiceUntil(t, c, "tts", "stop")
					if len(queryLines(t, f, reserved)) != 1 || (agentPrimaryChatID(t, f.server, "mock-agent") == reserved) != primary {
						t.Fatal("first valid turn lost its original creation intent")
					}
				}
			})
		}
	}
}
