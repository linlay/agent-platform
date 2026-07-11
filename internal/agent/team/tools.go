package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/api"
)

var (
	ErrUnknownTool       = errors.New("unknown Team tool")
	ErrInvalidArguments  = errors.New("invalid Team tool arguments")
	ErrUnknownMember     = errors.New("unknown Team member")
	ErrNoAvailableMember = errors.New("Team has no available members")
)

type DelegateArgs struct {
	Mode      string `json:"mode"`
	MemberKey string `json:"memberKey,omitempty"`
}

type TaskSpec struct {
	MemberKey string   `json:"memberKey"`
	Task      string   `json:"task"`
	TaskName  string   `json:"taskName,omitempty"`
	Files     []string `json:"files,omitempty"`
}

type InvokeArgs struct {
	Tasks []TaskSpec `json:"tasks"`
}

type Dispatch struct {
	Kind         string     `json:"kind"`
	DelegateMode string     `json:"delegateMode,omitempty"`
	Tasks        []TaskSpec `json:"tasks"`
}

func HiddenToolDefinitions(members []MemberSpec, maxParallel int) []api.ToolDetailResponse {
	maxParallel = NormalizeMaxParallel(maxParallel)
	memberKeySchema := map[string]any{
		"type":        "string",
		"description": "Exact memberKey from the Team roster.",
	}
	if keys := memberKeys(members); len(keys) > 0 {
		memberKeySchema["enum"] = keys
	}
	meta := func() map[string]any {
		return map[string]any{
			"kind":           "backend",
			"sourceType":     "internal",
			"clientVisible":  false,
			"explicitOnly":   true,
			"internalOnly":   true,
			"catalogVisible": false,
		}
	}
	return []api.ToolDetailResponse{
		{
			Key:         ToolDelegate,
			Name:        ToolDelegate,
			Label:       "Route Team request",
			Description: "Route the original user request directly to one member, or fan it out unchanged to every Team member.",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"mode"},
				"properties": map[string]any{
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{DelegateModeDirect, DelegateModeFanout},
						"description": "Use direct only when one member is clearly intended; otherwise use fanout.",
					},
					"memberKey": memberKeySchema,
				},
			},
			Meta: meta(),
		},
		{
			Key:         ToolInvoke,
			Name:        ToolInvoke,
			Label:       "Invoke Team tasks",
			Description: "Run one batch of focused member tasks. Tasks in this call may execute in parallel; use another call for a later serial step.",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"tasks"},
				"properties": map[string]any{
					"tasks": map[string]any{
						"type":     "array",
						"minItems": 1,
						"maxItems": maxParallel,
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"memberKey", "task"},
							"properties": map[string]any{
								"memberKey": memberKeySchema,
								"task":      map[string]any{"type": "string", "description": "A self-contained task for this member."},
								"taskName":  map[string]any{"type": "string", "description": "A short user-facing task label."},
								"files":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
						},
					},
				},
			},
			Meta: meta(),
		},
	}
}

func IsHiddenTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ToolDelegate, ToolInvoke:
		return true
	default:
		return false
	}
}

func ParseDispatch(toolName string, args map[string]any, members []MemberSpec, maxParallel int) (Dispatch, error) {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case ToolDelegate:
		var decoded DelegateArgs
		if err := decodeArgs(args, &decoded); err != nil {
			return Dispatch{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
		}
		return parseDelegate(decoded, members)
	case ToolInvoke:
		var decoded InvokeArgs
		if err := decodeArgs(args, &decoded); err != nil {
			return Dispatch{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
		}
		return parseInvoke(decoded, members, maxParallel)
	default:
		return Dispatch{}, fmt.Errorf("%w: %s", ErrUnknownTool, strings.TrimSpace(toolName))
	}
}

func parseDelegate(args DelegateArgs, members []MemberSpec) (Dispatch, error) {
	mode := strings.ToLower(strings.TrimSpace(args.Mode))
	memberKey := strings.TrimSpace(args.MemberKey)
	switch mode {
	case DelegateModeDirect:
		if memberKey == "" {
			return Dispatch{}, fmt.Errorf("%w: memberKey is required for direct delegation", ErrInvalidArguments)
		}
		canonical, ok := canonicalMemberKey(members, memberKey)
		if !ok {
			return Dispatch{}, fmt.Errorf("%w: %s", ErrUnknownMember, memberKey)
		}
		return Dispatch{
			Kind:         DispatchKindDirect,
			DelegateMode: DelegateModeDirect,
			Tasks:        []TaskSpec{{MemberKey: canonical}},
		}, nil
	case DelegateModeFanout:
		if memberKey != "" {
			return Dispatch{}, fmt.Errorf("%w: memberKey must be omitted for fanout", ErrInvalidArguments)
		}
		keys := memberKeys(members)
		if len(keys) == 0 {
			return Dispatch{}, ErrNoAvailableMember
		}
		tasks := make([]TaskSpec, 0, len(keys))
		for _, key := range keys {
			tasks = append(tasks, TaskSpec{MemberKey: key})
		}
		return Dispatch{Kind: DispatchKindFanout, DelegateMode: DelegateModeFanout, Tasks: tasks}, nil
	default:
		return Dispatch{}, fmt.Errorf("%w: mode must be %q or %q", ErrInvalidArguments, DelegateModeDirect, DelegateModeFanout)
	}
}

func parseInvoke(args InvokeArgs, members []MemberSpec, maxParallel int) (Dispatch, error) {
	limit := NormalizeMaxParallel(maxParallel)
	if len(args.Tasks) == 0 || len(args.Tasks) > limit {
		return Dispatch{}, fmt.Errorf("%w: tasks must contain between 1 and %d items", ErrInvalidArguments, limit)
	}
	tasks := make([]TaskSpec, 0, len(args.Tasks))
	for index, task := range args.Tasks {
		memberKey := strings.TrimSpace(task.MemberKey)
		text := strings.TrimSpace(task.Task)
		if memberKey == "" || text == "" {
			return Dispatch{}, fmt.Errorf("%w: tasks[%d] requires memberKey and task", ErrInvalidArguments, index)
		}
		canonical, ok := canonicalMemberKey(members, memberKey)
		if !ok {
			return Dispatch{}, fmt.Errorf("%w: %s", ErrUnknownMember, memberKey)
		}
		files := make([]string, 0, len(task.Files))
		seenFiles := map[string]struct{}{}
		for _, file := range task.Files {
			file = strings.TrimSpace(file)
			if file == "" {
				continue
			}
			if _, exists := seenFiles[file]; exists {
				continue
			}
			seenFiles[file] = struct{}{}
			files = append(files, file)
		}
		tasks = append(tasks, TaskSpec{
			MemberKey: canonical,
			Task:      text,
			TaskName:  strings.TrimSpace(task.TaskName),
			Files:     files,
		})
	}
	return Dispatch{Kind: DispatchKindInvoke, Tasks: tasks}, nil
}

func decodeArgs(args map[string]any, target any) error {
	if args == nil {
		return errors.New("arguments are required")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func memberKeys(members []MemberSpec) []string {
	keys := make([]string, 0, len(members))
	seen := map[string]struct{}{}
	for _, member := range members {
		key := strings.TrimSpace(member.Key)
		lookup := strings.ToLower(key)
		if key == "" {
			continue
		}
		if _, ok := seen[lookup]; ok {
			continue
		}
		seen[lookup] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func canonicalMemberKey(members []MemberSpec, candidate string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	for _, member := range members {
		key := strings.TrimSpace(member.Key)
		if key != "" && strings.EqualFold(key, candidate) {
			return key, true
		}
	}
	return "", false
}
