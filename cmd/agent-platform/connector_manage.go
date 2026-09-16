package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/mcp"
)

// Connector management can run before full runtime startup, so installing or
// authenticating a package does not require models, Agents, or KBASE sidecars.
func runConnectorManagement(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: connector-manage <import|prepare|login|status|logout|set-token> --runtime-dir <path> [--id <id>] [--credentials-file <path>] [--overwrite] [package.zip]")
	}
	action := args[0]
	flags := flag.NewFlagSet("connector-manage "+action, flag.ContinueOnError)
	runtimeDir := flags.String("runtime-dir", "", "deployment runtime root")
	id := flags.String("id", "", "connector id")
	component := flags.String("component", "", "MCP component to authorize")
	overwrite := flags.Bool("overwrite", false, "replace an existing external package")
	credentialsFile := flags.String("credentials-file", "", "JSON file containing token field values (set-token only)")
	identityFile := flags.String("identity-file", "", "Desktop SSO token file (absolute path; defaults to <state-dir>/identity/access-token)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *runtimeDir == "" {
		return fmt.Errorf("--runtime-dir is required")
	}
	if *component != "" && action != "login" && action != "status" && action != "set-oauth-client" {
		return fmt.Errorf("--component is only valid for login, status or set-oauth-client")
	}
	if action != "set-token" && action != "set-oauth-client" && *credentialsFile != "" {
		return fmt.Errorf("--credentials-file is only valid for set-token or set-oauth-client")
	}
	runtimeRoot, err := filepath.Abs(*runtimeDir)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	stateDir, err := config.ResolveStateDir(cwd, runtimeRoot)
	if err != nil {
		return err
	}
	*identityFile, err = config.ResolveIdentityFile(stateDir, *identityFile)
	if err != nil {
		return err
	}
	root := filepath.Join(runtimeRoot, "connectors-center")
	if err := (connector.Sources{ExternalRoot: root, StateRoot: stateDir}).ValidateRoots(); err != nil {
		return err
	}
	sources := connector.Sources{ExternalRoot: root, StateRoot: filepath.Join(stateDir, "connectors"), LegacyStateRoot: filepath.Join(runtimeRoot, "connector-state")}
	if err := sources.MigrateLegacy(filepath.Join(runtimeRoot, "connectors")); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	encoder := json.NewEncoder(out)
	if action == "import" {
		if flags.NArg() != 1 {
			return fmt.Errorf("import requires one ZIP file")
		}
		file, err := os.Open(flags.Arg(0))
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		pkg, err := connector.ImportArchive(ctx, sources, file, info.Size(), *overwrite, mcp.ValidateConnectorPackages, nil)
		if err != nil {
			return err
		}
		result := map[string]any{"id": pkg.ID, "version": pkg.Version, "installed": true, "authMode": pkg.AuthMode}
		if pkg.CLI != nil {
			manager := connectorauth.New(ctx, sources, nil)
			prepared, prepareErr := manager.Prepare(ctx, pkg.ID)
			result["preparation"] = prepared
			if err := encoder.Encode(result); err != nil {
				return err
			}
			if prepareErr != nil {
				return fmt.Errorf("package imported, CLI preparation failed: %w", prepareErr)
			}
			return nil
		}
		return encoder.Encode(result)
	}
	if flags.NArg() != 0 || !connector.ValidID(*id) {
		return fmt.Errorf("a valid --id is required")
	}
	manager := connectorauth.New(ctx, sources, nil).WithIdentityFile(*identityFile)
	switch action {
	case "set-oauth-client":
		if *credentialsFile == "" {
			return fmt.Errorf("--credentials-file is required")
		}
		var info connectorauth.OAuthClientInfo
		if err := connector.ReadJSON(*credentialsFile, &info); err != nil {
			return fmt.Errorf("invalid OAuth client file")
		}
		result, err := manager.SetOAuthClient(ctx, *id, *component, info)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "prepare":
		prepared, err := manager.Prepare(ctx, *id)
		if outputErr := encoder.Encode(prepared); outputErr != nil {
			return outputErr
		}
		return err
	case "set-token":
		if *credentialsFile == "" {
			return fmt.Errorf("set-token requires --credentials-file")
		}
		var values map[string]string
		if err := connector.ReadJSON(*credentialsFile, &values); err != nil {
			return fmt.Errorf("cannot read token credentials JSON file")
		}
		s, err := manager.SetToken(ctx, *id, values)
		if err != nil {
			return err
		}
		return encoder.Encode(s)
	case "status":
		s, err := manager.StatusComponent(ctx, *id, *component)
		if err != nil {
			return err
		}
		return encoder.Encode(s)
	case "logout":
		if err := manager.Logout(ctx, *id); err != nil {
			return err
		}
		return encoder.Encode(map[string]string{"id": *id, "status": "unauthorized"})
	case "login":
		s, err := manager.StartComponent(*id, *component)
		if err != nil {
			return err
		}
		if err := encoder.Encode(s); err != nil {
			return err
		}
		previous, _ := json.Marshal(s)
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				manager.Cancel(*id)
				return ctx.Err()
			case <-ticker.C:
			}
			s, err = manager.StatusComponent(ctx, *id, *component)
			if err != nil {
				return err
			}
			data, _ := json.Marshal(s)
			if string(data) != string(previous) {
				if err := encoder.Encode(s); err != nil {
					return err
				}
				previous = data
			}
			switch s.Status {
			case "authorized":
				return nil
			case "failed", "canceled":
				return fmt.Errorf("connector login %s: %s", s.Status, s.Message)
			}
		}
	default:
		return fmt.Errorf("unknown connector action %q", action)
	}
}
