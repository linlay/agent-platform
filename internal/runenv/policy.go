package runenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type Mode string

const (
	ModeBind    Mode = "bind"
	ModeMutable Mode = "mutable"
)

type Target string

const (
	TargetHost      Target = "host"
	TargetContainer Target = "container"
)

type Approval string

const (
	ApprovalNone       Approval = "none"
	ApprovalEachChange Approval = "each-change"
)

var portableNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

var hardDeniedNames = map[string]struct{}{
	"AP_AGENT_CONFIG_HOME": {}, "AP_WORKSPACE_DIR": {}, "AP_CHAT_DIR": {}, "AP_ACCESS_TOKEN": {},
	"PATH": {}, "PATHEXT": {}, "COMSPEC": {}, "SYSTEMROOT": {}, "WINDIR": {},
	"ENV": {}, "BASH_ENV": {}, "ZDOTDIR": {}, "SHELLOPTS": {}, "CDPATH": {}, "GLOBIGNORE": {},
	"INPUTRC": {}, "HISTFILE": {}, "PS1": {}, "PS2": {}, "PS3": {}, "PS4": {}, "PROMPT": {}, "PROMPT_COMMAND": {},
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {}, "LD_AUDIT": {}, "LD_DEBUG": {},
	"DYLD_INSERT_LIBRARIES": {}, "DYLD_LIBRARY_PATH": {}, "DYLD_FRAMEWORK_PATH": {},
	"NODE_OPTIONS": {}, "PYTHONPATH": {}, "PYTHONHOME": {}, "RUBYOPT": {}, "PERL5OPT": {},
	"GIT_SSH_COMMAND": {}, "SSH_ASKPASS": {},
}

type KeyPolicy struct {
	Name           string
	Mode           Mode
	Secret         bool
	Pattern        string
	MaxBytes       int
	AllowEmpty     bool
	AllowMultiline bool
	Approval       Approval
	Targets        []Target
	matcher        *regexp.Regexp
}

type Policy struct {
	Keys map[string]KeyPolicy
}

func ParsePolicy(value any) (Policy, error) {
	if value == nil {
		return Policy{}, nil
	}
	root, ok := value.(map[string]any)
	if !ok {
		return Policy{}, fmt.Errorf("runtimeConfig.runEnv must be an object")
	}
	policy := Policy{Keys: make(map[string]KeyPolicy, len(root))}
	for rawName, raw := range root {
		name := strings.ToUpper(strings.TrimSpace(rawName))
		if err := ValidateName(name, nil); err != nil {
			return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q]: %w", rawName, err)
		}
		if rawName != name {
			return Policy{}, fmt.Errorf("runtimeConfig.runEnv key %q must use portable uppercase spelling %q", rawName, name)
		}
		values, ok := raw.(map[string]any)
		if !ok {
			return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q] must be an object", name)
		}
		for key := range values {
			switch key {
			case "mode", "secret", "pattern", "maxBytes", "allowEmpty", "allowMultiline", "approval", "targets":
			default:
				return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q] contains unknown field %q", name, key)
			}
		}
		for _, field := range []string{"secret", "allowEmpty", "allowMultiline"} {
			if value, exists := values[field]; exists {
				if _, ok := value.(bool); !ok {
					return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q].%s must be boolean", name, field)
				}
			}
		}
		if value, exists := values["maxBytes"]; exists {
			switch value.(type) {
			case int, int64:
			case float64:
				if value.(float64) != float64(int(value.(float64))) {
					return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q].maxBytes must be an integer", name)
				}
			default:
				return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q].maxBytes must be an integer", name)
			}
		}
		item := KeyPolicy{
			Name: name, Mode: Mode(strings.ToLower(strings.TrimSpace(text(values["mode"])))),
			Secret: boolean(values["secret"], true), Pattern: strings.TrimSpace(text(values["pattern"])),
			MaxBytes: integer(values["maxBytes"]), AllowEmpty: boolean(values["allowEmpty"], false),
			AllowMultiline: boolean(values["allowMultiline"], false),
			Approval:       Approval(strings.ToLower(strings.TrimSpace(text(values["approval"])))),
			Targets:        nil,
		}
		targets, targetsErr := parseTargets(values["targets"])
		if targetsErr != nil {
			return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q].targets: %w", name, targetsErr)
		}
		item.Targets = targets
		if item.Mode != ModeBind && item.Mode != ModeMutable {
			return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q].mode must be bind or mutable", name)
		}
		if item.Approval == "" {
			item.Approval = ApprovalNone
		}
		if item.Approval != ApprovalNone && item.Approval != ApprovalEachChange {
			return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q].approval must be none or each-change", name)
		}
		if item.MaxBytes < 0 {
			return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q].maxBytes must not be negative", name)
		}
		if len(item.Targets) == 0 {
			item.Targets = []Target{TargetHost}
		}
		if item.Pattern != "" {
			matcher, err := regexp.Compile("^(?:" + item.Pattern + ")$")
			if err != nil {
				return Policy{}, fmt.Errorf("runtimeConfig.runEnv[%q].pattern: %w", name, err)
			}
			item.matcher = matcher
		}
		policy.Keys[name] = item
	}
	return policy, nil
}

func ValidateName(name string, extraDenied []string) error {
	name = strings.TrimSpace(name)
	if !portableNamePattern.MatchString(name) {
		return fmt.Errorf("environment variable name must match %s", portableNamePattern.String())
	}
	upper := strings.ToUpper(name)
	if _, denied := hardDeniedNames[upper]; denied {
		return fmt.Errorf("environment variable %s is reserved or denied", name)
	}
	for _, item := range extraDenied {
		if strings.EqualFold(strings.TrimSpace(item), name) {
			return fmt.Errorf("environment variable %s is denied by platform policy", name)
		}
	}
	return nil
}

func (p Policy) Key(name string) (KeyPolicy, bool) {
	item, ok := p.Keys[strings.ToUpper(strings.TrimSpace(name))]
	return item, ok
}

func (p Policy) Empty() bool { return len(p.Keys) == 0 }

func (p Policy) Hash() string {
	names := make([]string, 0, len(p.Keys))
	for name := range p.Keys {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		item := p.Keys[name]
		items = append(items, map[string]any{
			"name": item.Name, "mode": item.Mode, "secret": item.Secret, "pattern": item.Pattern,
			"maxBytes": item.MaxBytes, "allowEmpty": item.AllowEmpty, "allowMultiline": item.AllowMultiline,
			"approval": item.Approval, "targets": item.Targets,
		})
	}
	raw, _ := json.Marshal(items)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (p KeyPolicy) AllowsTarget(target Target) bool {
	for _, candidate := range p.Targets {
		if candidate == target {
			return true
		}
	}
	return false
}

func (p KeyPolicy) ValidateValue(value string, defaultMaxBytes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("value must be valid UTF-8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("value must not contain NUL")
	}
	if value == "" && !p.AllowEmpty {
		return fmt.Errorf("value must not be empty")
	}
	if !p.AllowMultiline && strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("value must not contain CR or LF")
	}
	limit := p.MaxBytes
	if limit <= 0 || (defaultMaxBytes > 0 && defaultMaxBytes < limit) {
		limit = defaultMaxBytes
	}
	if limit > 0 && len([]byte(value)) > limit {
		return fmt.Errorf("value exceeds %d bytes", limit)
	}
	if p.matcher != nil && !p.matcher.MatchString(value) {
		return fmt.Errorf("value does not match the configured full-match pattern")
	}
	return nil
}

func HardDeniedNames() []string {
	out := make([]string, 0, len(hardDeniedNames))
	for name := range hardDeniedNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func text(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func boolean(value any, fallback bool) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}

func integer(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func parseTargets(value any) ([]Target, error) {
	var values []string
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("targets must contain strings")
			}
			values = append(values, value)
		}
	case []string:
		values = append(values, typed...)
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			trimmed = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
			if trimmed != "" {
				values = append(values, strings.Split(trimmed, ",")...)
			}
		} else {
			values = append(values, typed)
		}
	default:
		return nil, fmt.Errorf("targets must be an array of host/container values")
	}
	seen := map[Target]bool{}
	out := make([]Target, 0, len(values))
	for _, raw := range values {
		target := Target(strings.ToLower(strings.TrimSpace(raw)))
		if target != TargetHost && target != TargetContainer {
			return nil, fmt.Errorf("unsupported target %q", raw)
		}
		if !seen[target] {
			seen[target] = true
			out = append(out, target)
		}
	}
	return out, nil
}
