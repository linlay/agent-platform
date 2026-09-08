package builtins

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RunManifestCommand is a packaging-only entry point. It does not load runtime
// configuration, initialize stores, or weaken the normal startup verifier.
func RunManifestCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "verify" && args[0] != "refresh-after-signing") {
		return errors.New("builtins-manifest requires verify or refresh-after-signing")
	}
	flags := flag.NewFlagSet("builtins-manifest "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("bundle-root", "", "absolute Platform bundle root")
	expected := flags.String("expected-manifest-sha256", "", "manifest digest returned by pre-sign verification")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if !filepath.IsAbs(*root) || flags.NArg() != 0 {
		return errors.New("an absolute --bundle-root and no positional arguments are required")
	}
	if args[0] == "verify" && *expected != "" {
		return errors.New("--expected-manifest-sha256 is only valid with refresh-after-signing")
	}
	manifestPath := filepath.Join(*root, "builtins.manifest.json")
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if args[0] == "refresh-after-signing" {
		// Windows and Linux keep their release hashes. Only the Darwin packaging
		// pipeline is allowed to replace hashes after changing Mach-O signatures.
		if manifest.Platform.OS != "darwin" {
			return errors.New("refresh-after-signing is only supported for Darwin bundles")
		}
		if err := validateManifestSHA256(*expected); err != nil {
			return fmt.Errorf("--expected-manifest-sha256: %w", err)
		}
		if !strings.EqualFold(bytesSHA256(payload), *expected) {
			return errors.New("builtins manifest changed since pre-sign verification")
		}
		for i, component := range manifest.Components {
			if err := validateManifestSHA256(component.SHA256); err != nil {
				return err
			}
			digest, err := signedComponentDigest(*root, component)
			if err != nil {
				return fmt.Errorf("refresh builtin %s: %w", component.Name, err)
			}
			manifest.Components[i].SHA256 = digest
		}
	}
	if err := VerifyManifest(*root, manifest); err != nil {
		return err
	}
	current, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if string(current) != string(payload) {
		return errors.New("builtins manifest changed during verification")
	}
	if args[0] == "refresh-after-signing" {
		payload, err = json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		payload = append(payload, '\n')
		// This mutation is Darwin-only. Rename replaces the manifest atomically
		// there; never remove the old manifest before the replacement is ready.
		temp, err := os.CreateTemp(*root, ".builtins.manifest-*")
		if err != nil {
			return err
		}
		defer os.Remove(temp.Name())
		_, writeErr := temp.Write(payload)
		closeErr := temp.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Chmod(temp.Name(), 0o644); err != nil {
			return err
		}
		if err := os.Rename(temp.Name(), manifestPath); err != nil {
			return err
		}
	}
	return json.NewEncoder(stdout).Encode(struct {
		SchemaVersion  int    `json:"schemaVersion"`
		ManifestSHA256 string `json:"manifestSha256"`
	}{1, bytesSHA256(payload)})
}

func signedComponentDigest(root string, component ManifestComponent) (string, error) {
	paths := []string{component.Path}
	for _, output := range component.Tree {
		paths = append(paths, output.Path)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	for _, relative := range paths {
		clean, err := cleanRelativePath(relative)
		if err != nil {
			return "", err
		}
		candidate, err := joinWithin(canonicalRoot, clean)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", err
		}
		if resolved != candidate {
			return "", fmt.Errorf("builtin path contains a symbolic link: %s", relative)
		}
	}
	if len(component.Tree) > 0 {
		return TreeDigest(root, component.Tree)
	}
	filePath := filepath.Join(root, filepath.FromSlash(component.Path))
	info, err := os.Lstat(filePath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("builtin payload must be a regular file")
	}
	payload, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return bytesSHA256(payload), nil
}
