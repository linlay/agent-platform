package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// WatchCredentials observes only private state. Package sources and Agent
// runtime directories are never rewritten when an account changes.
func (r *RegistryReloader) WatchCredentials(ctx context.Context) {
	if r == nil || r.registry == nil {
		return
	}
	previous := credentialStateDigest(r.registry.sources.PersistentRoot())
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				next := credentialStateDigest(r.registry.sources.PersistentRoot())
				if next != previous {
					if r.Reload(ctx) == nil {
						previous = next
					}
				}
			}
		}
	}()
}

func credentialStateDigest(root string) string {
	h := sha256.New()
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == "config" {
				return filepath.SkipDir
			}
			return nil
		}
		switch entry.Name() {
		case "auth-state.json", "credentials.json", "oauth.json":
			if entry.Type().IsRegular() {
				// No token bytes are retained or included in diagnostics.
				if data, err := os.ReadFile(path); err == nil {
					fmt.Fprintf(h, "%s\x00%x\n", path, sha256.Sum256(data))
				}
			}
		}
		return nil
	})
	return fmt.Sprintf("%x", h.Sum(nil))
}
