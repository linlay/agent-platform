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

const schema = `CREATE TABLE mails (
 id TEXT PRIMARY KEY, kind TEXT NOT NULL, status TEXT NOT NULL,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 to_agent_key TEXT NOT NULL, to_chat_id TEXT NOT NULL, opened_chat INTEGER NOT NULL DEFAULT 0,
 origin_type TEXT NOT NULL, from_agent_key TEXT NOT NULL DEFAULT '', from_chat_id TEXT NOT NULL DEFAULT '',
 from_run_id TEXT NOT NULL DEFAULT '', from_tool_id TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '',
 input TEXT, message TEXT NOT NULL DEFAULT '', refs TEXT, files TEXT, purged_at INTEGER NOT NULL DEFAULT 0,
 hop INTEGER NOT NULL, access_level TEXT NOT NULL DEFAULT '', access_grant TEXT, model TEXT, request_digest TEXT NOT NULL DEFAULT '',
 answered_by TEXT NOT NULL DEFAULT '', reply_key TEXT NOT NULL DEFAULT '', reply_of_run TEXT NOT NULL DEFAULT '',
 outcome TEXT NOT NULL DEFAULT '', awaiting_id TEXT NOT NULL DEFAULT '', awaiting_mode TEXT NOT NULL DEFAULT '',
 awaiting_source TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '',
 delivery TEXT NOT NULL DEFAULT '', run_id TEXT NOT NULL DEFAULT '', steer_id TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0, consumed_at INTEGER NOT NULL DEFAULT 0,
 resume_phase TEXT NOT NULL DEFAULT '', evidence TEXT NOT NULL DEFAULT '', error TEXT
);
CREATE UNIQUE INDEX mails_idem ON mails(from_run_id,from_tool_id) WHERE from_tool_id <> '';
CREATE UNIQUE INDEX mails_reply ON mails(reply_key) WHERE kind = 'reply';
CREATE UNIQUE INDEX mails_ask ON mails(from_run_id,awaiting_id) WHERE kind = 'ask';
CREATE INDEX mails_deliver ON mails(to_chat_id,status,created_at,id);
CREATE INDEX mails_inbox ON mails(to_agent_key,created_at);
CREATE INDEX mails_run ON mails(run_id);
CREATE INDEX mails_status ON mails(status);
CREATE INDEX mails_answered ON mails(answered_by);`

const columns = `id,kind,status,created_at,updated_at,to_agent_key,to_chat_id,opened_chat,
origin_type,from_agent_key,from_chat_id,from_run_id,from_tool_id,subject,
input,message,refs,files,purged_at,hop,access_level,access_grant,model,request_digest,
answered_by,reply_key,reply_of_run,outcome,awaiting_id,awaiting_mode,awaiting_source,parent_id,
delivery,run_id,steer_id,attempts,consumed_at,resume_phase,evidence,error`

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
	spec := sqlitecontract.Spec{Name: "mail", ApplicationID: 0x41504D4C, UserVersion: 1, BuildCanonical: create}
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
		m.Delivery, m.RunID, m.SteerID, m.Attempts, m.ConsumedAt, m.ResumePhase, m.Evidence, jsonText(m.Error)}
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Mail, error) {
	var m Mail
	var input, refs, files, grant, model, errData sql.NullString
	err := row.Scan(&m.ID, &m.Kind, &m.Status, &m.CreatedAt, &m.UpdatedAt, &m.ToAgentKey, &m.ToChatID, &m.OpenedChat,
		&m.OriginType, &m.FromAgentKey, &m.FromChatID, &m.FromRunID, &m.FromToolID, &m.Subject,
		&input, &m.Message, &refs, &files, &m.PurgedAt, &m.Hop, &m.AccessLevel, &grant, &model, &m.RequestDigest,
		&m.AnsweredBy, &m.ReplyKey, &m.ReplyOfRun, &m.Outcome, &m.AwaitingID, &m.AwaitingMode, &m.AwaitingSource, &m.ParentID,
		&m.Delivery, &m.RunID, &m.SteerID, &m.Attempts, &m.ConsumedAt, &m.ResumePhase, &m.Evidence, &errData)
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
	_, err := tx.Exec("INSERT INTO mails ("+columns+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...)
	return err
}
func update(tx *sql.Tx, m Mail) error {
	parts := strings.Split(strings.ReplaceAll(columns, "\n", ""), ",")
	for i := 1; i < len(parts); i++ {
		parts[i] += "=?"
	}
	args := values(m)[1:]
	args = append(args, m.ID)
	_, err := tx.Exec("UPDATE mails SET "+strings.Join(parts[1:], ",")+" WHERE id=?", args...)
	return err
}
func (s *Store) Get(id string) (Mail, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM mails WHERE id=?", id))
}
func (s *Store) Invocation(runID, toolID string) (Mail, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM mails WHERE from_run_id=? AND from_tool_id=?", runID, toolID))
}
func (s *Store) Accept(m Mail) (Mail, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return m, false, err
	}
	defer tx.Rollback()
	if m.FromToolID != "" {
		old, e := scan(tx.QueryRow("SELECT "+columns+" FROM mails WHERE from_run_id=? AND from_tool_id=?", m.FromRunID, m.FromToolID))
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
		old, e := scan(tx.QueryRow("SELECT "+columns+" FROM mails WHERE kind='ask' AND from_run_id=? AND awaiting_id=?", m.FromRunID, m.AwaitingID))
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
	m, err := scan(tx.QueryRow("SELECT "+columns+" FROM mails WHERE id=?", id))
	if err != nil {
		return m, err
	}
	if err = change(&m); err != nil {
		return m, err
	}
	m.UpdatedAt = now()
	if err = update(tx, m); err != nil {
		return m, err
	}
	return m, tx.Commit()
}
func (s *Store) List(f Filter) ([]Mail, error) {
	where, args := []string{"1=1"}, []any{}
	for _, v := range []struct{ col, value string }{{"to_agent_key", f.AgentKey}, {"to_chat_id", f.ChatID}, {"kind", string(f.Kind)}, {"status", string(f.Status)}} {
		if v.value != "" {
			where = append(where, v.col+"=?")
			args = append(args, v.value)
		}
	}
	if !f.AllSubjects {
		where = append(where, "subject=?")
		args = append(args, f.Subject)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	offset := max(f.Offset, 0)
	args = append(args, limit, offset)
	order := "created_at,id"
	if f.NewestFirst {
		order = "created_at DESC,id DESC"
	}
	rows, err := s.db.Query("SELECT "+columns+" FROM mails WHERE "+strings.Join(where, " AND ")+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
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
// The deterministic reply key also survives repeated terminal callbacks.
func (s *Store) Complete(replies []Mail, letters []Mail, failed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range replies {
		var id string
		err := tx.QueryRow("SELECT id FROM mails WHERE kind='reply' AND reply_key=?", r.ReplyKey).Scan(&id)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err = insert(tx, r); err != nil {
			return err
		}
	}
	for _, m := range letters {
		var replyID string
		key := "run:" + m.RunID + ":" + m.FromChatID
		if failed {
			key = "letter:" + m.ID
		}
		if err := tx.QueryRow("SELECT id FROM mails WHERE kind='reply' AND reply_key=?", key).Scan(&replyID); err != nil {
			return err
		}
		status := Done
		if failed {
			status = Failed
		}
		if _, err := tx.Exec("UPDATE mails SET answered_by=?,status=?,updated_at=?,error=? WHERE id=? AND answered_by='' AND status NOT IN ('discarded','withdrawn','done','failed')", replyID, status, now(), jsonText(m.Error), m.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
