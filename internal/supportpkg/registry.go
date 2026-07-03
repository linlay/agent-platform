package supportpkg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	ManifestName = "manifest.json"
	PluginsDir   = "plugins"
)

type Target struct {
	OS   string
	Arch string
}

type Registry struct {
	root        string
	packages    []Package
	executables map[string]Executable
}

type Package struct {
	ID      string
	Version string
	Root    string
	Target  Target
}

type Executable struct {
	Name      string
	Path      string
	PluginID  string
	Version   string
	PluginDir string
}

type manifest struct {
	Kind        string            `json:"kind"`
	ID          string            `json:"id"`
	Version     string            `json:"version"`
	Platform    manifestPlatform  `json:"platform"`
	Executables map[string]string `json:"executables"`
}

type manifestPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

func DiscoverNearExecutable() (*Registry, string, []error) {
	executable, err := os.Executable()
	if err != nil {
		return NewRegistry(""), "", []error{fmt.Errorf("resolve executable path: %w", err)}
	}
	root := filepath.Join(filepath.Dir(executable), PluginsDir)
	registry, errs := LoadDir(root, Target{OS: runtime.GOOS, Arch: runtime.GOARCH})
	return registry, root, errs
}

func LoadDir(root string, target Target) (*Registry, []error) {
	root = strings.TrimSpace(root)
	registry := NewRegistry(root)
	if root == "" {
		return registry, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return registry, nil
		}
		return registry, []error{fmt.Errorf("read plugins dir %s: %w", root, err)}
	}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginDir := filepath.Join(root, entry.Name())
		pkg, execs, ok, err := loadPackage(pluginDir, target)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		registry.add(pkg, execs)
	}
	return registry, errs
}

func NewRegistry(root string) *Registry {
	return &Registry{
		root:        strings.TrimSpace(root),
		executables: map[string]Executable{},
	}
}

func (r *Registry) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

func (r *Registry) Packages() []Package {
	if r == nil {
		return nil
	}
	out := append([]Package(nil), r.packages...)
	return out
}

func (r *Registry) Executable(name string) (Executable, bool) {
	if r == nil {
		return Executable{}, false
	}
	executable, ok := r.executables[normalizeExecutableName(name)]
	return executable, ok
}

func (r *Registry) Executables() []Executable {
	if r == nil {
		return nil
	}
	out := make([]Executable, 0, len(r.executables))
	for _, executable := range r.executables {
		out = append(out, executable)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func (r *Registry) ExecutableCount() int {
	if r == nil {
		return 0
	}
	return len(r.executables)
}

func (r *Registry) add(pkg Package, execs []Executable) {
	r.packages = append(r.packages, pkg)
	sort.SliceStable(r.packages, func(i, j int) bool {
		return r.packages[i].ID < r.packages[j].ID
	})
	for _, executable := range execs {
		key := normalizeExecutableName(executable.Name)
		if key == "" {
			continue
		}
		if _, exists := r.executables[key]; exists {
			continue
		}
		r.executables[key] = executable
	}
}

func loadPackage(pluginDir string, target Target) (Package, []Executable, bool, error) {
	manifestPath := filepath.Join(pluginDir, ManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Package{}, nil, false, nil
		}
		return Package{}, nil, false, fmt.Errorf("read plugin manifest %s: %w", manifestPath, err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Package{}, nil, false, fmt.Errorf("parse plugin manifest %s: %w", manifestPath, err)
	}
	if !strings.EqualFold(strings.TrimSpace(m.Kind), "support-package") {
		return Package{}, nil, false, nil
	}
	id := strings.TrimSpace(m.ID)
	if id == "" {
		return Package{}, nil, false, fmt.Errorf("plugin manifest %s: id is required", manifestPath)
	}
	manifestTarget := Target{OS: strings.TrimSpace(m.Platform.OS), Arch: strings.TrimSpace(m.Platform.Arch)}
	if !targetMatches(manifestTarget, target) {
		return Package{}, nil, false, nil
	}
	if len(m.Executables) == 0 {
		return Package{}, nil, false, nil
	}
	pkg := Package{
		ID:      id,
		Version: strings.TrimSpace(m.Version),
		Root:    pluginDir,
		Target:  manifestTarget,
	}
	execs := make([]Executable, 0, len(m.Executables))
	names := make([]string, 0, len(m.Executables))
	for name := range m.Executables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		executableName := normalizeExecutableName(name)
		if executableName == "" {
			return Package{}, nil, false, fmt.Errorf("plugin manifest %s: executable name is required", manifestPath)
		}
		resolved, err := resolveExecutablePath(pluginDir, m.Executables[name])
		if err != nil {
			return Package{}, nil, false, nil
		}
		execs = append(execs, Executable{
			Name:      executableName,
			Path:      resolved,
			PluginID:  pkg.ID,
			Version:   pkg.Version,
			PluginDir: pluginDir,
		})
	}
	return pkg, execs, true, nil
}

func targetMatches(plugin Target, runtimeTarget Target) bool {
	return strings.EqualFold(strings.TrimSpace(plugin.OS), strings.TrimSpace(runtimeTarget.OS)) &&
		strings.EqualFold(strings.TrimSpace(plugin.Arch), strings.TrimSpace(runtimeTarget.Arch))
}

func resolveExecutablePath(pluginDir string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(pluginDir, path)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return abs, nil
}

func normalizeExecutableName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".exe")
	return name
}
