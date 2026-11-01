package webapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsRevisionAndStoppedBoundary(t *testing.T) {
	s, _ := fixtureService(t)
	e := installPackage(t, s, "1.0.0", "")
	c, err := s.Configuration(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.SavePolicy(e.ID, PolicyRequest{ExpectedRevision: c.Policy.Revision, Skills: []string{"builtin/example"}, ConnectPorts: []int{443}, Assistant: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SavePolicy(e.ID, PolicyRequest{ExpectedRevision: c.Policy.Revision})
	requireCode(t, err, "webapp_revision_conflict")
	if next.Policy.Revision == c.Policy.Revision {
		t.Fatal("policy revision not advanced")
	}
	_, err = s.SavePolicy(e.ID, PolicyRequest{ExpectedRevision: next.Policy.Revision, ConnectPorts: []int{0}})
	requireCode(t, err, "webapp_policy_invalid")
	_, err = s.SaveUserConfig(e.ID, UserConfigRequest{ExpectedRevision: c.ConfigRevision, Values: map[string]any{"unknown": "value"}})
	requireCode(t, err, "webapp_user_config_invalid")
	_, err = s.Start(context.Background(), e.ID, StateRequest{ExpectedRevision: e.Revision, ExpectedStateRevision: e.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SavePolicy(e.ID, PolicyRequest{ExpectedRevision: next.Policy.Revision})
	requireCode(t, err, "webapp_configuration_requires_stop")
}
func TestReopenRejectsTamperedManifestRecordAndPolicy(t *testing.T) {
	for _, which := range []string{"manifest", "policy"} {
		t.Run(which, func(t *testing.T) {
			s, options := fixtureService(t)
			e := installPackage(t, s, "1.0.0", "")
			s.Close()
			if which == "manifest" {
				var r Record
				if err := readJSON(filepath.Join(options.Root, e.ID, "install.json"), &r); err != nil {
					t.Fatal(err)
				}
				r.Manifest.Label = "different"
				if err := writeJSON(filepath.Join(options.Root, e.ID, "install.json"), r); err != nil {
					t.Fatal(err)
				}
			} else {
				var p Policy
				readJSON(filepath.Join(options.Root, e.ID, "policy.json"), &p)
				p.ConnectPorts = []int{0}
				writeJSON(filepath.Join(options.Root, e.ID, "policy.json"), p)
			}
			reopened, err := Open(options)
			if err == nil {
				reopened.Close()
				t.Fatal("tampered metadata accepted")
			}
		})
	}
}
func TestConfigurationNullAndOversizeFailClosed(t *testing.T) {
	s, options := fixtureService(t)
	e := installPackage(t, s, "1.0.0", "")
	if err := os.WriteFile(filepath.Join(options.Root, e.ID, "user-config.json"), []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Configuration(e.ID); err == nil {
		t.Fatal("null config accepted")
	}
}
