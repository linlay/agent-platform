package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"agent-platform/internal/builtins"
)

type rolloutUpdate struct {
	ComponentIndex int
	Name           string
	Mode           string
	From           string
	To             string
	Commit         string
	Component      builtins.Component
	ArtifactSource string
	ArtifactTarget string
	SHA256         string
}

type rolloutCandidate struct {
	Lock      builtins.Lock
	Original  []byte
	Updates   []rolloutUpdate
	Notices   []string
	LockPath  string
	TargetKey string
}

// offerUpdate applies the single-lock rollout state machine after local cache
// activation. Only hostTarget may change, regardless of how many targets were
// cross-built into the temporary collection.
func offerUpdate(lockPath, collectionRoot, durableRoot, hostTarget string, input io.Reader, output io.Writer, interactive bool) error {
	candidate, err := prepareRolloutCandidate(lockPath, collectionRoot, durableRoot, hostTarget)
	if err != nil {
		return err
	}
	for _, notice := range candidate.Notices {
		fmt.Fprintf(output, "[builtins-sync] %s\n", notice)
	}

	followers := make([]rolloutUpdate, 0, len(candidate.Updates))
	leaders := make([]rolloutUpdate, 0, len(candidate.Updates))
	for _, update := range candidate.Updates {
		if update.Mode == "leader" {
			leaders = append(leaders, update)
		} else {
			followers = append(followers, update)
		}
	}
	for _, update := range followers {
		fmt.Fprintf(output, "[builtins-sync] follower %s %s: target %s will automatically follow %s (commit %s)\n", update.Name, candidate.TargetKey, update.From, update.To, displayCommit(update.Commit))
	}

	selected := append([]rolloutUpdate(nil), followers...)
	if len(leaders) > 0 {
		fmt.Fprintln(output, "[builtins-sync] verified newer local builtin versions for this native host:")
		for _, update := range leaders {
			fmt.Fprintf(output, "  %s: %s -> %s (target %s, commit %s)\n", update.Name, update.From, update.To, candidate.TargetKey, displayCommit(update.Commit))
		}
		if !interactive {
			fmt.Fprintln(output, "[builtins-sync] non-interactive input; newer leader versions were not accepted")
		} else {
			fmt.Fprint(output, "Update scripts/release-assets/builtins.lock.json? Type yes to confirm: ")
			answer, readErr := bufio.NewReader(input).ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return fmt.Errorf("read canonical lock confirmation: %w", readErr)
			}
			if strings.TrimSpace(answer) == "yes" {
				selected = append(selected, leaders...)
			} else {
				fmt.Fprintln(output, "[builtins-sync] newer leader versions were not accepted")
			}
		}
	}
	if len(selected) == 0 {
		return nil
	}

	current, err := os.ReadFile(candidate.LockPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, candidate.Original) {
		return errors.New("canonical builtins lock changed while the update was being prepared")
	}
	for _, update := range selected {
		if err := persistArtifactAtomic(update.ArtifactSource, update.ArtifactTarget, update.SHA256); err != nil {
			return fmt.Errorf("persist %s %s artifact: %w", update.Name, candidate.TargetKey, err)
		}
	}
	current, err = os.ReadFile(candidate.LockPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, candidate.Original) {
		return errors.New("canonical builtins lock changed while artifacts were being persisted")
	}

	for _, update := range selected {
		candidate.Lock.Components[update.ComponentIndex] = update.Component
	}
	candidate.Lock.SchemaVersion = 2
	payload, err := json.MarshalIndent(candidate.Lock, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := writeFileAtomic(candidate.LockPath, payload, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(output, "[builtins-sync] updated canonical target %s: %s\n", candidate.TargetKey, candidate.LockPath)
	return nil
}

func prepareRolloutCandidate(lockPath, collectionRoot, durableRoot, hostTarget string) (rolloutCandidate, error) {
	if !filepath.IsAbs(collectionRoot) {
		return rolloutCandidate{}, errors.New("--builtins-root must be absolute")
	}
	if !filepath.IsAbs(durableRoot) {
		return rolloutCandidate{}, errors.New("--durable-builtins-root must be absolute")
	}
	goos, goarch, err := parseTarget(hostTarget)
	if err != nil {
		return rolloutCandidate{}, fmt.Errorf("--host-target: %w", err)
	}
	targetKey := goos + "-" + goarch
	absLockPath, err := filepath.Abs(lockPath)
	if err != nil {
		return rolloutCandidate{}, err
	}
	original, err := os.ReadFile(absLockPath)
	if err != nil {
		return rolloutCandidate{}, err
	}
	lock, err := builtins.LoadLock(absLockPath)
	if err != nil {
		return rolloutCandidate{}, err
	}
	lock.SchemaVersion = 2
	candidate := rolloutCandidate{Lock: lock, Original: original, LockPath: absLockPath, TargetKey: targetKey}

	for index := range candidate.Lock.Components {
		canonical := candidate.Lock.Components[index]
		if !isLocallyVersionedComponent(canonical.Name) {
			continue
		}
		repositoryRoot, err := joinWithin(collectionRoot, canonical.Repository)
		if err != nil {
			return rolloutCandidate{}, fmt.Errorf("%s repository: %w", canonical.Name, err)
		}
		durableRepositoryRoot, err := joinWithin(durableRoot, canonical.Repository)
		if err != nil {
			return rolloutCandidate{}, fmt.Errorf("%s durable repository: %w", canonical.Name, err)
		}
		localVersion, err := localComponentVersion(repositoryRoot, canonical.Version)
		if err != nil {
			return rolloutCandidate{}, fmt.Errorf("%s local version: %w", canonical.Name, err)
		}
		versionComparison, err := compareSemanticVersions(localVersion, canonical.Version)
		if err != nil {
			candidate.Notices = append(candidate.Notices, fmt.Sprintf("cannot compare %s versions %q and %q: %v", canonical.Name, canonical.Version, localVersion, err))
			continue
		}
		if versionComparison < 0 {
			candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s is behind target release %s; only the local cache was updated", canonical.Name, localVersion, canonical.Version))
			continue
		}

		commit, hasCommit, err := localComponentCommit(repositoryRoot)
		if err != nil {
			return rolloutCandidate{}, fmt.Errorf("%s local commit: %w", canonical.Name, err)
		}
		clean := true
		if hasCommit {
			clean, err = localComponentClean(repositoryRoot)
			if err != nil {
				return rolloutCandidate{}, fmt.Errorf("%s local status: %w", canonical.Name, err)
			}
		}

		currentTarget, targetExists := canonical.Targets[targetKey]
		if !targetExists && !canonical.Required {
			continue
		}
		mode := "follower"
		promoted := cloneComponent(canonical)
		if versionComparison > 0 {
			mode = "leader"
			if !clean {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s is newer than %s, but its repository has uncommitted changes; leader update was not offered", canonical.Name, localVersion, canonical.Version))
				continue
			}
			if strings.TrimSpace(canonical.Commit) != "" && !hasCommit {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s is newer than %s, but no exact Git commit is available; leader update was not offered", canonical.Name, localVersion, canonical.Version))
				continue
			}
			promoted.Version = localVersion
			if hasCommit {
				promoted.Commit = commit
			}
			if promoted.Name == "poppler-pdftotext" {
				updatedSource, ok := updateVersionInSource(promoted.Source, canonical.Version, localVersion)
				if !ok {
					candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s is newer than %s, but source %q cannot be updated safely; leader update was not offered", canonical.Name, localVersion, canonical.Version, canonical.Source))
					continue
				}
				promoted.Source = updatedSource
			}
			if targetExists {
				targetComparison, compareErr := compareSemanticVersions(currentTarget.Version, localVersion)
				if compareErr != nil {
					candidate.Notices = append(candidate.Notices, fmt.Sprintf("%s target %s has invalid actual version %q: %v", canonical.Name, targetKey, currentTarget.Version, compareErr))
					continue
				}
				if targetComparison >= 0 {
					candidate.Notices = append(candidate.Notices, fmt.Sprintf("%s target %s actual version %s is not behind leader version %s; refusing an ambiguous overwrite", canonical.Name, targetKey, currentTarget.Version, localVersion))
					continue
				}
			}
		} else {
			if strings.TrimSpace(canonical.Commit) != "" {
				if !hasCommit || commit != canonical.Commit {
					candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s checkout does not match target commit %s; target %s was not updated", canonical.Name, displayCommit(canonical.Commit), targetKey))
					continue
				}
			} else if hasCommit && (!targetExists || targetReleaseBehind(currentTarget, canonical)) {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("target release %s for %s has no commit to follow; target %s was not updated", canonical.Version, canonical.Name, targetKey))
				continue
			}
			if !clean {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s checkout has uncommitted changes; follower target %s was not updated", canonical.Name, targetKey))
				continue
			}
			if targetExists {
				targetComparison, compareErr := compareSemanticVersions(currentTarget.Version, canonical.Version)
				if compareErr != nil {
					candidate.Notices = append(candidate.Notices, fmt.Sprintf("%s target %s has invalid actual version %q: %v", canonical.Name, targetKey, currentTarget.Version, compareErr))
					continue
				}
				if targetComparison > 0 {
					candidate.Notices = append(candidate.Notices, fmt.Sprintf("%s target %s actual version %s is newer than component target %s; refusing an ambiguous overwrite", canonical.Name, targetKey, currentTarget.Version, canonical.Version))
					continue
				}
				if targetComparison == 0 && targetReleaseMatches(currentTarget, canonical) {
					mode = "verify"
				} else if targetComparison == 0 {
					candidate.Notices = append(candidate.Notices, fmt.Sprintf("%s target %s has the same version %s but different release metadata; immutable version conflict", canonical.Name, targetKey, canonical.Version))
					continue
				}
			}
		}

		newTarget, err := localTargetTemplate(promoted, currentTarget, targetExists, goos, goarch)
		if err != nil {
			return rolloutCandidate{}, err
		}
		newTarget.Version = promoted.Version
		newTarget.Source = promoted.Source
		newTarget.Commit = promoted.Commit
		artifactSource, err := joinWithin(repositoryRoot, newTarget.Path)
		if err != nil {
			return rolloutCandidate{}, fmt.Errorf("%s %s artifact: %w", canonical.Name, targetKey, err)
		}
		hash, err := fileSHA256(artifactSource)
		if err != nil {
			candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s %s archive is unavailable: %v", canonical.Name, targetKey, err))
			continue
		}
		newTarget.SHA256 = hash
		if err := validatePromotedArtifact(collectionRoot, promoted, targetKey, newTarget); err != nil {
			candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s %s archive failed validation: %v", canonical.Name, targetKey, err))
			continue
		}
		if mode == "verify" {
			if currentTarget.Path != newTarget.Path || !strings.EqualFold(currentTarget.SHA256, newTarget.SHA256) {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("%s target %s version %s rebuilt to a different path or SHA; immutable version conflict", canonical.Name, targetKey, canonical.Version))
			}
			continue
		}
		promoted.Targets[targetKey] = newTarget
		artifactTarget, err := joinWithin(durableRepositoryRoot, newTarget.Path)
		if err != nil {
			return rolloutCandidate{}, fmt.Errorf("%s %s durable artifact: %w", canonical.Name, targetKey, err)
		}
		candidate.Updates = append(candidate.Updates, rolloutUpdate{
			ComponentIndex: index,
			Name:           canonical.Name,
			Mode:           mode,
			From:           currentTarget.Version,
			To:             promoted.Version,
			Commit:         promoted.Commit,
			Component:      promoted,
			ArtifactSource: artifactSource,
			ArtifactTarget: artifactTarget,
			SHA256:         hash,
		})
	}
	return candidate, nil
}

func targetReleaseBehind(target builtins.Target, component builtins.Component) bool {
	comparison, err := compareSemanticVersions(target.Version, component.Version)
	return err == nil && comparison < 0
}

func targetReleaseMatches(target builtins.Target, component builtins.Component) bool {
	return target.Version == component.Version && target.Source == component.Source && target.Commit == component.Commit
}

func cloneComponent(component builtins.Component) builtins.Component {
	component.Targets = cloneTargets(component.Targets)
	component.Licenses = append([]string(nil), component.Licenses...)
	return component
}

func cloneTargets(source map[string]builtins.Target) map[string]builtins.Target {
	cloned := make(map[string]builtins.Target, len(source))
	for key, target := range source {
		if target.Tree != nil {
			tree := *target.Tree
			tree.Outputs = append([]builtins.TreeOutput(nil), target.Tree.Outputs...)
			target.Tree = &tree
		}
		if target.Metadata != nil {
			metadata := *target.Metadata
			target.Metadata = &metadata
		}
		cloned[key] = target
	}
	return cloned
}

func localComponentClean(repositoryRoot string) (bool, error) {
	command := exec.Command("git", "-c", "safe.directory="+filepath.ToSlash(repositoryRoot), "-C", repositoryRoot, "status", "--porcelain", "--untracked-files=normal")
	payload, err := command.Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(payload)) == "", nil
}

func validatePromotedArtifact(collectionRoot string, component builtins.Component, targetKey string, target builtins.Target) error {
	repositoryRoot, err := joinWithin(collectionRoot, component.Repository)
	if err != nil {
		return err
	}
	artifact, err := joinWithin(repositoryRoot, target.Path)
	if err != nil {
		return err
	}
	if component.Kind == "archive-tree" {
		root, err := os.MkdirTemp("", "builtin-promotion-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(root)
		component.Required = true
		component.Targets = map[string]builtins.Target{targetKey: target}
		lock := builtins.Lock{SchemaVersion: 2, DefaultRoot: ".", Components: []builtins.Component{component}}
		payload, err := json.Marshal(lock)
		if err != nil {
			return err
		}
		lockFile := filepath.Join(root, "lock.json")
		if err := os.WriteFile(lockFile, payload, 0o644); err != nil {
			return err
		}
		goos, goarch, _ := strings.Cut(targetKey, "-")
		_, err = builtins.Stage(builtins.StageOptions{
			RepoRoot: root, LockPath: lockFile, BuiltinsRoot: collectionRoot, OutputDir: filepath.Join(root, "stage"), GOOS: goos, GOARCH: goarch,
		})
		return err
	}
	if _, err := builtins.ReadTargetPayload(artifact, component.Kind, target); err != nil {
		return err
	}
	if target.Metadata != nil {
		for label, entry := range map[string]string{"Cargo metadata": target.Metadata.CargoMetadata, "SBOM": target.Metadata.SBOM} {
			payload, err := builtins.ReadArchiveEntry(artifact, target.Format, entry)
			if err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			if !json.Valid(payload) {
				return fmt.Errorf("%s is not JSON", label)
			}
		}
	}
	return nil
}

func persistArtifactAtomic(source, destination, expectedSHA string) error {
	actual, err := fileSHA256(source)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expectedSHA) {
		return fmt.Errorf("source SHA-256 mismatch: expected %s, got %s", expectedSHA, actual)
	}
	if info, statErr := os.Lstat(destination); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("destination exists and is not a regular file")
		}
		existing, hashErr := fileSHA256(destination)
		if hashErr != nil {
			return hashErr
		}
		if !strings.EqualFold(existing, expectedSHA) {
			return fmt.Errorf("destination already exists with different SHA-256: %s", existing)
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".builtin-artifact-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, sourceFile); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	copied, err := fileSHA256(temporaryPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(copied, expectedSHA) {
		return fmt.Errorf("copied artifact SHA-256 mismatch: expected %s, got %s", expectedSHA, copied)
	}
	// Link provides an atomic create-without-replacement operation. A plain
	// rename would replace an existing path on Unix after the earlier check.
	if err := os.Link(temporaryPath, destination); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	return nil
}

func updateVersionInSource(source, oldVersion, newVersion string) (string, bool) {
	oldValue := strings.TrimPrefix(strings.TrimSpace(oldVersion), "v")
	newValue := strings.TrimPrefix(strings.TrimSpace(newVersion), "v")
	if oldValue == "" || !strings.Contains(source, oldValue) {
		return "", false
	}
	return strings.Replace(source, oldValue, newValue, 1), true
}

func displayCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 12 {
		return commit[:12]
	}
	if commit == "" {
		return "n/a"
	}
	return commit
}

type semanticVersion struct {
	core       [3]string
	prerelease []string
}

func compareSemanticVersions(left, right string) (int, error) {
	a, err := parseSemanticVersion(left)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", left, err)
	}
	b, err := parseSemanticVersion(right)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", right, err)
	}
	for index := range a.core {
		if comparison := compareNumericIdentifier(a.core[index], b.core[index]); comparison != 0 {
			return comparison, nil
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) == 0 {
		return 0, nil
	}
	if len(a.prerelease) == 0 {
		return 1, nil
	}
	if len(b.prerelease) == 0 {
		return -1, nil
	}
	limit := len(a.prerelease)
	if len(b.prerelease) < limit {
		limit = len(b.prerelease)
	}
	for index := 0; index < limit; index++ {
		leftID, rightID := a.prerelease[index], b.prerelease[index]
		leftNumeric, rightNumeric := isNumeric(leftID), isNumeric(rightID)
		switch {
		case leftNumeric && rightNumeric:
			if comparison := compareNumericIdentifier(leftID, rightID); comparison != 0 {
				return comparison, nil
			}
		case leftNumeric:
			return -1, nil
		case rightNumeric:
			return 1, nil
		case leftID < rightID:
			return -1, nil
		case leftID > rightID:
			return 1, nil
		}
	}
	switch {
	case len(a.prerelease) < len(b.prerelease):
		return -1, nil
	case len(a.prerelease) > len(b.prerelease):
		return 1, nil
	default:
		return 0, nil
	}
}

func parseSemanticVersion(value string) (semanticVersion, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if build := strings.IndexByte(value, '+'); build >= 0 {
		value = value[:build]
	}
	coreValue, prereleaseValue, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(coreValue, ".")
	if len(parts) != 3 {
		return semanticVersion{}, errors.New("expected major.minor.patch")
	}
	var version semanticVersion
	for index, part := range parts {
		if !isNumeric(part) {
			return semanticVersion{}, errors.New("core identifiers must be non-negative integers")
		}
		version.core[index] = normalizeNumericIdentifier(part)
	}
	if !hasPrerelease {
		return version, nil
	}
	version.prerelease = strings.Split(prereleaseValue, ".")
	for _, identifier := range version.prerelease {
		if identifier == "" {
			return semanticVersion{}, errors.New("prerelease identifiers cannot be empty")
		}
		for _, character := range identifier {
			if (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && character != '-' {
				return semanticVersion{}, errors.New("prerelease identifiers contain an invalid character")
			}
		}
		if isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
			return semanticVersion{}, errors.New("numeric prerelease identifiers cannot contain leading zeroes")
		}
	}
	return version, nil
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func normalizeNumericIdentifier(value string) string {
	normalized := strings.TrimLeft(value, "0")
	if normalized == "" {
		return "0"
	}
	return normalized
}

func compareNumericIdentifier(left, right string) int {
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
