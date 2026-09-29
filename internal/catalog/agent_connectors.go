package catalog

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

// AgentConnectorCandidate carries the exact source revision used for a narrow
// edit, so unrelated edits cannot be overwritten between reading and writing.
type AgentConnectorCandidate struct {
	Source       EditableAgentSourceFile
	Content      string
	ConnectorIDs []string
}

func (r *FileRegistry) ReadAgentConnectors(key string) ([]string, error) {
	source, err := r.ReadEditableAgentSource(key)
	if err != nil {
		return nil, err
	}
	_, ids, err := agentConnectorTree(source.Content)
	return ids, err
}

func agentConnectorTree(content string) (map[string]any, []string, error) {
	tree, err := config.LoadYAMLTreeBytes([]byte(content))
	if err != nil {
		return nil, nil, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("agent file must be a map")
	}
	section, _ := root["connectorConfig"].(map[string]any)
	if root["connectorConfig"] != nil && section == nil {
		return nil, nil, fmt.Errorf("connectorConfig must be a map")
	}
	ids, err := parseConnectorIDs(section["connectors"])
	return root, append([]string{}, ids...), err
}

func (r *FileRegistry) PrepareAgentConnector(key, id string, enabled bool) (AgentConnectorCandidate, error) {
	if !connector.ValidID(id) {
		return AgentConnectorCandidate{}, fmt.Errorf("invalid connector id")
	}
	source, err := r.ReadEditableAgentSource(key)
	if err != nil {
		return AgentConnectorCandidate{}, err
	}
	root, ids, err := agentConnectorTree(source.Content)
	if err != nil {
		return AgentConnectorCandidate{}, err
	}
	candidate := AgentConnectorCandidate{Source: source, Content: source.Content, ConnectorIDs: ids}
	if slices.Contains(ids, id) == enabled {
		return candidate, nil
	}
	if enabled {
		ids = append(ids, id)
	} else {
		ids = slices.DeleteFunc(ids, func(value string) bool { return value == id })
	}
	section, _ := root["connectorConfig"].(map[string]any)
	if section == nil {
		section = map[string]any{}
		root["connectorConfig"] = section
	}
	values := make([]any, len(ids))
	for i, value := range ids {
		values[i] = value
	}
	section["connectors"] = values
	def, _, err := parseAgentTree(source.Source.Path, cloneAgentSnapshotMap(root))
	if err != nil {
		return AgentConnectorCandidate{}, err
	}
	// Validate package availability and skill collisions before saving, including
	// when runtime publication will be deferred by an active Agent lease.
	assembler := runtimeAgentAssembler{connectors: connector.Sources{
		ExternalRoot:        r.cfg.Paths.EffectiveConnectorsCenterDir(),
		BuiltinRoot:         r.cfg.Paths.BuiltinConnectorsDir,
		NativeDesktopDir:    r.cfg.Paths.NativeDesktopDir,
		NativeDesktopWebDir: r.cfg.Paths.NativeDesktopWebDir,
		StateRoot:           r.cfg.Paths.EffectiveConnectorStateDir(),
	}}
	if err := assembler.resolveConnectors(&def); err != nil {
		return AgentConnectorCandidate{}, err
	}
	candidate.Content = string(renderYAMLMap(root))
	candidate.ConnectorIDs = ids
	return candidate, nil
}

// Source editors may keep draft references to packages that are not installed.
// Validate declarations of available packages; execution still requires every package.
func (r *FileRegistry) validateEditableConnectorSelection(definition map[string]any) error {
	ids, err := parseConnectorIDs(mapNode(definition["connectorConfig"])["connectors"])
	if err != nil || len(ids) < 2 {
		return err
	}
	packages := make([]connector.Package, 0, len(ids))
	for _, id := range ids {
		pkg, err := r.cfg.Paths.ConnectorSources().Load(id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		packages = append(packages, pkg)
	}
	return connector.ValidateSelection(packages)
}
