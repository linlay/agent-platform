package connectorauth

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/connector"
)

// CLIEnvironment freezes only the credential locator and public templates.
func CLIEnvironment(pkg connector.Package) (connector.CredentialEnvironment, error) {
	binding := pkg.AuthBindings["cli"]
	d := connector.CredentialEnvironment{Root: pkg.PersistentRoot(), ID: pkg.ID, Mode: pkg.AuthMode, Env: binding.Env}
	if len(binding.Env) > 0 && (pkg.AuthMode == connector.AuthOAuth || pkg.AuthMode == connector.AuthMCP) {
		var err error
		pkg, err = OAuthComponent(pkg, "cli")
		if err != nil {
			return d, err
		}
		d.Resource, err = OAuthResource(pkg)
		if err != nil {
			return d, err
		}
		d.Destination = oauthDestination(pkg)
	}
	return d, nil
}

func ResolveEnvironment(ctx context.Context, d connector.CredentialEnvironment, identityFile string) (map[string]string, error) {
	if len(d.Env) == 0 {
		return nil, nil
	}
	values := map[string]string{}
	switch d.Mode {
	case connector.AuthToken:
		path, err := connector.CredentialsPath(d.Root, d.ID)
		if err != nil {
			return nil, err
		}
		if err := readPrivateJSON(path, &values); err != nil {
			// Pending values only change this diagnostic. They never become an
			// execution credential before the explicit Check commits them.
			dir, stateErr := StateDir(d.Root, d.ID)
			var pending map[string]string
			if stateErr == nil && readPrivateJSON(filepath.Join(dir, "pending-credentials.json"), &pending) == nil && len(pending) > 0 {
				return nil, fmt.Errorf("connector %s credentials have been saved and are awaiting verification; use Check in connector settings; no need to re-enter credentials", d.ID)
			}
			return nil, fmt.Errorf("connector %s requires credentials", d.ID)
		}
	case connector.AuthOAuth, connector.AuthMCP:
		token, err := AccessToken(ctx, d.Root, d.ID, d.Resource, nil, d.Destination)
		if err != nil {
			return nil, err
		}
		values["ACCESS_TOKEN"] = token
	case connector.AuthOneID:
		var err error
		values, err = agentconfig.ReadIdentityEnvironment(identityFile)
		if err != nil {
			return nil, fmt.Errorf("Desktop SSO is unavailable")
		}
	default:
		return nil, fmt.Errorf("unsupported credential environment")
	}
	env := map[string]string{}
	for name, template := range d.Env {
		value, err := connector.ResolveAuthTemplate(template, values)
		if err != nil || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("connector %s requires valid credentials", d.ID)
		}
		env[name] = value
	}
	return env, nil
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
