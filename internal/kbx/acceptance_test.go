package kbx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/kbase"
)

// Opt-in acceptance: indexes are created only under t.TempDir; the source
// directory is read-only. Current one-shot collection add is used exclusively
// by this harness to prepare reader fixtures, never by the Platform runtime.
func TestRealKnowledgeBases(t *testing.T) {
	sourceRoot := os.Getenv("KBX_ACCEPTANCE_SOURCE")
	bin := os.Getenv("KBX_ACCEPTANCE_BIN")
	if sourceRoot == "" || bin == "" {
		t.Skip("set KBX_ACCEPTANCE_SOURCE and KBX_ACCEPTANCE_BIN")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"冒烟文档", "AI建设文档", "研发中心各条线述职", "期货运营"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(sourceRoot, name)
			before := sourceFingerprint(t, root)
			t.Cleanup(func() {
				if after := sourceFingerprint(t, root); after != before {
					t.Error("source files changed")
				}
			})
			cfg := kbase.DefaultConfig()
			cfg.Enabled = true
			// KBX supports spreadsheets; explicitly opt in for this fixture.
			cfg.Include = append(cfg.Include, "**/*.xlsx")
			m := NewManager(Options{RuntimeDir: t.TempDir()}, testSource{"docs": {Key: "docs", WorkspaceRoot: root, Config: cfg}}, nil)
			l, err := m.resolve("docs")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(l.database), 0700); err != nil {
				t.Fatal(err)
			}
			config, err := m.config(l, false)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			out, err := m.runner.Run(ctx, l.database, config, "collection", "add", root, "--name", "workspace")
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			build := string(out)
			status, err := m.Status("docs")
			if err != nil || status.Files == 0 {
				t.Fatalf("status: %v %+v", err, status)
			}
			files, err := m.Files("docs", kbase.FilesOptions{HeadLimit: 0})
			if err != nil || len(files.Results) == 0 {
				t.Fatalf("files: %v %+v", err, files)
			}
			queries := []string{"管理", "系统", "测试", "风险", "工作"}
			verified := 0
			types := map[string]int{}
			for _, f := range files.Results {
				types[f.Ext]++
			}
			for _, q := range queries {
				r, err := m.Search(ctx, "docs", q, kbase.SearchOptions{Limit: 5})
				if err != nil {
					t.Fatalf("search %s: %v", q, err)
				}
				for _, hit := range r.Results {
					read, err := m.Read("docs", kbase.ReadOptions{ChunkID: hit.ChunkID})
					if err != nil {
						t.Fatalf("evidence: %v", err)
					}
					if read.Content != hit.Snippet {
						t.Fatalf("evidence differs from full chunk for %s", hit.Path)
					}
					verified++
				}
				if len(r.Results) > 0 {
					h := r.Results[0]
					ext := path.Ext(h.Path)
					narrow, err := m.Search(ctx, "docs", q, kbase.SearchOptions{Limit: 5, PathGlob: h.Path, Type: strings.ToUpper(ext)})
					if err != nil {
						t.Fatal(err)
					}
					if len(narrow.Results) == 0 {
						t.Fatalf("exact path filter lost known match %s", h.Path)
					}
					for _, hit := range narrow.Results {
						if hit.Path != h.Path {
							t.Fatalf("filter escaped: %s", hit.Path)
						}
					}
					dir := path.Dir(h.Path)
					if dir != "." {
						narrow, err = m.Search(ctx, "docs", q, kbase.SearchOptions{Limit: 5, PathPrefix: dir, Type: ext})
						if err != nil || len(narrow.Results) == 0 {
							t.Fatalf("prefix recall failed: %v", err)
						}
						for _, hit := range narrow.Results {
							if !strings.HasPrefix(hit.Path, dir+"/") {
								t.Fatal("prefix escaped")
							}
						}
					}
				}
			}
			if verified == 0 {
				t.Fatal("no real evidence validated")
			}
			if after := sourceFingerprint(t, root); after != before {
				t.Fatal("source files changed")
			}
			summary, _ := json.Marshal(map[string]any{"library": name, "indexed": status.Files, "visible": files.FileCount, "extensions": types, "evidenceVerified": verified, "sourceSHA256": before, "build": build})
			t.Logf("ACCEPTANCE %s", summary)
		})
	}
}
func sourceFingerprint(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", rel, info.Size(), info.ModTime().UnixNano())
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(h, f)
		f.Close()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestLiveChunkAndFilterContract(t *testing.T) {
	bin := os.Getenv("KBX_ACCEPTANCE_BIN")
	if bin == "" {
		t.Skip("set KBX_ACCEPTANCE_BIN")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, e := builtins.ConfigureProcessPath(); e != nil {
		t.Fatal(e)
	}
	m, l := newTestManager(t)
	if err := os.Remove(l.database); err != nil {
		t.Fatal(err)
	}
	root := l.spec.WorkspaceRoot
	for p, text := range map[string]string{"allowed/a.md": strings.Repeat("needle alpha policy checkpoint.\n", 500), "allowed-old/b.md": strings.Repeat("needle unrelated scope.\n", 500), "allowed/c.txt": "needle text extension", "other.md": "other unique marker"} {
		full := filepath.Join(root, p)
		if e := os.MkdirAll(filepath.Dir(full), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(full, []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
	}
	cfg, _ := m.config(l, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, e := m.runner.Run(ctx, l.database, cfg, "collection", "add", root, "--name", "workspace"); e != nil {
		t.Fatal(e)
	}
	r, e := m.Search(ctx, "docs", "needle", kbase.SearchOptions{Limit: 5, PathPrefix: "allowed", Type: ".MD"})
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Results) < 2 {
		t.Fatalf("expected multiple chunks from same document: %+v", r)
	}
	seen := map[string]bool{}
	for _, h := range r.Results {
		if h.Path != "allowed/a.md" {
			t.Fatalf("prefilter escaped: %s", h.Path)
		}
		if seen[h.ChunkID] {
			t.Fatal("duplicate chunk")
		}
		seen[h.ChunkID] = true
		read, e := m.Read("docs", kbase.ReadOptions{ChunkID: h.ChunkID})
		if e != nil || read.Content != h.Snippet {
			t.Fatalf("chunk evidence differs: %v", e)
		}
	}
	read, e := m.Read("docs", kbase.ReadOptions{Path: "allowed/a.md", Offset: 2, Limit: 3})
	if e != nil || read.StartLine != 2 || read.EndLine != 4 || !read.HasMore {
		t.Fatalf("line pagination: %v %+v", e, read)
	}
	empty, e := m.Search(ctx, "docs", "needle", kbase.SearchOptions{PathPrefix: "missing"})
	if e != nil || len(empty.Results) != 0 {
		t.Fatalf("empty filter: %v %+v", e, empty)
	}
	for _, bad := range []string{"../secret", "/tmp/secret", "kbx://other/secret"} {
		if _, e := m.Read("docs", kbase.ReadOptions{Path: bad}); e == nil {
			t.Fatalf("accepted unsafe path %s", bad)
		}
	}
	t.Logf("ACCEPTANCE synthetic: %d distinct chunks from one file; prefix boundary, extension case, exact evidence and line pagination passed", len(seen))
}
