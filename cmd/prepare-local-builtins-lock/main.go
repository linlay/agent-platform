// Command prepare-local-builtins-lock derives a throwaway lock for local
// builds. The canonical lock remains the release contract; this command only
// records the checksums of artifacts built into an isolated local collection.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"agent-platform/internal/builtins"
)

func main() {
	input := flag.String("input", "scripts/release-assets/builtins.lock.json", "canonical builtins lock")
	output := flag.String("output", "", "derived local lock output path")
	collectionRoot := flag.String("builtins-root", "", "absolute local builtin collection root")
	componentTargets := flag.String("print-component-targets", "", "print requested targets declared for one component in the canonical lock")
	var targets targetList
	flag.Var(&targets, "target", "target to refresh (repeatable, os/arch)")
	flag.Parse()

	if strings.TrimSpace(*componentTargets) != "" {
		resolved, err := declaredComponentTargets(*input, *componentTargets, targets)
		if err != nil {
			fmt.Fprintf(os.Stderr, "prepare local builtins lock: %v\n", err)
			os.Exit(1)
		}
		for _, target := range resolved {
			fmt.Println(target)
		}
		return
	}

	if err := run(*input, *output, *collectionRoot, targets); err != nil {
		fmt.Fprintf(os.Stderr, "prepare local builtins lock: %v\n", err)
		os.Exit(1)
	}
}

type targetList []string

func (targets *targetList) String() string { return strings.Join(*targets, ",") }

func (targets *targetList) Set(value string) error {
	*targets = append(*targets, value)
	return nil
}

func run(input, output, collectionRoot string, requestedTargets []string) error {
	if strings.TrimSpace(output) == "" {
		return errors.New("--output is required")
	}
	if !filepath.IsAbs(collectionRoot) {
		return errors.New("--builtins-root must be absolute")
	}
	if len(requestedTargets) == 0 {
		return errors.New("at least one --target is required")
	}
	input, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	collectionRoot = filepath.Clean(collectionRoot)
	lock, err := builtins.LoadLock(input)
	if err != nil {
		return err
	}

	// A local lock describes the exact isolated checkouts used for this build.
	// Keep the canonical lock untouched, but do not carry its release commit
	// into a cache manifest when the local source checkout is different.
	for index := range lock.Components {
		component := &lock.Components[index]
		if component.Kind != "archive" && component.Kind != "archive-tree" {
			continue
		}
		commit, ok, err := localComponentCommit(filepath.Join(collectionRoot, component.Repository))
		if err != nil {
			return fmt.Errorf("%s local commit: %w", component.Name, err)
		}
		if ok {
			component.Commit = commit
		}
	}

	for _, value := range requestedTargets {
		goos, goarch, err := parseTarget(value)
		if err != nil {
			return err
		}
		key := goos + "-" + goarch
		for index := range lock.Components {
			component := &lock.Components[index]
			target, ok := component.Targets[key]
			if component.Name == "kbase-lance-engine" {
				version, err := localComponentVersion(filepath.Join(collectionRoot, component.Repository), component.Version)
				if err != nil {
					return fmt.Errorf("%s local version: %w", component.Name, err)
				}
				component.Version = version
				target, err = localTargetTemplate(*component, goos, goarch)
				if err != nil {
					return err
				}
				component.Targets[key] = target
			} else if !ok {
				if !component.Required {
					continue
				}
				target, err = localTargetTemplate(*component, goos, goarch)
				if err != nil {
					return err
				}
				component.Targets[key] = target
			}
			artifact, err := joinWithin(filepath.Join(collectionRoot, component.Repository), target.Path)
			if err != nil {
				return fmt.Errorf("%s %s: %w", component.Name, key, err)
			}
			hash, err := fileSHA256(artifact)
			if err != nil {
				return fmt.Errorf("%s %s: %w", component.Name, key, err)
			}
			target.SHA256 = hash
			component.Targets[key] = target
		}
	}

	payload, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomic(output, payload, 0o644)
}

func localComponentCommit(repositoryRoot string) (string, bool, error) {
	command := exec.Command("git", "-c", "safe.directory="+filepath.ToSlash(repositoryRoot), "-C", repositoryRoot, "rev-parse", "--verify", "--quiet", "HEAD")
	payload, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return "", false, nil
		}
		// Some vendored collections intentionally contain no Git metadata. In
		// that case there is no checkout fact to record, so retain the canonical
		// provenance instead of inventing one.
		if _, statErr := os.Stat(filepath.Join(repositoryRoot, ".git")); errors.Is(statErr, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	commit := strings.TrimSpace(string(payload))
	if len(commit) != 40 {
		return "", false, fmt.Errorf("git rev-parse returned invalid commit %q", commit)
	}
	return commit, true, nil
}

func parseTarget(value string) (string, string, error) {
	goos, goarch, ok := strings.Cut(strings.TrimSpace(value), "/")
	if !ok || goos == "" || goarch == "" {
		return "", "", fmt.Errorf("target %q must be os/arch", value)
	}
	switch goos + "/" + goarch {
	case "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64":
		return goos, goarch, nil
	default:
		return "", "", fmt.Errorf("unsupported target %q", value)
	}
}

// declaredComponentTargets returns the requested targets that the canonical
// lock declares for componentName. Optional components can intentionally omit
// platform artifacts, so callers use this before invoking a local builder.
func declaredComponentTargets(input, componentName string, requestedTargets []string) ([]string, error) {
	componentName = strings.TrimSpace(componentName)
	if componentName == "" {
		return nil, errors.New("component name is required")
	}
	if len(requestedTargets) == 0 {
		return nil, errors.New("at least one --target is required")
	}
	lock, err := builtins.LoadLock(input)
	if err != nil {
		return nil, err
	}
	component, err := builtins.FindComponent(lock, componentName)
	if err != nil {
		return nil, err
	}

	resolved := make([]string, 0, len(requestedTargets))
	seen := make(map[string]struct{}, len(requestedTargets))
	for _, value := range requestedTargets {
		goos, goarch, err := parseTarget(value)
		if err != nil {
			return nil, err
		}
		key := goos + "-" + goarch
		if _, ok := component.Targets[key]; !ok {
			continue
		}
		target := goos + "/" + goarch
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		resolved = append(resolved, target)
	}
	return resolved, nil
}

func localTargetTemplate(component builtins.Component, goos, goarch string) (builtins.Target, error) {
	if component.Name != "kbase-lance-engine" {
		return builtins.Target{}, fmt.Errorf("required builtin %s has no target %s-%s", component.Name, goos, goarch)
	}
	var metadata *builtins.TargetMetadata
	for _, existing := range component.Targets {
		if existing.Metadata != nil {
			copy := *existing.Metadata
			metadata = &copy
			break
		}
	}
	if metadata == nil {
		return builtins.Target{}, errors.New("kbase-lance-engine lock has no metadata template")
	}
	binary := "kbase-lance-engine"
	format := "tar.gz"
	if goos == "windows" {
		binary += ".exe"
		format = "zip"
	}
	version := "v" + strings.TrimPrefix(strings.TrimSpace(component.Version), "v")
	return builtins.Target{
		Path:     fmt.Sprintf("dist/%s/kbase-lance-engine_%s_%s_%s.%s", version, version, goos, goarch, format),
		Format:   format,
		Entry:    binary,
		Output:   binary,
		Metadata: metadata,
	}, nil
}

func localComponentVersion(repositoryRoot string, fallback string) (string, error) {
	path := filepath.Join(repositoryRoot, "VERSION")
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return strings.TrimSpace(fallback), nil
	}
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(payload))
	if version == "" {
		return "", errors.New("VERSION is empty")
	}
	return version, nil
}

func joinWithin(root, child string) (string, error) {
	path := filepath.Join(root, child)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path %q escapes %s", child, root)
	}
	return path, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeFileAtomic(path string, payload []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".lock.*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
