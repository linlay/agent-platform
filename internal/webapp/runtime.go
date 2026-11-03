package webapp

import "context"

// RuntimeSpec is trusted, immutable host input, never a request DTO. Transport
// adapters only receive Scope; no application can supply roots or credentials.
type RuntimeSpec struct {
	ID, Generation, CodeRoot, DataRoot, ManagementRoot, InstallationID string
	Manifest                                                           Manifest
	UserConfig                                                         map[string]any
	Policy                                                             Policy
	OnUnavailable                                                      func(string)
	Check                                                              func() error
	Context                                                            context.Context
}
type ProcessExit struct {
	Code   int
	Clean  bool
	Reason string
}
type Process interface {
	Address() string
	Done() <-chan struct{}
	Exit() ProcessExit
	Stop(context.Context) error
}
type LogPage struct {
	Text   string `json:"text"`
	Cursor string `json:"cursor"`
	Reset  bool   `json:"reset"`
}
type Runner interface {
	Ready() bool
	Start(context.Context, RuntimeSpec) (Process, error)
	Logs(id, stream, cursor string) (LogPage, error)
}

func (s *Service) Logs(id, stream, cursor string) (LogPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records[id] == nil {
		return LogPage{}, fail("webapp_not_found", 404)
	}
	if stream != "stdout" && stream != "stderr" {
		return LogPage{}, fail("webapp_log_stream_invalid", 400)
	}
	if s.runner == nil {
		return LogPage{Text: "", Cursor: ""}, nil
	}
	return s.runner.Logs(id, stream, cursor)
}

// InstanceCheck also permits a backend to bootstrap while health is starting;
// page traffic still requires Acquire and the stricter running-state Check.
func (s *Service) instanceCheck(id, generation, policy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.active[id]
	if s.closed || s.closing || a == nil || a.generation != generation || a.ctx.Err() != nil || s.policies[id].Revision != policy || (a.state != "starting" && a.state != "running") {
		return fail("app_grant_required", 403)
	}
	return nil
}
