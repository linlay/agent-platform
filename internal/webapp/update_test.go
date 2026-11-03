package webapp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

type rejectedRunner struct{}

func (rejectedRunner) Ready() bool { return true }
func (rejectedRunner) Start(context.Context, RuntimeSpec) (Process, error) {
	return nil, fail("webapp_startup_timeout", 503)
}
func (rejectedRunner) Logs(string, string, string) (LogPage, error) { return LogPage{}, nil }
func TestRunningStaticUpdateKeepsDesiredStatePolicyIdentityAndData(t *testing.T) {
	s, options := fixtureService(t)
	e := installPackage(t, s, "1.0.0", "")
	oldRecord := *s.records[e.ID]
	if err := os.MkdirAll(filepath.Join(options.DataRoot, e.ID), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(options.DataRoot, e.ID, "retained"), []byte("keep"), 0600)
	e, err := s.Start(context.Background(), e.ID, StateRequest{ExpectedRevision: e.Revision, ExpectedStateRevision: e.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	oldScope, err := s.Acquire(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldScope.Release()
	updated := installPackage(t, s, "2.0.0", e.Revision)
	if updated.Version != "2.0.0" || updated.DesiredState != "running" || updated.ObservedState != "running" || updated.Generation == e.Generation {
		t.Fatal("update state", updated)
	}
	if oldScope.Check() == nil || oldScope.Context.Err() == nil {
		t.Fatal("old access survived")
	}
	if s.records[e.ID].InstallationID != oldRecord.InstallationID || s.policies[e.ID].Revision != oldScope.Policy.Revision {
		t.Fatal("update expanded authority or replaced identity")
	}
	raw, err := os.ReadFile(filepath.Join(options.DataRoot, e.ID, "retained"))
	if err != nil || string(raw) != "keep" {
		t.Fatal("data lost")
	}
}
func TestRunningUpdateHealthFailureRollsBackBeforePublishing(t *testing.T) {
	s, _ := fixtureService(t)
	s.runner = rejectedRunner{}
	e := installPackage(t, s, "1.0.0", "")
	e, err := s.Start(context.Background(), e.ID, StateRequest{ExpectedRevision: e.Revision, ExpectedStateRevision: e.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"schemaVersion":2,"id":"webapp-0123456789abcdef","key":"test-app","label":"Test app","version":"2.0.0","target":"linux-x64","frontend":{"root":"frontend","index":"index.html"},"backend":{"command":{"type":"electron-node","entry":"backend/main.cjs"},"health":{"type":"tcp"}}}`
	raw := packageBytes(t, "2.0.0", map[string][]byte{e.ID + "/webapp.json": []byte(manifest), e.ID + "/backend/main.cjs": []byte("throw new Error('bad update')")})
	u, err := s.Upload(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256, ExpectedRevision: e.Revision})
	requireCode(t, err, "webapp_update_health_failed")
	restored, err := s.Get(e.ID)
	if err != nil || restored.Version != "1.0.0" || restored.ObservedState != "running" || restored.Revision != e.Revision {
		t.Fatal("failed update published", restored, err)
	}
	scope, err := s.Acquire(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Release()
	f, err := scope.CodeFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}
