package connectorauth

import (
	"agent-platform/internal/agentconfig"
	"agent-platform/internal/bashast"
	"agent-platform/internal/connector"
	"context"
	"fmt"
	"mvdan.cc/sh/v3/shell"
	"mvdan.cc/sh/v3/syntax"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// CommandEnvironment derives a single connector's private environment at call time.
// It is not stored in model-facing metadata or the Agent-wide catalog.
func (m *Manager) CommandEnvironment(ctx context.Context, id string) (map[string]string, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return nil, err
	}
	if pkg.Builtin {
		return nil, nil
	}
	state, err := pkg.ReadConnection()
	if err != nil {
		return nil, err
	}
	if !state.Bound || !state.Enabled {
		return nil, fmt.Errorf("connector_disabled: enable this connector before calling it")
	}
	if pkg.CLI == nil {
		return nil, fmt.Errorf("connector has no CLI")
	}
	if err = m.requirePrepared(pkg); err != nil {
		return nil, err
	}
	values, err := m.credentialEnvironment(ctx, pkg)
	if err != nil {
		return nil, err
	}
	bins, err := pkg.CLIBinDirs()
	if err != nil {
		return nil, err
	}
	env, err := m.cliEnvironment(pkg)
	if err != nil {
		return nil, err
	}
	currentPath := ""
	for _, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			currentPath = strings.TrimPrefix(value, "PATH=")
		}
	}
	values["PATH"] = connector.PathValue(currentPath, bins, string(filepath.ListSeparator))
	return values, nil
}

// ResolveBashEnvironment rejects multi-connector shell invocations: one process
// cannot safely carry two different private HOME/login environments.
func (m *Manager) resolveBashConnectorID(ctx context.Context, ids []string, command string, runtimeDirs ...map[string]string) (string, error) {
	names := map[string]string{}
	packages := map[string]connector.Package{}
	for _, id := range ids {
		pkg, err := m.sources.Load(id)
		if err != nil {
			return "", err
		}
		if pkg.Builtin || pkg.CLI == nil {
			continue
		}
		packages[id] = pkg
		version, _ := pkg.CLI["versionCheck"].(map[string]any)
		commands, _ := version["command"].(map[string]any)
		osKey := runtime.GOOS
		if osKey == "windows" {
			osKey = "win32"
		}
		line, _ := commands[osKey].(string)
		words, err := shell.Fields(line, func(string) string { return "" })
		if err == nil && len(words) > 0 {
			names[strings.TrimSuffix(filepath.Base(words[0]), ".cmd")] = id
		}
	}

	parsed := bashast.ParseForSecurity(command)
	selected := ""
	for _, call := range parsed.Commands {
		if len(call.Argv) == 0 {
			continue
		}
		target := call.Argv[0]
		switch filepath.Base(target) {
		case "node", "python", "python3", "sh", "bash", "zsh":
			if len(call.Argv) > 1 && !strings.HasPrefix(call.Argv[1], "-") {
				target = call.Argv[1]
			}
		}
		id := names[strings.TrimSuffix(filepath.Base(target), ".cmd")]

		if filepath.IsAbs(target) {
			id = ""
			canonical, e := filepath.EvalSymlinks(filepath.Clean(target))
			if e == nil {
				for key, pkg := range packages {
					install, _ := pkg.InstallDir()
					roots := []string{pkg.Dir, install}
					if len(runtimeDirs) > 0 {
						roots = append(roots, runtimeDirs[0][key])
					}
					for _, root := range roots {
						if root == "" {
							continue
						}
						r, e := filepath.EvalSymlinks(root)
						if e == nil {
							rel, e := filepath.Rel(r, canonical)
							if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
								id = key
							}
						}
					}
				}
			}
		}

		if id != "" {
			if selected != "" && selected != id {
				return "", fmt.Errorf("invoke connectors in separate tool calls")
			}
			selected = id
		}
	}
	if selected == "" {
		return "", nil
	}
	tree, parseErr := syntax.NewParser().Parse(strings.NewReader(command), "")
	if parseErr != nil {
		return "", fmt.Errorf("connector command syntax is unsupported")
	}
	detached := false
	syntax.Walk(tree, func(node syntax.Node) bool {
		if stmt, ok := node.(*syntax.Stmt); ok && (stmt.Background || stmt.Disown || stmt.Coprocess) {
			detached = true
		}
		return !detached
	})
	if detached {
		return "", fmt.Errorf("connector commands must remain attached to their tool call")
	}
	// A user-secret environment must not flow to a helper, pipe, expansion or
	// second command supplied by the model. Each connector call is its own child.
	if parsed.Kind != bashast.Simple || len(parsed.Commands) != 1 || parsed.Commands[0].Uncertain || len(parsed.Commands[0].EnvVars) > 0 || len(parsed.Commands[0].Redirects) > 0 {
		return "", fmt.Errorf("invoke the connector as a separate command without shell wrappers")
	}
	for _, word := range parsed.Commands[0].Words {
		if word.Expansion {
			return "", fmt.Errorf("connector command expansions are not supported")
		}
	}

	return selected, nil
}

// credentialEnvironment is used only by status and business calls, never by init/version/auth.
func (m *Manager) credentialEnvironment(ctx context.Context, pkg connector.Package) (map[string]string, error) {
	values, err := pkg.CLIPrivateEnvironment()
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]string{}
	}
	if static, ok := pkg.CLI["staticEnv"].(map[string]any); ok {
		for key, value := range static {
			if text, ok := value.(string); ok {
				if _, reserved := values[key]; !reserved {
					values[key] = text
				}
			}
		}
	}
	dynamic, _ := pkg.CLI["env"].(map[string]any)
	if len(dynamic) == 0 {
		return values, nil
	}
	credentials := map[string]string{}
	switch pkg.AuthMode {
	case connector.AuthToken:
		var ready bool
		credentials, ready, err = TokenValues(pkg)
		if err != nil || !ready {
			return nil, fmt.Errorf("authorization_required")
		}
	case connector.AuthOneID:
		identity, e := agentconfig.ReadIdentityEnvironment(m.identityFile)
		if e != nil || identity[agentconfig.EnvAccessToken] == "" || !connector.IdentityMatchesOwner(pkg.Owner, identity[agentconfig.EnvAccessToken]) {
			return nil, fmt.Errorf("authorization_required")
		}
		credentials["ONEID_TOKEN"] = identity[agentconfig.EnvAccessToken]
	case connector.AuthOAuth:
		resource, e := OAuthResource(pkg)
		if e != nil {
			return nil, e
		}
		token, e := AccessToken(ctx, pkg.CredentialRoot(), pkg.ID, resource, m.client, oauthDestination(pkg))
		if e != nil {
			return nil, e
		}
		credentials["OAUTH_ACCESS_TOKEN"] = token
	default:
		return nil, fmt.Errorf("invalid dynamic CLI credentials")
	}
	pattern := regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)
	for key, value := range dynamic {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid dynamic CLI value")
		}
		missing := false
		text = pattern.ReplaceAllStringFunc(text, func(marker string) string {
			value, ok := credentials[marker[2:len(marker)-1]]
			if !ok {
				missing = true
			}
			return value
		})
		if missing {
			return nil, fmt.Errorf("authorization_required")
		}
		values[key] = text
	}
	return values, nil
}

func (m *Manager) ResolveBashEnvironment(ctx context.Context, ids []string, command string, runtimeDirs ...map[string]string) (map[string]string, error) {
	id, err := m.resolveBashConnectorID(ctx, ids, command, runtimeDirs...)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	return m.CommandEnvironment(ctx, id)
}
func (m *Manager) BeginBash(ctx context.Context, ids []string, command string, runtimeDirs map[string]string) (context.Context, func(), error) {
	id, err := m.resolveBashConnectorID(ctx, ids, command, runtimeDirs)
	if err != nil {
		return ctx, nil, err
	}
	if id == "" {
		return ctx, func() {}, nil
	}
	return m.beginBusiness(ctx, id)
}
