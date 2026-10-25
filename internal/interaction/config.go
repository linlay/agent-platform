// Package interaction defines user-selectable conversation capabilities.
package interaction

import (
	"fmt"
	"strings"
)

type Attachment struct {
	LocalFiles  bool `json:"localFiles"`
	Skills      bool `json:"skills"`
	Kbases      bool `json:"kbases"`
	ChatRecords bool `json:"chatRecords"`
}

type Config struct {
	DefaultDetail string     `json:"defaultDetail"`
	Model         bool       `json:"model"`
	AccessLevel   bool       `json:"accessLevel"`
	Connectors    bool       `json:"connectors"`
	Attachment    Attachment `json:"attachment"`
	// The show* fields only decide whether a client offers an entry. They never
	// gate a request: editingMode and planningMode stay valid API parameters.
	ShowEditingMode  bool `json:"showEditingMode"`
	ShowPlanningMode bool `json:"showPlanningMode"`
	ShowTerminal     bool `json:"showTerminal"`
}

// Defaults returns the template defaults of a mode for a native agent.
func Defaults(mode string) Config { return DefaultsForEngine(mode, false) }

// DefaultsForEngine also applies the engine: an ACP bridge owns Workspace
// editing, so the editing entry is never offered by default there.
func DefaultsForEngine(mode string, acp bool) Config {
	c := Config{DefaultDetail: "summary", Model: true, AccessLevel: true, Connectors: true, Attachment: Attachment{LocalFiles: true, ChatRecords: true, Skills: true, Kbases: true}}
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "KBASE":
		c.Model, c.AccessLevel, c.Connectors, c.Attachment.ChatRecords = false, false, false, false
	case "CODER":
		c.Attachment.ChatRecords = false
	case "PROXY", "CHANNEL":
		c.Model = false
	}
	if mode := strings.ToUpper(strings.TrimSpace(mode)); mode == "KBASE" || mode == "PROXY" || mode == "CHANNEL" {
		c.Attachment.Kbases = false
	}
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "", "GENERAL", "REACT", "CODER", "KBASE", "TEAM":
		c.ShowPlanningMode = true
	}
	// The editing toggle starts off and is sent explicitly once shown, so it is
	// offered by default only where a Run is read-only unless asked otherwise.
	c.ShowEditingMode = strings.EqualFold(strings.TrimSpace(mode), "KBASE") && !acp
	c.ShowTerminal = strings.EqualFold(strings.TrimSpace(mode), "CODER")
	return applyBackendAttachmentCapabilities(c, mode, acp)
}

// Local resource identities cannot be resolved by a remote execution backend.
// Explicit entry overrides do not add a capability to that backend.
func applyBackendAttachmentCapabilities(c Config, mode string, acp bool) Config {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	if acp || mode == "PROXY" || mode == "CHANNEL" {
		c.Attachment.Skills = false
		c.Attachment.Kbases = false
		c.Attachment.ChatRecords = false
	}
	return c
}

func Parse(mode string, raw any) (Config, error) { return ParseForEngine(mode, false, raw) }

func ParseForEngine(mode string, acp bool, raw any) (Config, error) {
	c := DefaultsForEngine(mode, acp)
	if raw == nil {
		return c, nil
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return c, fmt.Errorf("interactionConfig must be an object")
	}
	fields := map[string]*bool{"model": &c.Model, "accessLevel": &c.AccessLevel, "connectors": &c.Connectors,
		"showEditingMode": &c.ShowEditingMode, "showPlanningMode": &c.ShowPlanningMode, "showTerminal": &c.ShowTerminal}
	for key, value := range node {
		if key == "defaultDetail" {
			level, ok := value.(string)
			if !ok || (level != "summary" && level != "full") {
				return c, fmt.Errorf("interactionConfig.defaultDetail must be summary or full")
			}
			c.DefaultDetail = level
			continue
		}
		if key == "attachment" {
			attachment, ok := value.(map[string]any)
			if !ok {
				return c, fmt.Errorf("interactionConfig.attachment must be an object")
			}
			for name, v := range attachment {
				var target *bool
				switch name {
				case "skills":
					target = &c.Attachment.Skills
				case "kbases":
					target = &c.Attachment.Kbases
				case "localFiles":
					target = &c.Attachment.LocalFiles
				case "chatRecords":
					target = &c.Attachment.ChatRecords
				default:
					return c, fmt.Errorf("unknown interactionConfig.attachment field %q", name)
				}
				b, ok := v.(bool)
				if !ok {
					return c, fmt.Errorf("interactionConfig.attachment.%s must be a boolean", name)
				}
				*target = b
			}
			continue
		}
		target, ok := fields[key]
		if !ok {
			return c, fmt.Errorf("unknown interactionConfig field %q", key)
		}
		b, ok := value.(bool)
		if !ok {
			return c, fmt.Errorf("interactionConfig.%s must be a boolean", key)
		}
		*target = b
	}
	return applyBackendAttachmentCapabilities(c, mode, acp), nil
}

func (c Config) Validate(model bool, accessLevel string, skills bool) error {
	if model && !c.Model {
		return fmt.Errorf("interactionConfig.model is disabled")
	}
	if !c.AccessLevel && accessLevel != "" && accessLevel != "default" {
		return fmt.Errorf("interactionConfig.accessLevel is disabled")
	}
	if skills && !c.Attachment.Skills {
		return fmt.Errorf("interactionConfig.attachment.skills is disabled")
	}
	return nil
}

// References uses the same type classification as query reference preparation.
func (c Config) ValidateReference(kind string) error {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "skill":
		if !c.Attachment.Skills {
			return fmt.Errorf("interactionConfig.attachment.skills is disabled")
		}
	case "kbase":
		if !c.Attachment.Kbases {
			return fmt.Errorf("interactionConfig.attachment.kbases is disabled")
		}
	case "chat":
		if !c.Attachment.ChatRecords {
			return fmt.Errorf("interactionConfig.attachment.chatRecords is disabled")
		}
	case "selection", "site":
	default:
		if !c.Attachment.LocalFiles {
			return fmt.Errorf("interactionConfig.attachment.localFiles is disabled")
		}
	}
	return nil
}
