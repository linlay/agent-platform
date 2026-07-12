package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"
)

func (s *Server) handleChatArchive(w http.ResponseWriter, r *http.Request) {
	var req api.ArchiveChatRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	if chatID := strings.TrimSpace(r.URL.Query().Get("chatId")); chatID != "" {
		if len(req.ChatIDs) > 0 && (len(req.ChatIDs) != 1 || strings.TrimSpace(req.ChatIDs[0]) != chatID) {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId mismatch"))
			return
		}
		req.ChatIDs = []string{chatID}
	}
	if len(req.ChatIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatIds is required"))
		return
	}
	response, err := s.archiveChats(req.ChatIDs)
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleArchiveDelete(w http.ResponseWriter, r *http.Request) {
	var req api.ArchiveDeleteRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	chatID, idErr := queryOrBodyID(r, "chatId", req.ChatID)
	if idErr != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, idErr.Error()))
		return
	}
	response, err := s.deleteArchive(chatID)
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

func (s *Server) handleArchiveRestore(w http.ResponseWriter, r *http.Request) {
	var req api.ArchiveRestoreRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	if chatID := strings.TrimSpace(r.URL.Query().Get("chatId")); chatID != "" {
		if len(req.ChatIDs) > 0 && (len(req.ChatIDs) != 1 || strings.TrimSpace(req.ChatIDs[0]) != chatID) {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId mismatch"))
			return
		}
		req.ChatIDs = []string{chatID}
	}
	if len(req.ChatIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatIds is required"))
		return
	}
	response, err := s.restoreArchives(req.ChatIDs)
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
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
			if isTimeContractViolation(err) {
				return api.ArchiveChatResponse{}, err
			}
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
			} else if isTimeContractViolation(err) {
				return api.ArchiveChatResponse{}, err
			}
		}
		s.broadcast("chat.archived", map[string]any{"chatId": chatID, "agentKey": agentKey})
	}
	return api.ArchiveChatResponse{Results: results}, nil
}

func (s *Server) restoreArchives(chatIDs []string) (api.ArchiveRestoreResponse, error) {
	if s.deps.Archiver == nil {
		return api.ArchiveRestoreResponse{}, errors.New("archiver is not configured")
	}
	if len(chatIDs) == 0 {
		return api.ArchiveRestoreResponse{}, errors.New("chatIds is required")
	}
	results := make([]api.ArchiveRestoreResult, 0, len(chatIDs))
	for _, rawChatID := range chatIDs {
		chatID := strings.TrimSpace(rawChatID)
		result := api.ArchiveRestoreResult{ChatID: chatID}
		if !chat.ValidChatID(chatID) {
			result.Error = "invalid chatId"
			results = append(results, result)
			continue
		}
		summary, err := s.deps.Archiver.RestoreChat(chatID)
		if err != nil {
			if isTimeContractViolation(err) {
				return api.ArchiveRestoreResponse{}, err
			}
			result.Error = restoreResultError(err)
			results = append(results, result)
			continue
		}
		result.Success = true
		apiSummary := mapChatSummaries([]chat.Summary{summary})[0]
		result.Summary = &apiSummary
		results = append(results, result)
		s.broadcast("archive.restored", map[string]any{"chatId": chatID, "agentKey": summary.AgentKey, "summary": apiSummary})
	}
	return api.ArchiveRestoreResponse{Results: results}, nil
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

func restoreResultError(err error) string {
	switch {
	case errors.Is(err, chat.ErrChatNotFound):
		return "archive not found"
	case errors.Is(err, chat.ErrChatAlreadyActive):
		return "active chat already exists"
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

func (s *Server) loadArchiveDetail(ctx context.Context, chatID string, includeRawMessages bool) (api.ArchivedChatDetailResponse, error) {
	if s.deps.Archives == nil {
		return api.ArchivedChatDetailResponse{}, errors.New("archive store is not configured")
	}
	archived, err := s.deps.Archives.LoadArchived(chatID)
	if err != nil {
		return api.ArchivedChatDetailResponse{}, err
	}
	s.enrichToolMetadata(archived.Detail.Events, archived.Summary.AgentKey)
	response := api.ArchivedChatDetailResponse{
		ChatID:     archived.Detail.ChatID,
		ChatName:   archived.Detail.ChatName,
		CreatedAt:  archived.Summary.CreatedAt,
		LastRunAt:  archived.Summary.LastRunAt,
		ArchivedAt: archived.Summary.ArchivedAt,
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
	response.Usage = mapUsageDataPtr(archived.Summary.Usage)
	if usage := latestChatUsageFromEvents(archived.Detail.Events); usage != nil {
		response.Usage = usage
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
			CreatedAt:      hit.CreatedAt,
			LastRunAt:      hit.LastRunAt,
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
		OwnerType:      item.OwnerType,
		AgentKey:       item.AgentKey,
		TeamID:         item.TeamID,
		Source:         item.Source,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		LastRunAt:      item.LastRunAt,
		ArchivedAt:     item.ArchivedAt,
		LastRunID:      item.LastRunID,
		LastRunContent: item.LastRunContent,
		HasAttachments: item.HasAttachments,
	}
	if usage := mapUsageDataPtr(item.Usage); usage != nil {
		resp.Usage = usage
	}
	return resp
}

func mapRunSummary(run chat.RunSummary) api.RunSummary {
	usage := run.Usage
	usage.ModelKey = ""
	response := api.RunSummary{
		RunID:           run.RunID,
		ChatID:          run.ChatID,
		OwnerType:       run.OwnerType,
		AgentKey:        run.AgentKey,
		TeamID:          run.TeamID,
		InitialMessage:  run.InitialMessage,
		AssistantText:   run.AssistantText,
		FinishReason:    run.FinishReason,
		StartedAt:       run.StartedAt,
		Usage:           mapUsageData(usage),
		FeedbackType:    run.FeedbackType,
		FeedbackComment: run.FeedbackComment,
		FeedbackAt:      run.FeedbackAt,
	}
	if run.CompletedAt != 0 {
		completedAt := run.CompletedAt
		response.CompletedAt = &completedAt
	}
	return response
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
		if isTimeContractViolation(archiveErr) {
			sendTimeContractViolation(conn, req.ID, archiveErr)
			conn.CompleteRequest(req.ID)
			return
		}
		conn.SendError(req.ID, "unavailable", 503, archiveErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := validatePublicTimeContract(response); err != nil {
		sendTimeContractViolation(conn, req.ID, err)
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
		if isTimeContractViolation(listErr) {
			sendTimeContractViolation(conn, req.ID, listErr)
			conn.CompleteRequest(req.ID)
			return
		}
		conn.SendError(req.ID, "unavailable", 503, listErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := validatePublicTimeContract(response); err != nil {
		sendTimeContractViolation(conn, req.ID, err)
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
		if isTimeContractViolation(loadErr) {
			sendTimeContractViolation(conn, req.ID, loadErr)
			conn.CompleteRequest(req.ID)
			return
		}
		conn.SendError(req.ID, "internal_error", 500, loadErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := validatePublicTimeContract(response); err != nil {
		sendTimeContractViolation(conn, req.ID, err)
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
		if isTimeContractViolation(searchErr) {
			sendTimeContractViolation(conn, req.ID, searchErr)
			conn.CompleteRequest(req.ID)
			return
		}
		conn.SendError(req.ID, "unavailable", 503, searchErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := validatePublicTimeContract(response); err != nil {
		sendTimeContractViolation(conn, req.ID, err)
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
	if err := validatePublicTimeContract(response); err != nil {
		sendTimeContractViolation(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsArchiveRestore(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.ArchiveRestoreRequest](req)
	if err != nil || len(payload.ChatIDs) == 0 {
		conn.SendError(req.ID, "invalid_request", 400, "chatIds is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, restoreErr := s.restoreArchives(payload.ChatIDs)
	if restoreErr != nil {
		if isTimeContractViolation(restoreErr) {
			sendTimeContractViolation(conn, req.ID, restoreErr)
			conn.CompleteRequest(req.ID)
			return
		}
		conn.SendError(req.ID, "unavailable", 503, restoreErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := validatePublicTimeContract(response); err != nil {
		sendTimeContractViolation(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}
