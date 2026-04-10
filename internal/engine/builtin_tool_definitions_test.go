package engine

import "testing"

func TestLoadEmbeddedToolDefinitionsUsesCanonicalJavaBuiltinNames(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}

	byName := map[string]bool{}
	var memoryRead map[string]any
	var dateTime map[string]any
	for _, def := range defs {
		byName[def.Name] = true
		switch def.Name {
		case "_memory_read_":
			memoryRead = def.Parameters
		case "_datetime_":
			dateTime = def.Parameters
		}
	}

	for _, name := range []string{"_memory_write_", "_memory_read_", "_memory_search_"} {
		if !byName[name] {
			t.Fatalf("expected embedded builtin %s in %#v", name, byName)
		}
	}
	for _, legacy := range []string{"memory_write", "memory_read", "memory_search"} {
		if byName[legacy] {
			t.Fatalf("did not expect legacy builtin name %s in %#v", legacy, byName)
		}
	}

	properties, _ := memoryRead["properties"].(map[string]any)
	if len(properties) != 4 {
		t.Fatalf("expected _memory_read_ properties copied from Java, got %#v", memoryRead)
	}
	if _, ok := properties["sort"]; !ok {
		t.Fatalf("expected _memory_read_ schema to include sort, got %#v", memoryRead)
	}

	dateTimeProperties, _ := dateTime["properties"].(map[string]any)
	if _, ok := dateTimeProperties["timezone"]; !ok {
		t.Fatalf("expected _datetime_ schema to include timezone, got %#v", dateTime)
	}
	if _, ok := dateTimeProperties["offset"]; !ok {
		t.Fatalf("expected _datetime_ schema to include offset, got %#v", dateTime)
	}
}
