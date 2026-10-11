package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTEAMConfigurationValidation(t *testing.T) {
	tests := []struct {
		name, mode, body string
		valid            bool
	}{
		{"default", "TEAM", "teamConfig:\n  members: [worker]\n", true},
		{"bounded", "TEAM", "teamConfig:\n  members: [worker]\n  maxParallel: 3\n", true},
		{"empty", "TEAM", "teamConfig:\n  members: []\n", false},
		{"duplicate", "TEAM", "teamConfig:\n  members: [worker, worker]\n", false},
		{"self", "TEAM", "teamConfig:\n  members: [research]\n", false},
		{"missing", "TEAM", "", false},
		{"zero", "TEAM", "teamConfig:\n  members: [worker]\n  maxParallel: 0\n", false},
		{"large", "TEAM", "teamConfig:\n  members: [worker]\n  maxParallel: 6\n", false},
		{"fraction", "TEAM", "teamConfig:\n  members: [worker]\n  maxParallel: 1.5\n", false},
		{"ordinary null", "GENERAL", "teamConfig: null\n", false},
		{"ordinary", "GENERAL", "teamConfig:\n  members: [worker]\n", false},
		{"acp", "TEAM", "engine: acp\nteamConfig:\n  members: [worker]\n", false},
		{"explicit delegate", "TEAM", "teamConfig:\n  members: [worker]\ntoolConfig:\n  tools: [agent_delegate]\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yml")
			if err := os.WriteFile(path, []byte("key: research\nname: Research\nmode: "+tc.mode+"\nmodelConfig:\n  modelKey: model\n"+tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			def, err := parseAgentDefinitionForTest(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
			if tc.name == "default" && def.TeamConfig.MaxParallel != 5 {
				t.Fatalf("config=%#v", def.TeamConfig)
			}
		})
	}
}

func TestTEAMSnapshotFreezesCoordinatorAndMembers(t *testing.T) {
	r := &FileRegistry{agents: map[string]AgentDefinition{
		"research": {Key: "research", Mode: "TEAM", ModelKey: "before", Tools: []string{"file_read"}, TeamConfig: &TeamConfig{Members: []string{"worker"}, MaxParallel: 2}},
		"worker":   {Key: "worker", Mode: "GENERAL", Tools: []string{"file_read"}},
	}}
	before, ok := r.ResolveTeam("research")
	if !ok {
		t.Fatal("missing snapshot")
	}
	root, _ := before.AgentDefinition("research")
	root.TeamConfig.Members[0] = "mutated"
	root.Tools[0] = "mutated"
	worker, _ := before.AgentDefinition("worker")
	worker.Tools[0] = "mutated"
	r.agents["research"] = AgentDefinition{Key: "research", Mode: "TEAM", ModelKey: "after", TeamConfig: &TeamConfig{Members: []string{"missing"}, MaxParallel: 3}}
	after, _ := r.ResolveTeam("research")
	frozen, _ := before.AgentDefinition("research")
	member, _ := before.AgentDefinition("worker")
	if frozen.ModelKey != "before" || frozen.TeamConfig.Members[0] != "worker" || frozen.Tools[0] != "file_read" || member.Tools[0] != "file_read" {
		t.Fatalf("snapshot mutated: %#v %#v", frozen, member)
	}
	if after.Coordinator.ModelKey != "after" || len(after.InvalidAgentKeys) != 1 || after.RosterFingerprint == before.RosterFingerprint {
		t.Fatalf("new snapshot=%#v", after)
	}
}

func TestTEAMRunLeaseRetainsWholeSnapshotAndReleasesOnce(t *testing.T) {
	r := &FileRegistry{agents: map[string]AgentDefinition{
		"research": {Key: "research", Mode: "TEAM", TeamConfig: &TeamConfig{Members: []string{"worker"}, MaxParallel: 5}},
		"worker":   {Key: "worker", Mode: "GENERAL"},
	}}
	root, snapshot, release, ok := r.AcquireRunRuntime("research")
	if !ok || root.Key != "research" || snapshot == nil || r.runtimeUsers["research"] != 1 || r.runtimeUsers["worker"] != 1 {
		t.Fatalf("lease=%#v %#v %v", root, snapshot, r.runtimeUsers)
	}
	r.agents["worker"] = AgentDefinition{Key: "worker", Mode: "TEAM", TeamConfig: &TeamConfig{Members: []string{"other"}}}
	old, _ := snapshot.AgentDefinition("worker")
	if old.Mode != "GENERAL" {
		t.Fatal("active lease changed")
	}
	release()
	release()
	if len(r.runtimeUsers) != 0 || len(r.runtimeVersions) != 0 {
		t.Fatalf("leaked leases: %v %v", r.runtimeUsers, r.runtimeVersions)
	}
}
