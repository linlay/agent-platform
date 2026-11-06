package acp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/reference"
	runtimetypes "agent-platform/internal/runtime/types"
	sdk "github.com/coder/acp-go-sdk"
)

const maxPromptResourceBytes = 10 << 20

func (s *Sessions) ValidateContext(session contracts.QuerySession) error {
	if len(session.ToolNames) == 0 && len(session.ConnectorDirs) == 0 {
		return nil
	}
	details, err := s.Details(session.BackendSession.ChatID)
	if err != nil {
		return err
	}
	var capabilities sdk.AgentCapabilities
	if json.Unmarshal(details.Capabilities, &capabilities) != nil || !capabilities.McpCapabilities.Http {
		return agentengine.Error("capability_unsupported")
	}
	return nil
}

// Upload URLs are locators, never authority to read a caller-provided host path.
// ACP runs on the same host but only receives resources from this Chat.
func (s *Sessions) promptContent(req runtimetypes.QueryCommand, sessionChatID string) ([]sdk.ContentBlock, error) {
	blocks := []sdk.ContentBlock{sdk.TextBlock(req.Message)}
	details, err := s.Details(sessionChatID)
	if err != nil {
		return nil, err
	}
	var capabilities sdk.AgentCapabilities
	if err := json.Unmarshal(details.Capabilities, &capabilities); err != nil {
		return nil, err
	}
	var total int64
	for _, ref := range req.References {
		if ref.Type == "skill" || ref.Type == "kbase" {
			continue
		}
		key := reference.ResourceFileParamForChat(req.ChatID, ref.URL)
		owner, relative, err := chat.ParseResourceKey(key)
		if err != nil || owner != req.ChatID {
			return nil, fmt.Errorf("reference_unavailable: attachment must belong to the current chat")
		}
		root, err := os.OpenRoot(s.chats.ChatDir(req.ChatID))
		if err != nil {
			return nil, fmt.Errorf("reference_unavailable: %w", err)
		}
		file, err := root.Open(relative)
		root.Close()
		if err != nil {
			return nil, fmt.Errorf("reference_unavailable: %w", err)
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			return nil, fmt.Errorf("reference_unavailable: regular file required")
		}
		data, err := io.ReadAll(io.LimitReader(file, maxPromptResourceBytes-total+1))
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("reference_unavailable: %w", err)
		}
		total += int64(len(data))
		if total > maxPromptResourceBytes {
			return nil, fmt.Errorf("resource_reference_too_large: attachments exceed 10 MiB")
		}
		mimeType := http.DetectContentType(data)
		if strings.HasPrefix(mimeType, "image/") {
			if !capabilities.PromptCapabilities.Image {
				return nil, fmt.Errorf("capability_unsupported: engine does not accept images")
			}
			blocks = append(blocks, sdk.ImageBlock(base64.StdEncoding.EncodeToString(data), mimeType))
			continue
		}
		if !utf8.Valid(data) || !strings.HasPrefix(mimeType, "text/") {
			if !capabilities.PromptCapabilities.EmbeddedContext {
				return nil, fmt.Errorf("capability_unsupported: engine does not accept embedded files")
			}
			uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(s.chats.ChatDir(req.ChatID), relative))}).String()
			blocks = append(blocks, sdk.ResourceBlock(sdk.EmbeddedResourceResource{BlobResourceContents: &sdk.BlobResourceContents{Uri: uri, MimeType: &mimeType, Blob: base64.StdEncoding.EncodeToString(data)}}))
			continue
		}
		blocks = append(blocks, sdk.TextBlock("Attachment "+filepath.Base(relative)+":\n"+string(data)))
	}
	return blocks, nil
}
