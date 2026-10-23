package contracts

import (
	"strings"
	"sync"
)

// ResultSubmitToolName is the tool through which a Run hands its result to
// the caller. It ends the Run once a submission is accepted.
const ResultSubmitToolName = "result_submit"

func IsResultSubmitToolName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), ResultSubmitToolName)
}

const (
	ResultStatusSuccess = "success"
	ResultStatusFailure = "failure"
)

// ResultSubmission is the accepted result_submit call: success carries the
// output, failure the reason.
type ResultSubmission struct {
	Status string
	Output map[string]any
	Reason string
}

// ResultContract is set on a Run started through /api/execute. Its caller
// owns the output format; the Run owes exactly one accepted result_submit.
// The pointer is shared by every copy of the session, so the tool handler that
// accepts a submission and the Runtime that reads it see the same value.
type ResultContract struct {
	// OutputSchema is the JSON Schema of the success output.
	OutputSchema map[string]any
	// Input is the task data as compact JSON, shown to the model beside the
	// task message. Empty when the caller sent none.
	Input string

	mu         sync.Mutex
	submission *ResultSubmission
}

// Accept records the first submission and reports whether this one was it.
func (c *ResultContract) Accept(submission ResultSubmission) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submission != nil {
		return false
	}
	c.submission = &submission
	return true
}

// Submission returns the accepted submission, or nil before one is accepted.
func (c *ResultContract) Submission() *ResultSubmission {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submission == nil {
		return nil
	}
	out := *c.submission
	return &out
}

// TaskInput returns the task data shown to the model, or "" without a
// contract.
func (c *ResultContract) TaskInput() string {
	if c == nil {
		return ""
	}
	return c.Input
}
