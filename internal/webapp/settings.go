package webapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"
)

type Configuration struct {
	Entry          Entry          `json:"entry"`
	Manifest       Manifest       `json:"manifest"`
	Policy         Policy         `json:"policy"`
	UserConfig     map[string]any `json:"userConfig"`
	ConfigRevision string         `json:"configRevision"`
}
type UserConfigRequest struct {
	ExpectedRevision string         `json:"expectedRevision"`
	Values           map[string]any `json:"values"`
}
type PolicyRequest struct {
	ExpectedRevision string           `json:"expectedRevision"`
	AgentKey         string           `json:"agentKey"`
	Skills           []string         `json:"skills"`
	Connectors       []ConnectorScope `json:"connectors"`
	Assistant        bool             `json:"assistant"`
	Artifacts        bool             `json:"artifacts"`
	ConnectPorts     []int            `json:"connectPorts"`
}

var agentKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var connectorIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
var skillIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

func (s *Service) readUserConfig(id string) (map[string]any, string, error) {
	file := filepath.Join(s.root, id, "user-config.json")
	var values map[string]any
	raw, err := readManaged(file, 64<<10)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > 64<<10 {
		return nil, "", fail("webapp_user_config_invalid", 400)
	}
	if err := strictJSON(raw, &values); err != nil {
		return nil, "", err
	}
	if values == nil {
		return nil, "", fail("webapp_user_config_invalid", 400)
	}
	digest := sha256.Sum256(raw)
	for key, value := range defaultUserConfig(s.records[id].Manifest) {
		if _, ok := values[key]; !ok {
			values[key] = value
		}
	}
	return values, hex.EncodeToString(digest[:]), nil
}
func (s *Service) Configuration(id string) (Configuration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutationError(); err != nil {
		return Configuration{}, err
	}
	r := s.records[id]
	if r == nil {
		return Configuration{}, fail("webapp_not_found", 404)
	}
	values, revision, err := s.readUserConfig(id)
	if err != nil {
		return Configuration{}, err
	}
	return Configuration{Entry: s.entry(r), Manifest: r.Manifest, Policy: s.policies[id], UserConfig: values, ConfigRevision: revision}, nil
}
func validateUserValues(m Manifest, values map[string]any) error {
	bad := func() error { return fail("webapp_user_config_invalid", 400) }
	fields := map[string]UserField{}
	if m.UserConfig != nil {
		for _, f := range m.UserConfig.Fields {
			fields[f.Name] = f
		}
	}
	if values == nil || unsafeJSON(values) {
		return bad()
	}
	raw, err := json.Marshal(values)
	if err != nil || len(raw) > 64<<10 {
		return bad()
	}
	for name, v := range values {
		f, ok := fields[name]
		if !ok {
			return bad()
		}
		switch f.Type {
		case "text", "textarea":
			text, ok := v.(string)
			if !ok || len(utf16.Encode([]rune(text))) > f.MaxLength {
				return bad()
			}
		case "number":
			n, ok := v.(float64)
			if !ok || f.Min != nil && n < *f.Min || f.Max != nil && n > *f.Max {
				return bad()
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return bad()
			}
		case "select":
			text, ok := v.(string)
			if !ok {
				return bad()
			}
			if f.Source != "" {
				if text != "" && !agentKeyPattern.MatchString(text) {
					return bad()
				}
			} else {
				found := false
				for _, o := range f.Options {
					found = found || o.Value == text
				}
				if !found {
					return bad()
				}
			}
		default:
			return bad()
		}
	}
	for name, f := range fields {
		v, ok := values[name]
		if f.Required && (!ok || v == nil || v == "") {
			return bad()
		}
	}
	return nil
}
func (s *Service) SaveUserConfig(id string, request UserConfigRequest) (Configuration, error) {
	s.mu.Lock()
	r := s.records[id]
	if err := s.mutationError(); err != nil {
		s.mu.Unlock()
		return Configuration{}, err
	}
	if r == nil {
		s.mu.Unlock()
		return Configuration{}, fail("webapp_not_found", 404)
	}
	if s.active[id] != nil {
		s.mu.Unlock()
		return Configuration{}, fail("webapp_configuration_requires_stop", 409)
	}
	_, revision, err := s.readUserConfig(id)
	if err != nil {
		s.mu.Unlock()
		return Configuration{}, err
	}
	if request.ExpectedRevision != revision {
		s.mu.Unlock()
		return Configuration{}, fail("webapp_revision_conflict", 409)
	}
	if err := validateUserValues(r.Manifest, request.Values); err != nil {
		s.mu.Unlock()
		return Configuration{}, err
	}
	err = writeJSON(filepath.Join(s.root, id, "user-config.json"), request.Values)
	s.mu.Unlock()
	if err != nil {
		return Configuration{}, err
	}
	return s.Configuration(id)
}
func (s *Service) SavePolicy(id string, request PolicyRequest) (Configuration, error) {
	s.mu.Lock()
	if err := s.mutationError(); err != nil {
		s.mu.Unlock()
		return Configuration{}, err
	}
	if s.records[id] == nil {
		s.mu.Unlock()
		return Configuration{}, fail("webapp_not_found", 404)
	}
	if s.active[id] != nil {
		s.mu.Unlock()
		return Configuration{}, fail("webapp_configuration_requires_stop", 409)
	}
	if s.policies[id].Revision != request.ExpectedRevision {
		s.mu.Unlock()
		return Configuration{}, fail("webapp_revision_conflict", 409)
	}
	if err := validatePolicy(Policy{AgentKey: request.AgentKey, Skills: request.Skills, Connectors: request.Connectors, ConnectPorts: request.ConnectPorts}); err != nil {
		s.mu.Unlock()
		return Configuration{}, err
	}
	p := Policy{SchemaVersion: 1, Revision: randomID(), AgentKey: strings.TrimSpace(request.AgentKey), Skills: append([]string{}, request.Skills...), Connectors: append([]ConnectorScope{}, request.Connectors...), Assistant: request.Assistant, Artifacts: request.Artifacts, ConnectPorts: append([]int{}, request.ConnectPorts...)}
	err := writeJSON(filepath.Join(s.root, id, "policy.json"), p)
	if err == nil {
		s.policies[id] = p
	}
	s.mu.Unlock()
	if err != nil {
		return Configuration{}, err
	}
	return s.Configuration(id)
}

func validatePolicy(p Policy) error {
	bad := p.AgentKey != "" && !agentKeyPattern.MatchString(p.AgentKey) || len(p.Skills) > 128 || len(p.Connectors) > 128 || len(p.ConnectPorts) > 128
	seen := map[string]bool{}
	for _, id := range p.Skills {
		if len(id) > 256 || !skillIDPattern.MatchString(id) || seen[id] {
			bad = true
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, c := range p.Connectors {
		key := c.ConnectorID + "/" + c.Adapter
		if !connectorIDPattern.MatchString(c.ConnectorID) || c.Adapter != "cli" && c.Adapter != "mcp" || seen[key] {
			bad = true
		}
		seen[key] = true
	}
	ports := map[int]bool{}
	for _, port := range p.ConnectPorts {
		if port < 1 || port > 65535 || ports[port] {
			bad = true
		}
		ports[port] = true
	}
	if bad {
		return fail("webapp_policy_invalid", 400)
	}

	return nil
}
