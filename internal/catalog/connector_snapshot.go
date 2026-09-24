package catalog

import (
	"fmt"
	"path/filepath"

	"agent-platform/internal/connector"
)

// ConnectorSnapshot contains only package references and the tool selection,
// never Agent runtime environment, prompts or credential values.
type ConnectorSnapshot struct {
	AgentKey string           `json:"agentKey"`
	Mounts   []ConnectorMount `json:"mounts"`
	Tools    []string         `json:"tools"`
}

func (d AgentDefinition) FreezeConnectors() ConnectorSnapshot {
	return ConnectorSnapshot{AgentKey: d.Key, Mounts: append([]ConnectorMount(nil), d.ConnectorMounts...), Tools: append([]string(nil), d.Tools...)}
}
func (d *AgentDefinition) RestoreConnectors(sources connector.Sources, snapshot ConnectorSnapshot) error {
	if snapshot.AgentKey != d.Key {
		return fmt.Errorf("connector snapshot Agent mismatch")
	}
	d.Connectors = nil
	d.Tools = append([]string(nil), snapshot.Tools...)
	mounts := map[string]ConnectorMount{}
	for _, mount := range snapshot.Mounts {
		if !connector.ValidID(mount.ID) || filepath.Clean(mount.Dir) != filepath.Join(sources.SharedRoot(), mount.ID, mount.Digest) {
			return fmt.Errorf("invalid frozen connector path")
		}
		digest, err := connector.RuntimeFingerprint(mount.Dir)
		if err != nil || digest != mount.Digest {
			return fmt.Errorf("frozen connector integrity failure: %s", mount.ID)
		}
		d.Connectors = append(d.Connectors, mount.ID)
		mounts[mount.ID] = mount
	}
	if err := resolveConnectorPackages(d, func(id string) (connector.Package, error) {
		pkg, err := connector.LoadDirectory(mounts[id].Dir, id)
		pkg.Builtin = connector.IsBuiltin(id)
		pkg.StateRoot = sources.PersistentRoot()
		return pkg, err
	}); err != nil {
		return err
	}
	return d.bindConnectorRuntime()
}
