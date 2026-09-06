package runstate

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestManagerCoordinatesHistoryMaintenanceWithQueryAdmission(t *testing.T) {
	manager := newTestManager(t)
	release, err := manager.ReserveChatQuery("chat-lease", "query-1")
	if err != nil {
		t.Fatalf("ReserveChatQuery: %v", err)
	}
	request := contracts.CompactControlRequest{RequestID: "compact-1", CompactID: "cid-1", ChatID: "chat-lease", Trigger: "manual", Level: "summary"}
	if ack, err := manager.RouteCompactForChat(request); err != nil || ack.Status != "busy" {
		t.Fatalf("compact while query is preparing = %#v err=%v", ack, err)
	}
	release()
	owner, err := manager.RouteCompactForChat(request)
	if err != nil || owner.Status != "history_acquired" {
		t.Fatalf("history owner = %#v err=%v", owner, err)
	}
	joined, err := manager.RouteCompactForChat(request)
	if err != nil || joined.Status != "history_joined" {
		t.Fatalf("history join = %#v err=%v", joined, err)
	}
	if ack, err := manager.RouteCompactForChat(contracts.CompactControlRequest{RequestID: "compact-2", CompactID: "cid-2", ChatID: "chat-lease"}); err != nil || ack.Status != "busy" {
		t.Fatalf("different compact while leased = %#v err=%v", ack, err)
	}
	if _, err := manager.ReserveChatQuery("chat-lease", "query-2"); err == nil {
		t.Fatal("query admission succeeded during compact lease")
	} else {
		var conflict *contracts.ChatMaintenanceConflictError
		if !errors.As(err, &conflict) || conflict.Detail != "compact_in_progress" {
			t.Fatalf("query conflict = %T %v", err, err)
		}
	}
	if _, err := manager.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{RunID: "run-2", RequestID: "query-2", ChatID: "chat-lease"}); err == nil {
		t.Fatal("run registration succeeded during compact lease")
	}
	want := api.CompactResponse{Accepted: true, Status: "completed", RequestID: request.RequestID, CompactID: request.CompactID, ChatID: request.ChatID, Scope: "history"}
	manager.CompleteChatMaintenance(request.ChatID, request.RequestID, want)
	<-owner.Handle.Done()
	<-joined.Handle.Done()
	if owner.Handle.Result().CompactID != want.CompactID || joined.Handle.Result().Status != "completed" {
		t.Fatalf("maintenance results owner=%#v joined=%#v", owner.Handle.Result(), joined.Handle.Result())
	}
	replayed, err := manager.RouteCompactForChat(request)
	if err != nil || replayed.Status != "completed" || replayed.Handle.Result().CompactID != want.CompactID {
		t.Fatalf("maintenance replay = %#v err=%v", replayed, err)
	}
}

func TestManagerMarksNativeSessionCompactableAtRegistration(t *testing.T) {
	manager := newTestManager(t)
	_, nativeControl, _ := manager.Register(context.Background(), contracts.QuerySession{
		RunID:                     "run-native-compact",
		ChatID:                    "chat-native-compact",
		SupportsContextCompaction: true,
	})
	defer manager.Finish("run-native-compact")
	if !nativeControl.ContextCompactSupported() {
		t.Fatal("native run was not marked compactable during registration")
	}
	ack, err := manager.RouteCompactForChat(contracts.CompactControlRequest{
		RequestID: "request-native-compact",
		CompactID: "compact-native",
		ChatID:    "chat-native-compact",
		Trigger:   "manual",
		Level:     "summary",
	})
	if err != nil || ack.Status != "queued" {
		t.Fatalf("native compact route = %#v err=%v", ack, err)
	}

	manager.Finish("run-native-compact")
	_, unsupportedControl, _ := manager.Register(context.Background(), contracts.QuerySession{
		RunID:  "run-proxy-compact",
		ChatID: "chat-proxy-compact",
	})
	defer manager.Finish("run-proxy-compact")
	if unsupportedControl.ContextCompactSupported() {
		t.Fatal("unsupported run was marked compactable")
	}
	ack, err = manager.RouteCompactForChat(contracts.CompactControlRequest{
		RequestID: "request-proxy-compact",
		CompactID: "compact-proxy",
		ChatID:    "chat-proxy-compact",
		Trigger:   "manual",
		Level:     "summary",
	})
	if err != nil || ack.Status != "unsupported" {
		t.Fatalf("unsupported compact route = %#v err=%v", ack, err)
	}
}

func TestManagerMaintenanceAndRegistrationRaceIsExclusive(t *testing.T) {
	for i := 0; i < 32; i++ {
		manager := newTestManager(t)
		start := make(chan struct{})
		registered := make(chan bool, 1)
		compact := make(chan contracts.ActiveRunCompactAck, 1)
		failures := make(chan error, 2)
		go func() {
			<-start
			result, err := manager.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{RunID: "run", ChatID: "chat"})
			var conflict *contracts.ChatMaintenanceConflictError
			if err != nil && !errors.As(err, &conflict) {
				failures <- err
			}
			registered <- result.Registered
		}()
		go func() {
			<-start
			result, err := manager.RouteCompactForChat(contracts.CompactControlRequest{ChatID: "chat", RequestID: "compact", CompactID: "cid"})
			if err != nil {
				failures <- err
			}
			compact <- result
		}()
		close(start)
		didRegister, ack := <-registered, <-compact
		if len(failures) != 0 {
			t.Fatal(<-failures)
		}
		if didRegister {
			if ack.Status != "unsupported" {
				t.Fatalf("registered run allowed history maintenance: %#v", ack)
			}
		} else if ack.Status != "history_acquired" {
			t.Fatalf("neither registration nor maintenance acquired: %#v", ack)
		}
	}
}

func TestManagerQueryReservationCountsAndConcurrentMaintenanceReplay(t *testing.T) {
	manager := newTestManager(t)
	releaseFirst, err := manager.ReserveChatQuery("chat", "query")
	if err != nil {
		t.Fatal(err)
	}
	releaseSecond, err := manager.ReserveChatQuery("chat", "query")
	if err != nil {
		t.Fatal(err)
	}
	releaseFirst()
	releaseFirst()
	request := contracts.CompactControlRequest{ChatID: "chat", RequestID: "compact", CompactID: "cid"}
	if ack, err := manager.RouteCompactForChat(request); err != nil || ack.Status != "busy" {
		t.Fatalf("duplicate release consumed another admission: %#v, %v", ack, err)
	}
	releaseSecond()
	owner, err := manager.RouteCompactForChat(request)
	if err != nil || owner.Status != "history_acquired" {
		t.Fatalf("owner = %#v, %v", owner, err)
	}
	manager.CompleteChatMaintenance("chat", "wrong", api.CompactResponse{})
	select {
	case <-owner.Handle.Done():
		t.Fatal("wrong request completed maintenance")
	default:
	}
	results := make(chan contracts.ActiveRunCompactAck, 16)
	for i := 0; i < cap(results); i++ {
		go func() {
			ack, _ := manager.RouteCompactForChat(request)
			results <- ack
		}()
	}
	want := api.CompactResponse{Status: "completed", CompactID: "cid"}
	manager.CompleteChatMaintenance("chat", "compact", want)
	for i := 0; i < cap(results); i++ {
		ack := <-results
		if ack.Status != "history_joined" && ack.Status != "completed" {
			t.Fatalf("replay = %#v", ack)
		}
		<-ack.Handle.Done()
		if !reflect.DeepEqual(ack.Handle.Result(), want) {
			t.Fatalf("result = %#v", ack.Handle.Result())
		}
	}
	manager.CompleteChatMaintenance("chat", "compact", api.CompactResponse{Status: "failed"})
	if !reflect.DeepEqual(owner.Handle.Result(), want) {
		t.Fatal("duplicate completion overwrote result")
	}
}
