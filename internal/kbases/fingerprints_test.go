package kbases

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type vectorTestEngine struct {
	testEngine
	version atomic.Value
	started chan struct{}
	release chan struct{}
	fail    bool
	updates atomic.Int32
}

func (e *vectorTestEngine) VectorFingerprint(Definition) string { return e.version.Load().(string) }
func (e *vectorTestEngine) Update(ctx context.Context, db string, cs []Collection) error {
	e.updates.Add(1)
	return e.testEngine.Update(ctx, db, cs)
}
func (e *vectorTestEngine) RebuildVectors(ctx context.Context, _ string, _ Definition) error {
	close(e.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.release:
	}
	if e.fail {
		return errors.New("embedding failed")
	}
	return nil
}

func TestFingerprintClassification(t *testing.T) {
	old := indexFingerprints{Source: "a", Vector: "v1"}
	for _, tc := range []struct {
		next indexFingerprints
		want indexChange
	}{
		{old, indexUnchanged}, {indexFingerprints{Source: "a", Vector: "v2"}, indexVectorsChanged}, {indexFingerprints{Source: "b", Vector: "v2"}, indexSourcesChanged},
	} {
		if got := tc.next.changeFrom(old); got != tc.want {
			t.Fatalf("classification %v != %v", got, tc.want)
		}
	}
}

func TestVectorOnlyRebuildKeepsFullTextReadable(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			engine := &vectorTestEngine{started: make(chan struct{}), release: make(chan struct{}), fail: fail}
			engine.version.Store("v1")
			s := newStorageService(t, engine)
			d := createFixture(t, s)
			if _, err := s.Refresh(d.ID); err != nil {
				t.Fatal(err)
			}
			waitState(t, s, d.ID, "ready")
			engine.version.Store("v2")
			before, err := s.Get(d.ID)
			if err != nil || before.IndexedAt == 0 || !before.Degraded {
				t.Fatalf("changed vectors blocked text: %+v %v", before, err)
			}
			if _, err := s.refreshWithMode(d.ID, nil, true); err != nil {
				t.Fatal(err)
			}
			<-engine.started
			during, err := s.Get(d.ID)
			if err != nil || during.IndexedAt == 0 {
				t.Fatalf("vector rebuild blocked text: %+v %v", during, err)
			}
			if _, _, release, err := s.Acquire(d.ID); err != nil {
				t.Fatal(err)
			} else {
				release()
			}
			close(engine.release)
			waitIdle(t, s, d.ID)
			after, err := s.Get(d.ID)
			if err != nil || after.IndexedAt == 0 || after.Degraded != fail {
				t.Fatalf("vector completion: %+v %v", after, err)
			}
			if engine.updates.Load() != 1 {
				t.Fatal("vector-only rebuild scanned sources")
			}
			state, err := s.readState(d.ID)
			if err != nil {
				t.Fatal(err)
			}
			// Persisted vector interruption does not invalidate a committed text index.
			state.State = "indexing"
			state.VectorOnlyTask = true
			if err := s.saveState(d.ID, state); err != nil {
				t.Fatal(err)
			}
			restored, err := s.Get(d.ID)
			if err != nil || restored.IndexedAt == 0 || !restored.Degraded {
				t.Fatalf("interrupted vectors: %+v %v", restored, err)
			}
		})
	}
}

func TestSchedulerDetectsVectorContractChanges(t *testing.T) {
	engine := &vectorTestEngine{started: make(chan struct{}), release: make(chan struct{})}
	engine.version.Store("v1")
	s := newStorageService(t, engine)
	defer s.Close(context.Background())
	d := createFixture(t, s)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
	engine.version.Store("v2")
	select {
	case <-engine.started:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not rebuild changed model")
	}
	if engine.updates.Load() != 1 {
		t.Fatal("model-only scheduler change scanned sources")
	}
	close(engine.release)
	waitIdle(t, s, d.ID)
	after, err := s.Get(d.ID)
	if err != nil || after.VectorsPending || after.Degraded {
		t.Fatalf("vectors not committed: %+v %v", after, err)
	}
}
