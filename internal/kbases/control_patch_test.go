package kbases

import "testing"

func TestLibraryControlPatchRemovesOptionalSettings(t *testing.T) {
	s := controlLibraryService(t)
	args := createArguments(t.TempDir())
	definition := args["definition"].(map[string]any)
	definition["chunk"] = map[string]any{"maxChars": 3000}
	definition["textEncoding"] = "gb18030"
	definition["models"] = map[string]any{
		"embedding":      map[string]any{"modelKey": "embedding-model"},
		"reranker":       map[string]any{"modelKey": "reranker-model"},
		"queryExpansion": map[string]any{"modelKey": "expansion-model"},
	}
	definition["retrieval"] = map[string]any{"topK": 12, "rerank": true}
	result, err := s.ExecuteControl("create", args, "with-settings", func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	id := result["libraryId"].(string)
	update := map[string]any{"libraryId": id, "baseRevision": result["baseRevision"], "patch": map[string]any{
		"models": map[string]any{"reranker": nil}, "retrieval": map[string]any{"rerank": nil},
	}}
	if _, err := s.ExecuteControl("update", update, "remove-nested", func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	d, err := s.Get(id)
	if err != nil || d.Models == nil || d.Models.Reranker != nil || d.Models.Embedding == nil || d.Models.QueryExpansion == nil || d.Retrieval == nil || d.Retrieval.Rerank != nil || d.Retrieval.TopK == nil {
		t.Fatalf("nested removal lost sibling settings: %+v %v", d, err)
	}
	for _, patch := range []map[string]any{
		{"name": nil}, {"collections": nil}, {"unknown": nil}, {"models": map[string]any{"unknown": nil}},
	} {
		invalid := map[string]any{"libraryId": id, "baseRevision": d.BaseRevision, "patch": patch}
		if _, err := s.PrepareControl("update", invalid, "invalid"); err == nil {
			t.Fatalf("invalid removal accepted: %+v", patch)
		}
	}
	update["baseRevision"] = d.BaseRevision
	update["patch"] = map[string]any{"chunk": nil, "textEncoding": nil, "models": nil, "retrieval": nil}
	if _, err := s.ExecuteControl("update", update, "remove-top-level", func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	d, err = s.Get(id)
	if err != nil || d.Chunk != nil || d.Models != nil || d.Retrieval != nil || d.TextEncoding != "" || d.Description != "Preserve this" {
		t.Fatalf("top-level removal not persisted or lost source metadata: %+v %v", d, err)
	}
}
