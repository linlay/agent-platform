package knowledge

import (
	"reflect"
	"testing"
)

func TestFormatIndexedFilesPreservesFilteringTreeAndPagination(t *testing.T) {
	entries := []FileEntry{
		{Path: "docs/sub/b.md", Ext: ".md", Status: "active"},
		{Path: "docs/a.MD", Ext: ".MD", Status: "active", Size: 10},
		{Path: "docs/private.md", Ext: ".md", Status: "deleted"},
		{Path: "docs/a.txt", Ext: ".txt", Status: "active"},
		{Path: "docs-other/a.md", Ext: ".md", Status: "active"},
	}
	result, err := FormatIndexedFiles(entries, FilesOptions{Path: "docs", Pattern: "**/*.md", Type: "MD", HeadLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Glob matching remains case-sensitive; extension filtering is case-insensitive.
	if result.FileCount != 1 || result.Results[0].Path != "docs/sub/b.md" || result.Results[0].Dir != "docs/sub/" {
		t.Fatalf("filtered inventory changed: %#v", result)
	}
	result, err = FormatIndexedFiles(entries, FilesOptions{Mode: "tree", Path: "docs", Type: "md", Depth: 2, HeadLimit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.FileCount != 2 || result.DirCount != 1 || result.MatchCount != 3 || !result.Truncated || len(result.Results) != 1 || result.Results[0].Path != "docs/sub/" || result.Results[0].FileCount != 1 {
		t.Fatalf("tree pagination changed: %#v", result)
	}
	if !reflect.DeepEqual(entries[0], FileEntry{Path: "docs/sub/b.md", Ext: ".md", Status: "active"}) {
		t.Fatal("formatter mutated caller inventory")
	}
}

func TestIndexedPathPolicyKeepsGlobSeparatorsAndExclusions(t *testing.T) {
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{"docs/a.md", true}, {"docs/sub/a.md", false}, {"docs/private.md", false}, {"docs-other/a.md", false},
	} {
		if got := IndexedPathAllowed(tc.path, []string{"docs/*.md"}, []string{"docs/private.md"}); got != tc.allowed {
			t.Fatalf("%s allowed = %v, want %v", tc.path, got, tc.allowed)
		}
	}
}
