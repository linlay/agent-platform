package connectorauth

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/connector"
)

// CLIPhase separates preparation and login from credentialed component calls.
type CLIPhase string

const (
	CLIPhaseInit         CLIPhase = "init"
	CLIPhaseVersionCheck CLIPhase = "versionCheck"
	CLIPhaseAuth         CLIPhase = "auth"
	CLIPhaseSkipIf       CLIPhase = "skipIf"
	CLIPhaseStatus       CLIPhase = "status"
	CLIPhaseUnAuth       CLIPhase = "unAuth"
	CLIPhaseBusiness     CLIPhase = "business"
)

// CLIEnvironment freezes only the credential locator and public templates.
func CLIEnvironment(pkg connector.Package) (connector.CredentialEnvironment, error) {
	declaration, err := pkg.CLIExecutionEnvironment()
	d := connector.CredentialEnvironment{
		Root: pkg.PersistentRoot(), ID: pkg.ID, Mode: pkg.AuthMode, Env: declaration.Env, StaticEnv: declaration.StaticEnv,
		PackageDir: pkg.Dir, PackageBinDir: pkg.BinDir, PackageVersion: pkg.Version,
		InstallationDigest: pkg.InstallationDigest, InstallationRoot: pkg.InstallationRoot,
	}
	if err != nil {
		return d, err
	}
	if len(d.Env) > 0 && (pkg.AuthMode == connector.AuthOAuth || pkg.AuthMode == connector.AuthMCP) {
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

// ResolveCLIEnvironment always supplies the static component environment and
// reads current credentials only for status and business. It never uses process
// environment as a fallback and never changes the package or frozen binding.
func ResolveCLIEnvironment(ctx context.Context, d connector.CredentialEnvironment, identityFile string, phase CLIPhase) (map[string]string, error) {
	switch phase {
	case CLIPhaseStatus, CLIPhaseBusiness:
		return ResolveEnvironment(ctx, d, identityFile)
	case CLIPhaseInit, CLIPhaseVersionCheck, CLIPhaseAuth, CLIPhaseSkipIf, CLIPhaseUnAuth:
		return staticEnvironment(d)
	default:
		return nil, fmt.Errorf("invalid CLI execution phase")
	}
}

func ResolveEnvironment(ctx context.Context, d connector.CredentialEnvironment, identityFile string) (map[string]string, error) {
	env, err := staticEnvironment(d)
	if err != nil {
		return nil, err
	}
	if len(d.Env) == 0 {
		return env, nil
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
	if env == nil {
		env = map[string]string{}
	}
	for name, template := range d.Env {
		if _, exists := env[name]; exists {
			return nil, fmt.Errorf("connector %s has conflicting environment declarations", d.ID)
		}
		value, err := connector.ResolveAuthTemplate(template, values)
		if err != nil || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("connector %s requires valid credentials", d.ID)
		}
		env[name] = value
	}
	return env, nil
}

func staticEnvironment(d connector.CredentialEnvironment) (map[string]string, error) {
	if len(d.StaticEnv) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(d.StaticEnv))
	for name, value := range d.StaticEnv {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("connector %s has an invalid static environment", d.ID)
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
