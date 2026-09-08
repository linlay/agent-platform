package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
	"agent-platform/internal/view"
	"agent-platform/internal/ws"
)

func TestViewHTTPMountedScopeAndSnapshotAfterUnmount(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{notifications: ws.NewHub(), setupRuntime: func(_ string, cfg *config.Config) {
		dir := filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "forms")
		for name, content := range map[string]string{
			"connector.json":   `{"id":"forms","name":"Forms","version":"1.0.0","type":"view","auth_mode":"none"}`,
			"view.json":        `{"views":{"edit":{"renderer":"html","entry":"views/index.html","usage":["form","display"]}}}`,
			"views/index.html": "<p>original</p>",
		} {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
		p := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte("\nconnectorConfig:\n  connectors:\n    - forms\n")...)
		if err := os.WriteFile(p, data, 0644); err != nil {
			t.Fatal(err)
		}
	}})
	const chatID = "view-test-chat"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "test"); err != nil {
		t.Fatal(err)
	}
	startServerFixtureRun(t, fixture.chats.(*chat.FileStore), chatID, "view-run", 1700000000000)
	if err := fixture.chats.OnRunCompleted(chat.RunCompletion{ChatID: chatID, RunID: "view-run", AgentKey: "mock-agent", InitialMessage: "test", AssistantText: "view", FinishReason: "complete", StartedAtMillis: 1700000000000, UpdatedAtMillis: 1700000000250}); err != nil {
		t.Fatal(err)
	}
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}
	path := "/api/view?chatId=" + chatID + "&connectorId=forms&key=edit"
	rec := get(path)
	if rec.Code != 200 {
		t.Fatalf("view API: %d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data view.Document `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.HTML != "<p>original</p>" || response.Data.View.Hash == "" {
		t.Fatalf("document: %#v", response.Data)
	}
	if rec := get("/api/view?chatId=" + chatID + "&connectorId=other&key=edit"); rec.Code != 404 {
		t.Fatalf("unmounted status %d", rec.Code)
	}
	if rec := get("/api/view?chatId=" + chatID + "&connectorId=forms&key=../private"); rec.Code != 400 {
		t.Fatalf("path status %d", rec.Code)
	}
	// The frozen reference is sufficient for history, with no current catalog.
	registry := fixture.server.deps.Registry
	fixture.server.deps.Registry = nil
	if rec := get(path + "&hash=" + response.Data.View.Hash); rec.Code != 200 {
		t.Fatalf("history after unmount %d %s", rec.Code, rec.Body.String())
	}
	archives, err := chat.NewArchiveStoreAtStartup(fixture.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server.deps.Archives = archives
	archiver := chat.NewArchiver(fixture.chats.(*chat.FileStore), archives)
	if err := archiver.ArchiveChat(chatID); err != nil {
		t.Fatal(err)
	}
	if rec := get(path + "&hash=" + response.Data.View.Hash); rec.Code != 200 {
		t.Fatalf("archived view: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := archiver.RestoreChat(chatID); err != nil {
		t.Fatal(err)
	}
	if rec := get(path + "&hash=" + response.Data.View.Hash); rec.Code != 200 {
		t.Fatalf("restored view: %d", rec.Code)
	}

	fixture.server.deps.Registry = registry
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/view", ID: "view-test", Payload: marshalPayload(map[string]any{"chatId": chatID, "connectorId": "forms", "key": "edit", "hash": response.Data.View.Hash})}); err != nil {
		t.Fatal(err)
	}
	wire := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var frame map[string]any
		return json.Unmarshal(data, &frame) == nil && frame["id"] == "view-test" && frame["frame"] == "response"
	})
	var frame struct {
		Data struct {
			HTML string `json:"html"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wire, &frame); err != nil || frame.Data.HTML != "<p>original</p>" {
		t.Fatalf("WS view=%s err=%v", wire, err)
	}
}

func TestTeamMergedFormPreservesMemberViewReference(t *testing.T) {
	ref := &view.Reference{ConnectorID: "member-forms", Key: "edit", Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Renderer: "html"}
	forms, _, _ := teamMergedAwaitingDefinition([]*teamChildAwaiting{{PublicID: "task:wait", RawID: "wait", Task: preparedSubTask{taskID: "task"}, Ask: stream.AwaitAsk{Mode: "form", View: ref, Forms: []any{map[string]any{"id": "form-1", "form": map[string]any{"name": "original"}}}}}})
	inner := forms[0].(map[string]any)["form"].(map[string]any)
	if inner["view"].(map[string]any)["hash"] != ref.Hash {
		t.Fatalf("lost member view: %#v", forms)
	}
}

func TestSessionViewResolutionUsesOwnMountedAgent(t *testing.T) {
	s := &Server{deps: Dependencies{Config: config.Config{}}}
	session := contracts.QuerySession{ChatRoot: t.TempDir()}
	if err := s.configureSessionViews(&session, catalog.AgentDefinition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.ResolveView(context.Background(), view.Reference{ConnectorID: "unmounted", Key: "form"}, "form"); err == nil {
		t.Fatal("unmounted view resolved")
	}
}
