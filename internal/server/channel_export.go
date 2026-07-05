package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	platformws "agent-platform/internal/ws"
)

func (s *Server) rewriteChannelRequestPayload(ctx context.Context, reqType string, payload json.RawMessage) (json.RawMessage, *statusError) {
	channelID := channelIDFromContext(ctx)
	if channelID == "" {
		return payload, nil
	}
	switch reqType {
	case "/api/query":
		return s.rewriteChannelAgentPayload(channelID, payload, "query")
	case "/api/submit":
		return s.rewriteChannelAgentPayload(channelID, payload, "submit")
	case "/api/steer":
		return s.rewriteChannelAgentPayload(channelID, payload, "steer")
	case "/api/interrupt":
		return s.rewriteChannelAgentPayload(channelID, payload, "interrupt")
	case "/api/upload":
		return payload, s.validateChannelFileTransferPayload(channelID, payload, "upload")
	case "/api/resource":
		return payload, s.validateChannelFileTransferPayload(channelID, payload, "resource")
	default:
		return payload, nil
	}
}

func channelIDFromContext(ctx context.Context) string {
	if gateway, ok := platformws.GatewayFromContext(ctx); ok {
		return strings.TrimSpace(gateway.Channel)
	}
	return ""
}

func (s *Server) rewriteChannelAgentPayload(channelID string, payload json.RawMessage, operation string) (json.RawMessage, *statusError) {
	body := map[string]any{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, &statusError{status: http.StatusBadRequest, message: "invalid channel request payload"}
		}
	}
	externalKey := firstNonBlank(stringValue(body["externalAgentKey"]), stringValue(body["agentKey"]))
	localKey, export, ok := s.lookupChannelExport(channelID, externalKey)
	if !ok {
		return nil, &statusError{status: http.StatusForbidden, message: "agent is not exported on channel"}
	}
	if !channelExportAllows(export.Allow, operation) {
		return nil, &statusError{status: http.StatusForbidden, message: "operation is not allowed on channel export"}
	}
	body["agentKey"] = localKey
	delete(body, "externalAgentKey")
	rewritten, err := json.Marshal(body)
	if err != nil {
		return nil, &statusError{status: http.StatusInternalServerError, message: err.Error()}
	}
	return rewritten, nil
}

func (s *Server) validateChannelFileTransferPayload(channelID string, payload json.RawMessage, operation string) *statusError {
	chatID := ""
	body := map[string]any{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &body); err != nil {
			return &statusError{status: http.StatusBadRequest, message: "invalid channel file payload"}
		}
	}
	if operation == "upload" {
		chatID = stringValue(body["chatId"])
	} else {
		chatID = resourceChatID(stringValue(body["file"]))
	}
	if chatID == "" || s.deps.Chats == nil {
		return &statusError{status: http.StatusForbidden, message: "file transfer requires an exported chat"}
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		if errors.Is(err, chat.ErrChatNotFound) {
			return &statusError{status: http.StatusForbidden, message: "file transfer requires an existing exported chat"}
		}
		return &statusError{status: http.StatusInternalServerError, message: err.Error()}
	}
	if !s.localAgentExportAllows(channelID, summary.AgentKey, "fileTransfer") {
		return &statusError{status: http.StatusForbidden, message: "file transfer is not allowed on channel export"}
	}
	return nil
}

func (s *Server) lookupChannelExport(channelID, externalAgentKey string) (string, catalog.AgentChannelExport, bool) {
	channelID = strings.TrimSpace(channelID)
	externalAgentKey = strings.TrimSpace(externalAgentKey)
	if channelID == "" || externalAgentKey == "" || s.deps.Registry == nil {
		return "", catalog.AgentChannelExport{}, false
	}
	for _, summary := range s.deps.Registry.Agents("all") {
		def, ok := s.deps.Registry.AgentDefinition(summary.Key)
		if !ok || catalog.AgentIsChannelMode(def.Mode) {
			continue
		}
		for _, export := range def.ChannelConfig.Exports {
			if strings.TrimSpace(export.ChannelID) == channelID && strings.TrimSpace(export.ExternalAgentKey) == externalAgentKey {
				return def.Key, export, true
			}
		}
	}
	return "", catalog.AgentChannelExport{}, false
}

func (s *Server) localAgentExportAllows(channelID, localAgentKey string, operation string) bool {
	if s.deps.Registry == nil {
		return false
	}
	def, ok := s.deps.Registry.AgentDefinition(localAgentKey)
	if !ok || catalog.AgentIsChannelMode(def.Mode) {
		return false
	}
	for _, export := range def.ChannelConfig.Exports {
		if strings.TrimSpace(export.ChannelID) == strings.TrimSpace(channelID) && channelExportAllows(export.Allow, operation) {
			return true
		}
	}
	return false
}

func channelExportAllows(allow catalog.AgentChannelAllow, operation string) bool {
	switch operation {
	case "query":
		return allow.Query
	case "submit":
		return allow.Submit
	case "steer":
		return allow.Steer
	case "interrupt":
		return allow.Interrupt
	case "fileTransfer":
		return allow.FileTransfer
	default:
		return false
	}
}
