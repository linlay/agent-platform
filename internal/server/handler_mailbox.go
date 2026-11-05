package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/mailbox"
)

func (s *Server) handleMails(w http.ResponseWriter, r *http.Request) {
	subject := ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		subject = p.Subject
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	filter := mailbox.Filter{NewestFirst: true, AgentKey: q.Get("agentKey"), ChatID: q.Get("chatId"), Kind: mailbox.Kind(q.Get("kind")), Status: mailbox.Status(q.Get("status")), Subject: subject, Limit: limit, Offset: offset}
	rows, err := s.deps.Mailbox.Store.List(filter)
	if err != nil {
		writeJSON(w, 500, api.Failure(500, "failed to read mailbox"))
		return
	}
	total, err := s.deps.Mailbox.Store.Count(filter)
	if err != nil {
		writeJSON(w, 500, api.Failure(500, "failed to count mailbox"))
		return
	}
	summaries := make([]map[string]any, 0, len(rows))
	for _, m := range rows {
		b, _ := json.Marshal(m)
		var row map[string]any
		_ = json.Unmarshal(b, &row)
		delete(row, "input")
		delete(row, "accessGrant")
		delete(row, "files")
		delete(row, "references")
		text := []rune(s.deps.Mailbox.Body(m))
		if len(text) > 200 {
			text = text[:200]
		}
		row["message"] = string(text)
		summaries = append(summaries, row)
	}
	writeJSON(w, 200, api.Success(map[string]any{"mails": summaries, "total": total}))
}
func (s *Server) handleMailAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MailID string `json:"mailId"`
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, 400, api.Failure(400, "invalid mail action"))
		return
	}
	subject := ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		subject = p.Subject
	}
	m, err := s.deps.Mailbox.Action(r.Context(), input.MailID, input.Action, subject, httpControlScope(r.Context(), ""))
	if err != nil {
		code := http.StatusInternalServerError
		var failure *contracts.RunToolError
		switch {
		case errors.Is(err, mailbox.ErrNotFound):
			code = http.StatusNotFound
		case errors.As(err, &failure) && failure.Code == "mail_not_owned":
			code = http.StatusForbidden
		case errors.As(err, &failure) && failure.Code == "mail_state_conflict":
			code = http.StatusConflict
		case errors.As(err, &failure):
			code = http.StatusBadRequest
		}
		writeJSON(w, code, api.Failure(code, err.Error()))
		return
	}
	writeJSON(w, 200, api.Success(m))
}

func (s *Server) handleMailDetail(w http.ResponseWriter, r *http.Request) {
	m, err := s.deps.Mailbox.Store.Get(r.URL.Query().Get("mailId"))
	if err != nil {
		writeJSON(w, 404, api.Failure(404, "mail not found"))
		return
	}
	subject := ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		subject = p.Subject
	}
	if m.Subject != subject {
		writeJSON(w, 404, api.Failure(404, "mail not found"))
		return
	}
	if s.deps.Registry != nil {
		if def, ok := s.deps.Registry.AgentDefinition(m.FromAgentKey); ok {
			m.FromAgentName = def.Name
		}
	}
	m.Input = nil
	m.AccessGrant = nil
	writeJSON(w, 200, api.Success(m))
}
