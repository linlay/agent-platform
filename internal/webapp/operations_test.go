package webapp

import (
	"context"
	"testing"
	"time"
)

func TestAcceptedOperationSurvivesCallerAndPublishesAuthoritativeResult(t *testing.T) {
	s, _ := fixtureService(t)
	u := uploadPackage(t, s, "1.0.0")
	op, err := s.SubmitInstall(InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationID == "" || op.Result != nil {
		t.Fatal("not accepted", op)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		op, err = s.Operation(op.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if op.State == "succeeded" {
			break
		}
		if op.State == "failed" {
			t.Fatal(op.ErrorCode)
		}
		time.Sleep(time.Millisecond)
	}
	if op.State != "succeeded" || op.Result == nil || op.Result.ObservedState != "stopped" {
		t.Fatal("operation result", op)
	}
	bad, err := s.SubmitState("start", op.Result.ID, StateRequest{ExpectedRevision: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		bad, _ = s.Operation(bad.OperationID)
		if bad.State == "failed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if bad.ErrorCode != "webapp_revision_conflict" {
		t.Fatal("stale operation", bad)
	}
}
func TestCloseCancelsAcceptedWorkBeforeReleasingInstance(t *testing.T) {
	s, _ := fixtureService(t)
	began := make(chan struct{})
	finished := make(chan struct{})
	_, err := s.beginOperation("fixture", "", time.Minute, func(ctx context.Context) (Entry, error) {
		close(began)
		<-ctx.Done()
		close(finished)
		return Entry{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-began
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("close did not drain accepted work")
	}
}
