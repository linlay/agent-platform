package server

import (
	"path/filepath"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

func addConnectorAccessRoots(roots *contracts.RunAccessRoots, def catalog.AgentDefinition) error {
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

func runtimeConnectorMounts(mounts []contracts.SandboxExtraMount, def catalog.AgentDefinition) []contracts.SandboxExtraMount {
	if !hasRuntimeSandbox(def.Runtime) {
		return mounts
	}
	for _, mount := range def.ConnectorMounts {
		mounts = append(mounts, contracts.SandboxExtraMount{Source: mount.Dir, Destination: "/connectors/" + mount.ID, Mode: "ro"})
	}
	return mounts
}
