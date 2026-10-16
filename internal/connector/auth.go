package connector

import (
	"encoding/json"
	"fmt"
)

// AuthMode describes who supplies authentication, independently of component
// type. New declarations use explicit canonical values; the temporary manifest
// reader separately translates the previous release's field names and defaults.
// The long-term aliases oauth (MCP standard authorization) and oneid-token are
// normalized on read, so packages and APIs only observe canonical values.
type AuthMode string

const (
	// AuthNoAuth needs no authentication at all.
	AuthNoAuth AuthMode = "none"
	// AuthDelegated leaves authentication to the connector itself, for
	// example a CLI login command or QR code.
	AuthDelegated AuthMode = "self"
	AuthToken     AuthMode = "token"
	AuthOneID     AuthMode = "oneid"
	// AuthMCP authorizes with the MCP authorization specification (OAuth 2.1
	// with resource and authorization server discovery).
	AuthMCP AuthMode = "mcp"
)

const authModeValues = "none, self, token, mcp or oneid (aliases: oauth for mcp, oneid-token for oneid)"

func (m *AuthMode) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("authMode must be a string: %s", authModeValues)
	}
	switch AuthMode(value) {
	case AuthNoAuth, AuthDelegated, AuthToken, AuthOneID, AuthMCP:
		*m = AuthMode(value)
	case "oauth":
		*m = AuthMCP
	case "oneid-token":
		*m = AuthOneID
	default:
		return fmt.Errorf("authMode %q is not supported; use %s", value, authModeValues)
	}
	return nil
}

// ManagedCLI opts into the existing explicit CLI lifecycle adapter. It is a
// component capability, not an authentication mode or proof of authorization.
func (p Package) ManagedCLI() bool {
	return p.CLI != nil && p.CLI["auth"] != nil
}
