package webappbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	rt "agent-platform/internal/runtime/types"
	"agent-platform/internal/webapp"
)

type sdkCatalog struct{}

func (sdkCatalog) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	return catalog.AgentDefinition{Key: key, Skills: []string{"allowed"}}, key == "agent" || key == "changed"
}
func (sdkCatalog) SkillDefinition(id string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{ID: id, Name: id, Description: "safe metadata", Prompt: "private instructions", Dir: "/host/private"}, id == "allowed" || id == "denied"
}
func resourceScope() *webapp.Scope {
	return &webapp.Scope{InstallationID: "install-a", Generation: "g1", Check: func() error { return nil }, Policy: webapp.Policy{AgentKey: "agent", Skills: []string{"allowed"}, Artifacts: true}, UserConfig: map[string]any{}}
}
func TestSDKSkillsRespectGrantAndSelectedAgent(t *testing.T) {
	a := NewAdapters(AdapterOptions{Registry: sdkCatalog{}, Root: t.TempDir()})
	s := resourceScope()
	result, err := a.skills(context.Background(), s, json.RawMessage(`{}`), false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if string(raw) != `{"items":[{"skillId":"allowed","name":"allowed","description":"safe metadata","agentHasSkill":true}]}` {
		t.Fatalf("metadata projection: %s", raw)
	}
	if _, err = a.skills(context.Background(), s, json.RawMessage(`{"skillId":"denied"}`), true); err == nil {
		t.Fatal("non-granted skill accepted")
	}
	s.Manifest.Copilot = &webapp.Copilot{AgentKey: "changed"}
	if _, err = a.selected(s); err == nil {
		t.Fatal("manifest expanded agent grant")
	}
	s.Manifest.UserConfig = &webapp.UserConfig{Fields: []webapp.UserField{{Name: "agent", Type: "select", Source: "desktop.agents"}}}
	s.UserConfig["agent"] = "agent"
	if _, err = a.selected(s); err != nil {
		t.Fatal("configured agent ignored", err)
	}
}
func TestSDKArtifactOwnershipPersistsAndVerifiesPublishedBytes(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.EnsureChat("chat-sdk", "agent", "test")
	body := []byte("published SDK artifact")
	sum := sha256.Sum256(body)
	path := filepath.Join(store.ChatDir("chat-sdk"), "artifacts/run-sdk/report.txt")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, body, 0600)
	if err = store.AppendArtifactManifest("chat-sdk", "run-sdk", 1, []map[string]any{{"artifactId": "report", "url": "artifacts/run-sdk/report.txt", "name": "report.txt", "mimeType": "text/plain", "sizeBytes": len(body), "sha256": hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	options := AdapterOptions{Registry: sdkCatalog{}, Chats: store, Artifacts: chatresource.NewService(store), Root: t.TempDir()}
	a := NewAdapters(options)
	s := resourceScope()
	if err = a.ledger.claim(s, rt.RunRef{ChatID: "chat-sdk", RunID: "run-sdk", AgentKey: "agent"}); err != nil {
		t.Fatal(err)
	}
	// A fresh adapter reads the durable receipt; application writable data is
	// never consulted for ownership. Generation changes retain authorized history.
	a = NewAdapters(options)
	s.Generation = "g2"
	raw := json.RawMessage(`{"chatId":"chat-sdk","runId":"run-sdk","artifactId":"report"}`)
	result, err := a.artifact(context.Background(), s, raw, "artifact.read")
	if err != nil || result == nil {
		t.Fatal(result, err)
	}
	s.InstallationID = "reinstalled"
	if _, err = a.artifact(context.Background(), s, raw, "artifact.read"); err == nil {
		t.Fatal("new installation inherited old chats")
	}
	s.InstallationID = "install-a"
	os.WriteFile(path, []byte("changed"), 0600)
	if _, err = a.artifact(context.Background(), s, raw, "artifact.read"); err == nil {
		t.Fatal("tampered artifact accepted")
	}
	if err = os.WriteFile(filepath.Join(options.Root, "install-a.json"), []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = a.artifact(context.Background(), s, raw, "artifact.get"); err == nil {
		t.Fatal("corrupt ledger accepted")
	}
}

func TestAssistantRetentionCountsPendingAdmissionsAndExpiresOnRead(t *testing.T) {
	a := NewAdapters(AdapterOptions{Root: t.TempDir()})
	scope := resourceScope()
	for i := 0; i < 127; i++ {
		a.runs[fmt.Sprint(i)] = &assistantRun{terminal: true, expires: time.Now().Add(time.Hour)}
	}
	release, err := a.reserve(scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.reserve(scope); err == nil {
		t.Fatal("concurrent pending admission exceeded retention bound")
	}
	release()
	a.runs["expired"] = &assistantRun{installation: scope.InstallationID, generation: scope.Generation, terminal: true, expires: time.Now().Add(-time.Second)}
	a.mu.Lock()
	_, err = a.ownedRun(scope, "expired")
	_, present := a.runs["expired"]
	a.mu.Unlock()
	if err == nil || present {
		t.Fatal("expired terminal remained readable")
	}
}
