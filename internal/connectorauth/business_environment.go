package connectorauth

import (
	"context"
	"fmt"

	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

// TokenCLIEnvironmentSnapshot pairs the account environment with its revision
// under the same cross-process lock used by token updates. A command never
// executes account B while returning account A's revision.
func (m *Manager) TokenCLIEnvironmentSnapshot(ctx context.Context, pkg connector.Package, base []string) ([]string, string, error) {
	if pkg.AuthMode != connector.AuthToken {
		return nil, "", fmt.Errorf("token snapshot requires token authentication")
	}
	release, err := lockCredentials(ctx, pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return nil, "", err
	}
	defer release()
	if err := connector.RequireConfigured(pkg.PersistentRoot(), pkg.ID, pkg.AuthMode); err != nil {
		return nil, "", err
	}
	binding, err := CLIEnvironment(pkg)
	if err != nil {
		return nil, "", err
	}
	env, err := ApplyCLIProcessEnvironment(ctx, binding, base, m.identityFile, CLIPhaseBusiness)
	if err != nil {
		return nil, "", err
	}
	state, err := readAuthState(pkg.PersistentRoot(), pkg.ID)
	return env, state.Generation + ":" + state.Revision, err
}

// ApplyCLIProcessEnvironment consumes a frozen public descriptor only for its
// verified connector child. Installation and login paths never enter a generic
// shell's environment, and credentials are read at launch rather than frozen.
func ApplyCLIProcessEnvironment(ctx context.Context, binding connector.CredentialEnvironment, base []string, identityFile string, phase CLIPhase) ([]string, error) {
	env := append([]string(nil), base...)
	if binding.PackageDir != "" && binding.PackageVersion != "" {
		pkg := connector.Package{
			Manifest: connector.Manifest{ID: binding.ID, Version: binding.PackageVersion, AuthMode: binding.Mode},
			Dir:      binding.PackageDir, BinDir: binding.PackageBinDir, StateRoot: binding.Root,
			InstallationDigest: binding.InstallationDigest, InstallationRoot: binding.InstallationRoot,
		}
		var err error
		env, err = CLIInstallationEnvironment(pkg, env)
		if err != nil {
			return nil, err
		}
		env, err = applyCLIPrivateEnvironment(pkg, env)
		if err != nil {
			return nil, err
		}
	}
	values, err := ResolveCLIEnvironment(ctx, binding, identityFile, phase)
	if err != nil {
		return nil, err
	}
	for key, value := range values {
		env = hostenv.Set(env, key, value)
	}
	return env, nil
}
