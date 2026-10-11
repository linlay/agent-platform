package platformcontrol

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-platform/internal/contracts"
	"agent-platform/internal/kbases"
	"agent-platform/internal/toolinput"
)

func (h *ToolHandler) ConfigureLibraries(service *kbases.Service) *ToolHandler {
	h.libraries = service
	return h
}

func libraryReviewDefinition(d *kbases.Definition) string {
	if d == nil {
		return ""
	}
	// The generic source review consumes text. Exclude worker state from a
	// configuration comparison, and keep grants in their own explicit fields.
	b, _ := json.MarshalIndent(map[string]any{"name": d.Name, "description": d.Description,
		"collections": d.Collections, "chunk": d.Chunk, "textEncoding": d.TextEncoding,
		"models": d.Models, "retrieval": d.Retrieval}, "", "  ")
	return string(b)
}

func init() {
	argumentFields["kbase_library_query.list"] = map[string]string{"limit": "n", "offset": "n"}
	for _, action := range []string{"get", "status"} {
		argumentFields["kbase_library_query."+action] = map[string]string{"libraryId": "s!"}
	}
	argumentFields["kbase_library_query.validate"] = map[string]string{"definition": "o!"}
	argumentFields["kbase_library_manage.create"] = map[string]string{"definition": "o!", "requestId": "s"}
	argumentFields["kbase_library_manage.update"] = map[string]string{"libraryId": "s!", "baseRevision": "s!", "patch": "o!", "requestId": "s"}
	argumentFields["kbase_library_manage.delete"] = map[string]string{"libraryId": "s!", "baseRevision": "s!", "requestId": "s"}
}

func libraryInvocation(p map[string]any, e *contracts.ExecutionContext) string {
	if e == nil || e.Session.RunID == "" || e.CurrentToolID == "" {
		return ""
	}
	id := stringValue(p, "requestId")
	if id == "" {
		id = e.CurrentToolID
	}
	b, _ := json.Marshal([]string{e.Session.Subject, e.Session.AgentKey, e.Session.RunID, id})
	return string(b)
}

// Nested built-in booleans follow the same exact true/false string compatibility.
func normalizeLibraryBooleans(p map[string]any) error {
	for _, field := range []string{"definition", "patch"} {
		object, _ := p[field].(map[string]any)
		if collections, ok := object["collections"].([]any); ok {
			for _, raw := range collections {
				c, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				fields := map[string]string{"editable": "b", "defaultQuery": "b"}
				toolinput.NormalizeBooleanFields(c, fields)
				for key, kind := range fields {
					if value, exists := c[key]; exists {
						if err := toolinput.Validate(map[string]any{key: value}, map[string]string{key: kind}, "args."+field+".collections"); err != nil {
							return err
						}
					}
				}
			}
		}
		if retrieval, ok := object["retrieval"].(map[string]any); ok {
			fields := map[string]string{"rerank": "b", "queryExpansion": "b"}
			toolinput.NormalizeBooleanFields(retrieval, fields)
			for key, kind := range fields {
				if value, exists := retrieval[key]; exists {
					if field == "patch" && value == nil {
						continue // null removes an optional retrieval override.
					}
					if err := toolinput.Validate(map[string]any{key: value}, map[string]string{key: kind}, "args."+field+".retrieval"); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (h *ToolHandler) prepareLibraryApproval(_ context.Context, tool string, args map[string]any, e *contracts.ExecutionContext) (*contracts.ToolApproval, error) {
	_, p, err := h.admitted(tool, args, e)
	if err != nil {
		return nil, err
	}
	if h.libraries == nil || h.sources == nil || h.sources.Mutations == nil {
		return nil, fmt.Errorf("knowledge library management unavailable")
	}
	if err := normalizeLibraryBooleans(p); err != nil {
		return nil, err
	}
	action := stringValue(args, "action")
	unlock := h.sources.Mutations.LockAgentMutation()
	defer unlock()
	plan, err := h.libraries.PrepareControl(action, p, libraryInvocation(p, e))
	if err != nil {
		return nil, err
	}
	if plan.Reused || plan.Completed {
		return nil, nil // Returning an existing resource grants no source mutation.
	}
	key := ""
	if plan.After != nil {
		key = plan.After.ID
	}
	if plan.Before != nil {
		key = plan.Before.ID
	}
	return &contracts.ToolApproval{AllowAutoApprove: action == "update" && !plan.SourceGrantChanged,
		Fingerprint: contracts.ToolApprovalFingerprint(e, tool, action, plan.Digest),
		Title:       "Knowledge library / " + action,
		Form: map[string]any{"action": action, "resourceType": "knowledge_library", "resourceKey": key,
			"before": libraryReviewDefinition(plan.Before), "after": libraryReviewDefinition(plan.After),
			"baseRevision": plan.Revision, "sourcePermissions": plan.SourcePermissions, "sourceGrantChanged": plan.SourceGrantChanged}}, nil
}

func (h *ToolHandler) invokeLibrary(ctx context.Context, tool, action string, p map[string]any, e *contracts.ExecutionContext) (any, error) {
	if h.libraries == nil {
		return nil, fmt.Errorf("knowledge library management unavailable")
	}
	if err := normalizeLibraryBooleans(p); err != nil {
		return nil, err
	}
	if tool == "kbase_library_manage" {
		if h.sources == nil || h.sources.Mutations == nil {
			return nil, fmt.Errorf("knowledge library mutation coordination unavailable")
		}
		unlock := h.sources.Mutations.LockAgentMutation()
		defer unlock()
		return h.libraries.ExecuteControl(action, p, libraryInvocation(p, e), func(digest string) bool {
			return contracts.ConsumeToolApproval(e, contracts.ToolApprovalFingerprint(e, tool, action, digest))
		})
	}
	switch action {
	case "get":
		return h.libraries.Get(stringValue(p, "libraryId"))
	case "status":
		return h.libraries.LibraryStatus(ctx, stringValue(p, "libraryId"))
	case "validate":
		plan, err := h.libraries.PrepareControl("create", p, "")
		if err != nil {
			return map[string]any{"valid": false, "diagnostics": []any{candidateError("invalid_knowledge_library", err)}}, nil
		}
		return map[string]any{"valid": true, "definition": plan.After, "reused": plan.Reused}, nil
	case "list":
		limit, offset := 20, 0
		for key, dest := range map[string]*int{"limit": &limit, "offset": &offset} {
			if value, exists := p[key]; exists {
				b, _ := json.Marshal(value)
				var v float64
				if err := json.Unmarshal(b, &v); err != nil {
					return nil, fmt.Errorf("invalid %s", key)
				}
				if v < 0 || v > 1<<30 || v != float64(int(v)) || key == "limit" && (v < 1 || v > 100) {
					return nil, fmt.Errorf("invalid %s", key)
				}
				*dest = int(v)
			}
		}
		list, err := h.libraries.List()
		if err != nil {
			return nil, err
		}
		if offset > len(list) {
			offset = len(list)
		}
		end := offset + limit
		if end > len(list) {
			end = len(list)
		}
		return map[string]any{"items": list[offset:end], "total": len(list), "hasMore": end < len(list), "nextOffset": end}, nil
	}
	return nil, fmt.Errorf("unsupported knowledge library query")
}
