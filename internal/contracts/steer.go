package contracts

import (
	"fmt"

	"agent-platform/internal/api"
)

func cloneSteerInput(req api.SteerRequest) api.SteerRequest {
	refs := make([]api.Reference, len(req.References))
	for i, ref := range req.References {
		refs[i] = ref
		refs[i].Meta = cloneSystemInitMap(ref.Meta)
		if ref.SizeBytes != nil {
			size := *ref.SizeBytes
			refs[i].SizeBytes = &size
		}
	}
	if len(refs) > 0 {
		req.References = refs
	}
	if len(req.PreparedMessages) > 0 {
		req.PreparedMessages = cloneSystemInitValue(req.PreparedMessages).([]map[string]any)
	}
	return req
}

// SetSteerPreparer binds a native model input preparer using a frozen session.
// File IO runs outside the control lock; enqueue checks the lifecycle again.
func (c *RunControl) SetSteerPreparer(prepare func(api.SteerRequest) (api.SteerRequest, error)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steerPreparer = prepare
}

func (c *RunControl) PrepareAndEnqueueSteer(req api.SteerRequest) (bool, error) {
	if c == nil {
		return false, nil
	}
	c.mu.Lock()
	closed := c.steerClosed || c.interrupted.Load() || c.finished.Load()
	prepare := c.steerPreparer
	c.mu.Unlock()
	if closed {
		return false, nil
	}
	req.PreparedMessages = nil
	if len(req.References) > 0 {
		if prepare == nil {
			return false, fmt.Errorf("image steer is unavailable for this run")
		}
		var err error
		req, err = prepare(req)
		if err != nil {
			c.mu.Lock()
			closed := c.steerClosed || c.interrupted.Load() || c.finished.Load()
			c.mu.Unlock()
			if closed {
				return false, nil
			}
			return false, err
		}
	}
	return c.EnqueueSteer(req), nil
}
