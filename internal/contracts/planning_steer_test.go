package contracts

import (
	"context"
	"runtime"
	"testing"
	"time"

	"agent-platform/internal/api"
)

func TestPlanningSteerSupersedesQueuedAndWaitingConfirmation(t *testing.T) {
	for _, timing := range []string{"before_registration", "before_wait", "while_waiting"} {
		t.Run(timing, func(t *testing.T) {
			control := NewRunControl(context.Background(), "run")
			defer control.Finish()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			steer := api.SteerRequest{RunID: "run", Message: "revise", SteerID: "steer-1"}
			if timing == "before_registration" && !control.EnqueueSteer(steer) {
				t.Fatal("enqueue before confirmation")
			}
			publish := control.ExpectPlanningSubmit(AwaitingSubmitContext{AwaitingID: "plan", ItemCount: 1})
			if publish != (timing != "before_registration") {
				t.Fatalf("publish obsolete confirmation: %v", publish)
			}
			result := make(chan SubmitResult, 1)
			if timing == "while_waiting" {
				go func() { answer, _ := control.AwaitSubmitIndefinitely(ctx, "plan"); result <- answer }()
				for {
					control.mu.Lock()
					waiting := control.submitWaiters["plan"] != nil
					control.mu.Unlock()
					if waiting {
						break
					}
					if ctx.Err() != nil {
						t.Fatal("waiter not registered")
					}
					runtime.Gosched()
				}
			}
			if timing != "before_registration" && !control.EnqueueSteer(steer) {
				t.Fatal("enqueue")
			}
			// The generic executor may deliver the old ask after admission.
			control.ExpectSubmit(AwaitingSubmitContext{AwaitingID: "plan", Mode: "planning"})
			if _, ok := control.LookupAwaiting("plan"); ok {
				t.Fatal("obsolete awaiting resurrected")
			}
			if ack := control.ResolveSubmit(api.SubmitRequest{AwaitingID: "plan", SubmitID: "late", ContinuationRunID: "execute"}); ack.Accepted || ack.Status != "already_resolved" {
				t.Fatalf("stale approval accepted: %#v", ack)
			}
			if timing != "while_waiting" {
				answer, err := control.AwaitSubmitIndefinitely(ctx, "plan")
				if err != nil {
					t.Fatal(err)
				}
				result <- answer
			}
			select {
			case answer := <-result:
				if answer.Status != PlanningSuperseded || answer.Request.SubmitID != "" {
					t.Fatalf("unexpected resolution: %#v", answer)
				}
			case <-ctx.Done():
				t.Fatal("steer did not wake confirmation")
			}
			if !control.EnqueueSteer(api.SteerRequest{Message: "another", SteerID: "steer-2"}) {
				t.Fatal("next steer rejected")
			}
			queue := control.DrainSteers()
			if len(queue) != 2 || queue[0].SteerID != "steer-1" || queue[1].SteerID != "steer-2" {
				t.Fatalf("lost/reordered steer: %#v", queue)
			}
			if !control.ExpectPlanningSubmit(AwaitingSubmitContext{AwaitingID: "plan-2"}) {
				t.Fatal("next revision not confirmable")
			}
		})
	}
}

func TestPlanningSteerAndApproveHaveOneAtomicWinner(t *testing.T) {
	for i := 0; i < 200; i++ {
		control := NewRunControl(context.Background(), "run")
		control.ExpectPlanningSubmit(AwaitingSubmitContext{AwaitingID: "plan"})
		// Lifecycle refresh must retain the producer's steer policy.
		control.ExpectSubmit(AwaitingSubmitContext{AwaitingID: "plan", Mode: "planning"})
		start := make(chan struct{})
		steer := make(chan bool, 1)
		approve := make(chan SubmitAck, 1)
		go func() { <-start; steer <- control.EnqueueSteer(api.SteerRequest{Message: "revise"}) }()
		go func() {
			<-start
			approve <- control.ResolveSubmit(api.SubmitRequest{AwaitingID: "plan", SubmitID: "approve", ContinuationRunID: "execute"})
		}()
		close(start)
		steered, approved := <-steer, <-approve
		if steered == approved.Accepted {
			t.Fatalf("iteration %d: steer=%v approve=%#v", i, steered, approved)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := control.AwaitSubmitIndefinitely(ctx, "plan")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if steered && result.Status != PlanningSuperseded {
			t.Fatalf("steer winner lost: %#v", result)
		}
		if approved.Accepted && result.Request.ContinuationRunID != "execute" {
			t.Fatalf("approve winner lost: %#v", result)
		}
		control.Finish()
	}
}

func TestPlanningRejectKeepsSteerAdmissionOpen(t *testing.T) {
	control := NewRunControl(context.Background(), "run")
	defer control.Finish()
	control.ExpectPlanningSubmit(AwaitingSubmitContext{AwaitingID: "plan"})
	if ack := control.ResolveSubmit(api.SubmitRequest{AwaitingID: "plan", SubmitID: "reject"}); !ack.Accepted {
		t.Fatal(ack)
	}
	if !control.EnqueueSteer(api.SteerRequest{Message: "revise after rejection"}) {
		t.Fatal("reject closed steer gate")
	}
}
