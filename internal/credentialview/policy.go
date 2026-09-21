// Package credentialview produces observable copies of credential-bearing files.
// It never changes execution input, permissions, or the credential store itself.
package credentialview

import (
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/observability"
	"agent-platform/internal/pathutil"
)

const Hidden = "[REDACTED]"

type Source int

const (
	Ordinary Source = iota
	Provider
	ConnectorState
	Identity
)

type Policy struct{ Providers, Connectors, LegacyConnectors, IdentityFile string }

func FromConfig(cfg config.Config) Policy {
	providers := cfg.Providers.ExternalDir
	if providers == "" && cfg.Paths.RegistriesDir != "" {
		providers = filepath.Join(cfg.Paths.RegistriesDir, "providers")
	}
	identity := cfg.IdentityFile
	if identity == "" && cfg.Paths.EffectiveStateDir() != "" {
		identity, _ = config.ResolveIdentityFile(cfg.Paths.EffectiveStateDir(), "")
	}
	return Policy{providers, cfg.Paths.EffectiveConnectorStateDir(), cfg.Paths.LegacyConnectorStateDir, identity}
}

// Windows uses case-insensitive drive/UNC paths. Native paths additionally
// resolve symlinks (including existing parents of not-yet-created files).
func pathKey(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	windows := runtime.GOOS == "windows" || strings.HasPrefix(value, `\\`) || (len(value) > 2 && value[1] == ':' && (value[2] == '\\' || value[2] == '/'))
	if windows && runtime.GOOS != "windows" {
		return strings.ToLower(path.Clean(strings.ReplaceAll(value, `\`, "/")))
	}
	if c, err := pathutil.Canonicalize(value); err == nil {
		return c.Key
	}
	value = filepath.ToSlash(filepath.Clean(value))
	if windows || runtime.GOOS == "darwin" {
		return strings.ToLower(value)
	}
	return value
}
func inside(file, root string) bool {
	return root != "" && (file == root || strings.HasPrefix(file, strings.TrimRight(root, "/")+"/"))
}
func (p Policy) Source(file string) Source {
	if strings.TrimSpace(file) == "" {
		return Ordinary
	}
	key := pathKey(file)
	name := strings.ToLower(path.Base(strings.ReplaceAll(file, `\`, "/")))
	if (p.IdentityFile != "" && key == pathKey(p.IdentityFile)) || name == "sso-access-token" || name == "sso-access-token.txt" {
		return Identity
	}
	if inside(key, pathKey(p.Providers)) {
		return Provider
	}
	if inside(key, pathKey(p.Connectors)) || inside(key, pathKey(p.LegacyConnectors)) {
		return ConnectorState
	}
	return Ordinary
}
func secretKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
	switch key {
	case "apikey", "xapikey", "accesstoken", "refreshtoken", "idtoken", "token", "secret", "clientsecret", "password", "passwd", "authorization", "proxyauthorization", "cookie", "setcookie", "credentials", "privatekey", "headers":
		return true
	}
	return false
}
func maskJSON(value any, all bool) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			if secretKey(k) {
				out[k] = Hidden
			} else {
				out[k] = maskJSON(item, all)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = maskJSON(item, all)
		}
		return out
	case string:
		if all && v != "" {
			return Hidden
		}
		return observability.SanitizeLog(v)
	default:
		if all && value != nil {
			return Hidden
		}
		return value
	}
}

var yamlField = regexp.MustCompile(`^(\s*)(["']?)([A-Za-z0-9_-]+)["']?\s*:`)

func (p Policy) Text(file, content string, partial bool) string {
	source := p.Source(file)
	if source == Ordinary || content == "" || content == Hidden {
		return content
	}
	if source == Identity || partial {
		return Hidden
	}
	var value any
	if json.Unmarshal([]byte(content), &value) == nil {
		name := strings.ToLower(filepath.Base(file))
		all := source == ConnectorState && (name == "credentials.json" || name == "pending-credentials.json")
		out, err := json.MarshalIndent(maskJSON(value, all), "", "  ")
		if err == nil {
			return string(out)
		}
		return Hidden
	}
	// Connector credential state is JSON. Opaque CLI state and incomplete JSON
	// cannot be field-filtered reliably, so do not publish its body.
	if source == ConnectorState {
		return Hidden
	}
	lines := strings.Split(content, "\n")
	hideIndent := -1
	for i, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if hideIndent >= 0 {
			if strings.TrimSpace(line) == "" || indent > hideIndent {
				lines[i] = ""
				continue
			}
			hideIndent = -1
		}
		match := yamlField.FindStringSubmatch(line)
		if match != nil && secretKey(match[3]) {
			lines[i] = match[1] + match[2] + match[3] + match[2] + ": \"" + Hidden + "\""
			hideIndent = indent
		} else {
			lines[i] = observability.SanitizeLog(line)
		}
	}
	return strings.Join(lines, "\n")
}

func IsFileMutation(tool string) bool { return tool == "file_write" || tool == "file_edit" }

// Arguments operates on a copy for display/history, never execution arguments.
func (p Policy) Arguments(tool, raw string, sessions ...contracts.QuerySession) string {
	if !IsFileMutation(tool) {
		return raw
	}
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return `{"redacted":true}`
	}
	file, _ := args["file_path"].(string)
	if len(sessions) > 0 {
		if resolved, err := accesspolicy.ResolveSessionPath(sessions[0], file); err == nil {
			file = resolved
		}
	}
	if p.Source(file) == Ordinary {
		return raw
	}
	for _, key := range []string{"content", "old_string", "new_string"} {
		if value, ok := args[key].(string); ok {
			args[key] = p.Text(file, value, tool == "file_edit")
		}
	}
	out, err := json.Marshal(args)
	if err != nil {
		return `{"redacted":true}`
	}
	return string(out)
}

// Grep retains file names/counts; content from credential sources is hidden
// because a matching line or context block is not a complete structured file.
func (p Policy) Grep(root, text string) string {
	if p.Source(root) != Ordinary {
		return Hidden
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		for j, c := range line {
			if c != ':' && c != '-' {
				continue
			}
			candidate := line[:j]
			if p.Source(candidate) != Ordinary {
				lines[i] = candidate + ":" + Hidden
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}
