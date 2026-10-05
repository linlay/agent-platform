package platformcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/agentcreation"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/conversation"
	"agent-platform/internal/filetools"
	"agent-platform/internal/models"
	"agent-platform/internal/toolinput"
)

func (h *ToolHandler) ConfigureControl(sources *adminsource.ControlService, chats *conversation.Service, modelRegistry *models.ModelRegistry) *ToolHandler {
	h.sources = sources
	h.conversations = chats
	h.models = modelRegistry
	return h
}
func rootCaller(e *contracts.ExecutionContext) bool {
	return e != nil && contracts.OrdinaryNativeRoot(e.Session)
}
func controlCaller(e *contracts.ExecutionContext) conversation.ControlCaller {
	return conversation.ControlCaller{Subject: e.Session.Subject, ChatID: e.Session.ChatID, AgentKey: e.Session.AgentKey, RunID: e.Session.RunID, ToolID: e.CurrentToolID}
}
func controlFail(code, message, stage string) contracts.ToolExecutionResult {
	r := errorResult(code, message)
	r.Structured["stage"] = stage
	r.Structured["executionState"] = "not_started"
	r.Structured["recovery"] = map[string]any{"strategy": "fix_input", "message": message}
	r.Output = contracts.CompactToolModelOutput(r.Structured, "")
	return r
}

var argumentFields = map[string]map[string]string{
	"catalog_query.resourceTypes": {},
	"catalog_query.list":          {"resourceType": "s!", "status": "s", "limit": "n", "cursor": "s"},
	"catalog_query.get":           {"resourceType": "s!", "resourceKey": "s!", "path": "s"},
	"catalog_query.defaults":      {"type": "s!"},
	"catalog_query.validate":      {"resourceType": "s!", "resourceKey": "s!", "content": "s!", "path": "s", "mcpUrl": "s"},
	"catalog_manage.apply":        {"resourceType": "s!", "resourceKey": "s!", "path": "s", "content": "s!", "baseRevision": "s", "preservePaths": "a", "mcpUrl": "s"},
	"catalog_manage.delete":       {"resourceType": "s!", "resourceKey": "s!", "baseRevision": "s!"},
	"chat_query.current":          {},
	"chat_query.list":             {"scope": "s", "archived": "b", "pinned": "b", "limit": "n", "cursor": "s"},
	"chat_query.search":           {"query": "s!", "scope": "s", "chatId": "s", "archived": "b", "limit": "n", "cursor": "s"},
	"chat_query.read":             {"chatId": "s!", "archived": "b", "view": "s!", "limit": "n", "cursor": "s"},
	"chat_query.artifacts":        {"chatId": "s", "runId": "s", "limit": "n", "cursor": "s"},
	"chat_manage.rename":          {"chatId": "s", "chatName": "s!"},
	"chat_manage.setPinned":       {"chatId": "s", "pinned": "b!"},
	"chat_manage.archive":         {"chatId": "s", "chatIds": "a"}, "chat_manage.restore": {"chatId": "s", "chatIds": "a"},
	"chat_manage.fork":                 {"sourceChatId": "s!", "sourceRunId": "s", "chatName": "s"},
	"chat_manage.export":               {"chatId": "s!", "archived": "b", "format": "s!"},
	"chat_manage.delete":               {"chatId": "s!", "archived": "b"},
	"platform_inspect.runtimeStatus":   {"component": "s"},
	"platform_inspect.securityExplain": {"path": "s", "access": "s", "tool": "s", "action": "s"},
}

func (h *ToolHandler) admitted(tool string, args map[string]any, e *contracts.ExecutionContext) (string, map[string]any, error) {
	if e == nil || e.Session.NativeConnectorTools[tool] != connector.PlatformControlConnectorID || e.Session.ConnectorDirs[connector.PlatformControlConnectorID] == "" {
		return "", nil, fmt.Errorf("connector_not_mounted: configure builtin.platform-control for this Agent through an authorized configuration change, then start a new Run")
	}
	if connector.IsPlatformRootTool(tool) && !rootCaller(e) {
		return "", nil, fmt.Errorf("caller_forbidden: ordinary native main root Run required; invoke this tool from the owning Agent main Run, outside Team, subtask or side-chat execution")
	}
	if err := toolinput.Validate(args, map[string]string{"action": "s!", "args": "o"}, ""); err != nil {
		var input *toolinput.Error
		if errors.As(err, &input) && input.Field == "action" {
			v, present := args["action"]
			return "", nil, toolinput.Choice("action", v, present, controlActionNames(tool))
		}
		return "", nil, err
	}
	action := args["action"].(string)
	descriptor, ok := connector.LookupControlAction(tool, action)
	if !ok {
		return "", nil, toolinput.Enum("action", action, controlActionNames(tool))
	}
	if contracts.IsReadOnlyToolExecutionPolicy(e.ToolExecutionPolicy) && !descriptor.ReadOnly {
		return "", nil, fmt.Errorf("stage_forbidden: this action mutates state; retry only in an execution stage that permits mutation")
	}
	params := map[string]any{}
	if raw, exists := args["args"]; exists {
		var ok bool
		params, ok = raw.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("args must be an object")
		}
	}
	fields := argumentFields[tool+"."+action]
	if err := validateControlEnums(tool, action, params); err != nil {
		return "", nil, err
	}
	if err := toolinput.Validate(params, fields, "args."); err != nil {
		return "", nil, err
	}
	if tool == "chat_manage" && (action == "archive" || action == "restore") {
		if _, _, err := conversation.ControlArchiveIDs(params); err != nil {
			return "", nil, toolinput.New("args", "either chatId or chatIds (1–100 distinct valid Chat IDs)", params, true, err.Error()+"; use {\"chatId\":\"<ID from chat_query.list>\"} or {\"chatIds\":[\"<ID from chat_query.list>\"]}.")
		}
	}
	return action, params, nil
}
func controlTarget(p map[string]any) adminsource.ControlTarget {
	return adminsource.ControlTarget{ResourceType: stringValue(p, "resourceType"), ResourceKey: stringValue(p, "resourceKey"), Path: stringValue(p, "path")}
}
func controlChange(action string, p map[string]any) adminsource.ControlChange {
	c := adminsource.ControlChange{ControlTarget: controlTarget(p), Action: action, Content: stringValue(p, "content"), BaseRevision: stringValue(p, "baseRevision"), MCPURL: stringValue(p, "mcpUrl")}
	if paths, ok := p["preservePaths"].([]any); ok {
		for _, p := range paths {
			c.PreservePaths = append(c.PreservePaths, p.(string))
		}
	}
	return c
}
func (h *ToolHandler) PrepareToolApproval(ctx context.Context, tool string, args map[string]any, e *contracts.ExecutionContext) (*contracts.ToolApproval, error) {
	if tool != "catalog_manage" && tool != "chat_manage" {
		return nil, nil
	}
	action, p, err := h.admitted(tool, args, e)
	if err != nil {
		return nil, err
	}
	var digest string
	var form map[string]any
	if tool == "catalog_manage" {
		if h.sources == nil {
			return nil, fmt.Errorf("catalog service unavailable")
		}
		plan, err := h.sources.Prepare(controlChange(action, p), e.Session.AgentKey)
		if err != nil {
			return nil, err
		}
		digest = plan.Digest
		form = map[string]any{"action": action, "resourceType": plan.Change.ResourceType, "resourceKey": plan.Change.ResourceKey, "path": plan.Change.Path, "baseRevision": plan.Change.BaseRevision, "before": plan.Before, "after": plan.After}
		if action == "apply" && plan.Change.ResourceType == "agent" && (plan.Change.Path == "" || plan.Change.Path == "agent.yml") {
			form["permissionFields"] = []string{"toolConfig", "connectorConfig", "skillConfig", "hostAccess", "accessLevel"}
		}
	} else {
		if action != "delete" {
			return nil, nil
		}
		if h.conversations == nil {
			return nil, fmt.Errorf("conversation service unavailable")
		}
		archived, _ := p["archived"].(bool)
		digest, err = h.conversations.ControlDeleteRevision(controlCaller(e), stringValue(p, "chatId"), archived)
		if err != nil {
			return nil, err
		}
		form = map[string]any{"action": action, "resourceType": "chat", "resourceKey": p["chatId"], "archived": archived, "baseRevision": digest}
	}
	return &contracts.ToolApproval{AllowAutoApprove: tool == "catalog_manage" && action == "apply", Fingerprint: contracts.ToolApprovalFingerprint(e, tool, action, digest), Title: tool + " / " + action, Form: form}, nil
}
func (h *ToolHandler) Invoke(ctx context.Context, tool string, args map[string]any, e *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	action, p, err := h.admitted(tool, args, e)
	if err != nil {
		return controlInputFailure("control_request_rejected", err, "admission"), nil
	}
	var value any
	switch tool {
	case "catalog_query":
		value, err = h.catalogQuery(ctx, action, p)
	case "catalog_manage":
		var plan *adminsource.ControlPlan
		if h.sources == nil {
			err = fmt.Errorf("catalog unavailable")
			break
		}
		plan, err = h.sources.Prepare(controlChange(action, p), e.Session.AgentKey)
		if err != nil {
			break
		}
		if !contracts.ConsumeToolApproval(e, contracts.ToolApprovalFingerprint(e, tool, action, plan.Digest)) {
			return controlFail("approval_required", "explicit approval of this exact invocation and baseline is required", "authorization"), nil
		}
		value, err = h.sources.Apply(ctx, controlChange(action, p), e.Session.AgentKey, plan.Digest)
	case "chat_query":
		value, err = h.chatQuery(action, p, e)
	case "chat_manage":
		if h.conversations == nil {
			err = fmt.Errorf("conversation unavailable")
			break
		}
		revision := ""
		if action == "delete" {
			archived, _ := p["archived"].(bool)
			revision, err = h.conversations.ControlDeleteRevision(controlCaller(e), stringValue(p, "chatId"), archived)
			if err != nil {
				break
			}
			if !contracts.ConsumeToolApproval(e, contracts.ToolApprovalFingerprint(e, tool, action, revision)) {
				return controlFail("approval_required", "explicit approval of this exact deletion is required", "authorization"), nil
			}
		}
		value, err = h.conversations.ControlManage(controlCaller(e), action, p, revision)
	case "platform_inspect":
		value, err = h.inspect(action, p, e)
	}
	if err != nil {
		failure := controlInputFailure("control_failed", err, "execution")
		var mutation *contracts.MutationError
		if errors.As(err, &mutation) && mutation.State != "not_started" {
			if mutation.State == "rolled_back" {
				failure = controlFail("control_rolled_back", sanitizeDiagnostic(err.Error()), "execution")
				failure.Structured["status"] = "rolled_back"
				failure.Structured["recovery"] = map[string]any{"strategy": "fix_input", "message": "The operation failed and the previous state was restored. Resolve the reported cause and submit a new request for approval before retrying."}
			} else {
				failure.Structured["recovery"] = map[string]any{"strategy": "inspect_state", "message": "Read the target state before retrying; the operation may have committed."}
			}
			failure.Structured["executionState"] = mutation.State
			failure.Output = contracts.CompactToolModelOutput(failure.Structured, "")
		}
		return failure, nil
	}
	b, _ := json.Marshal(value)
	var payload map[string]any
	if json.Unmarshal(b, &payload) != nil || payload == nil {
		payload = map[string]any{"items": value}
	}
	return successResult(payload), nil
}
func (h *ToolHandler) catalogQuery(ctx context.Context, action string, p map[string]any) (any, error) {
	if action == "resourceTypes" {
		return catalogResourceTypes(), nil
	}
	if action == "defaults" {
		typ := stringValue(p, "type")
		paths := map[string]string{"general": GeneralCreationPath, "coder": CoderCreationPath, "kbase": KBaseCreationPath}
		path, ok := paths[typ]
		if !ok {
			return nil, fmt.Errorf("type must be general, coder or kbase")
		}
		result := h.get(path)
		result.Structured["creationTemplates"] = h.cfg.AgentCreation
		groups := []map[string]any{}
		lookup := agentcreation.Lookup{SkillExists: func(id string) bool { _, ok := h.registry.SkillDefinition(id); return ok }, ToolExists: func(id string) bool { _, ok := h.registry.Tool(id); return ok }, ConnectorExists: func(id string) bool { _, err := h.cfg.Paths.ConnectorSources().Load(id); return err == nil }}
		for _, group := range h.cfg.AgentCreation.Groups {
			missing := agentcreation.MissingMembers(group, lookup)
			groups = append(groups, map[string]any{"key": group.Key, "available": len(missing) == 0, "missingMembers": missing})
		}
		result.Structured["groupAvailability"] = groups
		if h.models != nil {
			publicModels := []map[string]any{}
			for _, m := range h.models.List() {
				publicModels = append(publicModels, publicControlModel(m))
			}
			result.Structured["models"] = publicModels
		}
		return result.Structured, nil
	}
	t := controlTarget(p)
	if action == "validate" {
		if h.sources == nil {
			return nil, fmt.Errorf("catalog unavailable")
		}
		err := h.sources.Validate(t, stringValue(p, "content"))
		if err == nil && stringValue(p, "mcpUrl") != "" {
			_, err = h.sources.Prepare(controlChange("apply", p), "")
		}
		diagnostics := []map[string]any{}
		if err != nil {
			diagnostics = append(diagnostics, candidateError("invalid_candidate", err))
		}
		return map[string]any{"valid": err == nil, "diagnostics": diagnostics}, nil
	}
	if action == "get" {
		if t.ResourceType == "provider" || t.ResourceType == "mcp" {
			if t.Path != "" {
				return nil, fmt.Errorf("path is unsupported for read-only resource")
			}
			return h.readDiscoveryResource(t.ResourceType, t.ResourceKey)
		}
		if t.ResourceType == "tool" {
			v, ok := h.registry.Tool(t.ResourceKey)
			if !ok {
				return nil, fmt.Errorf("tool not found")
			}
			return map[string]any{"definition": v, "editable": false}, nil
		}
		if t.ResourceType == "model" {
			if h.models == nil {
				return nil, fmt.Errorf("models unavailable")
			}
			v, err := h.models.GetModel(t.ResourceKey)
			return map[string]any{"definition": publicControlModel(v), "editable": false}, err
		}
		if h.sources == nil {
			return nil, fmt.Errorf("catalog unavailable")
		}
		return h.sources.Read(t)
	}
	status := stringValue(p, "status")
	if status == "" {
		status = "all"
	}
	if status != "all" && status != "valid" && status != "invalid" {
		return nil, fmt.Errorf("status must be valid, invalid or all")
	}
	items := []map[string]any{}
	add := func(key string, valid bool, diagnostics any) {
		if status == "valid" && !valid || status == "invalid" && valid {
			return
		}
		items = append(items, map[string]any{"resourceKey": key, "resourceType": t.ResourceType, "valid": valid, "diagnostics": diagnostics})
	}
	switch t.ResourceType {
	case "tool":
		for _, v := range h.registry.Tools("") {
			add(v.Name, true, nil)
		}
	case "model":
		if h.models != nil {
			for _, v := range h.models.List() {
				add(v.Key, true, nil)
			}
		}
	case "provider":
		if h.models == nil {
			return nil, fmt.Errorf("models unavailable")
		}
		for _, v := range h.models.ProviderSummaries() {
			if status == "invalid" {
				continue
			}
			items = append(items, map[string]any{"resourceKey": v.Key, "resourceType": "provider", "valid": true, "diagnostics": nil, "editable": false, "definition": v})
		}
	case "mcp":
		packages, err := h.cfg.Paths.ConnectorSources().LoadAll()
		if err != nil {
			return nil, fmt.Errorf("cannot enumerate MCP components: connector sources unavailable; inspect connector list with status invalid")
		}
		if status != "invalid" {
			for _, pkg := range packages {
				for name := range pkg.MCP {
					items = append(items, publicMCPComponent(pkg, name))
				}
			}
		}
	case "connector":
		ids := map[string]bool{}
		for _, id := range connector.NativeConnectorIDs() {
			ids[id] = true
		}
		sources := h.cfg.Paths.ConnectorSources()
		for _, root := range []string{sources.BuiltinRoot, sources.ExternalRoot} {
			if root == "" {
				continue
			}
			entries, err := os.ReadDir(root)
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			for _, entry := range entries {
				if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && entry.Name() != "builtin.desktop" && entry.Name() != "builtin.desktop-web" {
					ids[entry.Name()] = true
				}
			}
		}
		for id := range ids {
			pkg, err := sources.Load(id)
			if err != nil {
				add(id, false, []map[string]any{candidateError("invalid_connector", err)})
			} else {
				add(id, true, nil)
				if status != "invalid" {
					item := items[len(items)-1]
					item["editable"] = !pkg.Builtin
					item["hasMcp"], item["hasCli"], item["hasView"], item["hasNative"] = len(pkg.MCP) > 0, pkg.CLI != nil, len(pkg.Views) > 0, len(pkg.Native) > 0
					keys := []string{}
					for name := range pkg.MCP {
						keys = append(keys, id+"/"+name)
					}
					sort.Strings(keys)
					item["mcpKeys"] = keys
				}
			}
		}
	case "agent":
		if r, ok := h.registry.(interface{ AdminAgents() []catalog.AdminAgent }); ok {
			for _, v := range r.AdminAgents() {
				_, valid := h.registry.AgentDefinition(v.Key)
				add(v.Key, valid, v.Diagnostics)
			}
		}
	case "team", "skill":
		base := h.cfg.Paths.TeamsDir
		if t.ResourceType == "skill" {
			base = h.cfg.Paths.SkillsCenterDir
		}
		entries, err := os.ReadDir(base)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, d := range entries {
			if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
				continue
			}
			key := d.Name()
			if t.ResourceType == "team" {
				_, ok := h.registry.TeamDefinition(key)
				add(key, ok, h.sourceDiagnostics(t.ResourceType, key, ok))
			} else {
				if _, err := os.Stat(filepath.Join(base, key, "package.json")); err == nil {
					b, err := os.ReadFile(filepath.Join(base, key, "package.json"))
					var manifest struct {
						Skills []struct {
							ID string `json:"id"`
						} `json:"skills"`
					}
					if err != nil || json.Unmarshal(b, &manifest) != nil {
						add(key, false, []map[string]any{diagnostic("error", "invalid_package", "invalid package.json")})
						continue
					}
					seen := map[string]bool{}
					for _, member := range manifest.Skills {
						id := key + "/" + member.ID
						if seen[id] {
							continue
						}
						seen[id] = true
						_, ok := h.registry.SkillDefinition(id)
						add(id, ok, h.sourceDiagnostics("skill", id, ok))
					}

				} else {
					_, ok := h.registry.SkillDefinition(key)
					add(key, ok, h.sourceDiagnostics(t.ResourceType, key, ok))
				}
			}
		}
	default:
		return nil, fmt.Errorf("unsupported resourceType")
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["resourceKey"].(string) < items[j]["resourceKey"].(string) })
	start := 0
	if cursor := stringValue(p, "cursor"); cursor != "" {
		for start < len(items) && items[start]["resourceKey"].(string) <= cursor {
			start++
		}
	}
	limit := 20
	if n, ok := p["limit"].(float64); ok {
		limit = int(n)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = items[end-1]["resourceKey"].(string)
	}
	return map[string]any{"items": items[start:end], "nextCursor": next, "total": len(items), "hasMore": next != ""}, nil
}
func (h *ToolHandler) chatQuery(action string, p map[string]any, e *contracts.ExecutionContext) (any, error) {
	if h.conversations == nil {
		return nil, fmt.Errorf("conversation unavailable")
	}
	c := controlCaller(e)
	switch action {
	case "current":
		sum, err := h.conversations.ResolveControlChat(c, c.ChatID, false)
		if err != nil {
			return nil, err
		}
		run, active, err := h.conversations.ActiveRun(c.ChatID)
		return map[string]any{"chat": sum, "active": active, "run": run}, err
	case "list":
		return h.conversations.ControlList(c, p)
	case "read":
		return h.conversations.ControlRead(c, p)
	case "search":
		return h.conversations.ControlSearch(c, p)
	case "artifacts":
		if c.Subject == "" {
			return nil, fmt.Errorf("artifact access requires an authenticated identity")
		}
		sum, err := h.conversations.ResolveControlChat(c, stringValue(p, "chatId"), false)
		if err != nil {
			return nil, err
		}
		limit := 20
		if n, ok := p["limit"].(float64); ok {
			limit = int(n)
		}
		offset := 0
		if cursor := stringValue(p, "cursor"); cursor != "" {
			if _, err = fmt.Sscanf(cursor, "%d", &offset); err != nil || offset < 0 {
				return nil, fmt.Errorf("invalid cursor")
			}
		}
		items, more, err := chatresource.NewService(h.conversations.Chats).ListArtifacts(sum.ChatID, stringValue(p, "runId"), offset, limit)
		next := ""
		if more {
			next = fmt.Sprint(offset + len(items))
		}
		return map[string]any{"artifacts": items, "nextCursor": next}, err
	}
	return nil, fmt.Errorf("unsupported action")
}
func (h *ToolHandler) inspect(action string, p map[string]any, e *contracts.ExecutionContext) (any, error) {
	if action == "runtimeStatus" {
		components := map[string]any{"connectors": h.connectorSnapshots(), "containerHub": map[string]any{"enabled": h.cfg.ContainerHub.Enabled}, "memory": map[string]any{"enabled": h.cfg.Memory.Enabled}, "catalog": map[string]any{"agents": len(h.registry.Agents("")), "teams": len(h.registry.Teams()), "skills": len(h.registry.Skills(""))}}
		if h.models != nil {
			components["models"] = map[string]any{"count": len(h.models.List())}
		}
		if h.RuntimeSnapshot != nil {
			for key, value := range h.RuntimeSnapshot() {
				components[key] = value
			}
		}
		component := stringValue(p, "component")
		if component != "" {
			v, ok := components[component]
			if !ok {
				return nil, toolinput.Enum("args.component", component, toolinput.Keys(components))
			}
			return map[string]any{"component": component, "state": v}, nil
		}
		return map[string]any{"components": components}, nil
	}
	path := stringValue(p, "path")
	tool := stringValue(p, "tool")
	if (path == "") == (tool == "") {
		return nil, fmt.Errorf("provide either path or tool")
	}
	if path != "" {
		access := stringValue(p, "access")
		if access == "" {
			access = "read"
		}
		if access != "read" && access != "write" {
			return nil, fmt.Errorf("access must be read or write")
		}
		plan, err := filetools.BuildAccessPlanFromPolicy(h.cfg.AccessPolicy, e.Session, filetools.AccessMode(access), path)
		return map[string]any{"plan": plan}, err
	}
	mounted := e.Session.AllowsTool(tool)
	if owner, ok := connector.NativeToolConnector(tool); ok {
		mounted = mounted && e.Session.NativeConnectorTools[tool] == owner && e.Session.ConnectorDirs[owner] != ""
	}
	allowed := mounted
	if connector.IsPlatformRootTool(tool) {
		allowed = allowed && rootCaller(e)
	}
	if a := stringValue(p, "action"); a != "" {
		d, ok := connector.LookupControlAction(tool, a)
		allowed = allowed && ok && (!contracts.IsReadOnlyToolExecutionPolicy(e.ToolExecutionPolicy) || d.ReadOnly)
	}
	return map[string]any{"tool": tool, "mounted": mounted, "allowed": allowed}, nil
}

func publicControlModel(v models.ModelDefinition) map[string]any {
	return map[string]any{"key": v.Key, "name": v.Name, "provider": v.Provider, "type": v.Type, "protocol": v.Protocol, "modelId": v.ModelID, "isFunction": v.IsFunction, "isVision": v.IsVision, "contextWindow": v.ContextWindow}
}

func (h *ToolHandler) connectorSnapshots() any {
	packages, err := h.cfg.Paths.ConnectorSources().LoadAll()
	if err != nil {
		return map[string]any{"status": "unavailable", "message": sanitizeDiagnostic(err.Error())}
	}
	items := []map[string]any{}
	for _, pkg := range packages {
		if len(items) >= 100 {
			break
		}
		item := map[string]any{"id": pkg.ID, "version": pkg.Version, "configurationRequired": pkg.AuthMode != connector.AuthNoAuth}
		if pkg.AuthMode == connector.AuthNoAuth {
			item["readiness"] = "no_auth"
		} else {
			state, e := pkg.ReadConnection()
			if e != nil {
				item["readiness"] = "unavailable"
			} else {
				item["configured"] = state.Configured
			}
		}
		items = append(items, item)
	}
	return map[string]any{"items": items, "count": len(packages), "cached": true}
}

func (h *ToolHandler) sourceDiagnostics(kind, key string, valid bool) any {
	if valid || h.sources == nil {
		return nil
	}
	target := adminsource.ControlTarget{ResourceType: kind, ResourceKey: key}
	source, err := h.sources.Read(target)
	if err == nil {
		err = h.sources.Validate(target, source.Content)
	}
	if err == nil {
		return []map[string]any{diagnostic("error", "unavailable", "source is not available in the published catalog")}
	}
	return []map[string]any{candidateError("invalid_source", err)}
}
