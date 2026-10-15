package connectorauth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/connector"
)

// CLIEnvironment freezes only the credential locator and the cli.json env
// templates that reference credentials; values are read at execution time.
func CLIEnvironment(pkg connector.Package) (connector.CredentialEnvironment, error) {
	d := connector.CredentialEnvironment{Root: pkg.PersistentRoot(), ID: pkg.ID, Mode: pkg.AuthMode}
	templates, err := pkg.CLICredentialTemplates()
	if err != nil {
		return d, err
	}
	d.Env = templates
	if len(templates) > 0 && (pkg.AuthMode == connector.AuthOAuth || pkg.AuthMode == connector.AuthMCP) {
		selected, err := cliOAuthComponent(pkg)
		if err != nil {
			return d, err
		}
		d.Resource, err = OAuthResource(selected)
		if err != nil {
			return d, err
		}
		d.Destination = oauthDestination(selected)
	}
	return d, nil
}

// cliOAuthComponent selects the grant a CLI uses. With MCP discovery the CLI
// shares the package's single MCP component grant; plain OAuth uses the
// package's own OAuth settings.
func cliOAuthComponent(pkg connector.Package) (connector.Package, error) {
	if pkg.AuthMode == connector.AuthMCP {
		return OAuthComponent(pkg, "")
	}
	return OAuthComponent(pkg, "cli")
}

func ResolveEnvironment(ctx context.Context, d connector.CredentialEnvironment, identityFile string) (map[string]string, error) {
	env, _, err := ResolveEnvironmentWithRevision(ctx, d, identityFile)
	return env, err
}

// ResolveEnvironmentWithRevision also returns the token revision read together
// with the values, so a caller can bind a request to the account it executes.
// Other modes return an empty revision.
func ResolveEnvironmentWithRevision(ctx context.Context, d connector.CredentialEnvironment, identityFile string) (map[string]string, string, error) {
	if len(d.Env) == 0 {
		return nil, "", nil
	}
	values := map[string]string{}
	revision := ""
	switch d.Mode {
	case connector.AuthToken:
		var err error
		values, revision, err = ReadTokenCredentials(ctx, d.Root, d.ID)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, "", fmt.Errorf("connector %s credentials are unavailable: %w", d.ID, ErrCredentialStoreUnavailable)
			}
			// Pending values only change this diagnostic. They never become an
			// execution credential before the explicit Check commits them.
			dir, stateErr := StateDir(d.Root, d.ID)
			var pending map[string]string
			if stateErr == nil && readPrivateJSON(filepath.Join(dir, "pending-credentials.json"), &pending) == nil && len(pending) > 0 {
				return nil, "", fmt.Errorf("connector %s: %w", d.ID, ErrTokenVerificationPending)
			}
			return nil, "", fmt.Errorf("connector %s requires credentials: %w", d.ID, ErrTokenRejected)
		}
	case connector.AuthOAuth, connector.AuthMCP:
		token, err := AccessToken(ctx, d.Root, d.ID, d.Resource, nil, d.Destination)
		if err != nil {
			return nil, "", err
		}
		values["ACCESS_TOKEN"] = token
	case connector.AuthOneID:
		var err error
		values, err = agentconfig.ReadIdentityEnvironment(identityFile)
		if err != nil {
			return nil, "", fmt.Errorf("Desktop SSO is unavailable")
		}
	default:
		return nil, "", fmt.Errorf("unsupported credential environment")
	}
	env := map[string]string{}
	for name, template := range d.Env {
		value, err := connector.ResolveAuthTemplate(template, values)
		if err != nil || strings.ContainsAny(value, "\x00\r\n") {
			return nil, "", fmt.Errorf("connector %s requires valid credentials: %w", d.ID, ErrTokenRejected)
		}
		env[name] = value
	}
	return env, revision, nil
}

func ResolveEnvironments(ctx context.Context, bindings []connector.CredentialEnvironment, identityFile string) (map[string]string, error) {
	result := map[string]string{}
	for _, binding := range bindings {
		env, err := ResolveEnvironment(ctx, binding, identityFile)
		if err != nil {
			return nil, err
		}
		for key, value := range env {
			if _, exists := result[key]; exists {
				return nil, fmt.Errorf("conflicting connector credential environment")
			}
			result[key] = value
		}
	}
	return result, nil
}
