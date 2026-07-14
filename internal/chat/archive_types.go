package chat

import "errors"

var ErrChatAlreadyArchived = errors.New("chat already archived")
var ErrChatAlreadyActive = errors.New("active chat already exists")

type ArchivedSummary struct {
	ChatID         string `json:"chatId"`
	ChatName       string `json:"chatName"`
	AgentKey       string `json:"agentKey,omitempty"`
	AgentMode      string `json:"agentMode,omitempty"`
	TeamID         string `json:"teamId,omitempty"`
	Source         string `json:"source,omitempty"`
	SourceChannel  string `json:"sourceChannel,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
	LastRunAt      int64  `json:"lastRunAt"`
	ArchivedAt     int64  `json:"archivedAt"`
	LastRunID      string `json:"lastRunId,omitempty"`
	LastRunContent string `json:"lastRunContent,omitempty"`
	Read           ChatReadState
	Usage          *UsageData `json:"usage,omitempty"`
	HasAttachments bool       `json:"hasAttachments"`
}

type ArchivedChat struct {
	Summary            ArchivedSummary
	Detail             Detail
	Runs               []RunSummary
	JSONLContent       string
	EventsContent      string
	RawMessagesContent string
}

type ArchiveSearchHit struct {
	ChatID         string `json:"chatId"`
	ChatName       string `json:"chatName"`
	AgentKey       string `json:"agentKey,omitempty"`
	TeamID         string `json:"teamId,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	LastRunAt      int64  `json:"lastRunAt"`
	LastRunID      string `json:"lastRunId,omitempty"`
	LastRunContent string `json:"lastRunContent,omitempty"`
	ArchivedAt     int64  `json:"archivedAt"`
	Snippet        string `json:"snippet"`
	Score          int    `json:"score"`
}

type ArchiveResult struct {
	ChatID  string `json:"chatId"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}
