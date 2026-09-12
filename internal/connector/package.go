// Package connector owns installed connector packages. It has no dependency on
// Agent, MCP sessions, HTTP handlers or the model's tool loop.
package connector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/view"
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

type Manifest struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Type        string          `json:"type"`
	AuthMode    AuthMode        `json:"auth_mode"`
	AuthBrowser string          `json:"auth_browser,omitempty"`
	Description string          `json:"description,omitempty"`
	Icon        string          `json:"icon,omitempty"`
	TokenSchema json.RawMessage `json:"token_schema,omitempty"`
	OAuth       json.RawMessage `json:"oauth,omitempty"`
}

// Package is an immutable, secret-free description of an installed package.
type Package struct {
	Manifest
	Builtin    bool
	StateRoot  string
	Dir        string
	BinDir     string
	Skills     []Skill
	MCP        map[string]map[string]any
	CLI        map[string]any
	Views      map[string]view.Definition
	iconSHA256 string
}

type Skill struct {
	Name string
	Dir  string
}

func ValidID(id string) bool { return idPattern.MatchString(id) && id != "." && id != ".." }

func Load(root, id string) (Package, error) {
	return loadDefinition(root, id, "", nil)
}

// loadDefinition validates a candidate in memory, against the installed package.
// Neither watchers nor running Agents can observe an invalid intermediate file.
func loadDefinition(root, id, file string, content []byte) (Package, error) {
	if !ValidID(id) {
		return Package{}, fmt.Errorf("invalid connector id %q", id)
	}
	dir, err := filepath.Abs(filepath.Join(root, id))
	if err != nil {
		return Package{}, err
	}
	if err := validateTree(dir); err != nil {
		return Package{}, fmt.Errorf("connector %s: %w", id, err)
	}
	var pkg Package
	read := func(name string, target any) error {
		if name == file {
			return DecodeJSON(content, target)
		}
		return ReadJSON(filepath.Join(dir, name), target)
	}
	if err := read("connector.json", &pkg.Manifest); err != nil {
		return Package{}, fmt.Errorf("connector %s manifest: %w", id, err)
	}
	pkg.Manifest.normalizeAuth()
	if err := validateManifest(id, pkg.Manifest); err != nil {
		return Package{}, err
	}
	pkg.Dir = dir
	if pkg.Icon != "" {
		icon, err := pkg.ReadIcon()
		if err != nil {
			return Package{}, fmt.Errorf("connector %s icon: %w", id, err)
		}
		pkg.iconSHA256 = icon.SHA256
	}
	if info, err := os.Stat(filepath.Join(dir, "bin")); err == nil {
		if !info.IsDir() {
			return Package{}, fmt.Errorf("connector %s bin must be a directory", id)
		}
		pkg.BinDir = filepath.Join(dir, "bin")
		if libs, err := os.Stat(filepath.Join(pkg.BinDir, "libs")); err == nil && !libs.IsDir() {
			return Package{}, fmt.Errorf("connector %s bin/libs must be a directory", id)
		} else if err != nil && !os.IsNotExist(err) {
			return Package{}, err
		}
	} else if !os.IsNotExist(err) {
		return Package{}, err
	}
	var mcpConfig struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := read("mcp.json", &mcpConfig); err == nil {
		if len(mcpConfig.Servers) == 0 {
			return Package{}, fmt.Errorf("connector %s mcpServers must not be empty", id)
		}
		pkg.MCP = mcpConfig.Servers
		for key := range pkg.MCP {
			if !ValidID(key) {
				return Package{}, fmt.Errorf("connector %s invalid MCP component %q", id, key)
			}
		}
	} else if pkg.Type == "mcp" || !os.IsNotExist(err) {
		return Package{}, fmt.Errorf("connector %s mcp.json: %w", id, err)
	}
	pkg.normalizeLegacyIdentityAuth()
	if err := read("cli.json", &pkg.CLI); err != nil {
		if pkg.Type == "cli" || !os.IsNotExist(err) {
			return Package{}, fmt.Errorf("connector %s cli.json: %w", id, err)
		}
	}
	if pkg.Type == "cli" && pkg.CLI == nil {
		return Package{}, fmt.Errorf("connector %s cli.json must be an object", id)
	}
	var viewConfig view.Config
	if err := read("view.json", &viewConfig); err == nil {
		if err := view.ValidateDefinitions(dir, viewConfig.Views); err != nil {
			return Package{}, fmt.Errorf("connector %s view.json: %w", id, err)
		}
		pkg.Views = viewConfig.Views
	} else if pkg.Type == "view" || !os.IsNotExist(err) {
		return Package{}, fmt.Errorf("connector %s view.json: %w", id, err)
	}
	pkg.Skills, err = loadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		return Package{}, fmt.Errorf("connector %s skills: %w", id, err)
	}
	return pkg, nil
}

// ValidateManifest validates connector.json without executing package commands.
func ValidateManifest(id string, content []byte) error {
	var manifest Manifest
	if err := DecodeJSON(content, &manifest); err != nil {
		return err
	}
	manifest.normalizeAuth()
	return validateManifest(id, manifest)
}

func validateManifest(id string, pkg Manifest) error {
	if !ValidID(id) || pkg.ID != id || strings.TrimSpace(pkg.Name) == "" || !validVersion(pkg.Version) {
		return fmt.Errorf("connector %s requires matching id, name and SemVer version", id)
	}
	if pkg.Type != "mcp" && pkg.Type != "cli" && pkg.Type != "view" {
		return fmt.Errorf("connector %s type must be mcp, cli or view", id)
	}
	if pkg.Icon != "" && !validIconPath(pkg.Icon) {
		return fmt.Errorf("connector %s icon must be a package-relative SVG or PNG path under assets/", id)
	}
	if pkg.AuthBrowser != "" && pkg.AuthBrowser != "system" && pkg.AuthBrowser != "embedded" {
		return fmt.Errorf("connector %s auth_browser must be system or embedded", id)
	}
	switch pkg.AuthMode {
	case AuthDelegated, AuthOneID, AuthMCP:
	case "token":
		if _, err := TokenFields(pkg); err != nil {
			return fmt.Errorf("connector %s: %w", id, err)
		}
	case "oauth":
		if len(pkg.OAuth) == 0 {
			return fmt.Errorf("connector %s oauth is required", id)
		}
	default:
		return fmt.Errorf("connector %s has invalid auth_mode", id)
	}
	if pkg.AuthMode != AuthToken && len(pkg.TokenSchema) != 0 || (pkg.AuthMode != AuthOAuth && pkg.AuthMode != AuthMCP) && len(pkg.OAuth) != 0 {
		return fmt.Errorf("connector %s has authentication fields for another mode", id)
	}
	return nil
}

func validVersion(version string) bool {
	if !versionPattern.MatchString(version) {
		return false
	}
	base, metadata, hasMetadata := strings.Cut(version, "+")
	if hasMetadata {
		for _, part := range strings.Split(metadata, ".") {
			if part == "" {
				return false
			}
		}
	}
	_, prerelease, hasPrerelease := strings.Cut(base, "-")
	if hasPrerelease {
		for _, part := range strings.Split(prerelease, ".") {
			if part == "" {
				return false
			}
			numeric := strings.Trim(part, "0123456789") == ""
			if numeric && len(part) > 1 && part[0] == '0' {
				return false
			}
		}
	}
	return true
}

func (p Package) ServerKeys() []string {
	keys := make([]string, 0, len(p.MCP))
	for name := range p.MCP {
		keys = append(keys, ServerKey(p.ID, name))
	}
	sort.Strings(keys)
	return keys
}

func ServerKey(id, component string) string {
	if component == "main" {
		return id
	}
	return id + "." + component
}

func loadSkills(root string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var skills []Skill
	single := false
	for _, entry := range entries {
		if entry.Name() == "SKILL.md" {
			single = true
			continue
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		name, err := skillName(path)
		if err != nil {
			return nil, err
		}
		if name != entry.Name() {
			return nil, fmt.Errorf("skill directory %q must match frontmatter name %q", entry.Name(), name)
		}
		skills = append(skills, Skill{Name: name, Dir: filepath.Dir(path)})
	}
	if single {
		if len(skills) > 0 {
			return nil, fmt.Errorf("single and multiple skill layouts cannot be mixed")
		}
		name, err := skillName(filepath.Join(root, "SKILL.md"))
		if err != nil {
			return nil, err
		}
		skills = []Skill{{Name: name, Dir: root}}
	}
	return skills, nil
}

func skillName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return "", fmt.Errorf("%s requires skill frontmatter", path)
	}
	name, description, closed := "", "", false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	if !closed || !ValidID(name) || description == "" {
		return "", fmt.Errorf("%s requires valid name and description", path)
	}
	return name, nil
}

func validateTree(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("package root must be a real directory")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(canonical, target)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("symlink escapes connector: %s", path)
			}
		} else if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported connector file: %s", path)
		}
		return nil
	})
}

// ReadJSON rejects duplicate keys, trailing values and unknown struct fields.
func ReadJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return DecodeJSON(data, target)
}

func DecodeJSON(data []byte, target any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := checkValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("JSON must contain exactly one value")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func checkValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("JSON object key must be a string")
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON key %q", name)
			}
			seen[name] = true
			if err := checkValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := checkValue(d); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}

// AuthorizationBrowser is presentation policy, independent of authentication mode.
func (m Manifest) AuthorizationBrowser() string {
	if m.AuthBrowser == "embedded" {
		return "embedded"
	}
	return "system"
}
