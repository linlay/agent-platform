package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestUploadAndResourceRoundTrip(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader("hello world")); err != nil {
		t.Fatalf("write upload body: %v", err)
	}
	if err := writer.WriteField("requestId", "req_upload"); err != nil {
		t.Fatalf("write requestId: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	assertUUIDLike(t, response.Data.ChatID)
	resourceReq := httptest.NewRequest(http.MethodGet, response.Data.Upload.URL, nil)
	resourceRec := httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected 200 resource, got %d", resourceRec.Code)
	}
	if got := resourceRec.Body.String(); got != "hello world" {
		t.Fatalf("unexpected resource content: %q", got)
	}

	matches, err := filepath.Glob(filepath.Join(fixture.cfg.Paths.ChatsDir, "*", "notes.txt"))
	if err != nil {
		t.Fatalf("glob upload path: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected uploaded file under %s, got %v", fixture.cfg.Paths.ChatsDir, matches)
	}
}

func TestResourceRoundTripRequiresValidTicketWhenEnabled(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.ChatImage = config.ChatImageTokenConfig{
				ResourceTicketEnabled: true,
				Secret:                "ticket-secret",
				TTLSeconds:            300,
			}
		},
	})
	server := fixture.server

	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader("hello ticket")); err != nil {
		t.Fatalf("write upload body: %v", err)
	}
	if err := writer.WriteField("chatId", "chat_ticket"); err != nil {
		t.Fatalf("write chatId: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}

	resourceReq := httptest.NewRequest(http.MethodGet, response.Data.Upload.URL, nil)
	resourceRec := httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without ticket, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}

	validTicket := fixture.server.ticketService.Issue("tester", response.Data.ChatID)
	resourceReq = httptest.NewRequest(http.MethodGet, response.Data.Upload.URL+"&t="+url.QueryEscape(validTicket), nil)
	resourceRec = httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid ticket, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}
	if got := resourceRec.Body.String(); got != "hello ticket" {
		t.Fatalf("unexpected resource body %q", got)
	}

	wrongTicket := fixture.server.ticketService.Issue("tester", "chat_other")
	resourceReq = httptest.NewRequest(http.MethodGet, response.Data.Upload.URL+"&t="+url.QueryEscape(wrongTicket), nil)
	resourceRec = httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with mismatched ticket, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}
}

func TestWebSocketUploadDownloadsGatewayURLAndReturnsUploadTicket(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
			cfg.GatewayWS.AuthToken = "gateway-upload-token"
		},
	})

	fileBody := []byte("gateway upload body")
	expectedSHA := sha256.Sum256(fileBody)
	var downloadAuthorized atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gateway-upload-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		downloadAuthorized.Store(true)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(fileBody)
	}))
	defer gateway.Close()

	server := newLoopbackServer(t, fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/upload",
		ID:    "req_upload_ws",
		Payload: ws.MarshalPayload(map[string]any{
			"chatId":    "chat_ws_upload",
			"requestId": "req_upload_ws",
			"upload": map[string]any{
				"id":        "upload_1",
				"type":      "file",
				"name":      "gateway.txt",
				"mimeType":  "text/plain; charset=utf-8",
				"sizeBytes": len(fileBody),
				"sha256":    hex.EncodeToString(expectedSHA[:]),
				"url":       gateway.URL,
			},
		}),
	}); err != nil {
		t.Fatalf("write websocket upload: %v", err)
	}

	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("decode websocket upload frame: %v", err)
		}
		return meta.Frame == ws.FrameResponse && meta.ID == "req_upload_ws"
	})
	var frame struct {
		Frame string             `json:"frame"`
		Type  string             `json:"type"`
		ID    string             `json:"id"`
		Code  int                `json:"code"`
		Msg   string             `json:"msg"`
		Data  api.UploadResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket upload response: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.Type != "/api/upload" || frame.ID != "req_upload_ws" || frame.Code != 0 {
		t.Fatalf("unexpected websocket upload response: %s", string(raw))
	}
	if !downloadAuthorized.Load() {
		t.Fatalf("expected gateway download request to carry auth token")
	}
	if frame.Data.ChatID != "chat_ws_upload" {
		t.Fatalf("expected chat_ws_upload, got %#v", frame.Data)
	}
	if frame.Data.Upload.Name != "gateway.txt" {
		t.Fatalf("expected uploaded name to be preserved, got %#v", frame.Data.Upload)
	}
	if frame.Data.Upload.MimeType != "text/plain; charset=utf-8" {
		t.Fatalf("expected mime type to be preserved, got %#v", frame.Data.Upload)
	}
	if frame.Data.Upload.SizeBytes != int64(len(fileBody)) {
		t.Fatalf("expected size %d, got %#v", len(fileBody), frame.Data.Upload)
	}
	if frame.Data.Upload.SHA256 != hex.EncodeToString(expectedSHA[:]) {
		t.Fatalf("expected sha256 to match, got %#v", frame.Data.Upload)
	}

	resourceReq := httptest.NewRequest(http.MethodGet, frame.Data.Upload.URL, nil)
	resourceRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected uploaded resource to be readable, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}
	if got := resourceRec.Body.String(); got != string(fileBody) {
		t.Fatalf("unexpected uploaded resource body %q", got)
	}
}

func TestWebSocketUploadRejectsInvalidUploadMetadata(t *testing.T) {
	tests := []struct {
		name        string
		payload     map[string]any
		errContains string
	}{
		{
			name: "size mismatch",
			payload: map[string]any{
				"chatId":    "chat_ws_upload",
				"requestId": "req_upload_size",
				"fileName":  "gateway.txt",
				"url":       "/download",
				"sizeBytes": 999,
			},
			errContains: "sizeBytes mismatch",
		},
		{
			name: "sha mismatch",
			payload: map[string]any{
				"chatId":    "chat_ws_upload",
				"requestId": "req_upload_sha",
				"fileName":  "gateway.txt",
				"url":       "/download",
				"sha256":    strings.Repeat("0", 64),
			},
			errContains: "sha256 mismatch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				writeProviderSSE(t, w, `[DONE]`)
			}, testFixtureOptions{
				notifications: ws.NewHub(),
				configure: func(cfg *config.Config) {
					cfg.WebSocket.Enabled = true
					cfg.WebSocket.WriteQueueSize = 4
					cfg.WebSocket.PingIntervalMs = 30000
					cfg.GatewayWS.BaseURL = ""
				},
			})

			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("gateway upload body"))
			}))
			defer gateway.Close()

			server := newLoopbackServer(t, fixture.server)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
			conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial websocket: %v", err)
			}
			defer conn.Close()

			waitForPushFrameType(t, conn, "connected")

			payload := make(map[string]any, len(tc.payload))
			for key, value := range tc.payload {
				payload[key] = value
			}
			payload["url"] = gateway.URL + "/download"

			if err := conn.WriteJSON(ws.RequestFrame{
				Frame:   ws.FrameRequest,
				Type:    "/api/upload",
				ID:      "req_invalid_upload",
				Payload: ws.MarshalPayload(payload),
			}); err != nil {
				t.Fatalf("write websocket upload: %v", err)
			}

			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read websocket upload error: %v", err)
			}
			var frame ws.ErrorFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode websocket upload error: %v", err)
			}
			if frame.Frame != ws.FrameError || frame.Type != "invalid_upload_metadata" || frame.Code != http.StatusBadRequest {
				t.Fatalf("unexpected websocket upload error: %s", string(raw))
			}
			if !strings.Contains(frame.Msg, tc.errContains) {
				t.Fatalf("expected error to contain %q, got %#v", tc.errContains, frame)
			}
		})
	}
}
