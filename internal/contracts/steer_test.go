package contracts

import (
	"agent-platform/internal/api"
	"context"
	"testing"
)

func TestSteerPreparationRechecksLifecycleAndOwnsInput(t *testing.T) {
	c := NewRunControl(context.Background(), "run-a")
	req := api.SteerRequest{RunID: "run-a", Message: "look", References: []api.Reference{{URL: "a.png"}}}
	if accepted, err := c.PrepareAndEnqueueSteer(req); accepted || err == nil {
		t.Fatal("unprepared image accepted")
	}
	started, release := make(chan struct{}), make(chan struct{})
	c.SetSteerPreparer(func(req api.SteerRequest) (api.SteerRequest, error) {
		close(started)
		<-release
		req.PreparedMessages = []map[string]any{{"role": "user", "content": "frozen"}}
		return req, nil
	})
	result := make(chan bool, 1)
	go func() { accepted, _ := c.PrepareAndEnqueueSteer(req); result <- accepted }()
	<-started
	c.CloseSteers()
	close(release)
	if <-result || len(c.DrainSteers()) != 0 {
		t.Fatal("closed run accepted prepared image")
	}
	c = NewRunControl(context.Background(), "run-b")
	req.PreparedMessages = []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": "original"}}}}
	if !c.EnqueueSteer(req) {
		t.Fatal("enqueue")
	}
	req.References[0].URL = "changed.png"
	req.PreparedMessages[0]["content"].([]map[string]any)[0]["text"] = "changed"
	got := c.DrainSteers()[0]
	if got.References[0].URL != "a.png" || got.PreparedMessages[0]["content"].([]map[string]any)[0]["text"] != "original" {
		t.Fatal("queue aliases caller input")
	}
}
