// Package interaction defines user-selectable conversation capabilities.
package interaction

import (
	"fmt"
	"strings"
)

type Attachment struct {
	LocalFiles  bool `json:"localFiles"`
	ChatRecords bool `json:"chatRecords"`
}

type Config struct {
	Model         bool       `json:"model"`
	AccessLevel   bool       `json:"accessLevel"`
	MustUseSkills bool       `json:"mustUseSkills"`
	Connectors    bool       `json:"connectors"`
	Attachment    Attachment `json:"attachment"`
}

func Defaults(mode string) Config {
	c := Config{Model: true, AccessLevel: true, MustUseSkills: true, Connectors: true, Attachment: Attachment{LocalFiles: true, ChatRecords: true}}
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "KBASE":
		c.Model, c.AccessLevel, c.Connectors, c.Attachment.ChatRecords = false, false, false, false
	case "CODER":
		c.Attachment.ChatRecords = false
	case "TEAM":
		c.Model, c.MustUseSkills, c.Connectors = false, false, false
	case "PROXY", "CHANNEL":
		c.Model = false
	}
	return c
}

func Parse(mode string, raw any) (Config, error) {
	c := Defaults(mode)
	if raw == nil {
		return c, nil
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return c, fmt.Errorf("interactionConfig must be an object")
	}
	fields := map[string]*bool{"model": &c.Model, "accessLevel": &c.AccessLevel, "mustUseSkills": &c.MustUseSkills, "connectors": &c.Connectors}
	for key, value := range node {
		if key == "attachment" {
			attachment, ok := value.(map[string]any)
			if !ok {
				return c, fmt.Errorf("interactionConfig.attachment must be an object")
			}
			for name, v := range attachment {
				var target *bool
				switch name {
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
	return c, nil
}

func (c Config) Validate(model bool, accessLevel string, skills bool) error {
	if model && !c.Model {
		return fmt.Errorf("interactionConfig.model is disabled")
	}
	if !c.AccessLevel && accessLevel != "" && accessLevel != "default" {
		return fmt.Errorf("interactionConfig.accessLevel is disabled")
	}
	if skills && !c.MustUseSkills {
		return fmt.Errorf("interactionConfig.mustUseSkills is disabled")
	}
	return nil
}

// References uses the same type classification as query reference preparation.
func (c Config) ValidateReference(kind string) error {
	switch strings.ToLower(strings.TrimSpace(kind)) {
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
