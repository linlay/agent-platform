// Package runtimerecovery restores a stopped runtime into an unpublished Desktop
// candidate. Only the explicitly approved 0.5.30 format is supported; no upgrade
// adapters, schema adoption or runtime execution belong here.
package runtimerecovery

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agent-platform/internal/automation"
	"agent-platform/internal/chat"
	"agent-platform/internal/mailbox"
)

const SupportedVersion = "0.5.30"

type Options struct{ Source, Target, Action, SourceConfigDir string }
type Result struct {
	Source            string   `json:"source"`
	Contract          string   `json:"contract"`
	Version           string   `json:"version"`
	Scopes            []string `json:"scopes"`
	PausedAutomations int      `json:"pausedAutomations"`
	RequiredBytes     uint64   `json:"requiredBytes"`
}

// Release resources win at the first resource name. User resources absent from
// the release survive. Persistent stores replace release seeds as whole trees.
var resources = []string{"agents", "skills-center", "tools", "connectors-center", "registries"}
var data = []string{"chats", "memory", "kbases", "automations", "owner", "root", "pan", "ru-kbases", "ru-previews", ".state"}

func Run(options Options) (Result, error) {
	result := Result{Contract: "v1", Version: SupportedVersion, Scopes: []string{}}
	if options.Action != "check" && options.Action != "restore" {
		return result, fmt.Errorf("recovery action must be check or restore")
	}
	source, err := filepath.Abs(options.Source)
	if err != nil || strings.TrimSpace(options.Source) == "" {
		return result, fmt.Errorf("source runtime is required")
	}
	target, err := filepath.Abs(options.Target)
	if err != nil || strings.TrimSpace(options.Target) == "" {
		return result, fmt.Errorf("target candidate is required")
	}
	// macOS /var and /tmp are system aliases. Canonicalize ancestors while
	// refusing a symlink supplied as the actual source/candidate directory.
	for _, root := range []*string{&source, &target} {
		if err := validateRoot(*root); err != nil {
			return result, err
		}
		*root, err = filepath.EvalSymlinks(*root)
		if err != nil {
			return result, err
		}
	}
	if within(source, target) || within(target, source) {
		return result, fmt.Errorf("source and candidate must be separate trees")
	}
	for _, root := range []string{source, target} {
		if err := validateRoot(root); err != nil {
			return result, err
		}
		if err := inspectTree(filepath.Join(root, "VERSION")); err != nil {
			return result, err
		}
		version, err := os.ReadFile(filepath.Join(root, "VERSION"))
		if err != nil {
			return result, err
		}
		if strings.TrimPrefix(strings.TrimSpace(string(version)), "v") != SupportedVersion {
			return result, fmt.Errorf("unsupported recovery version at %s: only v%s -> v%s is supported", root, SupportedVersion, SupportedVersion)
		}
	}
	result.Source = source
	if err := validateLayout(options.SourceConfigDir, source); err != nil {
		return result, err
	}
	for _, scope := range append(append([]string{}, resources...), data...) {
		path := filepath.Join(source, scope)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return result, err
		}
		if err := inspectTree(path); err != nil {
			return result, err
		}
		if err := filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err == nil {
				result.RequiredBytes += uint64(info.Size())
			}
			return err
		}); err != nil {
			return result, err
		}
		result.Scopes = append(result.Scopes, scope)
	}
	// Validate paths before any database is opened, including candidate parents.
	for _, scope := range append(append([]string{}, resources...), data...) {
		if _, err := os.Lstat(filepath.Join(target, scope)); os.IsNotExist(err) {
			continue
		}
		if err := inspectTree(filepath.Join(target, scope)); err != nil {
			return result, err
		}
	}
	if err := chat.ValidateRecovery(filepath.Join(source, "chats")); err != nil {
		return result, err
	}
	if err := mailbox.ValidateRecovery(filepath.Join(source, "chats")); err != nil {
		return result, err
	}
	if err := automation.ValidateRecovery(filepath.Join(source, "automations")); err != nil {
		return result, err
	}
	free, err := availableBytes(target)
	if err != nil {
		return result, err
	}
	if free < result.RequiredBytes {
		return result, fmt.Errorf("insufficient recovery space: need %d bytes, available %d", result.RequiredBytes, free)
	}
	if options.Action == "check" {
		return result, nil
	}
	report := filepath.Join(target, ".platform-recovery.json")
	if encoded, err := os.ReadFile(report); err == nil {
		var previous Result
		if json.Unmarshal(encoded, &previous) != nil || previous.Source != source || previous.Contract != "v1" || previous.Version != SupportedVersion {
			return result, fmt.Errorf("recovery report does not match this operation")
		}
		if err := chat.ValidateRecovery(filepath.Join(target, "chats")); err != nil {
			return result, err
		}
		return previous, nil
	} else if !os.IsNotExist(err) {
		return result, err
	}
	pending := filepath.Join(target, ".platform-recovery-pending")
	marker, err := os.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, fmt.Errorf("incomplete candidate: recreate it from the release package before retrying: %w", err)
	}
	if err := marker.Sync(); err != nil {
		marker.Close()
		return result, err
	}
	if err := marker.Close(); err != nil {
		return result, err
	}
	for _, scope := range result.Scopes {
		from, to := filepath.Join(source, scope), filepath.Join(target, scope)
		if scope == "registries" {
			if err := copyMissingTree(from, to); err != nil {
				return result, err
			}
		} else if contains(resources, scope) {
			entries, err := os.ReadDir(from)
			if err != nil {
				return result, err
			}
			if err := os.MkdirAll(to, 0700); err != nil {
				return result, err
			}
			windowsNames := map[string]bool{}
			if runtime.GOOS == "windows" {
				releaseEntries, err := os.ReadDir(to)
				if err != nil {
					return result, err
				}
				for _, entry := range releaseEntries {
					windowsNames[strings.ToLower(entry.Name())] = true
				}
			}
			for _, entry := range entries {
				if runtime.GOOS == "windows" && windowsNames[strings.ToLower(entry.Name())] {
					continue
				}
				dest := filepath.Join(to, entry.Name())
				// macOS uses the actual volume's case-sensitive/insensitive rules.
				if _, err := os.Lstat(dest); err == nil {
					continue
				} else if !os.IsNotExist(err) {
					return result, err
				}
				if err := copyTree(filepath.Join(from, entry.Name()), dest); err != nil {
					return result, err
				}
			}
		} else {
			if err := os.RemoveAll(to); err != nil {
				return result, err
			}
			if err := copyTree(from, to); err != nil {
				return result, err
			}
		}
	}
	// Restore definitions paused. Users can re-enable them using existing UI;
	// neither recovery nor the first successful startup replays scheduled work.
	registry := automation.NewRegistry(filepath.Join(target, "automations"))
	definitions, err := registry.Load()
	if err != nil {
		return result, err
	}
	for _, definition := range definitions {
		if !definition.Enabled {
			continue
		}
		definition.Enabled = false
		if err := registry.Persist(definition); err != nil {
			return result, err
		}
		result.PausedAutomations++
	}
	if err := chat.ValidateRecovery(filepath.Join(target, "chats")); err != nil {
		return result, err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(target, ".platform-recovery.json"), append(encoded, '\n'), 0600); err != nil {
		return result, err
	}
	if err := os.Remove(pending); err != nil {
		return result, err
	}
	return result, nil
}

// Registry ownership is per relative file, rather than per first directory.
func copyMissingTree(source, target string) error {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return copyTree(source, target)
	}
	if err != nil {
		return err
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() || !sourceInfo.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyMissingTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func validateRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("recovery root must be a real directory: %s", root)
	}
	return nil
}
func inspectTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("unsupported recovery file type: %s", path)
		}
		return nil
	})
}
func copyTree(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("recovery refuses symbolic link: %s", source)
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, 0700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported recovery file: %s", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
