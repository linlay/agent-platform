package webapp

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedManifestFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/manifest-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			Name     string          `json:"name"`
			Valid    bool            `json:"valid"`
			Manifest json.RawMessage `json:"manifest"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	if suite.SchemaVersion != 1 {
		t.Fatal("unknown fixture contract")
	}
	for _, c := range suite.Cases {
		t.Run(c.Name, func(t *testing.T) {
			_, err := ParseManifest(c.Manifest)
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%t err=%v", c.Valid, err)
			}
		})
	}
}
