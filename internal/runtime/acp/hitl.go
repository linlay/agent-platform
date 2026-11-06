package acp

import (
	"agent-platform/internal/agentengine"
	"agent-platform/internal/contracts"
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/view"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

func externalInteraction(ctx context.Context, in Incoming, req runtimetypes.QueryCommand, session contracts.QuerySession, emit func(contracts.AgentDelta)) (any, error) {
	control := contracts.RunControlFromContext(ctx)
	if control == nil || req.Origin != nil && req.Origin.Type == queryinput.OriginAutomation {
		return nil, agentengine.Error("interaction_required")
	}
	await := contracts.AwaitingSubmitContext{}
	title := ""
	permissionChoices := map[string]string{}
	approvals := []any{}
	toolDescription := ""
	var params struct{ SessionID string }
	if json.Unmarshal(in.Params, &params) != nil {
		return nil, agentengine.Error("elicitation_unsupported")
	}
	switch in.Method {
	case "session/request_permission":
		var permission struct {
			ToolCall struct {
				Title     string
				Locations []struct{ Path string }
			}
			Options []struct{ OptionID, Name, Kind string }
		}
		if json.Unmarshal(in.Params, &permission) != nil || len(permission.Options) > 64 {
			return nil, agentengine.Error("invalid_permission_option")
		}
		var rawRequest struct {
			ToolCall json.RawMessage `json:"toolCall"`
		}
		json.Unmarshal(in.Params, &rawRequest)
		toolDescription = string(rawRequest.ToolCall)
		title = permission.ToolCall.Title
		// The upstream approval title can be generic ("Edit files"). Keep the
		// advertised targets visible through the existing awaiting title.
		for _, location := range permission.ToolCall.Locations {
			if location.Path != "" {
				title += "\n" + location.Path
			}
		}
		await.Mode = "approval"
		seen := map[string]bool{}
		for _, option := range permission.Options {
			if option.OptionID == "" || option.Name == "" || seen[option.OptionID] {
				return nil, agentengine.Error("invalid_permission_option")
			}
			seen[option.OptionID] = true
			decision := ""
			switch option.Kind {
			case "allow_once":
				decision = "approve"
			case "reject_once":
				decision = "reject"
			}
			if decision != "" {
				if permissionChoices[decision] != "" {
					return nil, agentengine.Error("capability_unsupported")
				}
				permissionChoices[decision] = option.OptionID
				if decision == "approve" {
					title += "\n" + option.Name
				}
			}
		}
		// An allow-always option cannot be represented by a one-time approval.
		if permissionChoices["approve"] == "" {
			return nil, agentengine.Error("capability_unsupported")
		}

	case "elicitation/create":
		var err error
		await.Questions, err = elicitationQuestions(in.Params)
		if err != nil {
			return nil, err
		}
		await.Mode = "question"
	default:
		return nil, agentengine.Error("interaction_required")
	}
	deadline := time.Now().Add(5 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	id := "external_" + rand.Text()
	if await.Mode == "approval" {
		approvals = []any{map[string]any{"id": id, "description": title, "command": toolDescription, "options": []any{map[string]any{"decision": "approve"}, map[string]any{"decision": "reject"}}, "allowFreeText": false}}
	}
	await.AwaitingID = id
	// The RPC identity and upstream options stay in this invocation. The common
	// waiter sees only a neutral submit validator, never an ACP DTO.
	await.ValidateSubmit = func(submit queryinput.SubmitRequest) error {
		if ctx.Err() != nil || time.Now().After(deadline) {
			return fmt.Errorf("awaiting_expired")
		}
		if submit.OwnerID != session.Subject || submit.RunID != req.RunID || submit.ChatID != "" && submit.ChatID != req.ChatID {
			return fmt.Errorf("awaiting_forbidden")
		}
		if submit.SubmitID == "" {
			return fmt.Errorf("submitId is required")
		}
		if await.Mode == "approval" {
			if submit.Param != nil || submit.Params == nil {
				return fmt.Errorf("invalid_permission_option")
			}
			if len(submit.Params) == 0 {
				return nil
			}
			items, err := queryinput.DecodeSubmitParams(submit.Params)
			if err != nil || len(items) != 1 || items[0]["id"] != id || (items[0]["decision"] != "approve" && items[0]["decision"] != "reject") {
				return fmt.Errorf("invalid_permission_option")
			}
			return nil
		}
		return validateQuestionSubmit(await.Questions, submit)
	}
	control.ExpectSubmit(await)
	defer control.ClearExpectedSubmit(id)
	ask := contracts.DeltaAwaitAsk{AwaitingID: id, Mode: await.Mode, RunID: req.RunID, Approvals: approvals, Questions: projectQuestions(await.Questions), Timeout: int64(time.Until(deadline).Seconds())}
	if await.Mode == "question" {
		ask.View = view.Builtin("question")
	}
	emit(ask)
	result, err := control.AwaitSubmitWithTimeout(ctx, id, time.Until(deadline))
	if err != nil {
		return nil, err
	}
	input := result.Request.Input()
	var reply any
	answer := map[string]any{"mode": await.Mode, "status": "answered", "runId": req.RunID, "submitId": result.Request.SubmitID}
	if await.Mode == "question" {
		input = result.Request.Params
		content := map[string]string{}
		answers := []any{}
		for _, raw := range result.Request.Params {
			var value struct{ ID, Answer string }
			json.Unmarshal(raw, &value)
			content[value.ID] = value.Answer
			answers = append(answers, map[string]any{"id": value.ID, "answer": value.Answer})
		}
		action := "accept"
		if len(result.Request.Params) == 0 {
			action = "cancel"
		}
		reply = map[string]any{"action": action, "content": content}
		answer["answers"] = answers
	} else {
		outcome := map[string]any{"outcome": "cancelled"}
		items, _ := queryinput.DecodeSubmitParams(result.Request.Params)
		if len(items) == 1 {
			decision, _ := items[0]["decision"].(string)
			if optionID := permissionChoices[decision]; optionID != "" {
				outcome = map[string]any{"outcome": "selected", "optionId": optionID}
			}
			answer["approvals"] = items
		} else {
			answer = contracts.AwaitingErrorAnswer("approval", "user_dismissed", "用户关闭等待项")
			answer["runId"], answer["submitId"] = req.RunID, result.Request.SubmitID
		}
		reply = map[string]any{"outcome": outcome}
	}
	emit(contracts.DeltaRequestSubmit{ChatID: req.ChatID, RunID: req.RunID, AwaitingID: id, SubmitID: result.Request.SubmitID, Input: input})
	emit(contracts.DeltaAwaitingAnswer{AwaitingID: id, Answer: answer})
	return reply, nil
}

// Project only schema semantics the existing question UI can faithfully express.
func elicitationQuestions(raw json.RawMessage) ([]any, error) {
	unsupported := agentengine.Error("elicitation_unsupported")
	var request struct {
		Mode, Message   string
		RequestedSchema map[string]json.RawMessage
	}
	if json.Unmarshal(raw, &request) != nil || request.Mode != "form" {
		return nil, unsupported
	}
	schema := request.RequestedSchema
	for key := range schema {
		switch key {
		case "type", "properties", "required", "title", "description", "$schema", "additionalProperties":
		default:
			return nil, unsupported
		}
	}
	if v, ok := schema["additionalProperties"]; ok && string(v) != "false" {
		return nil, unsupported
	}
	var kind string
	var properties map[string]map[string]json.RawMessage
	var required []string
	if json.Unmarshal(schema["type"], &kind) != nil || kind != "object" || json.Unmarshal(schema["properties"], &properties) != nil || len(properties) == 0 || len(properties) > 32 {
		return nil, unsupported
	}
	if v, ok := schema["required"]; ok && json.Unmarshal(v, &required) != nil {
		return nil, unsupported
	}
	needs := map[string]bool{}
	for _, id := range required {
		if properties[id] == nil || needs[id] {
			return nil, unsupported
		}
		needs[id] = true
	}
	ids := make([]string, 0, len(properties))
	for id := range properties {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	questions := []any{}
	for _, id := range ids {
		// The existing AGW question contract requires an answer for each field.
		// Do not turn optional ACP fields into mandatory UI questions.
		if !needs[id] {
			return nil, unsupported
		}
		field := properties[id]
		for key := range field {
			switch key {
			case "type", "title", "description", "enum", "enumNames", "oneOf", "_meta":
			default:
				return nil, unsupported
			}
		}
		// ACP display metadata is not a JSON Schema constraint. Secret-input
		// hints need a dedicated UI, so do not silently render those as plain text.
		if raw, ok := field["_meta"]; ok {
			var meta map[string]json.RawMessage
			if json.Unmarshal(raw, &meta) != nil || meta == nil {
				return nil, unsupported
			}
			// Recognize only the verified hint shape, independent of vendor namespace.
			// Unknown hints can carry input constraints; do not silently ignore them.
			for _, raw := range meta {
				var hints map[string]json.RawMessage
				if json.Unmarshal(raw, &hints) != nil || hints == nil {
					return nil, unsupported
				}
				for key, raw := range hints {
					var flag *bool
					if (key != "isSecret" && key != "isOther") || json.Unmarshal(raw, &flag) != nil || flag == nil || (key == "isSecret" && *flag) {
						return nil, unsupported
					}
				}
			}

		}
		if json.Unmarshal(field["type"], &kind) != nil || kind != "string" {
			return nil, unsupported
		}
		title := id
		if v, ok := field["title"]; ok {
			if json.Unmarshal(v, &title) != nil {
				return nil, unsupported
			}
		}
		if raw, ok := field["description"]; ok {
			var description string
			if json.Unmarshal(raw, &description) != nil {
				return nil, unsupported
			}
			if description != "" {
				title += "\n" + description
			}
		}
		if request.Message != "" {
			title = request.Message + "\n" + title
		}
		question := map[string]any{"id": id, "question": title, "required": needs[id], "type": "text"}
		options := []any{}
		if v, ok := field["enum"]; ok {
			var values, labels []string
			if json.Unmarshal(v, &values) != nil || len(values) == 0 {
				return nil, unsupported
			}
			if v, ok := field["enumNames"]; ok {
				if json.Unmarshal(v, &labels) != nil || len(labels) != len(values) {
					return nil, unsupported
				}
			}
			for i, value := range values {
				label := value
				if labels != nil {
					label = labels[i]
				}
				options = append(options, map[string]any{"id": value, "label": label})
			}
		} else if _, ok := field["enumNames"]; ok {
			return nil, unsupported
		}
		if v, ok := field["oneOf"]; ok {
			if len(options) > 0 {
				return nil, unsupported
			}
			var values []map[string]json.RawMessage
			if json.Unmarshal(v, &values) != nil || len(values) == 0 {
				return nil, unsupported
			}
			for _, value := range values {
				for key := range value {
					if key != "const" && key != "title" && key != "description" {
						return nil, unsupported
					}
				}
				var id, label string
				if json.Unmarshal(value["const"], &id) != nil {
					return nil, unsupported
				}
				label = id
				if v, ok := value["title"]; ok && json.Unmarshal(v, &label) != nil {
					return nil, unsupported
				}
				if raw, ok := value["description"]; ok {
					var description string
					if json.Unmarshal(raw, &description) != nil {
						return nil, unsupported
					}
					if description != "" {
						label += "\n" + description
					}
				}
				options = append(options, map[string]any{"id": id, "label": label})
			}
		}
		if len(options) > 0 {
			seen := map[string]bool{}
			for _, raw := range options {
				id := raw.(map[string]any)["id"].(string)
				if seen[id] {
					return nil, unsupported
				}
				seen[id] = true
			}
			question["type"], question["options"] = "select", options
		}
		data, _ := json.Marshal(question)
		if dto.Validate("ExternalQuestion", data) != nil {
			return nil, unsupported
		}
		questions = append(questions, question)
	}
	return questions, nil
}

// Keep schema constraints and upstream IDs private; AGW already has option values.
func projectQuestions(questions []any) []any {
	result := make([]any, 0, len(questions))
	for _, raw := range questions {
		q := contracts.CloneMap(raw.(map[string]any))
		delete(q, "required")
		if options, ok := q["options"].([]any); ok {
			projected := make([]any, 0, len(options))
			for _, raw := range options {
				option := contracts.CloneMap(raw.(map[string]any))
				option["value"] = option["id"]
				delete(option, "id")
				projected = append(projected, option)
			}
			q["options"] = projected
		}
		result = append(result, q)
	}
	return result
}
