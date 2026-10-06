package config

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type configMigration struct {
	before  map[string][]byte
	after   map[string][]byte
	retired []string
	notes   []string
}

// RunConfigMigration is explicit and offline. Preview never prints config values.
func RunConfigMigration(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("config-migrate", flag.ContinueOnError)
	flags.SetOutput(out)
	root := flags.String("config-dir", ".", "Platform config root (contains configs/)")
	agents := flags.String("agents-dir", "", "optional editable Agent directory to remove retired embedding declarations")
	apply := flags.Bool("apply", false, "apply after validation; stop Platform first")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	dir, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	m, err := planConfigMigration(filepath.Join(dir, "configs"), *agents)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(m.after))
	for path := range m.after {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fmt.Fprintf(out, "write %s\n", path)
	}
	for _, path := range m.retired {
		fmt.Fprintf(out, "retire %s\n", path)
	}
	for _, note := range m.notes {
		fmt.Fprintln(out, note)
	}
	if !*apply {
		fmt.Fprintln(out, "Preview only. Stop Platform, then rerun with --apply. No indexes are changed.")
		return nil
	}
	if len(paths) == 0 && len(m.retired) == 0 {
		fmt.Fprintln(out, "Nothing to migrate.")
		return nil
	}
	backup, err := m.commit(filepath.Join(dir, "config-backups"))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Migration complete. Original files and restore manifest: %s\n", backup)
	return nil
}

func (m *configMigration) read(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("configuration must be a regular file: %s", path)
	}
	if _, err = LoadYAMLTreeBytesWithOptions(b, YAMLTreeOptions{RejectDuplicateKeys: true}); err != nil {
		return "", fmt.Errorf("invalid YAML in %s", path)
	}
	m.before[path] = b
	return string(b), nil
}
func migrationRender(v map[string]YAMLSourceValue) (string, error) {
	s, err := renderACPMap(v, 0)
	return s + "\n", err
}
func migrationObject(v map[string]YAMLSourceValue) (YAMLSourceValue, error) {
	body, err := renderACPMap(v, 2)
	if err != nil {
		return YAMLSourceValue{}, err
	}
	if len(v) == 0 {
		return YAMLSourceValue{Head: "{}"}, nil
	}
	return YAMLSourceValue{Body: body}, nil
}
func migrationPut(target map[string]YAMLSourceValue, key string, value YAMLSourceValue, path string) error {
	if _, exists := target[key]; exists {
		return fmt.Errorf("migration conflict at %s.%s; resolve explicitly", path, key)
	}
	target[key] = value
	return nil
}
func planConfigMigration(dir, agents string) (*configMigration, error) {
	m := &configMigration{before: map[string][]byte{}, after: map[string][]byte{}}
	maps := map[string]map[string]YAMLSourceValue{}
	for _, name := range []string{"agent-settings.yml", "agent-prompt.yml", "tools.yml", "runtime.yml"} {
		path := filepath.Join(dir, name)
		s, err := m.read(path)
		if err != nil {
			return nil, err
		}
		v, err := YAMLSourceMap(s)
		if err != nil {
			return nil, err
		}
		maps[name] = v
	}
	settings, prompts, tools, runtime := maps["agent-settings.yml"], maps["agent-prompt.yml"], maps["tools.yml"], maps["runtime.yml"]
	changed := map[string]bool{}
	for _, key := range []string{"preset-tools", "preset-connectors"} {
		if v, ok := tools[key]; ok {
			if err := migrationPut(settings, key, v, "agent-settings"); err != nil {
				return nil, err
			}
			delete(tools, key)
			changed["tools.yml"] = true
			changed["agent-settings.yml"] = true
		}
	}
	var oldModel string
	if source := m.before[filepath.Join(dir, "runtime.yml")]; len(source) > 0 {
		fields, err := YAMLSourceMap(string(source), "kbx", "embedding")
		if err != nil {
			return nil, err
		}
		oldModel = migrationModelIdentity(fields["model-key"])
	}
	for _, name := range retiredAgentFiles {
		path := filepath.Join(dir, name)
		s, err := m.read(path)
		if err != nil {
			return nil, err
		}
		if _, exists := m.before[path]; !exists {
			continue
		}
		v, err := YAMLSourceMap(s)
		if err != nil {
			return nil, err
		}
		switch name {
		case "general-settings.yml", "coder-settings.yml", "kbase-settings.yml":
			mode := strings.TrimSuffix(name, "-settings.yml")
			if value, ok := v["acp-bridges"]; ok {
				if err = migrationPut(settings, "acp-bridges", value, "agent-settings"); err != nil {
					return nil, err
				}
				delete(v, "acp-bridges")
			}
			if _, ok := v["workspace-agents"]; ok {
				tree, err := LoadYAMLTreeBytes([]byte(s))
				if err != nil {
					return nil, err
				}
				w, _ := tree.(map[string]any)["workspace-agents"].(map[string]any)
				enabled, _ := w["enabled"].(bool)
				if enabled {
					file, _ := w["file"].(string)
					if strings.TrimSpace(file) == "" {
						return nil, fmt.Errorf("%s workspace-agents.file is empty", path)
					}
					fields, err := YAMLSourceMap(s, "workspace-agents")
					if err != nil {
						return nil, err
					}
					delete(fields, "enabled")
					v["workspace-agents"], err = migrationObject(fields)
					if err != nil {
						return nil, err
					}
				} else {
					delete(v, "workspace-agents")
				}
			}
			if mode == "kbase" {
				if _, ok := v["embedding"]; ok {
					fields, err := YAMLSourceMap(s, "embedding")
					if err != nil {
						return nil, err
					}
					oldModel = migrationModelIdentity(fields["modelKey"])
					if value, ok := fields["modelKey"]; ok {
						fields["model-key"] = value
						delete(fields, "modelKey")
					}
					ev, err := migrationObject(fields)
					if err != nil {
						return nil, err
					}
					kv, err := migrationObject(map[string]YAMLSourceValue{"embedding": ev})
					if err != nil {
						return nil, err
					}
					if err = migrationPut(runtime, "kbx", kv, "runtime"); err != nil {
						return nil, err
					}
					changed["runtime.yml"] = true
					delete(v, "embedding")
				}
				for _, key := range []string{"index", "maintenance", "refresh", "extraction"} {
					if _, ok := v[key]; ok {
						delete(v, key)
						m.notes = append(m.notes, "Retired kbase-settings."+key+" (original retained in backup)")
					}
				}
			}
			value, err := migrationObject(v)
			if err != nil {
				return nil, err
			}
			if err = migrationPut(settings, mode, value, "agent-settings"); err != nil {
				return nil, err
			}
			changed["agent-settings.yml"] = true
		case "prompts.yml", "coder-prompts.yml", "kbase-prompts.yml":
			key := "shared"
			if name != "prompts.yml" {
				key = strings.TrimSuffix(name, "-prompts.yml")
			}
			value, err := migrationObject(v)
			if err != nil {
				return nil, err
			}
			if err = migrationPut(prompts, key, value, "agent-prompt"); err != nil {
				return nil, err
			}
			changed["agent-prompt.yml"] = true
		case "ai-tools.yml":
			for key, value := range v {
				if key == "speech" {
					tree, _ := LoadYAMLTreeBytes([]byte(s))
					sp, _ := tree.(map[string]any)[key].(map[string]any)
					for _, raw := range sp {
						entry, _ := raw.(map[string]any)
						if entry["enabled"] == true {
							return nil, fmt.Errorf("speech is not implemented; remove enabled speech configuration before migration")
						}
					}
					m.notes = append(m.notes, "Removed inactive speech example (original retained in backup)")
					continue
				}
				if err = migrationPut(tools, key, value, "tools"); err != nil {
					return nil, err
				}
			}
			changed["tools.yml"] = true
		}
		m.retired = append(m.retired, path)
	}
	if agents != "" {
		if err := filepath.WalkDir(agents, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			if filepath.Dir(path) != filepath.Clean(agents) && filepath.Base(path) != "agent.yml" {
				return nil
			}
			s, err := m.read(path)
			if err != nil {
				return err
			}
			tree, err := LoadYAMLTreeBytes([]byte(s))
			if err != nil {
				return err
			}
			root, ok := tree.(map[string]any)
			if !ok {
				return nil
			}
			kb, ok := root["kbaseConfig"].(map[string]any)
			if !ok {
				return nil
			}
			_, ok = kb["embedding"].(map[string]any)
			if !ok {
				return nil
			}
			modelFields, err := YAMLSourceMap(s, "kbaseConfig", "embedding")
			if err != nil {
				return err
			}
			key := migrationModelIdentity(modelFields["modelKey"])
			if key != "" && key != oldModel {
				return fmt.Errorf("Agent embedding conflicts with deployment model: %s; select the global model explicitly before migration", path)
			}
			fields, err := YAMLSourceMap(s, "kbaseConfig")
			if err != nil {
				return err
			}
			delete(fields, "embedding")
			replacement, err := migrationObject(fields)
			if err != nil {
				return err
			}
			top, err := YAMLSourceMap(s)
			if err != nil {
				return err
			}
			after, err := ReplaceYAMLSourceValues(s, top, map[string]YAMLSourceValue{"kbaseConfig": replacement})
			if err != nil {
				return err
			}
			m.after[path] = []byte(after)
			return nil
		}); err != nil {
			return nil, err
		}
	} else {
		m.notes = append(m.notes, "Agent YAML is not scanned; use --agents-dir to migrate retired Agent embedding declarations.")
	}
	for name := range changed {
		s, err := migrationRender(maps[name])
		if err != nil {
			return nil, err
		}
		m.after[filepath.Join(dir, name)] = []byte(s)
	}
	// Validate candidates in isolation, without loading deployment secrets or touching indexes.
	temp, err := os.MkdirTemp("", "platform-config-migrate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	for name := range maps {
		path := filepath.Join(dir, name)
		b := m.before[path]
		if next, ok := m.after[path]; ok {
			b = next
		}
		if err = os.WriteFile(filepath.Join(temp, name), b, 0600); err != nil {
			return nil, err
		}
	}
	c := defaultConfig(LoadOptions{})
	for _, load := range []func() error{func() error { return c.applyAgentSettingsFile(filepath.Join(temp, "agent-settings.yml")) }, func() error { return c.applyAgentPromptFile(filepath.Join(temp, "agent-prompt.yml")) }, func() error { return c.applyToolsFile(filepath.Join(temp, "tools.yml"), false) }, func() error { return c.applyRuntimeFile(filepath.Join(temp, "runtime.yml")) }} {
		if err = load(); err != nil {
			return nil, fmt.Errorf("candidate validation: %w", err)
		}
	}
	return m, nil
}
func (m *configMigration) commit(root string) (string, error) {
	// Refuse changes since preview/planning and preserve every source before publication.
	for path, b := range m.before {
		now, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(now, b) {
			return "", fmt.Errorf("source changed during migration: %s", path)
		}
	}
	for path := range m.after {
		if _, ok := m.before[path]; !ok {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return "", fmt.Errorf("destination appeared: %s", path)
			}
		}
	}
	backup := filepath.Join(root, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(backup, 0700); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(m.before))
	for path := range m.before {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var manifest strings.Builder
	for i, path := range paths {
		name := fmt.Sprintf("%04d-%s", i, filepath.Base(path))
		if err := os.WriteFile(filepath.Join(backup, name), m.before[path], 0600); err != nil {
			return "", err
		}
		fmt.Fprintf(&manifest, "%s\t%s\n", name, path)
	}
	for path := range m.after {
		if _, ok := m.before[path]; !ok {
			fmt.Fprintf(&manifest, "NEW\t%s\n", path)
		}
	}
	if err := os.WriteFile(filepath.Join(backup, "restore.tsv"), []byte(manifest.String()), 0600); err != nil {
		return "", err
	}
	written := []string{}
	rollback := func(cause error) (string, error) {
		var failures []string
		for _, path := range written {
			var err error
			if b, ok := m.before[path]; ok {
				err = os.WriteFile(path, b, 0600)
			} else {
				err = os.Remove(path)
			}
			if err != nil {
				failures = append(failures, path)
			}
		}
		return backup, fmt.Errorf("migration failed: %w; backup %s; restore failures: %v", cause, backup, failures)
	}
	for path, b := range m.after {
		tmp, err := os.CreateTemp(filepath.Dir(path), ".config-migrate-")
		if err != nil {
			return rollback(err)
		}
		name := tmp.Name()
		_, err = tmp.Write(b)
		closeErr := tmp.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, path)
		}
		if err != nil {
			os.Remove(name)
			return rollback(err)
		}
		written = append(written, path)
	}
	for _, path := range m.retired {
		if err := os.Remove(path); err != nil {
			return rollback(err)
		}
		written = append(written, path)
	}
	return backup, nil
}

// Compare declaration identities before environment expansion. Distinct unresolved
// expressions must never collapse to the same empty model during migration.
func migrationModelIdentity(value YAMLSourceValue) string {
	raw := strings.TrimSpace(value.Head)
	switch raw {
	case "", "null", "~", "{}":
		return ""
	}
	return strings.Trim(raw, "\"'")
}
