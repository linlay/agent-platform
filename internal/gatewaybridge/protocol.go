package gatewaybridge

import "encoding/json"

// WSMessage 是智能体网关使用的顶层帧结构（沿用 agent-wecom-ws-bridge 协议）。
type WSMessage struct {
	Cmd       string          `json:"cmd"`
	RequestID string          `json:"requestId,omitempty"`
	TargetID  string          `json:"targetId,omitempty"`
	Body      json.RawMessage `json:"body"`
}

const (
	CmdUserMessage   = "userMessage"
	CmdUserUpload    = "userUpload"
	CmdAgentResponse = "agentResponse"
	CmdAgentPush     = "agentPush"
)

// QueryBody 是 userMessage 里的 query 类请求体。
type QueryBody struct {
	RequestID string `json:"requestId,omitempty"`
	ChatID    string `json:"chatId"`
	AgentKey  string `json:"agentKey,omitempty"`
	RunID     string `json:"runId,omitempty"`
	Role      string `json:"role,omitempty"`
	Message   string `json:"message"`
}

// SubmitBody 是 userMessage 里的 HITL submit 请求体。
// 同时兼容旧网关（toolId）与新平台（awaitingId）的字段名：
// 任一存在即视为 submit，内部统一映射为平台的 awaitingId。
type SubmitBody struct {
	RunID      string          `json:"runId"`
	ToolID     string          `json:"toolId,omitempty"`
	AwaitingID string          `json:"awaitingId,omitempty"`
	Params     json.RawMessage `json:"params,omitempty"`
}

// UserUploadBody 是 userUpload 帧的 body。
type UserUploadBody struct {
	RequestID string     `json:"requestId"`
	ChatID    string     `json:"chatId"`
	Upload    UploadFile `json:"upload"`
}

// UploadFile 描述网关侧提供的待下载文件信息。
type UploadFile struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	MimeType  string `json:"mimeType"`
	SizeBytes int64  `json:"sizeBytes"`
	URL       string `json:"url"`
}

// AgentPushBody 是 agentPush 的 body 内容。
type AgentPushBody struct {
	Markdown string `json:"markdown"`
}

// BridgeErrorBody 是以 agentResponse 帧形态下发的错误体。沿用 wecom-bridge
// 格式 {"type":"bridge.error","error":"..."}，便于网关侧继续按旧形态识别。
type BridgeErrorBody struct {
	Type  string `json:"type"`
	Error string `json:"error"`
}

// IsSubmit 判断 raw body 是否为 HITL submit 请求；同时接受旧字段 toolId
// 与新字段 awaitingId，便于过渡期兼容。
func IsSubmit(raw json.RawMessage) bool {
	var probe struct {
		RunID      string `json:"runId"`
		ToolID     string `json:"toolId"`
		AwaitingID string `json:"awaitingId"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	if probe.RunID == "" {
		return false
	}
	return probe.ToolID != "" || probe.AwaitingID != ""
}
