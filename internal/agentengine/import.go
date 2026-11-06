package agentengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/runtimefs"
)

type installation struct {
	dto.EngineInstallation
	resource dto.EngineResource
	dir      string
	node     string
	pin      *os.File
	retired  chan struct{}
}

func checkedFile(root, relative string) (string, error) {
	if !filepath.IsLocal(relative) || strings.ContainsAny(relative, "\\:\x00") {
		return "", Error("artifact_untrusted")
	}
	current := root
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == ".." || part == "." || part == "" {
			return "", Error("artifact_untrusted")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", Error("artifact_untrusted")
		}
	}
	return current, nil
}
func readContract(root, relative, name string, target any) error {
	file, err := checkedFile(root, relative)
	if err != nil {
		return err
	}
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return Error("artifact_untrusted")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return Decode(name, data, target)
}
func fingerprint(root string) (string, error) {
	var bytes int64
	entries := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries++
		bytes += info.Size()
		if entries > 100000 || info.Size() > 512<<20 || bytes > 2<<30 {
			return Error("artifact_untrusted")
		}
		if d.Type()&os.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return Error("artifact_untrusted")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sum, err := runtimefs.Digest(root)
	return fmt.Sprintf("sha256:%x", sum), err
}
func (s *Service) importEntry(entry dto.EngineInstallation) (installation, error) {
	result := installation{EngineInstallation: entry}
	if !strings.HasPrefix(entry.EngineID, entry.PluginID+"/") {
		return result, Error("artifact_untrusted")
	}
	root, err := checkedFile(s.authority.DeliveryRoot, entry.InstallationRef)
	if err != nil {
		return result, err
	}
	if err := runtimefs.PrivateOwned(root); err != nil {
		return result, Error("artifact_untrusted")
	}
	var record dto.TrustedDeliveryRecord
	if err := readContract(root, "record.json", "TrustedDeliveryRecord", &record); err != nil {
		return result, err
	}
	if err := runtimefs.PrivateOwned(filepath.Join(root, "record.json")); err != nil {
		return result, Error("artifact_untrusted")
	}
	if record.OwnerHostID != s.authority.HostID || record.InstallationRef != entry.InstallationRef || record.PluginID != entry.PluginID || record.PluginVersion != entry.PluginVersion || record.ArtifactDigest != entry.ArtifactDigest {
		return result, Error("artifact_untrusted")
	}
	if record.Platform.Os != runtime.GOOS || record.Platform.Arch != runtime.GOARCH {
		return result, Error("unsupported_deployment")
	}
	if record.Source.Kind == "local-import" && record.Source.AuthorizationRef != "local-import:"+entry.InstallationRef {
		return result, Error("artifact_untrusted")
	}
	if record.Source.Kind == "market" && !strings.HasSuffix(record.Source.AuthorizationRef, "#"+entry.PluginID+"@"+entry.PluginVersion) {
		return result, Error("artifact_untrusted")
	}
	source, err := checkedFile(root, "package")
	if err != nil {
		return result, err
	}
	before, err := fingerprint(source)
	if err != nil {
		return result, err
	}
	if before != entry.ArtifactDigest {
		return result, Error("artifact_digest_mismatch")
	}
	parent := filepath.Join(s.root, "ru-agent-engines", filepath.FromSlash(entry.EngineID))
	if err := realDirectories(s.root, parent); err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(parent, ".import-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	if err := runtimefs.CopyTree(source, stage); err != nil {
		return result, err
	}
	got, err := fingerprint(stage)
	if err != nil || got != before {
		return result, Error("artifact_digest_mismatch")
	}
	// Validate the private copy: nothing is executed or parsed again from the mutable delivery.
	var manifest dto.EnginePluginManifest
	if err := readContract(stage, "manifest.json", "EnginePluginManifest", &manifest); err != nil {
		return result, Error("manifest_unsupported")
	}
	if manifest.ID != entry.PluginID || manifest.Version != entry.PluginVersion || manifest.Desktop.BundleTopLevelDir != manifest.ID || manifest.Platform != record.Platform {
		return result, Error("manifest_unsupported")
	}
	seen := map[string]bool{}
	for _, engine := range manifest.Resources.AgentEngines {
		if seen[engine.ID] {
			return result, Error("manifest_unsupported")
		}
		seen[engine.ID] = true
		if entry.EngineID == manifest.ID+"/"+engine.ID {
			result.resource = engine
		}
	}
	if result.resource.ID == "" {
		return result, Error("manifest_unsupported")
	}
	node := filepath.Join(filepath.Dir(filepath.Dir(s.authority.DeliveryRoot)), "bin", "node")
	if runtime.GOOS == "windows" {
		node += ".exe"
	}
	if s.standalone {
		node = s.node
	}
	if err := validateDependencies(stage, node, result.resource); err != nil {
		return result, err
	}
	if needsNode(result.resource) {
		result.node = node
	}
	result.dir = filepath.Join(parent, strings.TrimPrefix(before, "sha256:"))
	if err := runtimefs.Publish(stage, result.dir); err != nil {
		return result, Error("artifact_digest_mismatch")
	}
	return result, nil
}
func realDirectories(root, target string) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || !filepath.IsLocal(rel) {
		return Error("artifact_untrusted")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Error("artifact_untrusted")
		}
	}
	return nil
}
func needsNode(engine dto.EngineResource) bool {
	if engine.Launch.Runtime == "node" {
		return true
	}
	for _, dep := range engine.Dependencies {
		if dep.Delivery == "host-managed" && dep.ID == "node" {
			return true
		}
	}
	return false
}

func validateExecutable(root, relative string) error {
	file, err := regularFile(root, relative)
	if err != nil {
		return Error("dependency_missing")
	}
	switch runtime.GOOS {
	case "windows":
		if !strings.EqualFold(filepath.Ext(file), ".exe") {
			return Error("dependency_missing")
		}
	case "darwin", "linux":
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm()&0111 == 0 {
			return Error("dependency_missing")
		}
	default:
		return Error("unsupported_deployment")
	}
	return nil
}

func nodeVersion(node string) (string, error) {
	if !filepath.IsAbs(node) {
		return "", Error("dependency_missing")
	}
	if _, err := regularFile(filepath.Dir(node), filepath.Base(node)); err != nil {
		return "", Error("dependency_missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-p", `JSON.stringify({version:process.versions.node,arch:process.arch})`)
	cmd.Env = []string{}
	cmd.Dir = filepath.Dir(node)
	output, err := cmd.Output()
	if err != nil {
		return "", Error("dependency_missing")
	}
	var actual struct{ Version, Arch string }
	if json.Unmarshal(output, &actual) != nil {
		return "", Error("dependency_missing")
	}
	if actual.Arch == "x64" {
		actual.Arch = "amd64"
	}
	if actual.Arch != runtime.GOARCH {
		return "", Error("dependency_missing")
	}
	return actual.Version, nil
}

func validateDependencies(root, node string, engine dto.EngineResource) error {
	for _, arg := range engine.Launch.Args {
		if strings.ContainsRune(arg, 0) {
			return Error("manifest_unsupported")
		}
	}
	if _, err := regularFile(root, engine.Launch.Entry); err != nil {
		return Error("dependency_missing")
	}
	if engine.Launch.Runtime == "executable" {
		if err := validateExecutable(root, engine.Launch.Entry); err != nil {
			return err
		}
	}
	version := ""
	if needsNode(engine) {
		var err error
		version, err = nodeVersion(node)
		if err != nil {
			return err
		}
	}
	hasNode := false
	seen := map[string]bool{}
	for _, dep := range engine.Dependencies {
		if seen[dep.ID] {
			return Error("manifest_unsupported")
		}
		seen[dep.ID] = true
		if dep.Delivery == "host-managed" {
			if dep.ID != "node" || dep.Version != version || dep.Path != nil || dep.Entry != nil {
				return Error("dependency_missing")
			}
			hasNode = true
		} else {
			if dep.Path == nil {
				return Error("dependency_missing")
			}
			file, err := checkedFile(root, *dep.Path)
			if err != nil {
				return Error("dependency_missing")
			}
			info, err := os.Stat(file)
			if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
				return Error("dependency_missing")
			}
			if dep.Entry != nil {
				if *dep.Entry != *dep.Path && !strings.HasPrefix(*dep.Entry, *dep.Path+"/") {
					return Error("dependency_missing")
				}
				if _, err := regularFile(root, *dep.Entry); err != nil {
					return Error("dependency_missing")
				}
			}
		}
	}
	if engine.Launch.Runtime == "node" && !hasNode {
		return Error("dependency_missing")
	}

	methods := map[string]bool{}
	if engine.Authentication != nil {
		for _, method := range engine.Authentication.AgentMethods {
			if methods[method] || strings.ContainsRune(method, 0) {
				return Error("manifest_unsupported")
			}
			methods[method] = true
		}
		for _, method := range engine.Authentication.TerminalMethods {
			if methods[method.MethodID] {
				return Error("manifest_unsupported")
			}
			methods[method.MethodID] = true
			for _, arg := range method.Args {
				if strings.ContainsRune(arg, 0) {
					return Error("manifest_unsupported")
				}
			}
		}
	}
	return nil
}
func regularFile(root, relative string) (string, error) {
	p, err := checkedFile(root, relative)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", Error("dependency_missing")
	}
	return p, nil
}
