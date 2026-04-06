package engine

import (
	"errors"
	"testing"
)

func TestExpandToolArgsTemplatesResolvesPreviousResultPaths(t *testing.T) {
	expanded, err := ExpandToolArgsTemplates(map[string]any{
		"path":   "${previousResult.file.path}",
		"prefix": "value=${previousResult.file.name}",
	}, map[string]any{
		"file": map[string]any{
			"path": "/tmp/a.txt",
			"name": "a.txt",
		},
	})
	if err != nil {
		t.Fatalf("expand templates: %v", err)
	}
	args := expanded.(map[string]any)
	if args["path"] != "/tmp/a.txt" {
		t.Fatalf("expected full replacement, got %#v", args["path"])
	}
	if args["prefix"] != "value=a.txt" {
		t.Fatalf("expected string interpolation, got %#v", args["prefix"])
	}
}

func TestExpandToolArgsTemplatesFailsOnMissingValue(t *testing.T) {
	_, err := ExpandToolArgsTemplates("${previousResult.file.missing}", map[string]any{
		"file": map[string]any{"path": "/tmp/a.txt"},
	})
	if !errors.Is(err, ErrToolArgsTemplateMissingValue) {
		t.Fatalf("expected ErrToolArgsTemplateMissingValue, got %v", err)
	}
}
