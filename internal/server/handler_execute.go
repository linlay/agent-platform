package server

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

// executeRequestPart is the multipart field that carries the JSON request.
const executeRequestPart = "request"

// handleExecute runs one task in a new Chat and answers with the accepted
// result_submit. The caller waits for the whole Run; disconnecting cancels it.
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	command, err := s.decodeExecuteCommand(r)
	if err != nil {
		writeQueryStartError(w, err)
		return
	}
	result, err := s.deps.Runtime.Execute(r.Context(), command)
	if err != nil {
		writeQueryStartError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(result))
}

func (s *Server) decodeExecuteCommand(r *http.Request) (runtimetypes.ExecuteCommand, error) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var body []byte
	var files map[string]*multipart.FileHeader
	switch mediaType {
	case "multipart/form-data":
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return runtimetypes.ExecuteCommand{}, executeBadRequest("invalid multipart form")
		}
		values := r.MultipartForm.Value[executeRequestPart]
		if len(values) != 1 {
			return runtimetypes.ExecuteCommand{}, executeBadRequest(`multipart request needs exactly one "request" field with the JSON request`)
		}
		body = []byte(values[0])
		var err error
		if files, err = executeFileParts(r.MultipartForm); err != nil {
			return runtimetypes.ExecuteCommand{}, err
		}
	case "", "application/json":
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return runtimetypes.ExecuteCommand{}, executeBadRequest("invalid request body")
		}
		body = data
	default:
		return runtimetypes.ExecuteCommand{}, executeBadRequest("Content-Type must be application/json or multipart/form-data")
	}
	var req api.ExecuteRequest
	if err := queryinput.DecodeStrictJSON(body, &req); err != nil {
		if errors.Is(err, queryinput.ErrUnknownField) {
			return runtimetypes.ExecuteCommand{}, &statusError{Status: http.StatusBadRequest, Code: "unknown_field", Message: err.Error(), Data: map[string]any{"error": map[string]any{"code": "unknown_field", "message": err.Error()}}}
		}
		return runtimetypes.ExecuteCommand{}, executeBadRequest("invalid request body")
	}
	command := runtimetypes.ExecuteCommand{
		OriginalQuery:   append([]byte(nil), body...),
		RequestID:       req.RequestID,
		AgentKey:        req.AgentKey,
		Message:         req.Message,
		AccessLevel:     req.AccessLevel,
		Input:           req.Input,
		OutputSchema:    req.OutputSchema,
		Locale:          requestLocale(r, s.deps.Config.Prompts.Runtime.ResolveLocale("")),
		ResourceBaseURL: s.requestBaseURL(r),
		ChatSource:      chatSourceFromContext(r.Context()),
	}
	if principal := PrincipalFromContext(r.Context()); principal != nil {
		command.Caller.Subject = strings.TrimSpace(principal.Subject)
	}
	for part := range files {
		command.FileParts = append(command.FileParts, part)
	}
	if len(files) > 0 {
		source := ""
		if subject := command.Caller.Subject; subject != "" {
			source = api.ChatSourceQueryPrefix + subject
		}
		agentKey := strings.TrimSpace(req.AgentKey)
		command.StoreFiles = func(chatID string) (map[string]string, error) {
			return s.storeExecuteFiles(chatID, agentKey, source, files)
		}
	}
	return command, nil
}

// executeFileParts takes one file per multipart field. Stored files keep their
// base names in the Chat directory, so two parts may not share a name.
func executeFileParts(form *multipart.Form) (map[string]*multipart.FileHeader, error) {
	files := map[string]*multipart.FileHeader{}
	names := map[string]string{}
	for part, headers := range form.File {
		if part == executeRequestPart {
			return nil, executeBadRequest(`"request" must be a text field, not a file`)
		}
		if len(headers) != 1 {
			return nil, executeBadRequest("file part " + part + " must carry exactly one file")
		}
		name := safeFilename(headers[0].Filename)
		if strings.EqualFold(name, uploadManifestName) {
			return nil, executeBadRequest("file name is reserved by the system; rename the file before uploading")
		}
		key := strings.ToLower(name)
		if other, exists := names[key]; exists {
			return nil, executeBadRequest("file parts " + other + " and " + part + " have the same file name " + name)
		}
		names[key] = part
		files[part] = headers[0]
	}
	return files, nil
}

// storeExecuteFiles creates the Chat and stores the request's files in it as
// uploads, returning the @chat path of each part.
func (s *Server) storeExecuteFiles(chatID string, agentKey string, source string, files map[string]*multipart.FileHeader) (map[string]string, error) {
	summary, created, err := s.deps.Chats.EnsureChatWithSource(chatID, agentKey, "", source)
	if err != nil {
		return nil, err
	}
	if created {
		s.broadcast("chat.created", chatCreatedPayload(chatID, summary.ChatName, agentKey, summary.CreatedAt, summary.Source))
	}
	paths := make(map[string]string, len(files))
	for part, header := range files {
		stored, err := s.storeUploadedPart(chatID, header)
		if err != nil {
			return nil, err
		}
		alias, err := chat.BuildChatAliasRef(stored.Name)
		if err != nil {
			return nil, err
		}
		paths[part] = alias
	}
	return paths, nil
}

func executeBadRequest(message string) *statusError {
	return &statusError{Status: http.StatusBadRequest, Code: "invalid_request", Message: message, Data: map[string]any{"error": map[string]any{"code": "invalid_request", "message": message}}}
}
