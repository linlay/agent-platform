package config

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrACPArguments = errors.New("invalid ACP registration arguments")
	ErrACPConflict  = errors.New("ACP bridge belongs to another plugin or has unmanaged configuration")
	ErrACPConfig    = errors.New("ACP configuration cannot be safely updated")
	ErrACPWrite     = errors.New("ACP configuration write failed; retry registration")
	acpIdentifier   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
)

// ACPRegistrationStore owns a single Platform process's configuration mutations.
// Ownership and connection settings are committed together in the same file.
// The active snapshot is immutable: registration does not hot-reload ACP routes.
type ACPRegistrationStore struct {
	mu     sync.Mutex
	path   string
	active map[string]CoderACPBridgeConfig
}

type ACPRegistration struct {
	SourcePluginID string  `json:"sourcePluginId"`
	BridgeID       string  `json:"bridgeId"`
	BaseURL        string  `json:"baseUrl,omitempty"`
	TimeoutMS      int     `json:"timeoutMs,omitempty"`
	AuthToken      *string `json:"authToken,omitempty"`
}

type ACPRegistrationResult struct {
	Changed         bool `json:"changed"`
	Removed         bool `json:"removed"`
	RestartRequired bool `json:"restartRequired"`
}

func NewACPRegistrationStore(settings CoderSettingsConfig) *ACPRegistrationStore {
	active := make(map[string]CoderACPBridgeConfig, len(settings.ACPBridges))
	for k, v := range settings.ACPBridges {
		active[k] = v
	}
	return &ACPRegistrationStore{path: settings.SourcePath, active: active}
}

func (s *ACPRegistrationStore) Mutate(input ACPRegistration, remove bool) (ACPRegistrationResult, error) {
	result := ACPRegistrationResult{}
	if !acpIdentifier.MatchString(input.SourcePluginID) || !acpIdentifier.MatchString(input.BridgeID) {
		return result, ErrACPArguments
	}
	if !remove {
		parsed, err := url.Parse(input.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || input.TimeoutMS < 0 {
			return result, ErrACPArguments
		}
		input.BaseURL = strings.TrimRight(input.BaseURL, "/")
		if input.TimeoutMS == 0 {
			input.TimeoutMS = 300000
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return result, ErrACPConfig
	}
	if info, err := os.Lstat(s.path); err == nil {
		if !info.Mode().IsRegular() {
			return result, ErrACPConfig
		}
	} else if !os.IsNotExist(err) {
		return result, ErrACPConfig
	}
	before, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return result, ErrACPConfig
	}
	source := string(before)
	root, err := YAMLSourceMap(source)
	if err != nil {
		return result, ErrACPConfig
	}
	tree, err := LoadYAMLTreeBytes(before)
	values, ok := tree.(map[string]any)
	if err != nil || !ok {
		return result, ErrACPConfig
	}
	if _, retired := values["acp-proxies"]; retired {
		return result, ErrACPConfig
	}
	bridges, err := parseCoderACPBridges(values["acp-bridges"], nil)
	if err != nil {
		return result, ErrACPConfig
	}
	entries, err := YAMLSourceMap(source, "acp-bridges")
	if err != nil {
		return result, ErrACPConfig
	}
	existing, exists := bridges[input.BridgeID]
	var fields map[string]YAMLSourceValue
	owner := ""
	if exists {
		fields, err = YAMLSourceMap(source, "acp-bridges", input.BridgeID)
		if err != nil {
			return result, ErrACPConfig
		}
		raw := values["acp-bridges"].(map[string]any)[input.BridgeID].(map[string]any)
		if rawOwner, present := raw["desktop-plugin-id"]; present {
			owner, ok = rawOwner.(string)
			if !ok || owner == "" {
				return result, ErrACPConfig
			}
		}
	}
	if owner != "" && owner != input.SourcePluginID {
		return result, ErrACPConflict
	}
	if remove {
		if exists && owner == "" {
			return result, ErrACPConflict
		}
		delete(entries, input.BridgeID)
		delete(bridges, input.BridgeID)
		result.Changed, result.Removed = exists, exists
	} else {
		next := CoderACPBridgeConfig{BaseURL: input.BaseURL, TimeoutMS: input.TimeoutMS, AuthToken: existing.AuthToken}
		if input.AuthToken != nil {
			next.AuthToken = *input.AuthToken
		}
		// Legacy Desktop entries may be adopted only when connection settings match
		// exactly. A colliding manual entry must never be silently overwritten.
		if exists && owner == "" && existing != next {
			return result, ErrACPConflict
		}
		result.Changed = !exists || existing != next || owner != input.SourcePluginID
		if fields == nil {
			fields = map[string]YAMLSourceValue{}
		}
		fields["base-url"] = YAMLSourceValue{Head: strconv.Quote(next.BaseURL)}
		fields["timeout-ms"] = YAMLSourceValue{Head: strconv.Itoa(next.TimeoutMS)}
		fields["desktop-plugin-id"] = YAMLSourceValue{Head: strconv.Quote(input.SourcePluginID)}
		if input.AuthToken != nil {
			fields["auth-token"] = YAMLSourceValue{Head: strconv.Quote(*input.AuthToken)}
		}
		body, err := renderACPMap(fields, 4)
		if err != nil {
			return ACPRegistrationResult{}, ErrACPConfig
		}
		entries[input.BridgeID] = YAMLSourceValue{Body: body, Indent: 2}
		bridges[input.BridgeID] = next
	}
	result.RestartRequired = !reflect.DeepEqual(bridges, s.active)
	if !result.Changed {
		return result, nil
	}
	// An empty block mapping has no space in its value span (the span
	// starts immediately after the colon). Keep a separator when making it inline.
	replacement := YAMLSourceValue{Head: " {}"}
	if len(entries) > 0 {
		body, err := renderACPMap(entries, 2)
		if err != nil {
			return ACPRegistrationResult{}, ErrACPConfig
		}
		replacement = YAMLSourceValue{Body: body}
	}
	var after string
	if _, exists := root["acp-bridges"]; exists {
		after, err = ReplaceYAMLSourceValues(source, root, map[string]YAMLSourceValue{"acp-bridges": replacement})
	} else {
		value, valueErr := (YAMLSourceValue{}).WithValue(replacement, "\n")
		err = valueErr
		after = strings.TrimRight(source, "\r\n") + "\nacp-bridges: " + value + "\n"
	}
	if err != nil {
		return ACPRegistrationResult{}, ErrACPConfig
	}
	// Validate the exact bytes through the runtime parser before publication.
	// In particular, never persist a token whose YAML interpretation changes it.
	parsed, err := LoadYAMLTreeBytes([]byte(after))
	updated, ok := parsed.(map[string]any)
	if err != nil || !ok {
		return ACPRegistrationResult{}, ErrACPConfig
	}
	actual, err := parseCoderACPBridges(updated["acp-bridges"], nil)
	if err != nil || !reflect.DeepEqual(actual, bridges) {
		return ACPRegistrationResult{}, ErrACPArguments
	}
	delete(values, "acp-bridges")
	delete(updated, "acp-bridges")
	if !reflect.DeepEqual(values, updated) {
		return ACPRegistrationResult{}, ErrACPConfig
	}
	if err := writeACPSettings(s.path, []byte(after)); err != nil {
		return ACPRegistrationResult{}, ErrACPWrite
	}
	return result, nil
}

func renderACPMap(values map[string]YAMLSourceValue, indent int) (string, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		rendered, err := (YAMLSourceValue{Indent: indent, Tail: values[key].Tail}).WithValue(values[key], "\n")
		if err != nil {
			return "", err
		}
		lines = append(lines, strings.Repeat(" ", indent)+key+": "+rendered)
	}
	return strings.Join(lines, "\n"), nil
}

func writeACPSettings(target string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".acp-settings-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceACPSettings(file.Name(), target)
}
