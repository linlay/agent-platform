package resources

import (
	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellreview/types"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

const maxDependencyBytes = 8 << 20

func inspectHostConfig(path string) (types.FileInspection, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		canonical, err := pathutil.Canonicalize(path)
		if err != nil {
			return types.FileInspection{}, err
		}
		return types.FileInspection{Canonical: canonical.Host, Alias: path != canonical.Host}, nil
	}
	if err != nil {
		return types.FileInspection{}, err
	}
	if info.Size() > maxDependencyBytes {
		return types.FileInspection{}, errors.New("dependency exceeds review limit")
	}
	if !info.Mode().IsRegular() {
		return types.FileInspection{}, errors.New("dependency is not a regular file")
	}
	canonical, err := pathutil.Canonicalize(path)
	if err != nil {
		return types.FileInspection{}, err
	}
	f, err := openDependency(path)
	if err != nil {
		return types.FileInspection{}, err
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !current.Mode().IsRegular() {
		return types.FileInspection{}, errors.New("dependency type changed")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, maxDependencyBytes+1))
	if err != nil {
		return types.FileInspection{}, err
	}
	if n > maxDependencyBytes {
		return types.FileInspection{}, errors.New("dependency exceeds review limit")
	}
	after, err := f.Stat()
	if err != nil || current.Size() != after.Size() || !current.ModTime().Equal(after.ModTime()) {
		return types.FileInspection{}, errors.New("dependency changed during inspection")
	}
	return types.FileInspection{Exists: true, Canonical: canonical.Host, SHA256: hex.EncodeToString(hash.Sum(nil)), Alias: path != canonical.Host}, nil
}
