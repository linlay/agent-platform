package connector

import (
	"encoding/json"
	"fmt"
)

// AuthMode describes who supplies authentication, independently of component
// type. New declarations use explicit canonical values; the temporary manifest
// reader separately translates the previous release's field names and defaults.
type AuthMode string

const (
	// AuthNoAuth needs no authentication at all.
	AuthNoAuth AuthMode = "none"
	// AuthDelegated leaves authentication to the connector itself, for
	// example a CLI login command or QR code.
	AuthDelegated AuthMode = "self"
	AuthToken     AuthMode = "token"
	AuthOneID     AuthMode = "oneid"
	AuthOAuth     AuthMode = "oauth"
	AuthMCP       AuthMode = "mcp"
)

func (m *AuthMode) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("authMode must be none, self, token, oauth, mcp or oneid")
	}
	switch AuthMode(value) {
	case AuthNoAuth, AuthDelegated, AuthToken, AuthOneID, AuthOAuth, AuthMCP:
		*m = AuthMode(value)
	case "oneid-token":
		*m = AuthOneID
	default:
		return fmt.Errorf("authMode must be none, self, token, oauth, mcp or oneid")
	}
	return nil
}

// ManagedCLI opts into the existing explicit CLI lifecycle adapter. It is a
// component capability, not an authentication mode or proof of authorization.
func (p Package) ManagedCLI() bool {
	return p.CLI != nil && p.CLI["auth"] != nil
}
