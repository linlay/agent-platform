package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

var (
	ErrUnknownTool       = errors.New("unknown Team tool")
	ErrInvalidArguments  = errors.New("invalid Team tool arguments")
	ErrUnknownMember     = errors.New("unknown Team member")
	ErrDuplicateMember   = errors.New("duplicate Team member")
	ErrNoAvailableMember = errors.New("Team has no available members")
)

type TaskSpec struct {
	AgentKey string   `json:"agentKey"`
	Task     string   `json:"task,omitempty"`
	TaskName string   `json:"taskName,omitempty"`
	Files    []string `json:"files,omitempty"`
}

type DelegateArgs struct {
	Tasks []TaskSpec `json:"tasks"`
}

type Dispatch struct {
	Tasks []TaskSpec `json:"tasks"`
}

// BuildToolDefinition clones the embedded agent_delegate definition and
// freezes its member enum and batch size to the roster captured for this run.
func BuildToolDefinition(base api.ToolDetailResponse, members []MemberSpec) (api.ToolDetailResponse, error) {
	if !strings.EqualFold(strings.TrimSpace(base.Name), ToolDelegate) &&
		!strings.EqualFold(strings.TrimSpace(base.Key), ToolDelegate) {
		return api.ToolDetailResponse{}, fmt.Errorf("embedded Team tool %q is unavailable", ToolDelegate)
	}
	keys := memberKeys(members)
	if len(keys) == 0 {
		return api.ToolDetailResponse{}, ErrNoAvailableMember
	}

	definition := api.ToolDetailResponse{
		Key:           base.Key,
		Name:          base.Name,
		Label:         base.Label,
		Description:   base.Description,
		AfterCallHint: base.AfterCallHint,
		Parameters:    contracts.CloneMap(base.Parameters),
		OutputSchema:  contracts.CloneMap(base.OutputSchema),
		Meta:          contracts.CloneMap(base.Meta),
	}
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		return api.ToolDetailResponse{}, errors.New("embedded agent_delegate schema is missing properties")
	}
	tasks, ok := properties["tasks"].(map[string]any)
	if !ok {
		return api.ToolDetailResponse{}, errors.New("embedded agent_delegate schema is missing tasks")
	}
	tasks["maxItems"] = len(keys)
	items, ok := tasks["items"].(map[string]any)
	if !ok {
		return api.ToolDetailResponse{}, errors.New("embedded agent_delegate schema is missing task items")
	}
	itemProperties, ok := items["properties"].(map[string]any)
	if !ok {
		return api.ToolDetailResponse{}, errors.New("embedded agent_delegate task schema is missing properties")
	}
	agentKey, ok := itemProperties["agentKey"].(map[string]any)
	if !ok {
		return api.ToolDetailResponse{}, errors.New("embedded agent_delegate task schema is missing agentKey")
	}
	agentKey["enum"] = keys
	return definition, nil
}

func IsHiddenTool(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), ToolDelegate)
}

func ParseDispatch(toolName string, args map[string]any, members []MemberSpec) (Dispatch, error) {
	if !IsHiddenTool(toolName) {
		return Dispatch{}, fmt.Errorf("%w: %s", ErrUnknownTool, strings.TrimSpace(toolName))
	}
	var decoded DelegateArgs
	if err := decodeArgs(args, &decoded); err != nil {
		return Dispatch{}, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	return parseDelegate(decoded, members)
}

func parseDelegate(args DelegateArgs, members []MemberSpec) (Dispatch, error) {
	limit := len(memberKeys(members))
	if limit == 0 {
		return Dispatch{}, ErrNoAvailableMember
	}
	if len(args.Tasks) == 0 || len(args.Tasks) > limit {
		return Dispatch{}, fmt.Errorf("%w: tasks must contain between 1 and %d items", ErrInvalidArguments, limit)
	}

	tasks := make([]TaskSpec, 0, len(args.Tasks))
	seenMembers := make(map[string]struct{}, len(args.Tasks))
	for index, task := range args.Tasks {
		agentKey := strings.TrimSpace(task.AgentKey)
		if agentKey == "" {
			return Dispatch{}, fmt.Errorf("%w: tasks[%d] requires agentKey", ErrInvalidArguments, index)
		}
		canonical, ok := canonicalMemberKey(members, agentKey)
		if !ok {
			return Dispatch{}, fmt.Errorf("%w: %s", ErrUnknownMember, agentKey)
		}
		lookup := strings.ToLower(canonical)
		if _, duplicate := seenMembers[lookup]; duplicate {
			return Dispatch{}, fmt.Errorf("%w: %s", ErrDuplicateMember, canonical)
		}
		seenMembers[lookup] = struct{}{}

		if len(task.Files) > 10 {
			return Dispatch{}, fmt.Errorf("%w: tasks[%d].files must contain at most 10 items", ErrInvalidArguments, index)
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
			AgentKey: canonical,
			Task:     strings.TrimSpace(task.Task),
			TaskName: strings.TrimSpace(task.TaskName),
			Files:    files,
		})
	}
	return Dispatch{Tasks: tasks}, nil
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
		if key != "" && key == candidate {
			return key, true
		}
	}
	return "", false
}
