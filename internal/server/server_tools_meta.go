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
	"agent-platform/internal/i18n"
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

func (s *Server) listTools() []api.ToolSummary {
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
		sourceCategory := toolSourceCategory(tool)
		normalized := strings.ToLower(strings.TrimSpace(tool.Name))
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		sourceType := strings.TrimSpace(anyStringValue(tool.Meta["sourceType"]))
		serverKey := ""
		if strings.EqualFold(sourceType, "mcp") {
			serverKey = strings.TrimSpace(anyStringValue(tool.Meta["serverKey"]))
		}
		items = append(items, api.ToolSummary{
			Key:            tool.Key,
			Name:           tool.Name,
			Label:          tool.Label,
			Description:    tool.Description,
			Kind:           strings.TrimSpace(metaKind),
			SourceType:     sourceType,
			SourceCategory: sourceCategory,
			ServerKey:      serverKey,
		})
	}
	return items
}

func normalizeToolSourceCategory(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "platform", "external", "mcp":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func toolSourceCategory(tool api.ToolDetailResponse) string {
	if tool.Meta == nil {
		return ""
	}
	if value := normalizeToolSourceCategory(anyStringValue(tool.Meta["sourceCategory"])); value != "" {
		return value
	}
	sourceType := strings.ToLower(strings.TrimSpace(anyStringValue(tool.Meta["sourceType"])))
	switch sourceType {
	case "mcp":
		return "mcp"
	case "agent-local":
		return "external"
	case "local":
		return "platform"
	}
	kind := strings.ToLower(strings.TrimSpace(anyStringValue(tool.Meta["kind"])))
	if kind == "external" {
		return "external"
	}
	return ""
}

func summaryAgentKey(summary *chat.Summary) string {
	if summary == nil {
		return ""
	}
	return strings.TrimSpace(summary.AgentKey)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	writeJSONUnchecked(w, status, payload)
}

func writeJSONUnchecked(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if locale := responseLocale(w); locale != "" {
		payload = i18n.LocalizeValue(locale, payload)
	}
	_ = json.NewEncoder(w).Encode(payload)
}

type localeProvider interface {
	Locale() string
}

func responseLocale(w http.ResponseWriter) string {
	if provider, ok := w.(localeProvider); ok {
		return provider.Locale()
	}
	return ""
}

func localizeStreamEventData(locale string, event stream.EventData) stream.EventData {
	event.Payload = i18n.LocalizeEventPayload(locale, event.Type, event.Payload)
	return event
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

func normalizeQueryRole(role string) (string, bool) {
	return api.NormalizeQueryRole(role)
}

func defaultRole(role string) string {
	normalized, ok := api.NormalizeQueryRole(role)
	if !ok {
		return api.QueryRoleUser
	}
	return normalized
}

func (s *Server) buildAgentDetailResponse(def catalog.AgentDefinition) api.AgentDetailResponse {
	modelName, meta := s.buildAgentDetailMeta(def)
	response := api.AgentDetailResponse{
		Key:         def.Key,
		Name:        def.Name,
		Icon:        def.Icon,
		Description: def.Description,
		Role:        def.Role,
		Greetings:   append([]string(nil), def.Greetings...),
		Wonders:     append([]string(nil), def.Wonders...),
		Model:       modelName,
		Mode:        catalog.AgentModeForAPI(def.Mode),
		Tools:       effectiveAgentTools(def),
		Skills:      append([]string{}, def.Skills...),
		Controls:    cloneListMaps(def.Controls),
		Meta:        meta,
	}
	if catalog.AgentUsesACPCoderBackend(def) {
		modelOptions := s.buildModelOptionsForAgent(def.Key)
		response.ModelOptions = &modelOptions
		response.ModelConfig = coderModelConfigFromOptions(modelOptions)
		applyACPCoderModelMeta(&response, response.ModelConfig)
	}
	return response
}

func applyACPCoderModelMeta(response *api.AgentDetailResponse, modelConfig map[string]any) {
	if response == nil {
		return
	}
	modelKey := strings.TrimSpace(modelConfigString(modelConfig, "modelKey"))
	if modelKey == "" {
		return
	}
	response.Model = modelKey
	if response.Meta == nil {
		response.Meta = map[string]any{}
	}
	response.Meta["model"] = modelKey
	response.Meta["modelKey"] = modelKey
	response.Meta["modelKeys"] = []string{modelKey}
}

func (s *Server) buildAgentDetailMeta(def catalog.AgentDefinition) (string, map[string]any) {
	modelName := strings.TrimSpace(def.ModelKey)
	meta := map[string]any{}
	if modelName != "" {
		meta["modelKey"] = modelName
		meta["modelKeys"] = []string{modelName}
	}
	if modelName != "" && s.deps.Models != nil {
		model, provider, err := s.deps.Models.Get(modelName)
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
	if strings.TrimSpace(def.ACPBridgeID) != "" {
		meta["acpBridgeId"] = strings.TrimSpace(def.ACPBridgeID)
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
			"protocol":  proxyProtocol(def.ProxyConfig),
			"transport": proxyUpstreamTransport(def.ProxyConfig),
		}
	}
	if channelMeta := agentChannelConfigResponse(def.ChannelConfig, def.Key); len(channelMeta) > 0 {
		meta["channelConfig"] = channelMeta
	}
	if hasRuntimeSandbox(def.Runtime) {
		meta["sandbox"] = normalizedRuntimeMeta(def.Runtime)
	}
	return modelName, meta
}

func agentChannelConfigResponse(cfg catalog.AgentChannelConfig, localAgentKey string) map[string]any {
	meta := map[string]any{}
	if strings.TrimSpace(cfg.ChannelID) != "" {
		meta["channelId"] = strings.TrimSpace(cfg.ChannelID)
	}
	if strings.TrimSpace(cfg.RemoteAgentKey) != "" {
		meta["remoteAgentKey"] = strings.TrimSpace(cfg.RemoteAgentKey)
	}
	if len(cfg.Exports) > 0 {
		exports := make([]map[string]any, 0, len(cfg.Exports))
		for _, export := range cfg.Exports {
			exports = append(exports, map[string]any{
				"channelId":        strings.TrimSpace(export.ChannelID),
				"externalAgentKey": catalog.EffectiveChannelExportExternalKey(localAgentKey, export),
				"allow": map[string]any{
					"query":        export.Allow.Query,
					"submit":       export.Allow.Submit,
					"steer":        export.Allow.Steer,
					"interrupt":    export.Allow.Interrupt,
					"fileTransfer": export.Allow.FileTransfer,
				},
			})
		}
		meta["exports"] = exports
	}
	return meta
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
	if mounts := normalizeRuntimeMounts(runtime["sandboxMounts"]); len(mounts) > 0 {
		out["sandboxMounts"] = mounts
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

func cloneToolDetailResponse(value api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           value.Key,
		Name:          value.Name,
		Label:         value.Label,
		Description:   value.Description,
		AfterCallHint: value.AfterCallHint,
		Parameters:    contracts.CloneMap(value.Parameters),
		OutputSchema:  contracts.CloneMap(value.OutputSchema),
		Meta:          contracts.CloneMap(value.Meta),
	}
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
