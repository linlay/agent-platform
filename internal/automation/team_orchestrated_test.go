package automation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTEAMAutomationUsesAgentKey(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(root)
	def := Definition{ID: "team-job", Name: "Team Job", Cron: "17 9 * * *", AgentKey: "research", Query: Query{Message: "report"}}
	if err := r.Persist(def); err != nil {
		t.Fatal(err)
	}
	loaded, err := r.parseDefinition(filepath.Join(root, "team-job.yml"))
	if err != nil || loaded.AgentKey != "research" || loaded.ToQueryRequest().AgentKey != "research" {
		t.Fatalf("definition=%#v err=%v", loaded, err)
	}
	if err := os.WriteFile(filepath.Join(root, "missing.yml"), []byte("name: Missing\ncron: '17 9 * * *'\nquery:\n  message: report\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.parseDefinition(filepath.Join(root, "missing.yml")); err == nil {
		t.Fatal("missing agent accepted")
	}
}
