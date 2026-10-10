package session

import (
	"agent-platform/internal/runtimeskills"
	"os"
	"path/filepath"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
)

func AddConnectorAccessRoots(roots *contracts.RunAccessRoots, def catalog.AgentDefinition) error {
	paths := def.ConnectorRuntimeSkillDirs()
	if def.RuntimeRevision != "" {
		paths = append(paths, def.RuntimeDir)
	}
	skills, err := runtimeskills.Dirs(def.RuntimeDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, dir := range skills {
		paths = append(paths, dir)
	}
	for _, mount := range def.ConnectorMounts {
		paths = append(paths, mount.Dir)
	}
	for _, path := range paths {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		canonical, err = filepath.Abs(canonical)
		if err != nil {
			return err
		}
		if !containsRuntimeRoot(roots.ReadRoots, canonical) {
			roots.ReadRoots = append(roots.ReadRoots, canonical)
		}
		if !containsRuntimeRoot(roots.ReadonlyRoots, canonical) {
			roots.ReadonlyRoots = append(roots.ReadonlyRoots, canonical)
		}
	}
	return nil
}

func RuntimeConnectorMounts(mounts []contracts.SandboxExtraMount, def catalog.AgentDefinition) []contracts.SandboxExtraMount {
	if !HasRuntimeSandbox(def.Runtime) {
		return mounts
	}
	for _, mount := range def.ConnectorMounts {
		mounts = append(mounts, contracts.SandboxExtraMount{Source: mount.Dir, Destination: "/connectors/" + mount.ID, Mode: "ro"})
	}
	return mounts
}

func RuntimeConnectorDirs(def catalog.AgentDefinition) map[string]string {
	dirs := make(map[string]string, len(def.ConnectorMounts))
	for _, mount := range def.ConnectorMounts {
		dirs[mount.ID] = mount.Dir
	}
	return dirs
}

func RuntimeNativeConnectorTools(def catalog.AgentDefinition) map[string]string {
	mounted := map[string]bool{}
	for _, mount := range def.ConnectorMounts {
		mounted[mount.ID] = true
	}
	result := map[string]string{}
	for _, name := range def.ConnectorNativeTools {
		if id, ok := connector.NativeToolConnector(name); ok && mounted[id] {
			result[name] = id
		}
	}
	return result
}

func RuntimeSkillDirs(def catalog.AgentDefinition) map[string]string {
	dirs, _ := runtimeskills.Dirs(def.RuntimeDir)
	return dirs
}

func containsRuntimeRoot(roots []string, p string) bool {
	for _, root := range roots {
		if root == p {
			return true
		}
	}
	return false
}
