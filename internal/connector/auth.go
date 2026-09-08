package connector

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// AuthMode describes who supplies authentication, independently of component type.
// Its zero value delegates authentication to the connector and encodes as JSON null.
type AuthMode string

const (
	AuthDelegated AuthMode = ""
	AuthToken     AuthMode = "token"
	AuthOneID     AuthMode = "oneid-token"
	AuthOAuth     AuthMode = "oauth"
	AuthMCP       AuthMode = "mcp"
)

func (m AuthMode) MarshalJSON() ([]byte, error) {
	if m == AuthDelegated || m == "none" || m == "cli" {
		return []byte("null"), nil
	}
	return json.Marshal(string(m))
}

func (m *AuthMode) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*m = AuthDelegated
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("auth_mode must be a string or null")
	}
	switch AuthMode(value) {
	case AuthToken, AuthOneID, AuthOAuth, AuthMCP:
		*m = AuthMode(value)
	case "none", "cli": // Read older installed packages without changing their source.
		*m = AuthDelegated
	default:
		return fmt.Errorf("auth_mode must be token, oneid-token, oauth, mcp or null")
	}
	return nil
}

func (m *Manifest) normalizeAuth() {
	// The former oauth mode only implemented MCP discovery. Preserve those
	// packages while giving new oauth declarations their separate meaning.
	if m.AuthMode == AuthOAuth {
		var settings struct {
			Discovery bool `json:"discovery"`
		}
		if json.Unmarshal(m.OAuth, &settings) == nil && settings.Discovery {
			m.AuthMode = AuthMCP
		}
	}
}

// ManagedCLI opts into the existing explicit CLI lifecycle adapter. It is a
// component capability, not an authentication mode or proof of authorization.
func (p Package) ManagedCLI() bool {
	platform, _ := p.CLI["platform"].(map[string]any)
	return platform["npmPackage"] != nil
}

func (p *Package) normalizeLegacyIdentityAuth() {
	if p.AuthMode != AuthDelegated || len(p.MCP) == 0 {
		return
	}
	for _, component := range p.MCP {
		platform, _ := component["platform"].(map[string]any)
		if component["type"] != "streamableHttp" || platform["authSource"] != "identity-file" {
			return
		}
	}
	p.AuthMode = AuthOneID
}
