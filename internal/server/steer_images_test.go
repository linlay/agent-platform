package server

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"
	gws "github.com/gorilla/websocket"
)

func TestImageSteerHTTPAndWSFreezePersistAndContinue(t *testing.T) {
	for _, transport := range []string{"http", "ws"} {
		t.Run(transport, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			requests := make(chan map[string]any, 3)
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				requests <- payload
				if calls.Add(1) == 1 {
					writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"draft"}}]}`)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
			}, testFixtureOptions{notifications: ws.NewHub(), setupRuntime: func(root string, _ *config.Config) {
				p := filepath.Join(root, "registries", "models", "mock-model.yml")
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, append(data, []byte("\nisVision: true\n")...), 0600); err != nil {
					t.Fatal(err)
				}
			}})
			server := newLoopbackServer(t, fixture.server)
			defer server.Close()
			defer unblock()
			const chatID = "chat-image-steer"
			var refs []api.Reference
			var expectedURLs []string
			for i, name := range []string{"red.png", "blue.png"} {
				var data bytes.Buffer
				img := image.NewRGBA(image.Rect(0, 0, 2, 2))
				img.Set(0, 0, color.RGBA{R: uint8(80 + i), A: 255})
				if err := png.Encode(&data, img); err != nil {
					t.Fatal(err)
				}
				expectedURLs = append(expectedURLs, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data.Bytes()))
				var body bytes.Buffer
				form := multipart.NewWriter(&body)
				_ = form.WriteField("chatId", chatID)
				_ = form.WriteField("agentKey", "mock-agent")
				file, err := form.CreateFormFile("file", name)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = file.Write(data.Bytes())
				_ = form.Close()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/api/upload", &body)
				req.Header.Set("Content-Type", form.FormDataContentType())
				fixture.server.ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Fatalf("upload: %s", rec.Body.String())
				}
				var result struct {
					Data api.UploadResponse `json:"data"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				u := result.Data.Upload
				refs = append(refs, api.Reference{ID: u.ID, Type: u.Type, Name: u.Name, Path: u.Path, URL: u.URL, MimeType: u.MimeType})
			}
			response, err := http.Post(server.URL+"/api/query", "application/json", strings.NewReader(`{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"start"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			var runID string
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(line, "data: {") {
					event := decodeSSELine(t, line)
					if id, ok := event["runId"].(string); ok {
						runID = id
					}
					if event["delta"] == "draft" {
						break
					}
				}
			}
			payload := api.SteerRequest{RunID: runID, ChatID: chatID, AgentKey: "mock-agent", SteerID: "image-1", Message: "use both images", References: refs}
			var ack api.SteerResponse
			if transport == "http" {
				encoded, _ := json.Marshal(payload)
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/api/steer", bytes.NewReader(encoded))
				req.Header.Set("Content-Type", "application/json")
				fixture.server.ServeHTTP(rec, req)
				var result struct {
					Data api.SteerResponse `json:"data"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				ack = result.Data
			} else {
				conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteJSON(map[string]any{"frame": "request", "type": "/api/steer", "id": "steer-control", "payload": payload}); err != nil {
					t.Fatal(err)
				}
				for {
					var frame map[string]json.RawMessage
					if err := conn.ReadJSON(&frame); err != nil {
						t.Fatal(err)
					}
					if string(frame["id"]) == `"steer-control"` {
						if err := json.Unmarshal(frame["data"], &ack); err != nil {
							t.Fatal(err)
						}
						break
					}
				}
			}
			if !ack.Accepted {
				t.Fatalf("steer rejected: %#v", ack)
			}
			// Mutating the uploaded files after admission cannot alter this steer.
			for _, ref := range refs {
				if err := os.WriteFile(filepath.Join(fixture.chats.ChatDir(chatID), ref.Name), []byte("replaced"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			tail, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(tail, []byte(`"type":"request.steer"`)) || bytes.Contains(tail, []byte("data:image/")) || bytes.Contains(tail, []byte("request.steer.snapshot")) {
				t.Fatalf("unexpected public stream: %s", tail)
			}
			<-requests // Initial query.
			assertImages := func(payload map[string]any) {
				t.Helper()
				encoded, _ := json.Marshal(payload)
				for _, url := range expectedURLs {
					if bytes.Count(encoded, []byte(url)) != 1 {
						t.Fatalf("expected each frozen image exactly once: %s", encoded)
					}
				}
			}
			assertImages(<-requests)
			jsonl, err := fixture.chats.LoadJSONLContent(chatID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(jsonl, `"_type":"steer"`) != 1 || strings.Count(jsonl, expectedURLs[0]) != 1 {
				t.Fatalf("image input must persist once: %s", jsonl)
			}
			detail, err := fixture.chats.LoadChat(chatID)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(detail)
			if !bytes.Contains(encoded, []byte(`"references"`)) {
				t.Fatal("replay missing references")
			}
			next, err := http.Post(server.URL+"/api/query", "application/json", strings.NewReader(`{"chatId":"`+chatID+`","agentKey":"mock-agent","message":"continue"}`))
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.Copy(io.Discard, next.Body)
			_ = next.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			assertImages(<-requests)
		})
	}
}
