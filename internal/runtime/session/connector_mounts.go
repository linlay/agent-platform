package session

import (
	"path/filepath"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

func AddConnectorAccessRoots(roots *contracts.RunAccessRoots, def catalog.AgentDefinition) error {
	paths := def.ConnectorRuntimeSkillDirs()
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
		roots.ReadRoots = append(roots.ReadRoots, canonical)
		roots.ReadonlyRoots = append(roots.ReadonlyRoots, canonical)
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
	result := map[string]string{}
	for _, name := range def.ConnectorNativeTools {
		result[name] = "builtin.desktop"
	}
	return result
}
