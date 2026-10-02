// Package toolargs defines platform-owned tool input contract checks.
// It never rewrites arguments or inspects protocol passthrough objects.
package toolargs

import (
	"fmt"
	"strings"
)

type fieldRename struct{ old, current string }

// RejectLegacy rejects removed input names before policies or execution can
// accidentally ignore them. Values and the supplied map are never modified.
func RejectLegacy(tool string, args map[string]any) error {
	tool = strings.ToLower(strings.TrimSpace(tool))
	var fields []fieldRename
	switch tool {
	case "file_read":
		fields = []fieldRename{{"file_path", "filePath"}, {"add_line_numbers", "addLineNumbers"}}
	case "file_write":
		fields = []fieldRename{{"file_path", "filePath"}}
	case "file_edit":
		fields = []fieldRename{{"file_path", "filePath"}, {"old_string", "oldString"}, {"new_string", "newString"}, {"replace_all", "replaceAll"}}
	case "file_glob", "kbase_files":
		fields = []fieldRename{{"head_limit", "headLimit"}}
	case "file_grep":
		fields = []fieldRename{{"head_limit", "headLimit"}, {"output_mode", "outputMode"}, {"-A", "afterContext"}, {"-B", "beforeContext"}, {"-C", "context"}, {"-i", "caseInsensitive"}, {"-n", "lineNumbers"}}
	case "regex":
		fields = []fieldRename{{"case_insensitive", "caseInsensitive"}}
	case "sleep":
		fields = []fieldRename{{"duration_ms", "durationMs"}}
	case "image_generate":
		fields = []fieldRename{{"response_format", "responseFormat"}}
	case "vision_recognize":
		fields = []fieldRename{{"output_format", "outputFormat"}}
	default:
		return nil
	}
	if err := rejectFields(args, "", fields); err != nil {
		return err
	}
	if tool != "image_generate" && tool != "vision_recognize" {
		return nil
	}
	checkImage := func(node map[string]any, path string) error {
		fields := []fieldRename{{"reference_name", "referenceName"}, {"file_path", "filePath"}}
		if tool == "image_generate" {
			fields = append(fields, fieldRename{"source_type", "sourceType"})
		}
		if err := rejectFields(node, path, fields); err != nil {
			return err
		}
		if tool == "image_generate" {
			switch node["sourceType"] {
			case "reference_name":
				return fmt.Errorf("unsupported value for %ssourceType; use referenceName", path)
			case "file_path":
				return fmt.Errorf("unsupported value for %ssourceType; use filePath", path)
			}
		}
		return nil
	}
	switch images := args["images"].(type) {
	case []any:
		for i, image := range images {
			if node, ok := image.(map[string]any); ok {
				if err := checkImage(node, fmt.Sprintf("images[%d].", i)); err != nil {
					return err
				}
			}
		}
	case []map[string]any:
		for i, node := range images {
			if err := checkImage(node, fmt.Sprintf("images[%d].", i)); err != nil {
				return err
			}
		}
	}
	if tool == "image_generate" {
		if mask, ok := args["mask"].(map[string]any); ok {
			return checkImage(mask, "mask.")
		}
	}
	return nil
}

func rejectFields(args map[string]any, path string, fields []fieldRename) error {
	for _, field := range fields {
		if _, exists := args[field.old]; exists {
			return fmt.Errorf("unsupported argument %q; use %q", path+field.old, path+field.current)
		}
	}
	return nil
}
