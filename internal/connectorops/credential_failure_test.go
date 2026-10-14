package connectorops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestFirstPendingTokenInvocationRequestsVerification(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "connector.json"), []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"token","token_schema":{"fields":[{"key":"KEY","required":true}]}}`), 0600)
	os.WriteFile(filepath.Join(dir, "cli.json"), []byte(`{}`), 0600)
	state, err := connectorauth.StateDir(sources.PersistentRoot(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(state, 0700)
	pending, _ := json.Marshal(map[string]string{"KEY": "candidate"})
	if err := os.WriteFile(filepath.Join(state, "pending-credentials.json"), pending, 0600); err != nil {
		t.Fatal(err)
	}
	s := Service{Auth: connectorauth.New(t.Context(), sources, nil), Sources: sources}
	scope := Scope{Subject: "owner", IdempotencyNamespace: "app", Execution: []Permission{{ConnectorID: "demo", Adapter: "cli"}}, Check: func() error { return nil }}
	_, err = s.Invoke(t.Context(), scope, Request{ConnectorID: "demo", Adapter: "cli", Args: []string{"read"}})
	var failed *Error
	if !errors.As(err, &failed) || failed.Code != "connector_verification_required" || failed.Status != 409 {
		t.Fatal("pending first account lost its Check guidance", err)
	}
}

func TestCredentialFailureKeepsAuthenticationCategory(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{
		{fmt.Errorf("connector demo: %w", connectorauth.ErrTokenVerificationPending), "connector_verification_required", 409},
		{connectorauth.ErrOAuthClientSetupRequired, "connector_setup_required", 409},
		{connectorauth.ErrOAuthRefreshFailed, "connector_unavailable", 503},
		{connectorauth.ErrOAuthCredentialUnavailable, "connector_unavailable", 503},
		{fmt.Errorf("read: %w", connectorauth.ErrCredentialStoreUnavailable), "connector_unavailable", 503},
		{context.DeadlineExceeded, "connector_unavailable", 503},
		{connectorauth.ErrOAuthAuthorizationRequired, "connector_auth_required", 401},
		{fmt.Errorf("connector demo requires credentials: %w", connectorauth.ErrTokenRejected), "connector_auth_required", 401},
	} {
		var failed *Error
		if !errors.As(credentialFailure(tc.err), &failed) || failed.Code != tc.code || failed.Status != tc.status {
			t.Fatalf("%v: got %v", tc.err, failed)
		}
	}
}
