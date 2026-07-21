package kbase

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSelectRetrievalIgnoresLegacySQLiteDatabaseWithoutGeneration(t *testing.T) {
	storageDir := t.TempDir()
	legacyPath := filepath.Join(storageDir, "kbase.db")
	legacy := []byte("legacy SQLite database")
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatalf("write legacy database: %v", err)
	}

	manager := NewManager(ManagerOptions{}, nil, nil)
	retrieval, generationID, available, err := manager.generations.SelectRetrieval(context.Background(), resolvedConfig{StorageDir: storageDir})
	if err != nil {
		t.Fatalf("select retrieval: %v", err)
	}
	if retrieval != nil || generationID != "" || available {
		t.Fatalf("legacy database unexpectedly selected: retrieval=%T generation=%q available=%t", retrieval, generationID, available)
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy database: %v", err)
	}
	if string(got) != string(legacy) {
		t.Fatalf("legacy database changed: %q", got)
	}
}
