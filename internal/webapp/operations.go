package webapp

import (
	"context"
	"sort"
	"time"
)

type Operation struct {
	OperationID string `json:"operationId"`
	AppID       string `json:"appId,omitempty"`
	Kind        string `json:"kind"`
	State       string `json:"state"`
	CreatedAt   int64  `json:"createdAt"`
	Result      *Entry `json:"result,omitempty"`
	ErrorCode   string `json:"errorCode,omitempty"`
}

func (s *Service) beginOperation(kind, id string, timeout time.Duration, run func(context.Context) (Entry, error)) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutationError(); err != nil {
		return Operation{}, err
	}
	active := 0
	cutoff := time.Now().Add(-time.Hour).UnixMilli()
	for key, op := range s.operations {
		if op.State == "running" || op.State == "queued" {
			active++
		} else if op.CreatedAt < cutoff {
			delete(s.operations, key)
		}
	}
	if active >= 8 {
		return Operation{}, fail("webapp_operations_capacity", 429)
	}
	if len(s.operations) >= 256 {
		oldest := ""
		var when int64
		for key, op := range s.operations {
			if op.State != "queued" && op.State != "running" && (oldest == "" || op.CreatedAt < when) {
				oldest = key
				when = op.CreatedAt
			}
		}
		if oldest != "" {
			delete(s.operations, oldest)
		}
	}
	op := &Operation{OperationID: randomID(), AppID: id, Kind: kind, State: "queued", CreatedAt: nowMillis()}
	s.operations[op.OperationID] = op
	accepted := *op
	s.operationWG.Add(1)
	go func() {
		defer s.operationWG.Done()
		s.mu.Lock()
		op.State = "running"
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(s.operationContext, timeout)
		defer cancel()
		result, err := run(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			op.State = "failed"
			op.ErrorCode, _ = ErrorCode(err)
		} else {
			op.State = "succeeded"
			op.AppID = result.ID
			op.Result = &result
		}
	}()
	return accepted, nil
}
func (s *Service) SubmitInstall(req InstallRequest) (Operation, error) {
	return s.beginOperation("install", "", 240*time.Second, func(ctx context.Context) (Entry, error) { return s.Install(ctx, req) })
}
func (s *Service) SubmitState(action, id string, req StateRequest) (Operation, error) {
	if !idPattern.MatchString(id) {
		return Operation{}, fail("webapp_not_found", 404)
	}
	switch action {
	case "start", "stop", "restart":
	default:
		return Operation{}, fail("webapp_operation_invalid", 400)
	}
	return s.beginOperation(action, id, 35*time.Second, func(ctx context.Context) (Entry, error) {
		switch action {
		case "start":
			return s.Start(ctx, id, req)
		case "stop":
			return s.Stop(ctx, id, req)
		default:
			return s.Restart(ctx, id, req)
		}
	})
}
func (s *Service) Operation(id string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.operations[id]
	if op == nil {
		return Operation{}, fail("webapp_operation_not_found", 404)
	}
	return *op, nil
}
func (s *Service) Operations() []Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Operation{}
	for _, op := range s.operations {
		result = append(result, *op)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	return result
}
