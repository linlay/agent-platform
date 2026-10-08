package session

import (
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

func NormalizedAgentTools(def catalog.AgentDefinition) []string {
	tools := make([]string, 0, len(def.Tools))
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

	return tools
}

func EffectiveAgentTools(def catalog.AgentDefinition) []string {
	return NormalizedAgentTools(def)
}

func HasRuntimeSandbox(runtime map[string]any) bool {
	if len(runtime) == 0 {
		return false
	}
	return strings.TrimSpace(StringValue(runtime["environmentId"])) != ""
}

func NormalizeRuntimeMounts(value any) []map[string]any {
	switch mounts := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(mounts))
		for _, mount := range mounts {
			out = append(out, NormalizeRuntimeMount(mount))
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(mounts))
		for _, raw := range mounts {
			mount, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, NormalizeRuntimeMount(mount))
		}
		return out
	default:
		return nil
	}
}

func NormalizeRuntimeMount(mount map[string]any) map[string]any {
	return map[string]any{
		"platform":    StringValue(mount["platform"]),
		"source":      NullableStringValue(mount["source"]),
		"destination": NullableStringValue(mount["destination"]),
		"mode":        StringValue(mount["mode"]),
	}
}

func RuntimeExtraMounts(value any) []contracts.SandboxExtraMount {
	mounts := NormalizeRuntimeMounts(value)
	if len(mounts) == 0 {
		return nil
	}
	out := make([]contracts.SandboxExtraMount, 0, len(mounts))
	for _, mount := range mounts {
		out = append(out, contracts.SandboxExtraMount{
			Platform:    StringValue(mount["platform"]),
			Source:      StringValue(mount["source"]),
			Destination: StringValue(mount["destination"]),
			Mode:        StringValue(mount["mode"]),
		})
	}
	return out
}

func RuntimeExtraMountsForMustUseSkills(value any, includeSkillsCenter bool) []contracts.SandboxExtraMount {
	mounts := RuntimeExtraMounts(value)
	if !includeSkillsCenter {
		return mounts
	}
	for index := range mounts {
		if strings.EqualFold(strings.TrimSpace(mounts[index].Platform), "skills-center") {
			mounts[index].Mode = "ro"
			return mounts
		}
	}
	return append(mounts, contracts.SandboxExtraMount{Platform: "skills-center", Mode: "ro"})
}

func StringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func NullableStringValue(value any) any {
	text := StringValue(value)
	if text == "" {
		return nil
	}
	return text
}

func ExtractRuntimeField(runtime map[string]any, key string) string {
	if runtime == nil {
		return ""
	}
	v, _ := runtime[key].(string)
	return strings.TrimSpace(v)
}
