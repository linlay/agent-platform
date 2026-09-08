// Package connectormigrate implements the one-time, offline registry migration.
// It is never called by the runtime loader or a watcher.
package connectormigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/mcp"
)

type Result struct {
	Connectors []string `json:"connectors"`
	Retired    []string `json:"retired,omitempty"`
	Agents     []string `json:"agents"`
	Applied    bool     `json:"applied"`
	BackupDir  string   `json:"backupDir,omitempty"`
}

func Run(runtimeRoot string, apply bool) (Result, error) {
	root, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return Result{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return Result{}, err
	}
	stateDir, err := config.ResolveStateDir(cwd, root)
	if err != nil {
		return Result{}, err
	}
	if err := (connector.Sources{ExternalRoot: filepath.Join(root, "connectors-center"), StateRoot: stateDir}).ValidateRoots(); err != nil {
		return Result{}, err
	}
	for _, scope := range migrationScopes {
		if scope != ".state/connectors" && connector.RootsOverlap(stateDir, filepath.Join(root, scope)) {
			return Result{}, fmt.Errorf("AP_RUNTIME_STATE_DIR must not overlap migration scope %s", scope)
		}
	}
	stateNamespace := filepath.Join(stateDir, "connectors")
	legacy := filepath.Join(root, "registries", "mcp-servers")
	legacyExists := false
	if _, err := os.Stat(legacy); err == nil {
		legacyExists = true
	} else if !os.IsNotExist(err) {
		return Result{}, err
	}
	stageParent := ""
	if apply {
		stageParent = root
	}
	stage, err := os.MkdirTemp(stageParent, ".connector-migration-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stage)
	before, err := fingerprints(root, stateNamespace)
	if err != nil {
		return Result{}, err
	}
	for _, scope := range migrationScopes {
		if err := copyTree(migrationScopePath(root, scope, stateNamespace), filepath.Join(stage, filepath.FromSlash(scope))); err != nil {
			return Result{}, err
		}
	}
	result := Result{}
	layoutChanged := false
	for _, scope := range []string{"connectors", "connectors-center/.state", "connectors-center/.credentials", "connector-state"} {
		if _, err := os.Lstat(filepath.Join(root, scope)); err == nil {
			layoutChanged = true
			result.Retired = append(result.Retired, scope)
		}
	}
	sources := connector.Sources{ExternalRoot: filepath.Join(stage, "connectors-center"), StateRoot: filepath.Join(stage, ".state", "connectors")}
	if err := sources.MigrateLegacy(filepath.Join(stage, "connectors")); err != nil {
		return Result{}, err
	}
	err = filepath.WalkDir(filepath.Join(stage, "registries", "mcp-servers"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") || !catalog.ShouldLoadRuntimeName(entry.Name()) {
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(path)); ext != ".yml" && ext != ".yaml" {
			return fmt.Errorf("unexpected legacy registry file %s", path)
		}
		relative, err := filepath.Rel(filepath.Join(stage, "registries", "mcp-servers"), path)
		if err != nil {
			return err
		}
		originalPath := filepath.Join(legacy, relative)
		tree, err := config.LoadYAMLTree(path)
		if err != nil {
			return err
		}
		manifest, component, credentials, err := mcp.ConvertLegacy(originalPath, tree)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", path, err)
		}
		if connector.IsBuiltin(manifest.ID) {
			return fmt.Errorf("legacy MCP id %q uses the Platform-reserved builtin namespace; rename it before migration", manifest.ID)
		}
		servers := component["mcpServers"].(map[string]any)
		main := servers["main"].(map[string]any)
		if main["type"] == "stdio" {
			platform := main["platform"].(map[string]any)
			for _, value := range []any{main["command"], platform["workingDirectory"]} {
				if path, ok := value.(string); ok && pathWithin(legacy, path) {
					return fmt.Errorf("stdio command or workingDirectory is inside the retired registry; relocate its files before migration")
				}
			}
		}
		dir := filepath.Join(stage, "connectors-center", manifest.ID)
		if _, err := os.Stat(dir); err == nil {
			return fmt.Errorf("connector %q already exists; migration does not overwrite it", manifest.ID)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := writeJSON(filepath.Join(dir, "connector.json"), manifest, 0o644); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(dir, "mcp.json"), component, 0o644); err != nil {
			return err
		}
		if len(credentials) > 0 {
			path, err := connector.CredentialsPath(sources.PersistentRoot(), manifest.ID)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return fmt.Errorf("connector %q credentials already exist; migration does not overwrite them", manifest.ID)
			}
			if err := writeJSON(path, credentials, 0o600); err != nil {
				return err
			}
		}
		result.Connectors = append(result.Connectors, manifest.ID)
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	agents := filepath.Join(stage, "agents")
	err = filepath.WalkDir(agents, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !catalog.ShouldLoadRuntimeName(name) {
			return nil
		}
		if name != "agent.yml" && name != "agent.yaml" && filepath.Dir(path) != agents {
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(path)); ext != ".yml" && ext != ".yaml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		converted, changed, err := migrateAgent(data)
		if err == nil {
			var builtinChanged bool
			converted, builtinChanged, err = migrateBuiltinSkills(converted)
			changed = changed || builtinChanged
		}
		if err != nil {
			return fmt.Errorf("migrate agent %s: %w", name, err)
		}
		if !changed {
			return nil
		}
		tree, err := config.LoadYAMLTreeBytes(converted)
		if err != nil {
			return err
		}
		definition := tree.(map[string]any)
		mounts := definition["connectorConfig"].(map[string]any)["connectors"].([]any)
		for _, mount := range mounts {
			id := mount.(string)
			if connector.BuiltinSkillConnector("builtin-"+strings.TrimPrefix(id, "builtin.")) == id {
				continue
			}
			if _, err := connector.Load(filepath.Join(stage, "connectors-center"), mount.(string)); err != nil {
				return fmt.Errorf("agent %s references unavailable connector %s: %w", name, mount, err)
			}
		}
		if err := os.WriteFile(path, converted, 0o600); err != nil {
			return err
		}
		rel, _ := filepath.Rel(stage, path)
		result.Agents = append(result.Agents, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	for _, scope := range []string{"connectors/builtin.dbx", "connectors/builtin.httpx", "connectors-center/builtin.dbx", "connectors-center/builtin.httpx", "connectors-center/.builtin-state", "skills-center/builtin-dbx", "skills-center/builtin-httpx", "connectors/.builtin-state"} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(scope))); err == nil {
			result.Retired = append(result.Retired, scope)
		} else if !os.IsNotExist(err) {
			return Result{}, err
		}
		if err := os.RemoveAll(filepath.Join(stage, filepath.FromSlash(scope))); err != nil {
			return Result{}, err
		}
	}
	for _, name := range []string{"builtin.dbx", "builtin.httpx", ".builtin-state"} {
		if err := os.RemoveAll(filepath.Join(stage, "connectors-center", name)); err != nil {
			return Result{}, err
		}
	}
	if _, err := mcp.NewRegistryWithSources(sources); err != nil {
		return Result{}, fmt.Errorf("validate migrated connectors: %w", err)
	}
	if !apply || !legacyExists && !layoutChanged && len(result.Agents) == 0 && len(result.Retired) == 0 {
		return result, nil
	}
	after, err := fingerprints(root, stateNamespace)
	if err != nil {
		return Result{}, err
	}
	if before != after {
		return Result{}, fmt.Errorf("runtime sources changed during migration; retry after stopping the runtime")
	}
	backup := filepath.Join(root, ".connector-migration-backup-"+time.Now().UTC().Format("20060102T150405.000000000"))
	if err := os.Mkdir(backup, 0o700); err != nil {
		return Result{}, err
	}
	var moved []string
	rollback := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			scope := moved[i]
			target := migrationScopePath(root, scope, stateNamespace)
			_ = os.RemoveAll(target)
			_ = os.Rename(filepath.Join(backup, filepath.FromSlash(scope)), target)
		}
	}
	for _, scope := range migrationScopes {
		target := migrationScopePath(root, scope, stateNamespace)
		old := filepath.Join(backup, filepath.FromSlash(scope))
		if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
			rollback()
			return Result{}, err
		}
		if _, err := os.Stat(target); err == nil {
			if err := os.Rename(target, old); err != nil {
				rollback()
				return Result{}, err
			}
		} else if !os.IsNotExist(err) {
			rollback()
			return Result{}, err
		}
		moved = append(moved, scope)
		if scope == "connectors-center" || scope == ".state/connectors" || scope == "agents" {
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				rollback()
				return Result{}, err
			}
			if err := os.Rename(filepath.Join(stage, scope), target); err != nil {
				rollback()
				return Result{}, err
			}
		}
	}
	result.Applied = true
	result.BackupDir = backup
	return result, nil
}

func migrateAgent(data []byte) ([]byte, bool, error) {
	if !strings.Contains(string(data), "mcp-servers:") {
		return data, false, nil
	}
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		return nil, false, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("agent must be an object")
	}
	tools, _ := root["toolConfig"].(map[string]any)
	raw, exists := tools["mcp-servers"]
	if !exists {
		return data, false, nil
	}
	if root["connectorConfig"] != nil {
		return nil, false, fmt.Errorf("agent already declares connectorConfig; merge explicitly before migration")
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false, fmt.Errorf("mcp-servers must be an array")
	}
	ids := []string{}
	for _, item := range items {
		id, ok := item.(string)
		id = strings.ToLower(strings.TrimSpace(id))
		if !ok || !connector.ValidID(id) {
			return nil, false, fmt.Errorf("invalid connector reference")
		}
		ids = append(ids, id)
	}
	// Preserve every unrelated byte, comment, secret reference and prompt.
	lines := strings.Split(string(data), "\n")
	out := []string{}
	removing := false
	inToolConfig := false
	toolChildIndent := -1
	indent := 0
	removed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		current := len(line) - len(strings.TrimLeft(line, " "))
		if current == 0 && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			inToolConfig = strings.HasPrefix(line, "toolConfig:") && strings.TrimSpace(strings.TrimPrefix(line, "toolConfig:")) == ""
		}
		if inToolConfig && current > 0 && trimmed != "" && !strings.HasPrefix(trimmed, "#") && toolChildIndent < 0 {
			toolChildIndent = current
		}
		if !removed && inToolConfig && current == toolChildIndent && strings.HasPrefix(trimmed, "mcp-servers:") {
			removing = true
			removed = true
			indent = current
			continue
		}
		if removing {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || current > indent || current == indent && strings.HasPrefix(trimmed, "-") {
				continue
			}
			removing = false
		}
		out = append(out, line)
	}
	if !removed {
		return nil, false, fmt.Errorf("unsupported inline toolConfig; expand YAML before migration")
	}
	out = append(out, "connectorConfig:", "  connectors:")
	for _, id := range ids {
		out = append(out, "    - "+id)
	}
	if len(ids) == 0 {
		out[len(out)-1] = "  connectors: []"
	}
	return []byte(strings.Join(out, "\n") + "\n"), true, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeJSON(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), mode)
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == source {
			return os.MkdirAll(target, 0o700)
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := migrationStateLink(source, path, filepath.Base(target) == "connectors" && filepath.Base(filepath.Dir(target)) == ".state")
			if err != nil {
				return err
			}
			return os.Symlink(link, dest)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(dest, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported migration file %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, info.Mode().Perm())
	})
}

func fingerprints(root string, stateNamespaces ...string) (string, error) {
	stateNamespace := filepath.Join(root, ".state", "connectors")
	if len(stateNamespaces) > 0 {
		stateNamespace = stateNamespaces[0]
	}
	hash := sha256.New()
	for _, scope := range migrationScopes {
		source := migrationScopePath(root, scope, stateNamespace)
		err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				link, err := migrationStateLink(source, path, scope == ".state/connectors")
				if err != nil {
					return err
				}
				fmt.Fprintf(hash, "link\x00%s\x00%s\x00", path, link)
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%s\x00%d\x00", path, len(data))
			_, err = hash.Write(data)
			return err
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func migrationStateLink(scopeRoot, path string, connectorNamespace bool) (string, error) {
	namespace := filepath.Join(scopeRoot, ".state")
	if connectorNamespace {
		namespace = scopeRoot
	} else if name := filepath.Base(scopeRoot); name != "connectors" && name != "connectors-center" && name != "connector-state" {
		return "", fmt.Errorf("migration does not follow symlinks: %s", path)
	}
	rel, err := filepath.Rel(namespace, path)
	if err != nil {
		return "", err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 || !connector.ValidID(parts[0]) || connector.IsBuiltin(parts[0]) {
		return "", fmt.Errorf("migration link is outside connector state: %s", path)
	}
	return connector.InternalStateLink(filepath.Join(namespace, parts[0]), path)
}

func migrationScopePath(root, scope, stateNamespace string) string {
	if scope == ".state/connectors" {
		return stateNamespace
	}
	return filepath.Join(root, filepath.FromSlash(scope))
}
