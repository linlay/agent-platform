package agent

import "strings"

// ModeCapabilities describes runtime policy for an agent mode. These values
// are resolved once when a query session is built so downstream packages do
// not need to infer policy from mode strings.
type ModeCapabilities struct {
	InvokeChildren  bool
	RunAsChild      bool
	FileChangeHooks bool
}

type ModeProfile struct {
	IconName    string
	ToolNames   []string
	ContextTags []string
	Budget      map[string]any
}

// ModeDescriptor is the static contract owned by a built-in mode.
type ModeDescriptor struct {
	Mode         string
	MainStage    string
	MainCacheKey string
	CreatePrefix string
	Profile      ModeProfile
	Capabilities ModeCapabilities
}

func (d ModeDescriptor) NormalizedMode() string {
	return strings.ToUpper(strings.TrimSpace(d.Mode))
}

func (d ModeDescriptor) Clone() ModeDescriptor {
	d.Profile.ToolNames = append([]string(nil), d.Profile.ToolNames...)
	d.Profile.ContextTags = append([]string(nil), d.Profile.ContextTags...)
	d.Profile.Budget = cloneMap(d.Profile.Budget)
	return d
}

// SystemInitSpec is the mode-owned input consumed by the shared LLM profile
// compiler. It intentionally contains no protocol- or persistence-specific
// fields.
type SystemInitSpec struct {
	CacheKey              string
	FingerprintStage      string
	PromptStage           string
	Mode                  string
	Stage                 string
	ToolNames             []string
	SystemPrompt          string
	UseSharedSystemPrompt bool
	IncludeAfterCallHints bool
	Initial               bool
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = cloneMap(typed)
		case []string:
			out[key] = append([]string(nil), typed...)
		case []any:
			out[key] = append([]any(nil), typed...)
		default:
			out[key] = value
		}
	}
	return out
}
