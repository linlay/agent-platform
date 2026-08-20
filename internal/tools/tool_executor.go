package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/memory"
	"agent-platform/internal/models"
	"agent-platform/internal/runtimeenv"
	"agent-platform/internal/skills"
)

// ArtifactPusher 是 tool_artifact 产物外发的最小依赖面：由应用层注入（通常是
// artifactpusher.Pusher），nil 时外发自动跳过。
type ArtifactPusher interface {
	Push(chatID string, artifact map[string]any)
}

type RuntimeToolExecutor struct {
	cfg             config.Config
	sandbox         SandboxClient
	chats           chat.Store
	memory          memory.Store
	models          *models.ModelRegistry
	skillCandidates skills.CandidateStore
	artifactPusher  ArtifactPusher
	clientRequest   ClientRequestInvoker
	clientTargets   ClientTargetStore
	desktopMain     DesktopMainTargetProvider
	fileChangeHooks []FileChangeHook
	fileStateMu     sync.Mutex
	httpClient      *http.Client
	defs            []api.ToolDetailResponse
	runtimeEnv      runtimeenv.Info
}

func NewRuntimeToolExecutor(cfg config.Config, sandbox SandboxClient, chats chat.Store, memoryStore memory.Store, skillCandidates skills.CandidateStore) (*RuntimeToolExecutor, error) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		return nil, err
	}
	filtered := make([]api.ToolDetailResponse, 0, len(defs))
	for _, def := range defs {
		if !cfg.ContainerHub.Enabled && strings.EqualFold(strings.TrimSpace(def.Name), "bash_sandbox") {
			continue
		}
		filtered = append(filtered, def)
	}
	configureImageGenerateProfileSchema(filtered, cfg.ImageGenerate)
	return &RuntimeToolExecutor{
		cfg:             cfg,
		sandbox:         sandbox,
		chats:           chats,
		memory:          memoryStore,
		skillCandidates: skillCandidates,
		httpClient:      &http.Client{},
		defs:            filtered,
		runtimeEnv:      runtimeenv.Detect(),
	}, nil
}

func configureImageGenerateProfileSchema(definitions []api.ToolDetailResponse, cfg config.ImageGenerateConfig) {
	profileNames := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		if name = strings.TrimSpace(name); name != "" {
			profileNames = append(profileNames, name)
		}
	}
	sort.Strings(profileNames)

	for index := range definitions {
		if definitions[index].Name != "image_generate" {
			continue
		}
		properties := AnyMapNode(definitions[index].Parameters["properties"])
		profile := AnyMapNode(properties["profile"])
		if len(profile) == 0 {
			return
		}
		if len(profileNames) > 0 {
			enum := make([]any, len(profileNames))
			for index, name := range profileNames {
				enum[index] = name
			}
			profile["enum"] = enum
		} else {
			delete(profile, "enum")
		}
		description := "Optional. Configured image-generation profile. Omit it to use the configured default profile"
		if defaultProfile := strings.TrimSpace(cfg.DefaultProfile); defaultProfile != "" {
			description += " (" + defaultProfile + ")"
		}
		profile["description"] = description + "."
		return
	}
}

func (t *RuntimeToolExecutor) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), t.defs...)
}

// WithArtifactPusher 注入产物外发实现；不调用时 tool_artifact 只改本地盘不推网关。
func (t *RuntimeToolExecutor) WithArtifactPusher(pusher ArtifactPusher) *RuntimeToolExecutor {
	if t != nil {
		t.artifactPusher = pusher
	}
	return t
}

func (t *RuntimeToolExecutor) WithClientRequestInvoker(invoker ClientRequestInvoker) *RuntimeToolExecutor {
	if t != nil {
		t.clientRequest = invoker
	}
	return t
}

func (t *RuntimeToolExecutor) WithWebClientTargetStore(store WebClientTargetStore) *RuntimeToolExecutor {
	if t != nil {
		t.clientTargets = webClientTargetStoreAdapter{store: store}
	}
	return t
}

type webClientTargetStoreAdapter struct {
	store WebClientTargetStore
}

func (a webClientTargetStoreAdapter) BindClientTarget(runID string, target ClientTarget) bool {
	return a.store != nil && a.store.BindWebClientTarget(runID, target)
}

func (a webClientTargetStoreAdapter) ResolveClientTarget(runID string) (ClientTarget, bool) {
	if a.store == nil {
		return ClientTarget{}, false
	}
	return a.store.ResolveWebClientTarget(runID)
}

func (t *RuntimeToolExecutor) WithClientTargetStore(store ClientTargetStore) *RuntimeToolExecutor {
	if t != nil {
		t.clientTargets = store
	}
	return t
}

func (t *RuntimeToolExecutor) WithDesktopMainTargetProvider(provider DesktopMainTargetProvider) *RuntimeToolExecutor {
	if t != nil {
		t.desktopMain = provider
	}
	return t
}

func (t *RuntimeToolExecutor) WithFileChangeHooks(hooks ...FileChangeHook) *RuntimeToolExecutor {
	if t != nil {
		t.fileChangeHooks = append([]FileChangeHook(nil), hooks...)
	}
	return t
}

func (t *RuntimeToolExecutor) WithModelRegistry(registry *models.ModelRegistry) *RuntimeToolExecutor {
	if t != nil {
		t.models = registry
	}
	return t
}

func (t *RuntimeToolExecutor) WithHTTPClient(client *http.Client) *RuntimeToolExecutor {
	if t != nil && client != nil {
		t.httpClient = client
	}
	return t
}

func (t *RuntimeToolExecutor) WithRuntimeEnv(info runtimeenv.Info) *RuntimeToolExecutor {
	if t != nil && !info.IsZero() {
		t.runtimeEnv = info
	}
	return t
}

func (t *RuntimeToolExecutor) runtimeInfo() runtimeenv.Info {
	if t != nil && !t.runtimeEnv.IsZero() {
		return t.runtimeEnv
	}
	return runtimeenv.Detect()
}

func (t *RuntimeToolExecutor) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	result, err := t.invoke(ctx, toolName, args, execCtx)
	if err == nil && runtimeToolUsesCompactModelOutput(toolName) {
		result.Output = CompactToolModelOutput(result.Structured, result.Output)
	}
	return result, err
}

func (t *RuntimeToolExecutor) invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx != nil && execCtx.ReadFileState == nil {
		execCtx.ReadFileState = map[string]ReadFileSnapshot{}
	}
	if execCtx != nil && execCtx.Session.ScopedFilePolicy != nil && !sessionToolAllowed(execCtx.Session.ToolNames, toolName) {
		return ToolExecutionResult{
			Output:   "tool is outside the dedicated KBASE run tool set",
			Error:    "kbase_editing_tool_unsupported",
			ExitCode: -1,
		}, nil
	}
	switch strings.TrimSpace(toolName) {
	case "agent_delegate":
		return ToolExecutionResult{Output: "agent_delegate is only executed by an orchestrated Team coordinator", Error: "internal_tool_only", ExitCode: -1}, nil
	case "datetime":
		return t.invokeDateTime(args), nil
	case "artifact_publish":
		return t.invokeArtifactPublish(args, execCtx)
	case "desktop_action":
		return t.invokeDesktopAction(ctx, args, execCtx)
	case "desktop_cdp":
		return t.invokeDesktopCDP(ctx, args, execCtx)
	case "file_read":
		return t.invokeRead(args, execCtx)
	case "file_write":
		return t.invokeWrite(ctx, args, execCtx)
	case "file_edit":
		return t.invokeEdit(ctx, args, execCtx)
	case "file_glob":
		return t.invokeGlob(ctx, args, execCtx)
	case "file_grep":
		return t.invokeGrep(ctx, args, execCtx)
	case PlanAddTasksToolName:
		return t.invokePlanAddTasks(args, execCtx)
	case PlanGetTasksToolName:
		return t.invokePlanGetTasks(execCtx)
	case PlanUpdateTaskToolName:
		return t.invokePlanUpdateTask(args, execCtx)
	case FinalizePlanningToolName:
		return t.invokePlanningWrite(toolName, args, execCtx)
	case "regex":
		return t.invokeRegex(args), nil
	case "vision_recognize":
		return t.invokeVisionRecognize(ctx, args, execCtx)
	case "image_generate":
		return t.invokeImageGenerate(ctx, args, execCtx)
	case "web_fetch", "WebFetch":
		return t.invokeWebFetch(ctx, args, execCtx)
	case "bash":
		if execCtx != nil && hasRuntimeSandbox(execCtx.Session) {
			if !t.cfg.ContainerHub.Enabled {
				return ToolExecutionResult{
					Output:   "sandbox execution is required by agent runtimeConfig.environmentId but container-hub is unavailable",
					Error:    "sandbox_not_available",
					ExitCode: -1,
				}, nil
			}
			return t.invokeSandboxBash(ctx, args, execCtx)
		}
		return t.invokeHostBash(ctx, args, execCtx)
	case "memory_search":
		return t.invokeMemorySearch(toolName, args, execCtx)
	case "memory_read":
		return t.invokeMemoryRead(toolName, args, execCtx)
	case "memory_write":
		return t.invokeMemoryWrite(toolName, args, execCtx)
	case "memory_update":
		return t.invokeMemoryUpdate(toolName, args, execCtx)
	case "memory_forget":
		return t.invokeMemoryForget(toolName, args, execCtx)
	case "memory_timeline":
		return t.invokeMemoryTimeline(toolName, args, execCtx)
	case "memory_promote":
		return t.invokeMemoryPromote(toolName, args, execCtx)
	case "memory_consolidate":
		return t.invokeMemoryConsolidate(toolName, args, execCtx)
	case "_session_search_", "session_search":
		return t.invokeSessionSearch(toolName, args, execCtx)
	case "_skill_candidate_write_", "skill_candidate_write":
		return t.invokeSkillCandidateWrite(toolName, args, execCtx)
	case "_skill_candidate_list_", "skill_candidate_list":
		return t.invokeSkillCandidateList(toolName, args, execCtx)
	default:
		return ToolExecutionResult{
			Output:   "tool not registered: " + toolName,
			Error:    "tool_not_registered",
			ExitCode: -1,
		}, nil
	}
}

func runtimeToolUsesCompactModelOutput(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "bash_sandbox",
		"desktop_action", "desktop_cdp",
		"file_read", "file_write", "file_edit", "file_glob", "file_grep",
		"image_generate", "vision_recognize", "web_fetch",
		"regex":
		return true
	default:
		return false
	}
}

func sessionToolAllowed(toolNames []string, requested string) bool {
	requested = strings.TrimSpace(requested)
	for _, toolName := range toolNames {
		if strings.TrimSpace(toolName) == requested {
			return true
		}
	}
	return false
}

func hasRuntimeSandbox(session QuerySession) bool {
	return session.AgentHasRuntimeSandbox
}

func stringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func defaultStringArg(args map[string]any, key string, fallback string) string {
	if value := stringArg(args, key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func structuredResult(payload map[string]any) ToolExecutionResult {
	return structuredResultWithExit(payload, 0)
}

func bashResult(stdout, stderr, mode, cwd string, exitCode int, hardError string) ToolExecutionResult {
	if exitCode == 0 && stderr == "" && strings.TrimSpace(hardError) == "" {
		return ToolExecutionResult{
			Output:   stdout,
			ExitCode: 0,
		}
	}

	payload := map[string]any{
		"exitCode": exitCode,
		"mode":     mode,
		"cwd":      cwd,
		"stdout":   stdout,
		"stderr":   stderr,
	}
	if strings.TrimSpace(hardError) != "" {
		payload["error"] = hardError
	}
	result := structuredResultWithExit(payload, exitCode)
	result.Error = strings.TrimSpace(hardError)
	return result
}

func structuredResultWithExit(payload map[string]any, exitCode int) ToolExecutionResult {
	data, _ := json.Marshal(payload)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		ExitCode:   exitCode,
	}
}
