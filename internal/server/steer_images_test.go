package server

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	for _, scenario := range []string{"images", "files", "mixed", "images-tools"} {
		for _, transport := range []string{"http", "ws"} {
			t.Run(scenario+"/"+transport, func(t *testing.T) {
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
					if err := os.WriteFile(p, append(data, []byte(fmt.Sprintf("\nisVision: %t\n", scenario != "files" && scenario != "images-tools"))...), 0600); err != nil {
						t.Fatal(err)
					}
				}})
				server := newLoopbackServer(t, fixture.server)
				defer server.Close()
				defer unblock()
				const chatID = "chat-image-steer"
				var refs []api.Reference
				var expectedURLs []string
				names := []string{"red.png", "blue.png"}
				if scenario == "files" {
					names = []string{"page.html", "notes.md"}
				}
				if scenario == "mixed" {
					names = []string{"red.png", "notes.md"}
				}
				for i, name := range names {
					var data bytes.Buffer
					img := image.NewRGBA(image.Rect(0, 0, 2, 2))
					img.Set(0, 0, color.RGBA{R: uint8(80 + i), A: 255})
					if err := png.Encode(&data, img); err != nil {
						t.Fatal(err)
					}
					if strings.HasSuffix(name, ".png") {
						if scenario != "images-tools" {
							expectedURLs = append(expectedURLs, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data.Bytes()))
						}
					} else {
						data.Reset()
						data.WriteString("<!doctype html><p>file reference</p>")
					}
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
				runID, readTail := startSteerTransportRun(t, server.URL, transport, chatID)
				payload := api.SteerRequest{RunID: runID, ChatID: chatID, AgentKey: "mock-agent", SteerID: "image-1", Message: "", References: refs}
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
					raw := wsTestControlResponse(t, server.URL, "/api/steer", payload)
					var response struct {
						Data api.SteerResponse `json:"data"`
					}
					if err := json.Unmarshal(raw, &response); err != nil {
						t.Fatal(err)
					}
					ack = response.Data
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
				tail, err := readTail()
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
					if scenario == "images-tools" && bytes.Contains(encoded, []byte("image_url")) {
						t.Fatalf("non-vision request contains image blocks: %s", encoded)
					}
					for _, name := range names {
						if !bytes.Contains(encoded, []byte(name)) {
							t.Fatalf("missing file reference %s: %s", name, encoded)
						}
					}
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
				if strings.Count(jsonl, `"_type":"steer"`) != 1 {
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
}

func TestAttachmentOnlyQueryRejectedHTTPAndWS(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) { t.Error("empty query reached model"); w.WriteHeader(500) }, testFixtureOptions{notifications: ws.NewHub()})
	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for _, name := range []string{"image.png", "page.html", "notes.md"} {
		payload := api.QueryRequest{AgentKey: "mock-agent", Message: " \n\t", References: []api.Reference{{Type: "file", URL: name}}}
		encoded, _ := json.Marshal(payload)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("POST", "/api/query", bytes.NewReader(encoded)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "message is required") {
			t.Fatalf("HTTP query: %d %s", rec.Code, rec.Body.String())
		}
		if err := conn.WriteJSON(map[string]any{"frame": "request", "type": "/api/query", "id": name, "payload": payload}); err != nil {
			t.Fatal(err)
		}
		for {
			var frame map[string]json.RawMessage
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatal(err)
			}
			if string(frame["id"]) != `"`+name+`"` {
				continue
			}
			raw, _ := json.Marshal(frame)
			if !bytes.Contains(raw, []byte("message is required")) {
				t.Fatalf("WS query: %s", raw)
			}
			break
		}
	}
}

// Starts and observes the Run on the same transport used for its controls.
func startSteerTransportRun(t *testing.T, baseURL, transport, chatID string) (string, func() ([]byte, error)) {
	t.Helper()
	payload := map[string]any{"chatId": chatID, "agentKey": "mock-agent", "message": "start"}
	var runID string
	if transport == "http" {
		body, _ := json.Marshal(payload)
		response, err := http.Post(baseURL+"/api/query", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		reader := bufio.NewReader(response.Body)
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
		return runID, func() ([]byte, error) { return io.ReadAll(reader) }
	}
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(baseURL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	readConnectedPush(t, conn)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	sendSelectionLaneRequest(t, conn, "query", "/api/query", payload)
	for {
		var frame ws.StreamFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Event != nil {
			if id, ok := frame.Event.Value("runId").(string); ok {
				runID = id
			}
			if frame.Event.Value("delta") == "draft" {
				break
			}
		}
	}
	return runID, func() ([]byte, error) {
		var data bytes.Buffer
		for {
			var frame ws.StreamFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return nil, err
			}
			if frame.Event != nil {
				raw, _ := json.Marshal(frame.Event)
				data.Write(raw)
			}
			if frame.Reason != "" {
				return data.Bytes(), nil
			}
		}
	}
}
