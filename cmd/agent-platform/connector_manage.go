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
		return fmt.Errorf("usage: connector-manage <import|login|status|logout> --runtime-dir <path> [--id <id>] [--overwrite] [package.zip]")
	}
	action := args[0]
	flags := flag.NewFlagSet("connector-manage "+action, flag.ContinueOnError)
	runtimeDir := flags.String("runtime-dir", "", "deployment runtime root")
	id := flags.String("id", "", "connector id")
	overwrite := flags.Bool("overwrite", false, "replace an existing external package")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *runtimeDir == "" {
		return fmt.Errorf("--runtime-dir is required")
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
		return encoder.Encode(map[string]any{"id": pkg.ID, "version": pkg.Version, "installed": true, "authMode": pkg.AuthMode})
	}
	if flags.NArg() != 0 || !connector.ValidID(*id) {
		return fmt.Errorf("a valid --id is required")
	}
	manager := connectorauth.New(ctx, sources, func(_ context.Context, changedID string) error {
		// Wake the standard source watcher after CLI login/logout; file contents
		// and definition hashes do not change. Live API login reloads directly.
		file, err := connector.ReadFile(root, changedID, "connector.json")
		if err != nil {
			return err
		}
		_, err = connector.SaveDefinition(root, file, file.SHA256, nil, nil)
		return err
	})
	switch action {
	case "status":
		s, err := manager.Status(ctx, *id)
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
		s, err := manager.Start(*id)
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
			s, err = manager.Status(ctx, *id)
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
