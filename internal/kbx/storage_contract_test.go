package kbx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"agent-platform/internal/knowledge"
)

func TestIndexScopePathsAreStable(t *testing.T) {
	for _, tc := range []struct {
		name, location, chunkJSON string
		chunk                     knowledge.ChunkConfig
	}{
		{"runtime defaults", "runtime", `{"unit":"estimatedTokens","maxTokens":1000,"overlapTokens":100}`, knowledge.DefaultChunkConfig()},
		{"workspace chars", "workspace", `{"unit":"chars","maxChars":3200,"overlapChars":320}`, knowledge.ChunkConfig{Unit: "chars", MaxChars: 3200, OverlapChars: 320}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := knowledge.DefaultConfig()
			cfg.Enabled = true
			cfg.Include, cfg.Exclude = []string{"docs/**"}, []string{"docs/private/**"}
			cfg.Chunk, cfg.Storage.Location = tc.chunk, tc.location
			manager := NewManager(Options{RuntimeDir: t.TempDir()}, testSource{"docs": {Key: "docs", WorkspaceRoot: t.TempDir(), Config: cfg}}, nil)
			library, err := manager.resolve("docs")
			if err != nil {
				t.Fatal(err)
			}
			rootJSON, err := json.Marshal(library.spec.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			// Freeze the pre-migration serialization, including field order and omitted zeros.
			identity := `{"Root":` + string(rootJSON) + `,"Include":["docs/**"],"Exclude":["docs/private/**"],"Chunk":` + tc.chunkJSON + `}`
			sum := sha256.Sum256([]byte(identity))
			scope := hex.EncodeToString(sum[:8])
			root, err := canonicalRoot(manager.options.RuntimeDir)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(root, "docs", "kbx", scope, "index.sqlite")
			if tc.location == "workspace" {
				want = filepath.Join(library.spec.WorkspaceRoot, ".kbx-platform", "docs", scope, "index.sqlite")
			}
			if library.database != want {
				t.Fatalf("existing index path changed: %s, want %s", library.database, want)
			}
		})
	}
}
