package connectorops

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestOneIDCLIRejectedBeforeAuthenticationAndWriteReceipt(t *testing.T) {
	for _, cli := range []string{`{}`, `{"env":{"SERVICE_TOKEN":"${AP_ID_TOKEN}"}}`} {
		for _, loggedIn := range []bool{false, true} {
			name := "no templates"
			if cli != `{}` {
				name = "credential template"
			}
			if loggedIn {
				name += "/signed in"
			} else {
				name += "/signed out"
			}
			t.Run(name, func(t *testing.T) {
				sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
				dir := filepath.Join(sources.ExternalRoot, "demo")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for name, data := range map[string]string{
					"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","authMode":"oneid"}`,
					"cli.json":       cli,
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
				pkg, err := sources.Load("demo")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pkg.SetConfigured(true); err != nil {
					t.Fatal(err)
				}
				identity := filepath.Join(t.TempDir(), "identity")
				wantStatus := "unauthorized"
				if loggedIn {
					if err := os.WriteFile(identity, []byte("synthetic-test-token"), 0600); err != nil {
						t.Fatal(err)
					}
					wantStatus = "authorized"
				}
				manager := connectorauth.New(t.Context(), sources, nil).WithSsoIdTokenFile(identity)
				status, err := manager.StatusComponent(t.Context(), "demo", "")
				if err != nil || status.Status != wantStatus {
					t.Fatalf("identity status = %q, err = %v", status.Status, err)
				}
				service := Service{Auth: manager, Sources: sources}
				scope := Scope{Subject: "owner", IdempotencyNamespace: "fixture", Execution: []Permission{{ConnectorID: "demo", Adapter: "cli"}}, Check: func() error { return nil }}
				for _, key := range []string{"", "oneid-write-123"} {
					req := Request{ConnectorID: "demo", Adapter: "cli", Args: []string{"business"}, IdempotencyKey: key}
					for range 2 {
						_, err := service.Invoke(t.Context(), scope, req)
						var failure *Error
						if !errors.As(err, &failure) || failure.Code != "connector_oneid_cli_unsupported" || failure.Status != http.StatusUnprocessableEntity {
							t.Fatalf("key %q: unsupported CLI returned %v", key, err)
						}
					}
				}
				if _, err := os.Stat(filepath.Join(sources.PersistentRoot(), "demo", "invocations")); !os.IsNotExist(err) {
					t.Fatalf("unsupported CLI created write receipts: %v", err)
				}
				scope.Execution = nil
				_, err = service.Invoke(t.Context(), scope, Request{ConnectorID: "demo", Adapter: "cli", Args: []string{"business"}})
				if err == nil || err.Error() != "connector_execution_not_allowed" {
					t.Fatalf("unsupported CLI bypassed permission check: %v", err)
				}
			})
		}
	}
}
