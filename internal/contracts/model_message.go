package contracts

type ModelMessage struct {
	Role             string          `json:"role"`
	Content          any             `json:"content,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolCalls        []ModelToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
}

type ModelToolCall struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function ModelFunctionCall `json:"function"`
}

type ModelFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
