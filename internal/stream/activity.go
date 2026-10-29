package stream

import (
	"strings"

	"agent-platform/internal/i18n"
)

// run.activity tells a viewer, in one sentence, what the Run (or one
// of its subtasks, by taskId) is doing now. A newer activity replaces the
// older one of the same scope; an empty message clears it while the user
// reads visible output (content or a plan), and a Run terminal event clears
// them all. Progress activities carry an internal "activity" template that
// the output boundary renders in the viewer's locale, plus its kind at the
// top level so a viewer or broker can tell an announcement apart after
// rendering; a model retry carries "retry" instead.

// ActivityKindAnnounce is a progress activity the model published through an
// announcing tool: what the Run does next, in the model's own words.
const ActivityKindAnnounce = "announce"

func newActivityEvent(runID, chatID, taskID string, activity map[string]any) StreamEvent {
	payload := map[string]any{
		"runId":    runID,
		"chatId":   chatID,
		"kind":     anyString(activity["kind"]),
		"message":  i18n.ActivityMessage(i18n.DefaultLocale, activity),
		"activity": clonePayload(activity),
	}
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		payload["taskId"] = taskID
	}
	return NewEvent("run.activity", payload)
}

func newRetryActivityEvent(runID, chatID, taskID string, retry map[string]any) StreamEvent {
	payload := map[string]any{
		"runId":   runID,
		"chatId":  chatID,
		"message": i18n.RetryActivityMessage(i18n.DefaultLocale, retry),
		"retry":   retry,
	}
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		payload["taskId"] = taskID
	}
	return NewEvent("run.activity", payload)
}

// IsProgressActivity reports a run.activity that says what the Run is doing
// now. It is delivered with Seq 0: the next one replaces it, so it takes no
// place in the replay log and is resent on attach instead.
func IsProgressActivity(eventType string, payload map[string]any) bool {
	return eventType == "run.activity" && !IsRetryActivity(eventType, payload)
}

// IsRetryActivity reports a run.activity that describes a model retry.
func IsRetryActivity(eventType string, payload map[string]any) bool {
	if eventType != "run.activity" {
		return false
	}
	_, ok := payload["retry"].(map[string]any)
	return ok
}

// IsAnnounceActivity reports a progress activity that announces next steps.
func IsAnnounceActivity(eventType string, payload map[string]any) bool {
	return IsProgressActivity(eventType, payload) && activityKind(payload) == ActivityKindAnnounce
}

// activityKind reads the kind from the template, or from the top level of an
// activity already rendered upstream.
func activityKind(payload map[string]any) string {
	if activity, ok := payload["activity"].(map[string]any); ok {
		return anyString(activity["kind"])
	}
	return anyString(payload["kind"])
}

// ActivityTracker derives progress activities from published events for full
// streams. Delivery filtering keeps only announcements and clears for summary.
type ActivityTracker struct {
	runID  string
	chatID string
	last   map[string]map[string]any
	// beforeAwait keeps what a scope was doing when it started waiting for
	// the user; an approved tool continues without a new start event.
	beforeAwait map[string]map[string]any
}

// NewActivityTracker creates a tracker without delaying or suppressing activities.
func NewActivityTracker(runID, chatID string) *ActivityTracker {
	return &ActivityTracker{
		runID: runID, chatID: chatID,
		last: map[string]map[string]any{}, beforeAwait: map[string]map[string]any{},
	}
}

// Track keeps the original event and returns the activities it implies, to be
// published right after it.
func (t *ActivityTracker) Track(eventType string, payload map[string]any) (bool, []StreamEvent) {
	if t == nil {
		return true, nil
	}
	taskID := strings.TrimSpace(anyString(payload["taskId"]))
	switch eventType {
	case "run.activity":
		if IsRetryActivity(eventType, payload) {
			// Retries pass independently of the current progress state.
			delete(t.last, taskID)
			return true, nil
		}
		if activity, ok := payload["activity"].(map[string]any); ok {
			t.last[taskID] = activity
			return true, nil
		}
		// An activity rendered upstream always passes.
		delete(t.last, taskID)
		return true, nil
	case "run.complete", "run.cancel", "run.error":
		if taskID == "" {
			t.last = map[string]map[string]any{}
			t.beforeAwait = map[string]map[string]any{}
		}
		return true, nil
	case "task.complete", "task.cancel", "task.error":
		delete(t.last, taskID)
		delete(t.beforeAwait, taskID)
		return true, nil
	case "awaiting.ask":
		if previous := t.last[taskID]; previous != nil && anyString(previous["kind"]) != "awaiting" {
			t.beforeAwait[taskID] = previous
		}
	}
	var activity map[string]any
	if eventType == "awaiting.answer" {
		activity = t.beforeAwait[taskID]
		delete(t.beforeAwait, taskID)
	} else {
		activity = derivedActivity(eventType, payload)
	}
	if activity == nil {
		return true, nil
	}
	if eventType == "task.start" {
		// Delegation is what the parent is doing; the subtask reports its own.
		taskID = ""
	}
	t.last[taskID] = activity
	chatID := t.chatID
	if chatID == "" {
		chatID = anyString(payload["chatId"])
	}
	runID := t.runID
	if runID == "" {
		runID = anyString(payload["runId"])
	}
	return true, []StreamEvent{newActivityEvent(runID, chatID, taskID, activity)}
}

func derivedActivity(eventType string, payload map[string]any) map[string]any {
	switch eventType {
	case "reasoning.start":
		return map[string]any{"kind": "reasoning", "reasoningId": anyString(payload["reasoningId"])}
	case "content.start", "planning.start":
		// The output itself shows progress; clear the line instead.
		return map[string]any{"kind": "idle"}
	case "context.compact.start":
		return map[string]any{"kind": "compacting"}
	case "awaiting.ask":
		return map[string]any{"kind": "awaiting", "mode": anyString(payload["mode"])}
	case "task.start":
		return map[string]any{"kind": "subtask", "taskName": anyString(payload["taskName"])}
	}
	return nil
}
