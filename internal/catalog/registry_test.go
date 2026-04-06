package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAgentFileReadsContextTagsBudgetAndStageSettings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yml")
	if err := os.WriteFile(path, []byte(
		"key: demo\n"+
			"name: Demo\n"+
			"mode: REACT\n"+
			"modelConfig:\n"+
			"  modelKey: demo-model\n"+
			"contextTags:\n"+
			"  - execution_policy\n"+
			"  - agent_identity\n"+
			"budget:\n"+
			"  runTimeoutMs: 1000\n"+
			"stageSettings:\n"+
			"  stage: alpha\n",
	), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}

	def, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parse agent file: %v", err)
	}
	if len(def.ContextTags) != 2 || def.Budget["runTimeoutMs"] != int64(1000) && def.Budget["runTimeoutMs"] != 1000 {
		t.Fatalf("expected parsed context tags and budget, got %#v", def)
	}
	if def.StageSettings["stage"] != "alpha" {
		t.Fatalf("expected stage settings, got %#v", def.StageSettings)
	}
}
