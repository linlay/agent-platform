package connector

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"agent-platform/internal/shellenv"
)

// cli.json env holds ordinary environment variables for this connector's CLI.
// A value is a literal or contains ${VAR} templates. Platform variables and
// $HOME are expanded by Platform, because a process does not expand variables
// inside another variable's value. Credential variables reach only this
// connector's own child process.
const (
	EnvConnectorDir       = "CONNECTOR_DIR"
	EnvConnectorConfigDir = "CONNECTOR_CONFIG_DIR"
	EnvOAuthAccessToken   = "OAUTH_ACCESS_TOKEN"
	EnvOneIDToken         = "ONEID_TOKEN"
	envHome               = "HOME"
)

var cliEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var bareHome = regexp.MustCompile(`\$HOME([^A-Za-z0-9_]|$)`)

type cliEnvEntry struct {
	value      string
	credential bool
}

// cliEnvironment validates cli.json env without resolving paths or secrets.
func (p Package) cliEnvironment() (map[string]cliEnvEntry, error) {
	raw, exists := p.CLI["env"]
	if p.CLI == nil || !exists {
		return nil, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("cli.json env must be an object of strings")
	}
	credentials := p.credentialVariables()
	result := make(map[string]cliEnvEntry, len(object))
	for name, rawValue := range object {
		value, ok := rawValue.(string)
		if !ok || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("cli.json env.%s must be a single-line string", name)
		}
		if !cliEnvName.MatchString(name) || shellenv.UnsafeOverride(name) || strings.HasPrefix(strings.ToUpper(name), "AP_") ||
			name == EnvConnectorDir || name == EnvConnectorConfigDir {
			return nil, fmt.Errorf("cli.json env.%s uses a reserved variable name", name)
		}
		entry := cliEnvEntry{value: value}
		for _, match := range envReference.FindAllStringSubmatch(value, -1) {
			switch ref := match[1]; {
			case ref == EnvConnectorDir || ref == EnvConnectorConfigDir || ref == envHome:
			case credentials[ref]:
				entry.credential = true
			default:
				return nil, fmt.Errorf("cli.json env.%s references unknown variable ${%s}", name, ref)
			}
		}
		result[name] = entry
	}
	return result, nil
}

// credentialVariables lists the template names the authentication mode supplies.
func (p Package) credentialVariables() map[string]bool {
	switch p.AuthMode {
	case AuthToken:
		fields, _ := TokenFields(p.Manifest)
		names := make(map[string]bool, len(fields))
		for _, field := range fields {
			names[field.Key] = true
		}
		return names
	case AuthOAuth, AuthMCP:
		return map[string]bool{EnvOAuthAccessToken: true}
	case AuthOneID:
		return map[string]bool{EnvOneIDToken: true}
	}
	return nil
}

func (p Package) connectorConfigDir() (string, error) {
	dir, err := StateDir(p.PersistentRoot(), p.ID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config"), nil
}

// expandPlatformVariables resolves ${CONNECTOR_DIR}, ${CONNECTOR_CONFIG_DIR},
// ${HOME} and $HOME. Credential templates are left for execution time.
func (p Package) expandPlatformVariables(value string) (string, error) {
	value = bareHome.ReplaceAllStringFunc(value, func(match string) string {
		return "${HOME}" + strings.TrimPrefix(match, "$HOME")
	})
	var expandErr error
	value = envReference.ReplaceAllStringFunc(value, func(ref string) string {
		var resolved string
		var err error
		switch ref[2 : len(ref)-1] {
		case EnvConnectorDir:
			resolved = p.Dir
		case EnvConnectorConfigDir:
			resolved, err = p.connectorConfigDir()
		case envHome:
			resolved, err = os.UserHomeDir()
		default:
			return ref
		}
		if err != nil && expandErr == nil {
			expandErr = fmt.Errorf("connector %s cannot resolve %s", p.ID, ref)
		}
		return resolved
	})
	return value, expandErr
}

// CLIConfigEnvironment is the public part of the CLI environment: every env
// entry that references no credential. It applies to every lifecycle phase,
// Host Bash, Terminal and stdio MCP.
func (p Package) CLIConfigEnvironment() (map[string]string, error) {
	result := map[string]string{}
	entries, err := p.cliEnvironment()
	if err != nil {
		return nil, err
	}
	for key, entry := range entries {
		if entry.credential {
			continue
		}
		if result[key], err = p.expandPlatformVariables(entry.value); err != nil {
			return nil, err
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// CLICredentialTemplates returns env entries that reference credentials, with
// platform variables expanded and protocol names mapped to the internal
// credential sources. Values are resolved only for this connector's child.
func (p Package) CLICredentialTemplates() (map[string]string, error) {
	entries, err := p.cliEnvironment()
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for key, entry := range entries {
		if !entry.credential {
			continue
		}
		value, err := p.expandPlatformVariables(entry.value)
		if err != nil {
			return nil, err
		}
		value = strings.ReplaceAll(value, "${"+EnvOAuthAccessToken+"}", "${ACCESS_TOKEN}")
		value = strings.ReplaceAll(value, "${"+EnvOneIDToken+"}", "${AP_ACCESS_TOKEN}")
		result[key] = value
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}
