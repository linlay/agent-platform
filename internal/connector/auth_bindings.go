package connector

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// AuthBinding contains templates only, never account credentials. "cli" targets
// the CLI component; "mcp:<name>" targets a named MCP component.
type AuthBinding struct {
	OAuth   json.RawMessage   `json:"oauth,omitempty"`
	Grant   string            `json:"grant,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// CredentialEnvironment is frozen into a run without resolving secret values.
type CredentialEnvironment struct {
	Root        string
	ID          string
	Mode        AuthMode
	Resource    string
	Destination string
	Env         map[string]string
}

var authVariable = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

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

func (p Package) ValidateAuthBindings() error {
	allowed := map[string]string{}
	switch p.AuthMode {
	case AuthToken:
		fields, err := TokenFields(p.Manifest)
		if err != nil {
			return err
		}
		for _, f := range fields {
			allowed[f.Key] = "configured"
		}
	case AuthOAuth, AuthMCP:
		allowed["ACCESS_TOKEN"] = "configured"
	case AuthOneID:
		allowed["AP_ACCESS_TOKEN"] = "configured"
	default:
		if len(p.AuthBindings) != 0 {
			return fmt.Errorf("delegated authentication uses configEnv, not auth_bindings")
		}
	}
	for target, binding := range p.AuthBindings {
		if (len(binding.OAuth) > 0 || binding.Grant != "") && p.AuthMode != AuthOAuth && p.AuthMode != AuthMCP {
			return fmt.Errorf("OAuth bindings require oauth or mcp mode")
		}
		httpTarget := false
		if target == "cli" {
			if p.CLI == nil {
				return fmt.Errorf("auth binding targets missing CLI")
			}
		} else if name, ok := strings.CutPrefix(target, "mcp:"); ok {
			component, exists := p.MCP[name]
			if !exists {
				return fmt.Errorf("auth binding targets missing MCP component")
			}
			httpTarget = component["type"] == "http" || component["type"] == "streamableHttp"
		} else {
			return fmt.Errorf("auth binding target must be cli or mcp:<name>")
		}
		if httpTarget && len(binding.Env) != 0 || !httpTarget && len(binding.Headers) != 0 {
			return fmt.Errorf("auth binding does not match component transport")
		}
		seen := map[string]bool{}
		for name, template := range binding.Headers {
			key := strings.ToLower(name)
			if !headerName.MatchString(name) || seen[key] || key == "host" || key == "content-length" || key == "connection" || strings.HasPrefix(key, "mcp-") {
				return fmt.Errorf("invalid authentication header")
			}
			seen[key] = true
			if p.AuthMode != AuthToken {
				return fmt.Errorf("OAuth and OneID manage HTTP Authorization automatically")
			}
			if err := validateAuthTemplate(template, allowed); err != nil {
				return err
			}
		}
		for name, template := range binding.Env {
			if !envName.MatchString(name) || strings.HasPrefix(name, "AP_") || strings.HasSuffix(name, "_CONFIG_DIR") || name == "PATH" || name == "HOME" || name == "NODE_OPTIONS" || name == "PYTHONPATH" || name == "BASH_ENV" || name == "ENV" || name == "ZDOTDIR" || name == "SHELLOPTS" || name == "BASHOPTS" || strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") {
				return fmt.Errorf("invalid credential environment variable")
			}
			if err := validateAuthTemplate(template, allowed); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAuthTemplate(template string, allowed map[string]string) error {
	if strings.ContainsAny(template, "\r\n\x00") || !authVariable.MatchString(template) {
		return fmt.Errorf("auth binding requires a credential template")
	}
	resolved, err := ResolveAuthTemplate(template, allowed)
	if err != nil || strings.Contains(resolved, "${") {
		return fmt.Errorf("auth binding references an undeclared credential")
	}
	return nil
}
