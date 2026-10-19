package connectorauth

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"golang.org/x/oauth2"
)

func deletionFixture(t *testing.T, mode string) connector.Sources {
	t.Helper()
	sources := connector.Sources{ExternalRoot: filepath.Join(t.TempDir(), "packages"), StateRoot: filepath.Join(t.TempDir(), "custom-state", "connectors")}
	manifest := map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": mode}
	if mode == "token" {
		manifest["tokenSchema"] = map[string]any{"fields": []map[string]any{{"key": "API_KEY", "type": "password", "required": true}}}
	}
	writeAuthPackage(t, sources.ExternalRoot, "demo", manifest, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	return sources
}

func archiveBeforeDelete(t *testing.T, sources connector.Sources) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	entries, err := os.ReadDir(filepath.Join(sources.ExternalRoot, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		payload, err := os.ReadFile(filepath.Join(sources.ExternalRoot, "demo", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		f, err := w.Create(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func reimportDeleted(t *testing.T, sources connector.Sources, data []byte) connector.Package {
	t.Helper()
	_, err := connector.ImportArchive(t.Context(), sources, bytes.NewReader(data), int64(len(data)), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func requireNoPrivateState(t *testing.T, sources connector.Sources) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(sources.PersistentRoot(), "demo")); !os.IsNotExist(err) {
		t.Fatalf("deleted connector state was recreated: %v", err)
	}
}

func TestCredentialReadsDoNotCreateDeletedOrAbsentState(t *testing.T) {
	sources := deletionFixture(t, "mcp")
	for _, deleted := range []bool{false, true} {
		if deleted {
			if err := connector.DeletePackage(t.Context(), sources, "demo", nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := AccessToken(t.Context(), sources.PersistentRoot(), "demo", "https://example.test/mcp", nil); err == nil {
			t.Fatal("read returned a token without credentials")
		}
		if _, _, err := ReadTokenCredentials(t.Context(), sources.PersistentRoot(), "demo"); err == nil {
			t.Fatal("read returned deleted token credentials")
		}
		requireNoPrivateState(t, sources)
	}
}

func TestDeletedGenerationRejectsLateOAuthMetadataAndRegistrationAfterReimport(t *testing.T) {
	sources := deletionFixture(t, "mcp")
	archive := archiveBeforeDelete(t, sources)
	before, err := readAuthState(sources.PersistentRoot(), "demo")
	if err != nil || before.Generation != "" {
		t.Fatalf("fixture must cover an initial empty generation: %+v %v", before, err)
	}
	if err := connector.DeletePackage(t.Context(), sources, "demo", nil, nil); err != nil {
		t.Fatal(err)
	}
	pkg := reimportDeleted(t, sources, archive)
	after, err := readAuthState(sources.PersistentRoot(), "demo")
	if err != nil || after.Generation == "" || after.Generation == before.Generation {
		t.Fatalf("re-import accepted the old generation: %+v %v", after, err)
	}
	metadata := filepath.Join(sources.PersistentRoot(), "demo", "discovery-old.json")
	if err := saveLoginDiscovery(t.Context(), pkg, before.Generation, metadata, map[string]any{"old": true}); err == nil {
		t.Fatal("late OAuth discovery restored state")
	}
	if err := saveRegisteredClient(t.Context(), pkg, OAuthClientInfo{ClientID: "old-client"}, before.Generation); err == nil {
		t.Fatal("late OAuth registration restored state")
	}
	m := New(t.Context(), sources, nil)
	if err := m.completeLogin(t.Context(), pkg, m.epoch("demo"), before.Generation); err == nil {
		t.Fatal("late OAuth completion marked the new install configured")
	}
	requireNoPrivateState(t, sources)
	if err := saveLoginDiscovery(t.Context(), pkg, after.Generation, metadata, map[string]any{"new": true}); err != nil {
		t.Fatalf("fresh login cannot save metadata: %v", err)
	}
}

func TestDeletedGenerationRejectsLateConnectionFromOtherManager(t *testing.T) {
	sources := deletionFixture(t, "self")
	archive := archiveBeforeDelete(t, sources)
	old := New(t.Context(), sources, nil)
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	epoch := old.epoch("demo")
	before, err := readAuthState(sources.PersistentRoot(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.DeletePackage(t.Context(), sources, "demo", nil, nil); err != nil {
		t.Fatal(err)
	}
	reimportDeleted(t, sources, archive)
	if err := old.markConfiguredAt(pkg, epoch, before.Generation); err == nil {
		t.Fatal("old connection configured the new installation")
	}
	requireNoPrivateState(t, sources)
	if status, err := New(t.Context(), sources, nil).Connect("demo"); err != nil || status.Status != "delegated" {
		t.Fatalf("fresh connection cannot be confirmed: %+v %v", status, err)
	}
}

func TestDeleteAndReimportRejectsOtherManagersPendingTokenValidation(t *testing.T) {
	sources := deletionFixture(t, "token")
	archive := archiveBeforeDelete(t, sources)
	started, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(proceed) }) })
	other := New(t.Context(), sources, nil).WithCredentialValidator(func(context.Context, connector.Package, map[string]string) error {
		close(started)
		<-proceed
		return nil
	})
	result := make(chan error, 1)
	go func() {
		_, err := other.SetToken(t.Context(), "demo", map[string]string{"API_KEY": "old-account"})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("credential verification did not start")
	}
	m := New(t.Context(), sources, nil)
	release, err := m.BeginDelete(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.DeletePackage(t.Context(), sources, "demo", nil, nil); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	reimportDeleted(t, sources, archive)
	once.Do(func() { close(proceed) })
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("old verification was accepted by the re-imported connector")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old credential verification did not finish")
	}
	requireNoPrivateState(t, sources)
	fresh := New(t.Context(), sources, nil).WithCredentialValidator(acceptTokenForPersistenceTest)
	if session, err := fresh.SetToken(t.Context(), "demo", map[string]string{"API_KEY": "new-account"}); err != nil || session.Status != "authorized" {
		t.Fatalf("fresh account cannot be configured: %+v %v", session, err)
	}
}

func TestBeginDeleteCancelsLocalValidationBeforeReturning(t *testing.T) {
	sources := deletionFixture(t, "token")
	started := make(chan struct{})
	m := New(t.Context(), sources, nil).WithCredentialValidator(func(ctx context.Context, _ connector.Package, _ map[string]string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		_, err := m.SetToken(t.Context(), "demo", map[string]string{"API_KEY": "candidate"})
		done <- err
	}()
	<-started
	release, err := m.BeginDelete(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := <-done; err == nil {
		t.Fatal("canceled validation accepted its candidate")
	}
	if _, err := m.StartPreparation("demo"); !errors.Is(err, connector.ErrBusy) {
		t.Fatalf("new preparation entered a deletion: %v", err)
	}
	requireNoPrivateState(t, sources)
}

func TestDeleteWaitsForRefreshAndOldSessionCannotRecreateState(t *testing.T) {
	sources := deletionFixture(t, "mcp")
	started, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(proceed) }) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-proceed
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"refreshed","token_type":"Bearer","expires_in":3600}`)
	}))
	defer upstream.Close()
	defer once.Do(func() { close(proceed) })
	resource := "https://example.test/mcp"
	err := saveCredential(sources.PersistentRoot(), "demo", oauthCredential{MCP: true, Resource: resource, Destination: resource,
		Config: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{TokenURL: upstream.URL, AuthStyle: oauth2.AuthStyleInParams}},
		Token:  &oauth2.Token{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() {
		_, err := AccessToken(t.Context(), sources.PersistentRoot(), "demo", resource, upstream.Client(), resource)
		refreshed <- err
	}()
	<-started
	deleted := make(chan error, 1)
	go func() { deleted <- connector.DeletePackage(t.Context(), sources, "demo", nil, nil) }()
	select {
	case err := <-deleted:
		t.Fatalf("deleted state while refresh still owned the lock: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	once.Do(func() { close(proceed) })
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delete did not finish after refresh released the lock")
	}
	if _, err := AccessToken(t.Context(), sources.PersistentRoot(), "demo", resource, upstream.Client(), resource); err == nil {
		t.Fatal("old session kept using deleted credentials")
	}
	requireNoPrivateState(t, sources)
}
