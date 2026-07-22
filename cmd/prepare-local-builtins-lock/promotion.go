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
	"sort"
	"strings"

	"agent-platform/internal/builtins"
)

type lockPromotion struct {
	Name        string
	From        string
	To          string
	Commit      string
	TargetCount int
}

type promotionCandidate struct {
	Lock       builtins.Lock
	Original   []byte
	Payload    []byte
	Promotions []lockPromotion
	Notices    []string
}

// offerUpdate is deliberately separate from ordinary local-lock generation.
// A normal sync always consumes local VERSION files, while this path only
// offers to promote complete, clean, strictly newer component releases into
// the canonical lock after every locked target can be verified.
func offerUpdate(lockPath, collectionRoot string, input io.Reader, output io.Writer, interactive bool) error {
	candidate, err := preparePromotionCandidate(lockPath, collectionRoot)
	if err != nil {
		return err
	}
	for _, notice := range candidate.Notices {
		fmt.Fprintf(output, "[builtins-sync] %s\n", notice)
	}
	if len(candidate.Promotions) == 0 {
		return nil
	}

	fmt.Fprintln(output, "[builtins-sync] verified newer local builtin versions:")
	for _, promotion := range candidate.Promotions {
		fmt.Fprintf(output, "  %s: %s -> %s (%d targets, commit %s)\n", promotion.Name, promotion.From, promotion.To, promotion.TargetCount, displayCommit(promotion.Commit))
	}
	if !interactive {
		fmt.Fprintln(output, "[builtins-sync] non-interactive input; canonical builtins lock was not changed")
		return nil
	}

	fmt.Fprint(output, "Update scripts/release-assets/builtins.lock.json? Type yes to confirm: ")
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read canonical lock confirmation: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		fmt.Fprintln(output, "[builtins-sync] canonical builtins lock was not changed")
		return nil
	}

	current, err := os.ReadFile(lockPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, candidate.Original) {
		return errors.New("canonical builtins lock changed while the update was being prepared")
	}
	if err := writeFileAtomic(lockPath, candidate.Payload, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(output, "[builtins-sync] updated canonical builtins lock: %s\n", lockPath)
	return nil
}

func preparePromotionCandidate(lockPath, collectionRoot string) (promotionCandidate, error) {
	if !filepath.IsAbs(collectionRoot) {
		return promotionCandidate{}, errors.New("--builtins-root must be absolute")
	}
	absLockPath, err := filepath.Abs(lockPath)
	if err != nil {
		return promotionCandidate{}, err
	}
	original, err := os.ReadFile(absLockPath)
	if err != nil {
		return promotionCandidate{}, err
	}
	lock, err := builtins.LoadLock(absLockPath)
	if err != nil {
		return promotionCandidate{}, err
	}
	candidate := promotionCandidate{Lock: lock, Original: original}

	for index := range candidate.Lock.Components {
		canonical := candidate.Lock.Components[index]
		if !isLocallyVersionedComponent(canonical.Name) {
			continue
		}
		repositoryRoot, err := joinWithin(collectionRoot, canonical.Repository)
		if err != nil {
			return promotionCandidate{}, fmt.Errorf("%s repository: %w", canonical.Name, err)
		}
		localVersion, err := localComponentVersion(repositoryRoot, canonical.Version)
		if err != nil {
			return promotionCandidate{}, fmt.Errorf("%s local version: %w", canonical.Name, err)
		}
		comparison, err := compareSemanticVersions(localVersion, canonical.Version)
		if err != nil {
			candidate.Notices = append(candidate.Notices, fmt.Sprintf("cannot compare %s versions %q and %q: %v", canonical.Name, canonical.Version, localVersion, err))
			continue
		}
		if comparison == 0 {
			continue
		}
		if comparison < 0 {
			candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s is older than locked version %s; downgrade was not offered", canonical.Name, localVersion, canonical.Version))
			continue
		}

		commit, hasCommit, err := localComponentCommit(repositoryRoot)
		if err != nil {
			return promotionCandidate{}, fmt.Errorf("%s local commit: %w", canonical.Name, err)
		}
		if hasCommit {
			clean, err := localComponentClean(repositoryRoot)
			if err != nil {
				return promotionCandidate{}, fmt.Errorf("%s local status: %w", canonical.Name, err)
			}
			if !clean {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s is newer than %s, but its repository has uncommitted changes; canonical update was not offered", canonical.Name, localVersion, canonical.Version))
				continue
			}
		} else if strings.TrimSpace(canonical.Commit) != "" {
			candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s is newer than %s, but no exact Git commit is available; canonical update was not offered", canonical.Name, localVersion, canonical.Version))
			continue
		}

		promoted := canonical
		promoted.Version = localVersion
		promoted.Targets = cloneTargets(canonical.Targets)
		if hasCommit {
			promoted.Commit = commit
		}
		if promoted.Name == "poppler-pdftotext" {
			updatedSource, ok := updateVersionInSource(promoted.Source, canonical.Version, localVersion)
			if !ok {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s is newer than %s, but source %q cannot be updated safely; canonical update was not offered", canonical.Name, localVersion, canonical.Version, canonical.Source))
				continue
			}
			promoted.Source = updatedSource
		}

		targetKeys := make([]string, 0, len(canonical.Targets))
		for key := range canonical.Targets {
			targetKeys = append(targetKeys, key)
		}
		sort.Strings(targetKeys)
		eligible := true
		for _, key := range targetKeys {
			goos, goarch, ok := strings.Cut(key, "-")
			if !ok || goos == "" || goarch == "" {
				return promotionCandidate{}, fmt.Errorf("%s has invalid target key %q", canonical.Name, key)
			}
			target, err := localTargetTemplate(promoted, canonical.Targets[key], true, goos, goarch)
			if err != nil {
				return promotionCandidate{}, err
			}
			artifact, err := joinWithin(repositoryRoot, target.Path)
			if err != nil {
				return promotionCandidate{}, fmt.Errorf("%s %s: %w", canonical.Name, key, err)
			}
			hash, err := fileSHA256(artifact)
			if err != nil {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s cannot update the canonical lock because target %s is unavailable: %v", canonical.Name, localVersion, key, err))
				eligible = false
				break
			}
			target.SHA256 = hash
			if err := validatePromotedArtifact(collectionRoot, promoted, key, target); err != nil {
				candidate.Notices = append(candidate.Notices, fmt.Sprintf("local %s version %s target %s failed promotion validation: %v", canonical.Name, localVersion, key, err))
				eligible = false
				break
			}
			promoted.Targets[key] = target
		}
		if !eligible {
			continue
		}
		candidate.Lock.Components[index] = promoted
		candidate.Promotions = append(candidate.Promotions, lockPromotion{
			Name: canonical.Name, From: canonical.Version, To: localVersion, Commit: promoted.Commit, TargetCount: len(targetKeys),
		})
	}

	payload, err := json.MarshalIndent(candidate.Lock, "", "  ")
	if err != nil {
		return promotionCandidate{}, err
	}
	candidate.Payload = append(payload, '\n')
	return candidate, nil
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
		lock := builtins.Lock{SchemaVersion: 1, DefaultRoot: ".", Components: []builtins.Component{component}}
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
