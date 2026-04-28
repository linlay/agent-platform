package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/ws"
)

func (s *Server) handleChatArchive(w http.ResponseWriter, r *http.Request) {
	var req api.ArchiveChatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	if len(req.ChatIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatIds is required"))
		return
	}
	response, err := s.archiveChats(req.ChatIDs)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleArchives(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	response, err := s.listArchives(api.ArchivesRequest{
		AgentKey: r.URL.Query().Get("agentKey"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	response, err := s.loadArchiveDetail(r.Context(), chatID, strings.EqualFold(r.URL.Query().Get("includeRawMessages"), "true"))
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "archive not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleArchiveSearch(w http.ResponseWriter, r *http.Request) {
	var req api.ArchiveSearchRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Query) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "query is required"))
		return
	}
	response, err := s.searchArchives(req)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleArchiveDelete(w http.ResponseWriter, r *http.Request) {
	var req api.ArchiveDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteArchive(req.ChatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "archive not found"))
		return
	}
	if errors.Is(err, os.ErrPermission) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid chatId"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleArchiveResource(w http.ResponseWriter, r *http.Request) {
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	fileParam := strings.TrimSpace(r.URL.Query().Get("file"))
	if !chat.ValidChatID(chatID) || fileParam == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId and file are required"))
		return
	}
	if s.deps.Archives == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "archive store is not configured"))
		return
	}
	if s.deps.Config.ResourceTicket.Enabled() {
		principal := PrincipalFromContext(r.Context())
		ticket := strings.TrimSpace(r.URL.Query().Get("t"))
		if principal == nil {
			if ticket == "" {
				writeJSON(w, http.StatusUnauthorized, api.Failure(http.StatusUnauthorized, "resource ticket required"))
				return
			}
			ticketChatID, err := s.ticketService.Verify(ticket)
			if err != nil {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, err.Error()))
				return
			}
			if ticketChatID != chatID {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource ticket chat mismatch"))
				return
			}
		}
	}
	path, err := s.deps.Archives.ResolveResource(chatID, fileParam)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "resource not found"))
			return
		}
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) archiveChats(chatIDs []string) (api.ArchiveChatResponse, error) {
	if s.deps.Archiver == nil {
		return api.ArchiveChatResponse{}, errors.New("archiver is not configured")
	}
	if len(chatIDs) == 0 {
		return api.ArchiveChatResponse{}, errors.New("chatIds is required")
	}
	results := make([]api.ArchiveChatResult, 0, len(chatIDs))
	for _, rawChatID := range chatIDs {
		chatID := strings.TrimSpace(rawChatID)
		result := api.ArchiveChatResult{ChatID: chatID}
		if !chat.ValidChatID(chatID) {
			result.Error = "invalid chatId"
			results = append(results, result)
			continue
		}
		if err := s.ensureNoActiveRun(chatID); err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		if err := s.deps.Archiver.ArchiveChat(chatID); err != nil {
			result.Error = archiveResultError(err)
			results = append(results, result)
			continue
		}
		result.Success = true
		results = append(results, result)
		agentKey := ""
		if s.deps.Archives != nil {
			if archived, err := s.deps.Archives.LoadArchived(chatID); err == nil && archived != nil {
				agentKey = archived.Summary.AgentKey
			}
		}
		s.broadcast("chat.archived", map[string]any{"chatId": chatID, "agentKey": agentKey})
	}
	return api.ArchiveChatResponse{Results: results}, nil
}

func (s *Server) ensureNoActiveRun(chatID string) error {
	if s.deps.Runs == nil {
		return nil
	}
	activeRun, ok, err := s.deps.Runs.ActiveRunForChat(chatID)
	var conflictErr *contracts.ActiveRunConflictError
	if errors.As(err, &conflictErr) {
		return errors.New("active run conflict")
	}
	if err != nil {
		return err
	}
	if ok || strings.TrimSpace(activeRun.RunID) != "" {
		return errors.New("active run conflict")
	}
	return nil
}

func archiveResultError(err error) string {
	switch {
	case errors.Is(err, chat.ErrChatNotFound):
		return "chat not found"
	case errors.Is(err, chat.ErrChatAlreadyArchived):
		return "already archived"
	case errors.Is(err, os.ErrPermission):
		return "invalid chatId"
	default:
		return err.Error()
	}
}

func (s *Server) listArchives(req api.ArchivesRequest) (api.ArchivesResponse, error) {
	if s.deps.Archives == nil {
		return api.ArchivesResponse{}, errors.New("archive store is not configured")
	}
	items, total, err := s.deps.Archives.ListArchived(req.AgentKey, req.Limit, req.Offset)
	if err != nil {
		return api.ArchivesResponse{}, err
	}
	response := api.ArchivesResponse{Total: total, Items: make([]api.ArchivedSummaryResponse, 0, len(items))}
	for _, item := range items {
		response.Items = append(response.Items, mapArchivedSummary(item))
	}
	return response, nil
}

func (s *Server) loadArchiveDetail(ctx context.Context, chatID string, includeRawMessages bool) (api.ChatDetailResponse, error) {
	if s.deps.Archives == nil {
		return api.ChatDetailResponse{}, errors.New("archive store is not configured")
	}
	archived, err := s.deps.Archives.LoadArchived(chatID)
	if err != nil {
		return api.ChatDetailResponse{}, err
	}
	s.enrichToolMetadata(archived.Detail.Events, archived.Summary.AgentKey)
	response := api.ChatDetailResponse{
		ChatID:     archived.Detail.ChatID,
		ChatName:   archived.Detail.ChatName,
		Events:     archived.Detail.Events,
		References: nil,
		Runs:       make([]api.RunSummary, 0, len(archived.Runs)),
	}
	for _, run := range archived.Runs {
		response.Runs = append(response.Runs, mapRunSummary(run))
	}
	if principal := PrincipalFromContext(ctx); principal != nil {
		response.ResourceTicket = s.ticketService.Issue(principal.Subject, archived.Detail.ChatID)
	}
	if includeRawMessages {
		response.RawMessages = archived.Detail.RawMessages
	}
	if archived.Detail.Plan != nil {
		response.Plan = archived.Detail.Plan
	}
	if archived.Detail.Artifact != nil {
		response.Artifact = archived.Detail.Artifact
	}
	if archived.Summary.Usage != nil && archived.Summary.Usage.TotalTokens > 0 {
		response.Usage = &api.ChatUsageData{
			PromptTokens:     archived.Summary.Usage.PromptTokens,
			CompletionTokens: archived.Summary.Usage.CompletionTokens,
			TotalTokens:      archived.Summary.Usage.TotalTokens,
		}
	}
	return response, nil
}

func (s *Server) searchArchives(req api.ArchiveSearchRequest) (api.ArchiveSearchResponse, error) {
	if s.deps.Archives == nil {
		return api.ArchiveSearchResponse{}, errors.New("archive store is not configured")
	}
	hits, err := s.deps.Archives.SearchArchived(req.Query, req.AgentKey, req.Limit)
	if err != nil {
		return api.ArchiveSearchResponse{}, err
	}
	results := make([]api.ArchiveSearchResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, api.ArchiveSearchResult{
			ChatID:         hit.ChatID,
			ChatName:       hit.ChatName,
			AgentKey:       hit.AgentKey,
			TeamID:         hit.TeamID,
			LastRunID:      hit.LastRunID,
			LastRunContent: hit.LastRunContent,
			ArchivedAt:     hit.ArchivedAt,
			Snippet:        hit.Snippet,
			Score:          hit.Score,
		})
	}
	return api.ArchiveSearchResponse{Query: strings.TrimSpace(req.Query), Count: len(results), Results: results}, nil
}

func (s *Server) deleteArchive(chatID string) (api.ArchiveDeleteResponse, error) {
	if s.deps.Archives == nil {
		return api.ArchiveDeleteResponse{}, errors.New("archive store is not configured")
	}
	chatID = strings.TrimSpace(chatID)
	if !chat.ValidChatID(chatID) {
		return api.ArchiveDeleteResponse{}, os.ErrPermission
	}
	if err := s.deps.Archives.DeleteArchived(chatID); err != nil {
		return api.ArchiveDeleteResponse{}, err
	}
	s.broadcast("archive.deleted", map[string]any{"chatId": chatID})
	return api.ArchiveDeleteResponse{ChatID: chatID, Deleted: true}, nil
}

func mapArchivedSummary(item chat.ArchivedSummary) api.ArchivedSummaryResponse {
	resp := api.ArchivedSummaryResponse{
		ChatID:         item.ChatID,
		ChatName:       item.ChatName,
		AgentKey:       item.AgentKey,
		TeamID:         item.TeamID,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		ArchivedAt:     item.ArchivedAt,
		LastRunID:      item.LastRunID,
		LastRunContent: item.LastRunContent,
		HasAttachments: item.HasAttachments,
	}
	if item.Usage != nil && item.Usage.TotalTokens > 0 {
		resp.Usage = &api.ChatUsageData{
			PromptTokens:     item.Usage.PromptTokens,
			CompletionTokens: item.Usage.CompletionTokens,
			TotalTokens:      item.Usage.TotalTokens,
		}
	}
	return resp
}

func mapRunSummary(run chat.RunSummary) api.RunSummary {
	return api.RunSummary{
		RunID:          run.RunID,
		ChatID:         run.ChatID,
		AgentKey:       run.AgentKey,
		InitialMessage: run.InitialMessage,
		AssistantText:  run.AssistantText,
		FinishReason:   run.FinishReason,
		StartedAt:      run.StartedAt,
		CompletedAt:    run.CompletedAt,
		Usage: api.ChatUsageData{
			PromptTokens:     run.Usage.PromptTokens,
			CompletionTokens: run.Usage.CompletionTokens,
			TotalTokens:      run.Usage.TotalTokens,
		},
		FeedbackType:    run.FeedbackType,
		FeedbackComment: run.FeedbackComment,
		FeedbackAt:      run.FeedbackAt,
	}
}

func (s *Server) wsChatArchive(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.ArchiveChatRequest](req)
	if err != nil || len(payload.ChatIDs) == 0 {
		conn.SendError(req.ID, "invalid_request", 400, "chatIds is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, archiveErr := s.archiveChats(payload.ChatIDs)
	if archiveErr != nil {
		conn.SendError(req.ID, "unavailable", 503, archiveErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsArchives(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.ArchivesRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, listErr := s.listArchives(payload)
	if listErr != nil {
		conn.SendError(req.ID, "unavailable", 503, listErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsArchive(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		ChatID             string `json:"chatId"`
		IncludeRawMessages bool   `json:"includeRawMessages"`
	}](req)
	if err != nil || !chat.ValidChatID(strings.TrimSpace(payload.ChatID)) {
		conn.SendError(req.ID, "invalid_request", 400, "chatId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, loadErr := s.loadArchiveDetail(ctx, payload.ChatID, payload.IncludeRawMessages)
	if errors.Is(loadErr, chat.ErrChatNotFound) {
		conn.SendError(req.ID, "not_found", 404, "archive not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if loadErr != nil {
		conn.SendError(req.ID, "internal_error", 500, loadErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsArchiveSearch(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.ArchiveSearchRequest](req)
	if err != nil || strings.TrimSpace(payload.Query) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "query is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, searchErr := s.searchArchives(payload)
	if searchErr != nil {
		conn.SendError(req.ID, "unavailable", 503, searchErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsArchiveDelete(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.ArchiveDeleteRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, deleteErr := s.deleteArchive(payload.ChatID)
	if errors.Is(deleteErr, chat.ErrChatNotFound) {
		conn.SendError(req.ID, "not_found", 404, "archive not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if errors.Is(deleteErr, os.ErrPermission) {
		conn.SendError(req.ID, "invalid_request", 400, "invalid chatId", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if deleteErr != nil {
		conn.SendError(req.ID, "unavailable", 503, deleteErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}
