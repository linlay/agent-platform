package connector

import (
	"fmt"
	"regexp"
)

// CredentialEnvironment is frozen into a run without resolving secret values.
// Env holds this connector's cli.json env entries that reference credentials;
// they are resolved only when its own child process starts.
type CredentialEnvironment struct {
	Root        string
	ID          string
	Mode        AuthMode
	Resource    string
	Destination string
	Env         map[string]string
}

var authVariable = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

func ResolveAuthTemplate(template string, values map[string]string) (string, error) {
	missing := false
	result := authVariable.ReplaceAllStringFunc(template, func(ref string) string {
		value, ok := values[ref[2:len(ref)-1]]
		if !ok || value == "" {
			missing = true
		}
		return value
	})
	if missing {
		return "", fmt.Errorf("connector credential is unavailable")
	}
	return result, nil
}

// validateAuthDeclarations checks rules that span connector.json and the
// component files. A connector that needs no authentication cannot ship login
// commands or ask the platform for an identity.
func (p Package) validateAuthDeclarations() error {
	if p.AuthMode == AuthNoAuth {
		if p.ManagedCLI() {
			return fmt.Errorf("authMode none cannot declare CLI auth")
		}
		for _, component := range p.MCP {
			platform, _ := component["platform"].(map[string]any)
			if source, _ := platform["authSource"].(string); source != "" {
				return fmt.Errorf("authMode none does not accept MCP authSource")
			}
		}
	}
	_, err := p.cliEnvironment()
	return err
}
