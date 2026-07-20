package catalog

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type SkillCandidateDiagnostic struct {
	Severity string
	Code     string
	Message  string
}

// ValidateSkillCandidate validates a complete SKILL.md candidate without
// writing it into the skills catalog.
func ValidateSkillCandidate(resourceKey string, content []byte, maxPromptChars int) []SkillCandidateDiagnostic {
	if err := ValidateEditableSkillKey(resourceKey); err != nil {
		return []SkillCandidateDiagnostic{{Severity: "error", Code: "invalid_skill_key", Message: err.Error()}}
	}
	if !utf8.Valid(content) {
		return []SkillCandidateDiagnostic{{Severity: "error", Code: "invalid_skill_encoding", Message: "SKILL.md must be UTF-8 text"}}
	}

	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return []SkillCandidateDiagnostic{{Severity: "error", Code: "missing_skill_frontmatter", Message: "SKILL.md must start with YAML frontmatter"}}
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return []SkillCandidateDiagnostic{{Severity: "error", Code: "invalid_skill_frontmatter", Message: "SKILL.md YAML frontmatter is not closed"}}
	}

	tree, err := config.LoadYAMLTreeBytes([]byte(strings.Join(lines[1:end], "\n")))
	if err != nil {
		return []SkillCandidateDiagnostic{{Severity: "error", Code: "invalid_skill_frontmatter", Message: err.Error()}}
	}
	frontmatter, ok := tree.(map[string]any)
	if !ok {
		return []SkillCandidateDiagnostic{{Severity: "error", Code: "invalid_skill_frontmatter", Message: "SKILL.md YAML frontmatter must be an object"}}
	}

	diagnostics := make([]SkillCandidateDiagnostic, 0)
	if strings.TrimSpace(contracts.AnyStringNode(frontmatter["name"])) == "" {
		diagnostics = append(diagnostics, SkillCandidateDiagnostic{Severity: "error", Code: "missing_skill_name", Message: "SKILL.md frontmatter.name is required"})
	}
	if strings.TrimSpace(contracts.AnyStringNode(frontmatter["description"])) == "" {
		diagnostics = append(diagnostics, SkillCandidateDiagnostic{Severity: "error", Code: "missing_skill_description", Message: "SKILL.md frontmatter.description is required"})
	}
	if strings.TrimSpace(strings.Join(lines[end+1:], "\n")) == "" {
		diagnostics = append(diagnostics, SkillCandidateDiagnostic{Severity: "error", Code: "empty_skill_body", Message: "SKILL.md body is required"})
	}
	if maxPromptChars > 0 && len(strings.TrimSpace(normalized)) > maxPromptChars {
		diagnostics = append(diagnostics, SkillCandidateDiagnostic{
			Severity: "warning",
			Code:     "skill_prompt_truncated",
			Message:  fmt.Sprintf("SKILL.md exceeds the configured prompt limit of %d characters", maxPromptChars),
		})
	}
	return diagnostics
}
