package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/connectorauth"
)

func TestConnectorAuthenticationDenialHasDistinctHTTPError(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{connectorauth.ErrCredentialAuthenticationDenied, http.StatusForbidden, "connector_authentication_denied"},
		{fmt.Errorf("credential operation: %w", connectorauth.ErrCredentialAuthenticationDenied), http.StatusForbidden, "connector_authentication_denied"},
		{connectorauth.ErrTokenRejected, http.StatusUnauthorized, "connector_auth_required"},
		{connectorauth.ErrCredentialCheckFailed, http.StatusServiceUnavailable, "connector_execution_failed"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.writeConnectorError(rec, tc.err)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatalf("wrong rejection category: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if !errors.Is(connectorauth.ErrCredentialAuthenticationDenied, connectorauth.ErrTokenRejected) {
		t.Fatal("denial lost its rejected-credential error identity")
	}
}
