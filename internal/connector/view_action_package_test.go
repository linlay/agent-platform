package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Optional integration check against an actual resource package, without
// executing its CLI or reading credentials.
func TestViewActionResourcePackage(t *testing.T) {
	dir := os.Getenv("VIEW_ACTION_PACKAGE")
	if dir == "" {
		t.Skip("set VIEW_ACTION_PACKAGE to validate a resource package")
	}
	pkg, err := Load(filepath.Dir(dir), filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for key, definition := range pkg.Views {
		for name, action := range definition.Actions {
			count++
			if action.ParamsSchema == nil || action.Adapter != "cli" && action.Adapter != "mcp" {
				t.Fatalf("%s/%s", key, name)
			}
			var schema map[string]any
			if json.Unmarshal(action.ParamsSchema, &schema) != nil || schema["additionalProperties"] != false {
				t.Fatal("open action schema")
			}
		}
	}
	if count == 0 {
		t.Fatal("package has no actions")
	}
}
