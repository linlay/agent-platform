package server

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func (s *Server) enrichToolMetadata(events []stream.EventData, _ string) {
	lookup := s.toolLookup()
	if lookup == nil {
		return
	}
	for i := range events {
		if events[i].Type != "tool.snapshot" {
			continue
		}
		toolName := events[i].String("toolName")
		if toolName == "" {
			continue
		}
		def, ok := lookup.Tool(toolName)
		if !ok {
			continue
		}
		if events[i].Payload == nil {
			events[i].Payload = map[string]any{}
		}
		if label := def.Label; label != "" {
			events[i].Payload["toolLabel"] = label
		}
	}
}

func (s *Server) toolLookup() contracts.ToolDefinitionLookup {
	if tl, ok := s.deps.Tools.(contracts.ToolDefinitionLookup); ok {
		return contracts.NewCompositeToolLookup(tl, s.deps.Registry)
	}
	return s.deps.Registry
}

func (s *Server) toolLookupWithOverrides(overrides map[string]api.ToolDetailResponse) contracts.ToolDefinitionLookup {
	base := s.toolLookup()
	if len(overrides) == 0 {
		return base
	}
	return overrideToolLookup{
		base:      base,
		overrides: cloneToolOverrides(overrides),
	}
}

func (s *Server) lookupInternalTool(toolName string) (api.ToolDetailResponse, bool) {
	if tl, ok := s.deps.Tools.(contracts.ToolDefinitionLookup); ok {
		if tool, exists := tl.Tool(toolName); exists {
			return tool, true
		}
	}
	return s.deps.Registry.Tool(toolName)
}

func (s *Server) listTools(kind string, tag string) []api.ToolSummary {
	needleKind := strings.ToLower(strings.TrimSpace(kind))
	needleTag := strings.ToLower(strings.TrimSpace(tag))
	defs := s.deps.Tools.Definitions()
	items := make([]api.ToolSummary, 0, len(defs))
	seen := map[string]struct{}{}
	for _, tool := range defs {
		canonical, ok := canonicalizePublicToolDefinition(tool)
		if !ok {
			continue
		}
		tool = canonical
		metaKind, _ := tool.Meta["kind"].(string)
		if needleKind != "" && strings.ToLower(strings.TrimSpace(metaKind)) != needleKind {
			continue
		}
		if needleTag != "" && !matchesToolTag(tool, needleTag) {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(tool.Name))
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		items = append(items, api.ToolSummary{
			Key:         tool.Key,
			Name:        tool.Name,
			Label:       tool.Label,
			Description: tool.Description,
			Meta:        contracts.CloneMap(tool.Meta),
		})
	}
	return items
}

func (s *Server) lookupTool(toolName string) (api.ToolDetailResponse, bool) {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "_sandbox_bash_", "bash_sandbox":
		return api.ToolDetailResponse{}, false
	}
	if tl, ok := s.deps.Tools.(contracts.ToolDefinitionLookup); ok {
		if tool, exists := tl.Tool(toolName); exists {
			return canonicalizePublicToolDefinition(tool)
		}
	}
	tool, ok := s.deps.Registry.Tool(toolName)
	if !ok {
		return api.ToolDetailResponse{}, false
	}
	return canonicalizePublicToolDefinition(tool)
}

func matchesToolTag(tool api.ToolDetailResponse, needle string) bool {
	fields := []string{
		tool.Key,
		tool.Name,
		tool.Label,
		tool.Description,
		tool.AfterCallHint,
		anyStringValue(tool.Meta["kind"]),
		anyStringValue(tool.Meta["viewportType"]),
		anyStringValue(tool.Meta["viewportKey"]),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

func summaryAgentKey(summary *chat.Summary) string {
	if summary == nil {
		return ""
	}
	return strings.TrimSpace(summary.AgentKey)
}

func applyToolOverride(def api.ToolDetailResponse, overrides map[string]api.ToolDetailResponse) api.ToolDetailResponse {
	if len(overrides) == 0 {
		return def
	}
	override, ok := overrides[strings.ToLower(strings.TrimSpace(def.Name))]
	if !ok {
		override, ok = overrides[strings.ToLower(strings.TrimSpace(def.Key))]
	}
	if !ok {
		return def
	}
	merged := def
	if strings.TrimSpace(override.Key) != "" {
		merged.Key = override.Key
	}
	if strings.TrimSpace(override.Name) != "" {
		merged.Name = override.Name
	}
	if strings.TrimSpace(override.Label) != "" {
		merged.Label = override.Label
	}
	if strings.TrimSpace(override.Description) != "" {
		merged.Description = override.Description
	}
	if strings.TrimSpace(override.AfterCallHint) != "" {
		merged.AfterCallHint = override.AfterCallHint
	}
	if len(override.Parameters) > 0 {
		merged.Parameters = contracts.CloneMap(override.Parameters)
	}
	if len(def.Meta) > 0 {
		merged.Meta = contracts.CloneMap(def.Meta)
	}
	if len(merged.Meta) == 0 {
		merged.Meta = map[string]any{}
	}
	for key, value := range override.Meta {
		merged.Meta[key] = value
	}
	return merged
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(target)
}

func decodeOptionalJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	return json.Unmarshal(data, target)
}

func defaultRole(role string) string {
	if strings.TrimSpace(role) == "" {
		return "user"
	}
	return strings.TrimSpace(role)
}

func (s *Server) buildAgentDetailResponse(def catalog.AgentDefinition) api.AgentDetailResponse {
	modelName, meta := s.buildAgentDetailMeta(def)
	return api.AgentDetailResponse{
		Key:         def.Key,
		Name:        def.Name,
		Icon:        def.Icon,
		Description: def.Description,
		Role:        def.Role,
		Wonders:     append([]string(nil), def.Wonders...),
		Model:       modelName,
		Mode:        catalog.AgentModeForAPI(def.Mode),
		Tools:       effectiveAgentTools(def),
		Skills:      append([]string{}, def.Skills...),
		Controls:    cloneListMaps(def.Controls),
		Meta:        meta,
	}
}

func (s *Server) buildAgentDetailMeta(def catalog.AgentDefinition) (string, map[string]any) {
	modelName := strings.TrimSpace(def.ModelKey)
	meta := map[string]any{}
	if def.ModelKey != "" {
		meta["modelKey"] = def.ModelKey
		meta["modelKeys"] = []string{def.ModelKey}
	}
	if s.deps.Models != nil {
		model, provider, err := s.deps.Models.Get(def.ModelKey)
		if err == nil {
			if strings.TrimSpace(model.ModelID) != "" {
				modelName = strings.TrimSpace(model.ModelID)
			}
			if strings.TrimSpace(model.Key) != "" {
				meta["modelKey"] = model.Key
				meta["modelKeys"] = []string{model.Key}
			}
			if strings.TrimSpace(provider.Key) != "" {
				meta["providerKey"] = provider.Key
			}
			if strings.TrimSpace(model.Protocol) != "" {
				meta["protocol"] = model.Protocol
			}
		}
	}
	if modelName == "" {
		modelName = def.ModelKey
	}
	if len(def.Skills) > 0 {
		meta["perAgentSkills"] = append([]string(nil), def.Skills...)
	}
	if strings.EqualFold(strings.TrimSpace(def.Mode), catalog.AgentModeCoder) && strings.TrimSpace(def.CoderBackend) != "" {
		meta["coderBackend"] = strings.ToLower(strings.TrimSpace(def.CoderBackend))
		if strings.TrimSpace(def.ACPProxyID) != "" {
			meta["acpProxyId"] = strings.TrimSpace(def.ACPProxyID)
		}
	}
	if strings.TrimSpace(def.Workspace.Root) != "" {
		workspaceMeta := map[string]any{
			"root": def.Workspace.Root,
		}
		meta["workspace"] = workspaceMeta
	}
	if len(def.Project.PromptFiles) > 0 || strings.TrimSpace(def.Project.Git.ExpectedBranch) != "" {
		meta["project"] = normalizedProjectMeta(def.Project)
	}
	if def.ProxyConfig != nil {
		meta["proxy"] = map[string]any{
			"protocol":  "agw-platform",
			"transport": proxyUpstreamTransport(def.ProxyConfig),
		}
	}
	if hasRuntimeSandbox(def.Runtime) {
		meta["sandbox"] = normalizedRuntimeMeta(def.Runtime)
	}
	return modelName, meta
}

func normalizedAgentTools(def catalog.AgentDefinition) []string {
	tools := make([]string, 0, len(def.Tools)+1)
	seen := map[string]struct{}{}
	for _, tool := range def.Tools {
		name := strings.TrimSpace(tool)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tools = append(tools, name)
	}
	if len(def.Skills) > 0 || hasRuntimeSandbox(def.Runtime) || hasRuntimeEnvOverrides(def.Runtime) {
		if _, ok := seen["bash"]; !ok {
			tools = append(tools, "bash")
			seen["bash"] = struct{}{}
		}
	}
	return tools
}

func effectiveAgentTools(def catalog.AgentDefinition) []string {
	return normalizedAgentTools(def)
}

func hasRuntimeSandbox(runtime map[string]any) bool {
	if len(runtime) == 0 {
		return false
	}
	return strings.TrimSpace(stringValue(runtime["environmentId"])) != ""
}

func hasRuntimeEnvOverrides(runtime map[string]any) bool {
	if len(runtime) == 0 {
		return false
	}
	env, ok := runtime["env"].(map[string]string)
	return ok && len(env) > 0
}

func normalizedRuntimeMeta(runtime map[string]any) map[string]any {
	if runtime == nil {
		return nil
	}
	out := map[string]any{
		"environmentId": stringValue(runtime["environmentId"]),
		"level":         strings.ToUpper(stringValue(runtime["level"])),
	}
	// Intentionally do not expose sandbox env values via API metadata.
	if mounts := normalizeRuntimeMounts(runtime["extraMounts"]); len(mounts) > 0 {
		out["extraMounts"] = mounts
	}
	return out
}

func normalizedProjectMeta(project catalog.AgentProjectConfig) map[string]any {
	out := map[string]any{}
	if len(project.PromptFiles) > 0 {
		items := make([]map[string]any, 0, len(project.PromptFiles))
		for _, item := range project.PromptFiles {
			items = append(items, map[string]any{
				"source": item.Source,
				"path":   item.Path,
			})
		}
		out["promptFiles"] = items
	}
	if strings.TrimSpace(project.Git.ExpectedBranch) != "" {
		out["git"] = map[string]any{
			"expectedBranch": project.Git.ExpectedBranch,
		}
	}
	return out
}

func normalizeRuntimeMounts(value any) []map[string]any {
	switch mounts := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(mounts))
		for _, mount := range mounts {
			out = append(out, normalizeRuntimeMount(mount))
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(mounts))
		for _, raw := range mounts {
			mount, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, normalizeRuntimeMount(mount))
		}
		return out
	default:
		return nil
	}
}

func normalizeRuntimeMount(mount map[string]any) map[string]any {
	return map[string]any{
		"platform":    stringValue(mount["platform"]),
		"source":      nullableStringValue(mount["source"]),
		"destination": nullableStringValue(mount["destination"]),
		"mode":        stringValue(mount["mode"]),
	}
}

func runtimeExtraMounts(value any) []contracts.SandboxExtraMount {
	mounts := normalizeRuntimeMounts(value)
	if len(mounts) == 0 {
		return nil
	}
	out := make([]contracts.SandboxExtraMount, 0, len(mounts))
	for _, mount := range mounts {
		out = append(out, contracts.SandboxExtraMount{
			Platform:    stringValue(mount["platform"]),
			Source:      stringValue(mount["source"]),
			Destination: stringValue(mount["destination"]),
			Mode:        stringValue(mount["mode"]),
		})
	}
	return out
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func anyStringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func nullableStringValue(value any) any {
	text := stringValue(value)
	if text == "" {
		return nil
	}
	return text
}

func extractRuntimeField(runtime map[string]any, key string) string {
	if runtime == nil {
		return ""
	}
	v, _ := runtime[key].(string)
	return strings.TrimSpace(v)
}

func cloneListMaps(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(src))
	for _, item := range src {
		out = append(out, contracts.CloneMap(item))
	}
	return out
}

func cloneToolOverrides(src map[string]api.ToolDetailResponse) map[string]api.ToolDetailResponse {
	if src == nil {
		return nil
	}
	out := make(map[string]api.ToolDetailResponse, len(src))
	for key, value := range src {
		out[key] = api.ToolDetailResponse{
			Key:           value.Key,
			Name:          value.Name,
			Label:         value.Label,
			Description:   value.Description,
			AfterCallHint: value.AfterCallHint,
			Parameters:    contracts.CloneMap(value.Parameters),
			Meta:          contracts.CloneMap(value.Meta),
		}
	}
	return out
}

func cloneToolDetailResponse(value api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           value.Key,
		Name:          value.Name,
		Label:         value.Label,
		Description:   value.Description,
		AfterCallHint: value.AfterCallHint,
		Parameters:    contracts.CloneMap(value.Parameters),
		Meta:          contracts.CloneMap(value.Meta),
	}
}

type overrideToolLookup struct {
	base      contracts.ToolDefinitionLookup
	overrides map[string]api.ToolDetailResponse
}

func (o overrideToolLookup) Tool(name string) (api.ToolDetailResponse, bool) {
	if o.base == nil {
		return api.ToolDetailResponse{}, false
	}
	tool, ok := o.base.Tool(name)
	if !ok {
		return api.ToolDetailResponse{}, false
	}
	return applyToolOverride(tool, o.overrides), true
}

func canonicalizePublicToolDefinition(tool api.ToolDetailResponse) (api.ToolDetailResponse, bool) {
	switch strings.ToLower(strings.TrimSpace(tool.Name)) {
	case "_sandbox_bash_", "bash_sandbox":
		return api.ToolDetailResponse{}, false
	case "bash":
		canonical := cloneToolDetailResponse(tool)
		canonical.Key = "bash"
		canonical.Name = "bash"
		canonical.Label = "执行命令"
		canonical.Description = "Run a command. Runtime decides whether to execute on the host or inside the sandbox based on the agent's runtimeConfig.environmentId. Always include a short Chinese description explaining the command purpose."
		return canonical, true
	default:
		return cloneToolDetailResponse(tool), true
	}
}

func newRunID() string {
	return chat.NewRunID()
}

func newChatID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		data[0:4],
		data[4:6],
		data[6:8],
		data[8:10],
		data[10:16],
	)
}
