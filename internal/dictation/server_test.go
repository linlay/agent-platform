package dictation

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/config"
	"github.com/gorilla/websocket"
)

func fixture(t *testing.T, upstream string) (*Server, string, Config) {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := Config{Listen: "127.0.0.1:11964",
		ModelFile:    write("model.yml", "key: asr\nprovider: provider\ntype: asr\nprotocol: DASHSCOPE_ASR\nmodelId: paraformer-realtime-v2\n"),
		ProviderFile: write("provider.yml", "key: provider\nbaseUrl: "+upstream+"\napiKey: fixture-secret\n"),
		Auth:         config.AuthConfig{Enabled: true, RequireExpiry: true, Issuer: "fixture", Audience: "dictation", AllowedSubjects: []string{"user"}, LocalPublicKeyFile: write("public.pem", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})))},
	}
	claims, _ := json.Marshal(map[string]any{"iss": "fixture", "aud": "dictation", "sub": "user", "exp": time.Now().Add(time.Hour).Unix()})
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(unsigned))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	token := unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	s, err := NewServer(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	// Unrelated corrupt registry files and missing TTS/Platform configuration are irrelevant.
	write("unrelated-model.yml", "broken: [")
	return s, token, cfg
}

func TestCapabilitiesIsolationAndAuthentication(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	s, token, cfg := fixture(t, upstream.URL)
	request := func(path, auth, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	for _, item := range []struct {
		path, token, origin string
		status              int
	}{
		{"/healthz", "", "", 200}, {"/api/dictation/capabilities", token, "", 200},
		{"/api/dictation/capabilities", "", "", 401}, {"/api/dictation/capabilities", "invalid", "", 401},
		{"/api/dictation/capabilities?token=x", token, "", 403}, {"/api/dictation/capabilities", token, "https://evil.test", 403},
		{"/api/query", token, "", 404}, {"/api/voice/ws", token, "", 404},
	} {
		if got := request(item.path, item.token, item.origin).Code; got != item.status {
			t.Errorf("%s: got %d want %d", item.path, got, item.status)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("capabilities connected to ASR")
	}
	if err := os.WriteFile(cfg.ModelFile, []byte("key: wrong\nprovider: other\ntype: tts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if body := request("/api/dictation/capabilities", token, "").Body.String(); !strings.Contains(body, `"enabled":false`) || strings.Contains(body, "fixture-secret") {
		t.Fatal(body)
	}
}

func TestStreamingSnapshotsTailAndCancellation(t *testing.T) {
	for _, mode := range []string{"finish", "cancel", "cancel-during-finish", "invalid-audio", "duplicate-start"} {
		t.Run(mode, func(t *testing.T) {
			done := make(chan struct{})
			pcm := make(chan int, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				if r.Header.Get("Authorization") != "Bearer fixture-secret" {
					t.Error("provider auth lost")
				}
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(3 * time.Second))
				var cmd struct {
					Header struct {
						Action string `json:"action"`
						ID     string `json:"task_id"`
					} `json:"header"`
				}
				if c.ReadJSON(&cmd) != nil || cmd.Header.Action != "run-task" {
					t.Error("missing ASR start")
					return
				}
				emit := func(kind, text string, final bool) {
					_ = c.WriteJSON(map[string]any{"header": map[string]any{"event": kind, "task_id": cmd.Header.ID}, "payload": map[string]any{"output": map[string]any{"sentence": map[string]any{"begin_time": 0, "text": text, "sentence_end": final}}}})
				}
				emit("task-started", "", false)
				total := 0
				for {
					kind, data, err := c.ReadMessage()
					if err != nil {
						return
					}
					if kind == websocket.BinaryMessage {
						total += len(data)
						continue
					}
					var finish struct {
						Header struct {
							Action string `json:"action"`
						} `json:"header"`
					}
					_ = json.Unmarshal(data, &finish)
					if finish.Header.Action != "finish-task" {
						t.Error("unexpected upstream command")
						return
					}
					pcm <- total
					if mode == "cancel-during-finish" {
						_, _, _ = c.ReadMessage()
						return
					}
					emit("result-generated", "hello", false)
					emit("result-generated", "hello world", false)
					emit("result-generated", "hello world!", true)
					emit("task-finished", "", false)
					return
				}
			}))
			defer upstream.Close()
			s, token, _ := fixture(t, upstream.URL)
			server := httptest.NewServer(s)
			defer server.Close()
			headers := http.Header{"Authorization": []string{"Bearer " + token}}
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/dictation/ws", headers)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			if err := c.WriteJSON(map[string]string{"type": "start"}); err != nil {
				t.Fatal(err)
			}
			var event struct{ Type, Text string }
			if err := c.ReadJSON(&event); err != nil || event.Type != "ready" {
				t.Fatalf("ready: %v %v", event, err)
			}
			switch mode {
			case "cancel":
				c.Close()
			case "invalid-audio":
				_ = c.WriteMessage(websocket.BinaryMessage, []byte{1})
			case "duplicate-start":
				_ = c.WriteJSON(map[string]string{"type": "start"})
			default:
				_ = c.WriteMessage(websocket.BinaryMessage, make([]byte, 640))
				_ = c.WriteMessage(websocket.BinaryMessage, []byte{3, 9})
				_ = c.WriteJSON(map[string]string{"type": "stop"})
				if total := <-pcm; total != 642 {
					t.Fatalf("tail lost: %d", total)
				}
				if mode == "cancel-during-finish" {
					c.Close()
					break
				}
				var snapshots []string
				for {
					if err := c.ReadJSON(&event); err != nil {
						t.Fatal(err)
					}
					snapshots = append(snapshots, event.Text)
					if event.Type == "final" {
						break
					}
					if event.Type != "partial" {
						t.Fatalf("unexpected %v", event)
					}
				}
				if strings.Join(snapshots, "|") != "hello|hello world|hello world!|hello world!" {
					t.Fatal(snapshots)
				}
			}
			if mode == "invalid-audio" || mode == "duplicate-start" {
				if err := c.ReadJSON(&event); err != nil || event.Type != "error" {
					t.Fatalf("expected error: %v %v", event, err)
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("upstream task was not released")
			}
			s.Close()
		})
	}
}

func TestConfigReadsOnlyDeclaredFiles(t *testing.T) {
	s, _, cfg := fixture(t, "https://speech.invalid")
	_ = s
	root := filepath.Dir(cfg.ModelFile)
	payload := []byte(fmt.Sprintf("listen: 127.0.0.1:11964\nmodelFile: %q\nproviderFile: %q\nauth:\n  enabled: true\n  requireExpiry: true\n  localPublicKeyFile: %q\n", cfg.ModelFile, cfg.ProviderFile, cfg.Auth.LocalPublicKeyFile))
	path := filepath.Join(root, "dictation.yml")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	asr, err := loaded.newASR()
	if err != nil {
		t.Fatal(err)
	}
	asr.Close()
	// Only a newly created task observes a changed definition.
	_ = os.WriteFile(cfg.ProviderFile, []byte("key: different\n"), 0600)
	if _, err := loaded.newASR(); err == nil {
		t.Fatal("provider mismatch accepted")
	}
}

func TestReleaseConfigTemplate(t *testing.T) {
	_, _, cfg := fixture(t, "https://speech.invalid")
	t.Setenv("DICTATION_MODEL_FILE", cfg.ModelFile)
	t.Setenv("DICTATION_PROVIDER_FILE", cfg.ProviderFile)
	t.Setenv("DICTATION_PUBLIC_KEY_FILE", cfg.Auth.LocalPublicKeyFile)
	loaded, err := LoadConfig("../../configs/dictation.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ModelFile != cfg.ModelFile || loaded.ProviderFile != cfg.ProviderFile || !loaded.Auth.RequireExpiry {
		t.Fatal("template lost declared inputs")
	}
	s, err := NewServer(context.Background(), loaded)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
