package types

import (
	"context"
	"encoding/json"
	"sync"

	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
)

type RunRef struct {
	RunID    string
	ChatID   string
	AgentKey string
	TeamID   string
	Caller   Caller
}

type RunHandle struct {
	RunID     string
	ChatID    string
	AgentKey  string
	TeamID    string
	StartedAt int64
	LastSeq   int64
	Status    string
	Detached  bool
}

type Subscription struct {
	ID     string
	Events <-chan stream.EventData

	once  sync.Once
	close func()
}

func NewSubscription(id string, events <-chan stream.EventData, close func()) *Subscription {
	return &Subscription{ID: id, Events: events, close: close}
}

func (s *Subscription) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.close != nil {
			s.close()
		}
	})
}

type SubmitCommand struct {
	RunRef
	AwaitingID        string
	SubmitID          string
	Locale            string
	Params            []json.RawMessage
	ContinuationRunID string
	ContinuationState any
}

type SubmitResult struct {
	Accepted   bool
	Status     string
	ChatID     string
	RunID      string
	AwaitingID string
	SubmitID   string
	Continued  bool
	ErrorCode  string
	Detail     string
}

type SteerCommand struct {
	RunRef
	RequestID  string
	SteerID    string
	Message    string
	References []Reference
}

type SteerResult struct {
	Accepted bool
	Status   string
	RunID    string
	SteerID  string
	Detail   string
}

type InterruptCommand struct {
	RunRef
	RequestID string
	Message   string
	Source    string
	Reason    string
	Detail    string
}

type InterruptResult struct {
	Accepted bool
	Status   string
	RunID    string
	Detail   string
}

type AccessLevelCommand struct {
	RunRef
	RequestID   string
	AccessLevel string
	Reason      string
}

type AccessLevelResult struct {
	Accepted            bool
	Status              string
	RunID               string
	PreviousAccessLevel string
	AccessLevel         string
	Version             int64
	Detail              string
}

type EventSink interface {
	Emit(context.Context, stream.EventData) error
}

type RunStartSink interface {
	OnRunStarted(chat.RunStart)
}
