package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"testing"
)

func TestAuthorizationOutcomesSurviveReplacementAndRestart(t *testing.T) {
	sources := connector.Sources{StateRoot: t.TempDir()}
	m := New(context.Background(), sources, nil)
	for _, s := range []Session{{ID: "old", AuthorizationID: "old", ConnectorID: "demo", Status: "authorized", URL: "https://secret.test", Message: "secret"}, {ID: "pending", AuthorizationID: "pending", ConnectorID: "demo", Status: "pending"}} {
		if err := m.persistAuthorization(s); err != nil {
			t.Fatal(err)
		}
	}
	restarted := New(context.Background(), sources, nil)
	old, err := restarted.SessionStatus("demo", "old")
	if err != nil || old.Status != "authorized" || old.URL != "" || old.Message != "" {
		t.Fatalf("%+v %v", old, err)
	}
	pending, err := restarted.SessionStatus("demo", "pending")
	if err != nil || pending.Status != "interrupted" {
		t.Fatalf("%+v %v", pending, err)
	}
	if _, err = restarted.SessionStatus("another", "old"); err == nil {
		t.Fatal("cross-connector lookup accepted")
	}
}
