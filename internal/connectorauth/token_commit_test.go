package connectorauth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

func blockingTokenFixture(t *testing.T) (*Manager, connector.Package, chan struct{}, chan struct{}) {
	t.Helper()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "state")}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token",
		"token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "required": true}}}},
		map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp", "headers": map[string]any{"X-Key": "${API_KEY}"}})
	entered, release := make(chan struct{}), make(chan struct{})
	m := New(t.Context(), sources, nil).WithCredentialValidator(func(_ context.Context, _ connector.Package, values map[string]string) error {
		if values["API_KEY"] == "new" {
			close(entered)
			<-release
		}
		return nil
	})
	if _, err := m.SetToken(t.Context(), "demo", map[string]string{"API_KEY": "old"}); err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	return m, pkg, entered, release
}

func TestTokenValidationKeepsActiveAccountReadableAndCommitsWithNewRevision(t *testing.T) {
	m, pkg, entered, release := blockingTokenFixture(t)
	_, oldRevision, err := ReadTokenCredentials(t.Context(), pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := m.SetToken(context.Background(), pkg.ID, map[string]string{"API_KEY": "new"})
		done <- err
	}()
	<-entered
	// A slow candidate check must not block executions of the active account.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	values, revision, err := ReadTokenCredentials(ctx, pkg.PersistentRoot(), pkg.ID)
	cancel()
	if err != nil || values["API_KEY"] != "old" || revision != oldRevision {
		t.Fatal("active account unreadable or changed during validation", values, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	values, revision, err = ReadTokenCredentials(t.Context(), pkg.PersistentRoot(), pkg.ID)
	if err != nil || values["API_KEY"] != "new" || revision == oldRevision {
		t.Fatal("committed account not paired with a new revision", values, revision, err)
	}
}

func TestTokenCommitRejectsStateChangedDuringValidation(t *testing.T) {
	m, pkg, entered, release := blockingTokenFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := m.SetToken(context.Background(), pkg.ID, map[string]string{"API_KEY": "new"})
		done <- err
	}()
	<-entered
	// Another process (for example connector-manage) changes the account meanwhile.
	unlock, err := lockCredentials(t.Context(), pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := changeAuthState(pkg.PersistentRoot(), pkg.ID, false); err != nil {
		t.Fatal(err)
	}
	unlock()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("stale validation overwrote a newer account state")
	}
	values, _, err := ReadTokenCredentials(t.Context(), pkg.PersistentRoot(), pkg.ID)
	if err != nil || values["API_KEY"] != "old" {
		t.Fatal("superseded candidate became active", values, err)
	}
}
