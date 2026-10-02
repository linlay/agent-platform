// Package general holds the rules that belong only to the general-purpose
// native agent type. It has no fixed tool set, prompt or stage of its own;
// those come from Agent configuration and Platform automatic capabilities.
package general

import "strings"

const (
	Mode = "GENERAL"
	// legacyMode is the spelling used before the type was renamed. It stays
	// accepted internally so historical chat/run snapshots keep their meaning.
	legacyMode = "REACT"
)

// CreatePrefix names keys generated for general agents created without one.
const CreatePrefix = "general"

var createToolNames = []string{
	"datetime",
	"file_read",
	"file_write",
	"file_edit",
	"file_glob",
	"file_grep",
	"regex",
	"bash",
	"artifact_publish",
	"ask_user_question",
	"plan_add_tasks",
	"plan_update_task",
	"plan_get_tasks",
	"web_fetch",
	"vision_recognize",
	"image_generate",
}

// CreateToolNames is the base tool list written into a new general agent when
// it is created through a capability template and agent-creation.yml does not
// configure base-tools. It is a creation template, never a load-time default:
// effective tools also include Platform presets and automatic capabilities.
func CreateToolNames() []string {
	return append([]string(nil), createToolNames...)
}

func IsMode(mode string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(mode))
	return normalized == Mode || normalized == legacyMode
}
