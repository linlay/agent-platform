package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/stream"
	terminalpkg "agent-platform/internal/terminal"
	"agent-platform/internal/ws"
)

type terminalOpenPayload struct {
	AgentKey string `json:"agentKey"`
	ChatID   string `json:"chatId,omitempty"`
	Cols     int    `json:"cols,omitempty"`
	Rows     int    `json:"rows,omitempty"`
}

type terminalIDPayload struct {
	TerminalID string `json:"terminalId"`
}

type terminalInputPayload struct {
	TerminalID string `json:"terminalId"`
	Data       string `json:"data"`
}

type terminalResizePayload struct {
	TerminalID string `json:"terminalId"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
}

func (s *Server) wsTerminalOpen(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalOpenPayload](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "invalid terminal open payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	session, statusErr := s.openTerminalSession(payload)
	if statusErr != nil {
		s.sendTerminalStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := conn.ReserveNamedStream(req.ID, session.ID()); err != nil {
		session.Close("closed")
		if protoErr, ok := err.(*ws.ProtocolError); ok {
			conn.SendProtocolError(req.ID, protoErr)
		} else {
			conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	conn.AttachStreamCleanup(req.ID, func() {
		session.Close("closed")
	})
	s.terminals.Start(session)

	var seq int64 = 1
	conn.SendStreamEvent(req.ID, terminalStreamEvent(seq, terminalpkg.Event{
		Type:       terminalpkg.EventOpened,
		TerminalID: session.ID(),
		AgentKey:   session.AgentKey(),
		CWD:        session.CWD(),
		Shell:      session.Shell(),
	}))

	reason := "closed"
	for {
		select {
		case <-conn.Done():
			session.Close("closed")
			return
		case event, ok := <-session.Events():
			if !ok {
				conn.FinishStream(req.ID, reason, seq)
				return
			}
			seq++
			if event.Type == terminalpkg.EventExit {
				if event.Reason != "" {
					reason = event.Reason
				} else {
					reason = "exit"
				}
			}
			conn.SendStreamEvent(req.ID, terminalStreamEvent(seq, event))
			if event.Type == terminalpkg.EventExit {
				conn.FinishStream(req.ID, reason, seq)
				return
			}
		}
	}
}

func (s *Server) wsTerminalInput(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalInputPayload](req)
	if err != nil || strings.TrimSpace(payload.TerminalID) == "" {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "terminalId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := s.terminals.Input(payload.TerminalID, payload.Data); err != nil {
		s.sendTerminalError(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"terminalId": strings.TrimSpace(payload.TerminalID)})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTerminalResize(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalResizePayload](req)
	if err != nil || strings.TrimSpace(payload.TerminalID) == "" {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "terminalId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := s.terminals.Resize(payload.TerminalID, payload.Cols, payload.Rows); err != nil {
		s.sendTerminalError(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"terminalId": strings.TrimSpace(payload.TerminalID)})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTerminalClose(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalIDPayload](req)
	if err != nil || strings.TrimSpace(payload.TerminalID) == "" {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "terminalId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := s.terminals.Close(payload.TerminalID); err != nil {
		s.sendTerminalError(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"terminalId": strings.TrimSpace(payload.TerminalID)})
	conn.CompleteRequest(req.ID)
}

func (s *Server) openTerminalSession(payload terminalOpenPayload) (*terminalpkg.Session, *statusError) {
	if s == nil || s.terminals == nil {
		return nil, &statusError{status: http.StatusServiceUnavailable, message: "terminal manager is not configured"}
	}
	agentKey := strings.TrimSpace(payload.AgentKey)
	if agentKey == "" {
		return nil, &statusError{status: http.StatusBadRequest, message: "agentKey is required"}
	}
	if s.deps.Registry == nil {
		return nil, &statusError{status: http.StatusServiceUnavailable, message: "agent registry is not configured"}
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return nil, &statusError{status: http.StatusBadRequest, message: "agent not found"}
	}
	if !strings.EqualFold(strings.TrimSpace(def.Mode), catalog.AgentModeCoder) {
		return nil, &statusError{status: http.StatusForbidden, message: "terminal is only supported for CODER agents"}
	}
	if catalog.AgentUsesACPCoderBackend(def) || hasRuntimeSandbox(def.Runtime) {
		return nil, &statusError{status: http.StatusNotImplemented, message: "terminal is only supported for native local CODER agents"}
	}
	cwd, err := s.resolveTerminalWorkspace(def, payload.ChatID)
	if err != nil {
		return nil, err
	}
	session, openErr := s.terminals.Open(terminalpkg.OpenRequest{
		AgentKey: agentKey,
		ChatID:   strings.TrimSpace(payload.ChatID),
		CWD:      cwd,
		Shell:    resolveTerminalShell(s.deps.Config.Bash.ShellExecutable),
		Cols:     payload.Cols,
		Rows:     payload.Rows,
		Env:      []string{"TERM=xterm-256color", "COLORTERM=truecolor"},
	})
	if openErr != nil {
		if errors.Is(openErr, terminalpkg.ErrUnsupported) {
			return nil, &statusError{status: http.StatusNotImplemented, message: "terminal is unsupported on this platform"}
		}
		return nil, &statusError{status: http.StatusInternalServerError, message: openErr.Error()}
	}
	return session, nil
}

func (s *Server) resolveTerminalWorkspace(def catalog.AgentDefinition, chatID string) (string, *statusError) {
	root := strings.TrimSpace(def.Workspace.Root)
	if root == "" {
		return "", &statusError{status: http.StatusBadRequest, message: "agent workspace is empty"}
	}
	if strings.EqualFold(root, catalog.AgentWorkspaceRootChat) {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" {
			return "", &statusError{status: http.StatusBadRequest, message: "chatId is required for @chat terminal workspace"}
		}
		dir, err := ensureChatAttachmentsDir(s.deps.Config.Paths, chatID)
		if err != nil {
			return "", &statusError{status: http.StatusInternalServerError, message: err.Error()}
		}
		if strings.TrimSpace(dir) == "" {
			return "", &statusError{status: http.StatusBadRequest, message: "chat workspace is empty"}
		}
		return dir, nil
	}
	if !filepath.IsAbs(root) {
		return "", &statusError{status: http.StatusBadRequest, message: "agent workspace must be absolute or @chat"}
	}
	dir, err := validatedWorkspaceDir(root)
	if err != nil {
		if statusErr, ok := err.(agentStatusError); ok {
			return "", &statusError{status: statusErr.status, message: statusErr.message}
		}
		return "", &statusError{status: http.StatusBadRequest, message: err.Error()}
	}
	return dir, nil
}

func resolveTerminalShell(configured string) string {
	return resolveTerminalShellForGOOS(configured, os.Getenv("SHELL"), runtime.GOOS)
}

func resolveTerminalShellForGOOS(configured string, envShell string, goos string) string {
	if shell := strings.TrimSpace(configured); shell != "" {
		return shell
	}
	if goos == "windows" {
		return "powershell.exe"
	}
	if shell := strings.TrimSpace(envShell); shell != "" {
		return shell
	}
	return "/bin/bash"
}

func terminalStreamEvent(seq int64, event terminalpkg.Event) stream.EventData {
	payload := map[string]any{
		"terminalId": event.TerminalID,
	}
	if event.AgentKey != "" {
		payload["agentKey"] = event.AgentKey
	}
	if event.ChatID != "" {
		payload["chatId"] = event.ChatID
	}
	if event.CWD != "" {
		payload["cwd"] = event.CWD
	}
	if event.Shell != "" {
		payload["shell"] = event.Shell
	}
	if event.Data != "" {
		payload["data"] = event.Data
	}
	if event.ExitCode != nil {
		payload["exitCode"] = *event.ExitCode
	}
	if event.Reason != "" {
		payload["reason"] = event.Reason
	}
	return stream.EventData{
		Seq:       seq,
		Type:      event.Type,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func (s *Server) sendTerminalStatusError(conn *ws.Conn, requestID string, err *statusError) {
	if err == nil {
		return
	}
	frameType := "invalid_request"
	switch err.status {
	case http.StatusForbidden:
		frameType = "forbidden"
	case http.StatusNotFound:
		frameType = "terminal_not_found"
	case http.StatusNotImplemented:
		frameType = "unsupported"
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		frameType = "internal_error"
	}
	conn.SendError(requestID, frameType, err.status, err.message, nil)
}

func (s *Server) sendTerminalError(conn *ws.Conn, requestID string, err error) {
	if errors.Is(err, terminalpkg.ErrNotFound) {
		conn.SendError(requestID, "terminal_not_found", http.StatusNotFound, "terminal not found", nil)
		return
	}
	if errors.Is(err, terminalpkg.ErrUnsupported) {
		conn.SendError(requestID, "unsupported", http.StatusNotImplemented, "terminal is unsupported", nil)
		return
	}
	conn.SendError(requestID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
}
