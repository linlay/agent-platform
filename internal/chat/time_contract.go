package chat

import (
	"fmt"
	"strings"

	"agent-platform/internal/timecontract"
)

// validatePersistedTimeContract validates current chat JSONL time fields. It
// deliberately validates the original decoded values before any
// replay helper can inherit a line timestamp or invent a replacement.  The
// caller must return the resulting Violation unchanged so HTTP/WS can expose
// field, location and expected to the client as time_contract_violation.
func validatePersistedTimeContract(lines []map[string]any, baseLocation string) error {
	for index, line := range lines {
		location := fmt.Sprintf("%s[%d]", strings.TrimSpace(baseLocation), index)
		lineType := strings.TrimSpace(stringFromAny(line["_type"]))
		switch lineType {
		case "query", StepLineTypeReact, StepLineTypeReactTool, "event", "submit", "steer", CompactCheckpointLineType, ToolCompactLineType:
			if err := requirePersistedEpochMillis(line, "updatedAt", location); err != nil {
				return err
			}
		}

		if lineType == "event" {
			event, ok := line["event"].(map[string]any)
			if !ok {
				return missingPersistedTime("timestamp", location+".event")
			}
			if err := requirePersistedEpochMillis(event, "timestamp", location+".event"); err != nil {
				return err
			}
		}
		if err := validatePersistedAwaiting(line["awaiting"], location+".awaiting"); err != nil {
			return err
		}
		if err := validatePersistedSubmitPayload(line["submit"], location+".submit"); err != nil {
			return err
		}
		if err := validatePersistedSubmitPayload(line["answer"], location+".answer"); err != nil {
			return err
		}
		if err := validatePersistedStepMessages(line["messages"], location+".messages"); err != nil {
			return err
		}
		if err := validatePersistedSources(line["sources"], location+".sources"); err != nil {
			return err
		}
	}
	return nil
}

func missingPersistedTime(field string, location string) error {
	return &timecontract.Violation{Field: field, Location: location + "." + field, Reason: "is required"}
}

func requirePersistedEpochMillis(object map[string]any, field string, location string) error {
	value, ok := object[field]
	if !ok {
		return missingPersistedTime(field, location)
	}
	_, err := timecontract.ParseEpochMillis(value, field, location+"."+field)
	return err
}

// optionalPersistedEpochMillis keeps an omitted optional instant omitted and
// validates an explicitly present value against the current time contract.
func optionalPersistedEpochMillis(object map[string]any, field string, location string) error {
	value, ok := object[field]
	if !ok {
		return nil
	}
	_, err := timecontract.ParseEpochMillis(value, field, location+"."+field)
	return err
}

func validatePersistedAwaiting(raw any, location string) error {
	for index, item := range toMapSlice(raw) {
		if len(item) == 0 {
			continue
		}
		if err := requirePersistedEpochMillis(item, "timestamp", fmt.Sprintf("%s[%d]", location, index)); err != nil {
			return err
		}
	}
	return nil
}

func validatePersistedSubmitPayload(raw any, location string) error {
	item, ok := raw.(map[string]any)
	if !ok || len(item) == 0 {
		return nil
	}
	return requirePersistedEpochMillis(item, "timestamp", location)
}

func validatePersistedStepMessages(raw any, location string) error {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || len(item) == 0 {
			continue
		}
		if err := requirePersistedEpochMillis(item, "ts", fmt.Sprintf("%s[%d]", location, index)); err != nil {
			return err
		}
	}
	return nil
}

func validatePersistedSources(raw any, location string) error {
	var items []map[string]any
	switch typed := raw.(type) {
	case *SourceState:
		if typed != nil {
			items = typed.Items
		}
	case SourceState:
		items = typed.Items
	case map[string]any:
		items = toMapSlice(typed["items"])
	}
	for index, item := range items {
		if len(item) == 0 {
			continue
		}
		if err := optionalPersistedEpochMillis(item, "timestamp", fmt.Sprintf("%s.items[%d]", location, index)); err != nil {
			return err
		}
	}
	return nil
}

// validateArchivedSummaryTimeContract is deliberately stricter than the
// SQLite schema defaults. Archive summary instants are public API fields, so
// zero, seconds, and string-derived values must be rejected rather than
// inferred from updatedAt, an ID, or a related run row.
func validateArchivedSummaryTimeContract(summary ArchivedSummary, location string) error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{name: "createdAt", value: summary.CreatedAt},
		{name: "updatedAt", value: summary.UpdatedAt},
		{name: "lastRunAt", value: summary.LastRunAt},
		{name: "archivedAt", value: summary.ArchivedAt},
	} {
		if err := timecontract.ValidateEpochMillis(field.value, field.name, location+"."+field.name); err != nil {
			return err
		}
	}
	if summary.Read.ReadAt != nil {
		if err := timecontract.ValidateEpochMillis(*summary.Read.ReadAt, "readAt", location+".readAt"); err != nil {
			return err
		}
	}
	return nil
}

func validateArchivedRunTimeContract(run RunSummary, location string) error {
	if err := timecontract.ValidateEpochMillis(run.StartedAt, "startedAt", location+".startedAt"); err != nil {
		return err
	}
	if err := timecontract.ValidateEpochMillis(run.CompletedAt, "completedAt", location+".completedAt"); err != nil {
		return err
	}
	if run.FeedbackAt != 0 {
		if err := timecontract.ValidateEpochMillis(run.FeedbackAt, "feedbackAt", location+".feedbackAt"); err != nil {
			return err
		}
	}
	return nil
}

func validateArchivedChatTimeContract(archived ArchivedChat, location string) error {
	if err := validateArchivedSummaryTimeContract(archived.Summary, location+".summary"); err != nil {
		return err
	}
	for index, run := range archived.Runs {
		if err := validateArchivedRunTimeContract(run, fmt.Sprintf("%s.runs[%d]", location, index)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(archived.JSONLContent) == "" {
		return nil
	}
	lines, err := readJSONLinesContent(archived.JSONLContent)
	if err != nil {
		return err
	}
	return validatePersistedTimeContract(lines, location+".jsonl")
}

func validateArchiveSearchHitTimeContract(hit ArchiveSearchHit, location string) error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{name: "createdAt", value: hit.CreatedAt},
		{name: "lastRunAt", value: hit.LastRunAt},
		{name: "archivedAt", value: hit.ArchivedAt},
	} {
		if err := timecontract.ValidateEpochMillis(field.value, field.name, location+"."+field.name); err != nil {
			return err
		}
	}
	return nil
}

// validateActiveSummaryTimeContract guards active SQLite records before they
// are returned or used as the basis for a mutation. LastRunAt is deliberately
// absent here: it is an internal archive handoff value and legitimately zero
// for chats that have never completed a run.
func validateActiveSummaryTimeContract(summary Summary, location string) error {
	if err := timecontract.ValidateEpochMillis(summary.CreatedAt, "createdAt", location+".createdAt"); err != nil {
		return err
	}
	if err := timecontract.ValidateEpochMillis(summary.UpdatedAt, "updatedAt", location+".updatedAt"); err != nil {
		return err
	}
	if summary.Read.ReadAt != nil {
		if err := timecontract.ValidateEpochMillis(*summary.Read.ReadAt, "readAt", location+".readAt"); err != nil {
			return err
		}
	}
	if summary.PendingAwaiting != nil {
		if err := timecontract.ValidateEpochMillis(summary.PendingAwaiting.CreatedAt, "createdAt", location+".pendingAwaiting.createdAt"); err != nil {
			return err
		}
	}
	return nil
}

func validateActiveRunTimeContract(run RunSummary, location string) error {
	return validateArchivedRunTimeContract(run, location)
}

func validateRunCompletionTimeContract(completion RunCompletion, location string) error {
	if err := timecontract.ValidateEpochMillis(completion.StartedAtMillis, "startedAt", location+".startedAt"); err != nil {
		return err
	}
	return timecontract.ValidateEpochMillis(completion.UpdatedAtMillis, "completedAt", location+".completedAt")
}
