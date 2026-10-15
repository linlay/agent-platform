package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/connectorauth"
)

func TestConnectorAuthenticationErrorsPreserveFailureCategory(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{connectorauth.ErrOAuthClientSetupRequired, http.StatusConflict, "connector_setup_required"},
		{connectorauth.ErrOAuthAuthorizationRequired, http.StatusUnauthorized, "connector_auth_required"},
		{connectorauth.ErrTokenRejected, http.StatusUnauthorized, "connector_auth_required"},
		{fmt.Errorf("credential operation: %w", connectorauth.ErrTokenRejected), http.StatusUnauthorized, "connector_auth_required"},
		{connectorauth.ErrOAuthRefreshFailed, http.StatusServiceUnavailable, "connector_execution_failed"},
		{connectorauth.ErrOAuthCredentialUnavailable, http.StatusServiceUnavailable, "connector_execution_failed"},
		{connectorauth.ErrCredentialCheckFailed, http.StatusServiceUnavailable, "connector_execution_failed"},
		{connectorauth.ErrCredentialValidatorUnavailable, http.StatusServiceUnavailable, "connector_execution_failed"},
		{connectorauth.ErrPreparationRequired, http.StatusConflict, "connector_preparation_required"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.writeConnectorError(rec, tc.err)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatalf("category %s: %d %s", tc.code, rec.Code, rec.Body.String())
		}
	}
}
