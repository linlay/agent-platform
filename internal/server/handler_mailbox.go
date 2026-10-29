package server

import (
	"errors"
	"net/http"
	"strconv"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/mailbox"
)

func (s *Server) handleMails(w http.ResponseWriter, r *http.Request) {
	if s.deps.Mailbox == nil {
		writeJSON(w, 503, api.Failure(503, "mailbox unavailable"))
		return
	}
	subject := ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		subject = p.Subject
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, err := s.deps.Mailbox.Store.List(mailbox.Filter{NewestFirst: true, AgentKey: q.Get("agentKey"), ChatID: q.Get("chatId"), Kind: mailbox.Kind(q.Get("kind")), Status: mailbox.Status(q.Get("status")), Subject: subject, Limit: limit, Offset: offset})
	if err != nil {
		writeJSON(w, 500, api.Failure(500, "failed to read mailbox"))
		return
	}
	writeJSON(w, 200, api.Success(map[string]any{"mails": rows}))
}
func (s *Server) handleMailAction(w http.ResponseWriter, r *http.Request) {
	if s.deps.Mailbox == nil {
		writeJSON(w, 503, api.Failure(503, "mailbox unavailable"))
		return
	}
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
