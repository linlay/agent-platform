package mailbox

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"agent-platform/internal/sqlitecontract"
	_ "modernc.org/sqlite"
)

const schema = `CREATE TABLE MAILS (
 MAIL_ID_ TEXT PRIMARY KEY, KIND_ TEXT NOT NULL, STATUS_ TEXT NOT NULL,
 CREATED_AT_ INTEGER NOT NULL, UPDATED_AT_ INTEGER NOT NULL,
 TO_AGENT_KEY_ TEXT NOT NULL, TO_CHAT_ID_ TEXT NOT NULL, OPENED_CHAT_ INTEGER NOT NULL DEFAULT 0,
 ORIGIN_TYPE_ TEXT NOT NULL, FROM_AGENT_KEY_ TEXT NOT NULL DEFAULT '', FROM_CHAT_ID_ TEXT NOT NULL DEFAULT '',
 FROM_RUN_ID_ TEXT NOT NULL DEFAULT '', FROM_TOOL_ID_ TEXT NOT NULL DEFAULT '', SUBJECT_ TEXT NOT NULL DEFAULT '',
 INPUT_ TEXT, MESSAGE_ TEXT NOT NULL DEFAULT '', REFERENCES_ TEXT, FILES_ TEXT, BODY_PURGED_AT_ INTEGER NOT NULL DEFAULT 0,
 HOP_ INTEGER NOT NULL, ACCESS_LEVEL_ TEXT NOT NULL DEFAULT '', ACCESS_GRANT_ TEXT, MODEL_ TEXT, REQUEST_DIGEST_ TEXT NOT NULL DEFAULT '',
 ANSWERED_BY_MAIL_ID_ TEXT NOT NULL DEFAULT '', REPLY_KEY_ TEXT NOT NULL DEFAULT '', REPLY_OF_RUN_ID_ TEXT NOT NULL DEFAULT '',
 OUTCOME_ TEXT NOT NULL DEFAULT '', AWAITING_ID_ TEXT NOT NULL DEFAULT '', AWAITING_MODE_ TEXT NOT NULL DEFAULT '',
 AWAITING_SOURCE_ TEXT NOT NULL DEFAULT '', PARENT_MAIL_ID_ TEXT NOT NULL DEFAULT '',
 DELIVERY_ TEXT NOT NULL DEFAULT '', RUN_ID_ TEXT NOT NULL DEFAULT '', STEER_ID_ TEXT NOT NULL DEFAULT '',
 ATTEMPTS_ INTEGER NOT NULL DEFAULT 0, CONSUMED_AT_ INTEGER NOT NULL DEFAULT 0,
 RESUME_PHASE_ TEXT NOT NULL DEFAULT '', EVIDENCE_ TEXT NOT NULL DEFAULT '', ERROR_ TEXT,
 TERMINAL_AT_ INTEGER NOT NULL DEFAULT 0, FROM_CHAT_DELETED_AT_ INTEGER NOT NULL DEFAULT 0, TO_CHAT_DELETED_AT_ INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IDX_MAILS_IDEM_ ON MAILS(FROM_RUN_ID_,FROM_TOOL_ID_) WHERE FROM_TOOL_ID_ <> '';
CREATE UNIQUE INDEX IDX_MAILS_REPLY_ ON MAILS(REPLY_KEY_) WHERE KIND_ = 'reply';
CREATE UNIQUE INDEX IDX_MAILS_ASK_ ON MAILS(FROM_RUN_ID_,AWAITING_ID_) WHERE KIND_ = 'ask';
CREATE INDEX IDX_MAILS_DELIVER_ ON MAILS(TO_CHAT_ID_,STATUS_,CREATED_AT_,MAIL_ID_);
CREATE INDEX IDX_MAILS_INBOX_ ON MAILS(TO_AGENT_KEY_,CREATED_AT_);
CREATE INDEX IDX_MAILS_RUN_ ON MAILS(RUN_ID_);
CREATE INDEX IDX_MAILS_STATUS_ ON MAILS(STATUS_);
CREATE INDEX IDX_MAILS_ANSWERED_ ON MAILS(ANSWERED_BY_MAIL_ID_);
CREATE INDEX IDX_MAILS_TERMINAL_ ON MAILS(TERMINAL_AT_,BODY_PURGED_AT_);`

const columns = `MAIL_ID_,KIND_,STATUS_,CREATED_AT_,UPDATED_AT_,TO_AGENT_KEY_,TO_CHAT_ID_,OPENED_CHAT_,
ORIGIN_TYPE_,FROM_AGENT_KEY_,FROM_CHAT_ID_,FROM_RUN_ID_,FROM_TOOL_ID_,SUBJECT_,
INPUT_,MESSAGE_,REFERENCES_,FILES_,BODY_PURGED_AT_,HOP_,ACCESS_LEVEL_,ACCESS_GRANT_,MODEL_,REQUEST_DIGEST_,
ANSWERED_BY_MAIL_ID_,REPLY_KEY_,REPLY_OF_RUN_ID_,OUTCOME_,AWAITING_ID_,AWAITING_MODE_,AWAITING_SOURCE_,PARENT_MAIL_ID_,
DELIVERY_,RUN_ID_,STEER_ID_,ATTEMPTS_,CONSUMED_AT_,RESUME_PHASE_,EVIDENCE_,ERROR_,TERMINAL_AT_,FROM_CHAT_DELETED_AT_,TO_CHAT_DELETED_AT_`

var ErrNotFound = errors.New("mail_not_found")
var errStale = errors.New("stale mail callback")

type Store struct {
	mu sync.Mutex
	db *sql.DB
}

func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "mail.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	create := func(db *sql.DB) error { _, err := db.Exec(schema); return err }
	spec := sqlitecontract.Spec{Name: "mail-v1", ApplicationID: 0x41504D4C, UserVersion: 1, BuildCanonical: create}
	if err = sqlitecontract.InitializeOrVerify(db, path, path, spec, nil, func() error { return create(db) }); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func jsonText(v any) string   { b, _ := json.Marshal(v); return string(b) }
func values(m Mail) []any {
	return []any{m.ID, m.Kind, m.Status, m.CreatedAt, m.UpdatedAt, m.ToAgentKey, m.ToChatID, m.OpenedChat,
		m.OriginType, m.FromAgentKey, m.FromChatID, m.FromRunID, m.FromToolID, m.Subject,
		string(m.Input), m.Message, jsonText(m.References), jsonText(m.Files), m.PurgedAt, m.Hop, m.AccessLevel, jsonText(m.AccessGrant), jsonText(m.Model), m.RequestDigest,
		m.AnsweredBy, m.ReplyKey, m.ReplyOfRun, m.Outcome, m.AwaitingID, m.AwaitingMode, m.AwaitingSource, m.ParentID,
		m.Delivery, m.RunID, m.SteerID, m.Attempts, m.ConsumedAt, m.ResumePhase, m.Evidence, jsonText(m.Error), m.TerminalAt, m.FromChatDeletedAt, m.ToChatDeletedAt}
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Mail, error) {
	var m Mail
	var input, refs, files, grant, model, errData sql.NullString
	err := row.Scan(&m.ID, &m.Kind, &m.Status, &m.CreatedAt, &m.UpdatedAt, &m.ToAgentKey, &m.ToChatID, &m.OpenedChat,
		&m.OriginType, &m.FromAgentKey, &m.FromChatID, &m.FromRunID, &m.FromToolID, &m.Subject,
		&input, &m.Message, &refs, &files, &m.PurgedAt, &m.Hop, &m.AccessLevel, &grant, &model, &m.RequestDigest,
		&m.AnsweredBy, &m.ReplyKey, &m.ReplyOfRun, &m.Outcome, &m.AwaitingID, &m.AwaitingMode, &m.AwaitingSource, &m.ParentID,
		&m.Delivery, &m.RunID, &m.SteerID, &m.Attempts, &m.ConsumedAt, &m.ResumePhase, &m.Evidence, &errData, &m.TerminalAt, &m.FromChatDeletedAt, &m.ToChatDeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if input.String != "" {
		m.Input = json.RawMessage(input.String)
	}
	for _, v := range []struct {
		raw string
		dst any
	}{{refs.String, &m.References}, {files.String, &m.Files}, {grant.String, &m.AccessGrant}, {model.String, &m.Model}, {errData.String, &m.Error}} {
		if v.raw != "" {
			if err := json.Unmarshal([]byte(v.raw), v.dst); err != nil {
				return m, fmt.Errorf("mail %s: invalid persisted JSON: %w", m.ID, err)
			}
		}
	}
	return m, nil
}
func insert(tx *sql.Tx, m Mail) error {
	args := values(m)
	_, err := tx.Exec("INSERT INTO MAILS ("+columns+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...)
	return err
}
func update(tx *sql.Tx, m Mail) error {
	parts := strings.Split(strings.ReplaceAll(columns, "\n", ""), ",")
	for i := 1; i < len(parts); i++ {
		parts[i] += "=?"
	}
	args := values(m)[1:]
	args = append(args, m.ID)
	_, err := tx.Exec("UPDATE MAILS SET "+strings.Join(parts[1:], ",")+" WHERE MAIL_ID_=?", args...)
	return err
}
func (s *Store) Get(id string) (Mail, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM MAILS WHERE MAIL_ID_=?", id))
}

// Lifecycle hooks run for every Run, so they read only indexed slices.
func (s *Store) query(where string, args ...any) ([]Mail, error) {
	rows, err := s.db.Query("SELECT "+columns+" FROM MAILS WHERE "+where+" ORDER BY CREATED_AT_,MAIL_ID_", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mail{}
	for rows.Next() {
		m, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) exists(where string, args ...any) (bool, error) {
	var one int
	err := s.db.QueryRow("SELECT 1 FROM MAILS WHERE "+where+" LIMIT 1", args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
func (s *Store) ByRun(runID string) ([]Mail, error) {
	return s.query("RUN_ID_=?", runID)
}
func (s *Store) AsksFromRun(runID string) ([]Mail, error) {
	return s.query("KIND_='ask' AND FROM_RUN_ID_=?", runID)
}
func (s *Store) Ask(runID, awaitingID string) (Mail, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM MAILS WHERE KIND_='ask' AND FROM_RUN_ID_=? AND AWAITING_ID_=?", runID, awaitingID))
}

// StartedLetter is the letter that opened runID, if it is still being worked.
func (s *Store) StartedLetter(runID string) (Mail, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM MAILS WHERE RUN_ID_=? AND KIND_='letter' AND DELIVERY_='started' AND STATUS_ IN ('delivered','inflight') ORDER BY CREATED_AT_,MAIL_ID_ LIMIT 1", runID))
}
func (s *Store) AnsweredLetters(replyID string) ([]string, error) {
	rows, err := s.db.Query("SELECT MAIL_ID_ FROM MAILS WHERE ANSWERED_BY_MAIL_ID_=? ORDER BY MAIL_ID_", replyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// OwnsRun grants control only for a Run opened by the caller's own letter.
func (s *Store) OwnsRun(runID, agentKey, subject string) (bool, error) {
	return s.exists("RUN_ID_=? AND KIND_='letter' AND DELIVERY_='started' AND FROM_AGENT_KEY_=? AND SUBJECT_=?", runID, agentKey, subject)
}
func (s *Store) OpenedBy(chatID, agentKey, subject string) (bool, error) {
	return s.exists("TO_CHAT_ID_=? AND OPENED_CHAT_=1 AND FROM_AGENT_KEY_=? AND SUBJECT_=? AND TO_CHAT_DELETED_AT_=0", chatID, agentKey, subject)
}
func (s *Store) OpenedChats(agentKey, subject string) ([]Mail, error) {
	return s.query("FROM_AGENT_KEY_=? AND SUBJECT_=? AND OPENED_CHAT_=1 AND TO_CHAT_DELETED_AT_=0", agentKey, subject)
}
func (s *Store) ForChat(chatID string) ([]Mail, error) {
	return s.query("FROM_CHAT_ID_=? OR TO_CHAT_ID_=?", chatID, chatID)
}

// Unsettled excludes the statuses that are terminal for every kind.
func (s *Store) Unsettled() ([]Mail, error) {
	return s.query("STATUS_ NOT IN ('done','failed','discarded','withdrawn')")
}
func (s *Store) Invocation(runID, toolID string) (Mail, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM MAILS WHERE FROM_RUN_ID_=? AND FROM_TOOL_ID_=?", runID, toolID))
}
func (s *Store) Accept(m Mail) (Mail, bool, error) {
	stampTerminal(&m)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return m, false, err
	}
	defer tx.Rollback()
	if m.FromToolID != "" {
		old, e := scan(tx.QueryRow("SELECT "+columns+" FROM MAILS WHERE FROM_RUN_ID_=? AND FROM_TOOL_ID_=?", m.FromRunID, m.FromToolID))
		if e == nil {
			if old.RequestDigest != m.RequestDigest {
				return old, false, failure("idempotency_conflict", "this invocation already used different arguments")
			}
			return old, false, nil
		}
		if !errors.Is(e, ErrNotFound) {
			return m, false, e
		}
	}
	if m.Kind == Ask {
		old, e := scan(tx.QueryRow("SELECT "+columns+" FROM MAILS WHERE KIND_='ask' AND FROM_RUN_ID_=? AND AWAITING_ID_=?", m.FromRunID, m.AwaitingID))
		if e == nil {
			return old, false, nil
		}
		if !errors.Is(e, ErrNotFound) {
			return m, false, e
		}
	}
	if err = insert(tx, m); err != nil {
		return m, false, err
	}
	return m, true, tx.Commit()
}
func (s *Store) Change(id string, change func(*Mail) error) (Mail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Mail{}, err
	}
	defer tx.Rollback()
	m, err := scan(tx.QueryRow("SELECT "+columns+" FROM MAILS WHERE MAIL_ID_=?", id))
	if err != nil {
		return m, err
	}
	if err = change(&m); err != nil {
		return m, err
	}
	m.UpdatedAt = now()
	stampTerminal(&m)
	if err = update(tx, m); err != nil {
		return m, err
	}
	return m, tx.Commit()
}
func (s *Store) List(f Filter) ([]Mail, error) {
	where, args := []string{"1=1"}, []any{}
	for _, v := range []struct{ col, value string }{{"TO_AGENT_KEY_", f.AgentKey}, {"TO_CHAT_ID_", f.ChatID}, {"KIND_", string(f.Kind)}, {"STATUS_", string(f.Status)}} {
		if v.value != "" {
			where = append(where, v.col+"=?")
			args = append(args, v.value)
		}
	}
	if !f.AllSubjects {
		where = append(where, "SUBJECT_=?")
		args = append(args, f.Subject)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	offset := max(f.Offset, 0)
	args = append(args, limit, offset)
	order := "CREATED_AT_,MAIL_ID_"
	if f.NewestFirst {
		order = "CREATED_AT_ DESC,MAIL_ID_ DESC"
	}
	rows, err := s.db.Query("SELECT "+columns+" FROM MAILS WHERE "+strings.Join(where, " AND ")+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mail{}
	for rows.Next() {
		m, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Complete atomically enqueues replies and marks only still-unanswered letters.
// The deterministic reply key also survives repeated terminal callbacks. It
// returns the replies it inserted and the letters it settled.
func (s *Store) Complete(replies []Mail, letters []Mail, failed bool) ([]Mail, []Mail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var inserted, settled []Mail
	// Re-read under the transaction: a deletion, discard or another completion
	// may have settled these letters after the caller took its snapshot.
	var pending []Mail
	for _, snapshot := range letters {
		current, err := scan(tx.QueryRow("SELECT "+columns+" FROM MAILS WHERE MAIL_ID_=?", snapshot.ID))
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if current.Terminal() || current.AnsweredBy != "" || current.FromChatDeletedAt != 0 {
			continue
		}
		current.Error = snapshot.Error
		pending = append(pending, current)
	}
	letters = pending
	for _, r := range replies {
		eligible := false
		for _, m := range letters {
			key := "run:" + m.RunID + ":" + m.FromChatID
			if failed {
				key = "letter:" + m.ID
			}
			if r.ReplyKey == key {
				eligible = true
				r.FromChatDeletedAt = m.ToChatDeletedAt
				r.ToChatDeletedAt = m.FromChatDeletedAt
				break
			}
		}
		if !eligible {
			continue
		}
		stampTerminal(&r)
		var id string
		err := tx.QueryRow("SELECT MAIL_ID_ FROM MAILS WHERE KIND_='reply' AND REPLY_KEY_=?", r.ReplyKey).Scan(&id)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		if err = insert(tx, r); err != nil {
			return nil, nil, err
		}
		inserted = append(inserted, r)
	}
	for _, m := range letters {
		var replyID string
		key := "run:" + m.RunID + ":" + m.FromChatID
		if failed {
			key = "letter:" + m.ID
		}
		if err := tx.QueryRow("SELECT MAIL_ID_ FROM MAILS WHERE KIND_='reply' AND REPLY_KEY_=?", key).Scan(&replyID); err != nil {
			return nil, nil, err
		}
		status := Done
		if failed {
			status = Failed
		}
		result, err := tx.Exec("UPDATE MAILS SET ANSWERED_BY_MAIL_ID_=?,STATUS_=?,UPDATED_AT_=?,TERMINAL_AT_=?,ERROR_=? WHERE MAIL_ID_=? AND ANSWERED_BY_MAIL_ID_='' AND STATUS_ NOT IN ('discarded','withdrawn','done','failed')", replyID, status, now(), now(), jsonText(m.Error), m.ID)
		if err != nil {
			return nil, nil, err
		}
		if n, _ := result.RowsAffected(); n > 0 {
			row, err := scan(tx.QueryRow("SELECT "+columns+" FROM MAILS WHERE MAIL_ID_=?", m.ID))
			if err != nil {
				return nil, nil, err
			}
			settled = append(settled, row)
		}
	}
	return inserted, settled, tx.Commit()
}

// TerminalAt is independent of metadata edits and body cleanup.
func stampTerminal(m *Mail) {
	if !m.Terminal() {
		m.TerminalAt = 0
	} else if m.TerminalAt == 0 {
		m.TerminalAt = now()
	}
}
