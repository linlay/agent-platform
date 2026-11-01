package webapp

import (
	"context"
	"testing"
	"time"
)

func TestStaticStopDrainsLeasesBeforeAllowingPackageReplacement(t *testing.T) {
	s, _ := fixtureService(t)
	entry := installPackage(t, s, "1.0.0", "")
	entry, err := s.Start(context.Background(), entry.ID, StateRequest{ExpectedRevision: entry.Revision, ExpectedStateRevision: entry.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := s.Acquire(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Release()
	done := make(chan error, 1)
	go func() {
		_, err := s.Stop(context.Background(), entry.ID, StateRequest{ExpectedRevision: entry.Revision, ExpectedStateRevision: entry.StateRevision})
		done <- err
	}()
	select {
	case <-scope.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not revoke leases")
	}
	u := uploadPackage(t, s, "2.0.0")
	_, err = s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256, ExpectedRevision: entry.Revision})
	requireCode(t, err, "webapp_update_requires_stop")
	select {
	case <-done:
		t.Fatal("stop returned before lease was released")
	default:
	}
	scope.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := scope.Check(); err == nil {
		t.Fatal("revoked scope remained valid")
	}
	if _, err := s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256, ExpectedRevision: entry.Revision}); err != nil {
		t.Fatal(err)
	}
}
func TestStaticShutdownPreservesDesiredStateAndRestoresFreshGeneration(t *testing.T) {
	s, options := fixtureService(t)
	entry := installPackage(t, s, "1.0.0", "")
	entry, err := s.Start(context.Background(), entry.ID, StateRequest{ExpectedRevision: entry.Revision, ExpectedStateRevision: entry.StateRevision})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.RestoreStatic(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.Get(entry.ID)
	if err != nil || restored.DesiredState != "running" || restored.ObservedState != "running" || restored.Generation == entry.Generation {
		t.Fatal("static desired state was lost", restored, err)
	}
}
