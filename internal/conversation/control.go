package conversation

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/conversationexport"
)

type ControlCaller struct{ Subject, ChatID, AgentKey, RunID, ToolID string }
type ControlMessage struct {
	RunID     string `json:"runId"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Offset    int    `json:"offset,omitempty"`
	Continued bool   `json:"continued,omitempty"`
}
type controlCursor struct {
	Fingerprint string `json:"f"`
	After       string `json:"a,omitempty"`
	ChatID      string `json:"chat,omitempty"`
	Version     int    `json:"v"`
	Index       int    `json:"i"`
	Offset      int    `json:"o,omitempty"`
	Bound       int    `json:"b,omitempty"`
	History     string `json:"h,omitempty"`
}

func controlDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func controlLimit(args map[string]any) (int, error) {
	if v, ok := args["limit"]; ok {
		n, ok := v.(float64)
		if !ok || n < 1 || n > 100 || n != float64(int(n)) {
			return 0, fmt.Errorf("limit must be an integer from 1 to 100")
		}
		return int(n), nil
	}
	return 20, nil
}
func controlString(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func controlBool(m map[string]any, k string) bool     { v, _ := m[k].(bool); return v }
func controlPage(args map[string]any, c ControlCaller) (controlCursor, error) {
	normalized := map[string]any{"subject": c.Subject, "current": c.ChatID, "agent": c.AgentKey}
	for k, v := range args {
		if k != "cursor" && k != "limit" {
			normalized[k] = v
		}
	}
	cur := controlCursor{Version: 2, Fingerprint: controlDigest(normalized)}
	if raw := controlString(args, "cursor"); raw != "" {
		if len(raw) > 3000 {
			return cur, fmt.Errorf("invalid cursor")
		}
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || len(b) > 2048 {
			return cur, fmt.Errorf("invalid cursor")
		}
		codec, err := controlCursorCodec()
		if err != nil {
			return cur, err
		}
		b, err = codec.Open(nil, nil, b, []byte("conversation-control-v2"))
		if err != nil {
			return cur, fmt.Errorf("invalid or expired cursor; restart query")
		}
		var saved controlCursor
		if json.Unmarshal(b, &saved) != nil || saved.Version != 2 || saved.Fingerprint != cur.Fingerprint || saved.Index < 0 || saved.Offset < 0 || saved.Bound < 0 {
			return cur, fmt.Errorf("cursor does not match this query")
		}
		cur = saved
	}
	return cur, nil
}

// Cursors may pass inaccessible records while scanning. Encrypt positions so a
// caller cannot extract another owner's Chat IDs. Process restart expires cursors.
var controlCursorCodec = sync.OnceValues(func() (cipher.AEAD, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
})

func controlNext(cur controlCursor) (string, error) {
	codec, err := controlCursorCodec()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(codec.Seal(nil, nil, b, []byte("conversation-control-v2"))), nil
}
func (s *Service) ResolveControlChat(c ControlCaller, id string, archived bool) (*chat.Summary, error) {
	if id == "" {
		id = c.ChatID
	}
	if !chat.ValidChatID(id) {
		return nil, fmt.Errorf("invalid chatId")
	}
	var summary *chat.Summary
	if archived {
		if s.Archives == nil {
			return nil, ErrNotConfigured
		}
		var err error
		summary, err = s.Archives.ControlSummary(id)
		if err != nil {
			return nil, err
		}
	} else {
		var e error
		summary, e = s.Chats.Summary(id)
		if e != nil {
			return nil, e
		}
	}
	if summary == nil {
		return nil, chat.ErrChatNotFound
	}
	if c.Subject == "" {
		if id != c.ChatID {
			return nil, fmt.Errorf("chat_forbidden: anonymous runs may access only the current Chat")
		}
	} else if owner, ok := strings.CutPrefix(summary.Source, "query:"); ok && owner != "" && owner != c.Subject {
		return nil, fmt.Errorf("chat_forbidden")
	}
	return summary, nil
}
func (s *Service) ControlMessages(c ControlCaller, id string, archived bool) (*chat.Summary, []ControlMessage, error) {
	summary, e := s.ResolveControlChat(c, id, archived)
	if e != nil {
		return nil, nil, e
	}
	var detail chat.Detail
	if archived {
		a, e := s.Archives.LoadArchived(summary.ChatID)
		if e != nil {
			return nil, nil, e
		}
		detail = a.Detail
	} else {
		detail, e = s.Chats.LoadChat(summary.ChatID)
		if e != nil {
			return nil, nil, e
		}
	}
	doc, e := conversationexport.BuildSnapshotDocument(summary, detail.Events, nil, time.Now().UnixMilli(), "zh-CN", nil)
	if errors.Is(e, conversationexport.ErrNoRootTurn) {
		return summary, []ControlMessage{}, nil
	}
	if e != nil {
		return nil, nil, e
	}
	messages := []ControlMessage{}
	for _, turn := range doc.Snapshot.Turns {
		for _, n := range turn.Nodes {
			if n.Role != "user" && n.Role != "assistant" {
				continue
			}
			if n.Kind != "message" && n.Kind != "content" {
				continue
			}
			if n.Text != "" {
				messages = append(messages, ControlMessage{RunID: turn.RunID, Role: n.Role, Content: n.Text})
			}
		}
	}
	return summary, messages, nil
}
func (s *Service) ControlRead(c ControlCaller, args map[string]any) (map[string]any, error) {
	id := controlString(args, "chatId")
	archived := controlBool(args, "archived")
	summary, e := s.ResolveControlChat(c, id, archived)
	if e != nil {
		return nil, e
	}
	view := controlString(args, "view")
	if view == "summary" {
		return map[string]any{"chat": summary}, nil
	}
	if view != "messages" {
		return nil, fmt.Errorf("view must be summary or messages")
	}
	_, messages, e := s.ControlMessages(c, id, archived)
	if e != nil {
		return nil, e
	}
	limit, e := controlLimit(args)
	if e != nil {
		return nil, e
	}
	cur, e := controlPage(args, c)
	if e != nil {
		return nil, e
	}
	if controlString(args, "cursor") == "" {
		cur.Bound = len(messages)
		cur.History = controlDigest(messages)
	}
	if cur.Bound > len(messages) || cur.History != controlDigest(messages[:min(cur.Bound, len(messages))]) {
		return nil, fmt.Errorf("history changed; restart reading")
	}
	end := cur.Bound
	out := []ControlMessage{}
	budget := 16000
	for cur.Index < end && len(out) < limit && budget > 0 {
		item := messages[cur.Index]
		runes := []rune(item.Content)
		if cur.Offset > len(runes) {
			return nil, fmt.Errorf("history changed")
		}
		remain := len(runes) - cur.Offset
		n := remain
		if n > budget {
			n = budget
		}
		item.Content = string(runes[cur.Offset : cur.Offset+n])
		item.Offset = cur.Offset
		item.Continued = n < remain
		out = append(out, item)
		budget -= n
		if n == remain {
			cur.Index++
			cur.Offset = 0
		} else {
			cur.Offset += n
		}
	}
	next := ""
	if cur.Index < end {
		next, e = controlNext(cur)
		if e != nil {
			return nil, e
		}
	}
	return map[string]any{"chatId": summary.ChatID, "messages": out, "nextCursor": next}, nil
}

// controlIDs requires storage paging rather than materializing the entire catalog.
func (s *Service) controlIDs(after string, archived bool) ([]string, error) {
	var store any = s.Chats
	if archived {
		if s.Archives == nil {
			return nil, ErrNotConfigured
		}
		store = s.Archives
	}
	reader, ok := store.(interface {
		ControlChatIDs(string, int) ([]string, error)
	})
	if !ok {
		return nil, fmt.Errorf("bounded conversation paging unavailable")
	}
	return reader.ControlChatIDs(after, 100)
}
func controlScope(args map[string]any) (string, error) {
	scope := controlString(args, "scope")
	if scope == "" {
		scope = "agent"
	}
	if scope != "agent" && scope != "instance" {
		return "", fmt.Errorf("scope must be agent or instance")
	}
	return scope, nil
}
func (s *Service) ControlList(c ControlCaller, args map[string]any) (map[string]any, error) {
	limit, err := controlLimit(args)
	if err != nil {
		return nil, err
	}
	cur, err := controlPage(args, c)
	if err != nil {
		return nil, err
	}
	scope, err := controlScope(args)
	if err != nil {
		return nil, err
	}
	archived := controlBool(args, "archived")
	ids, err := s.controlIDs(cur.After, archived)
	if err != nil {
		return nil, err
	}
	out := []chat.Summary{}
	scanned := 0
	for _, id := range ids {
		cur.After = id
		scanned++
		v, err := s.ResolveControlChat(c, id, archived)
		if err != nil {
			continue
		}
		if v.ChatName == "" && v.LastRunID == "" {
			continue
		}
		if scope == "agent" && v.AgentKey != c.AgentKey {
			continue
		}
		if pin, ok := args["pinned"].(bool); ok && v.Pinned != pin {
			continue
		}
		out = append(out, *v)
		if len(out) >= limit {
			break
		}
	}
	next := ""
	if scanned < len(ids) || len(ids) == 100 {
		next, err = controlNext(cur)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"chats": out, "nextCursor": next}, nil
}
func (s *Service) ControlSearch(c ControlCaller, args map[string]any) (map[string]any, error) {
	query := strings.TrimSpace(controlString(args, "query"))
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit, err := controlLimit(args)
	if err != nil {
		return nil, err
	}
	cur, err := controlPage(args, c)
	if err != nil {
		return nil, err
	}
	scope, err := controlScope(args)
	if err != nil {
		return nil, err
	}
	archived := controlBool(args, "archived")
	target := controlString(args, "chatId")
	ids := []string{}
	if target != "" {
		ids = []string{target}
	} else {
		ids, err = s.controlIDs(cur.After, archived)
		if err != nil {
			return nil, err
		}
	}
	hits := []map[string]any{}
	skipped := []string{}
	more := false
	scanned := 0
	deadline := time.Now().Add(2 * time.Second)
	budget := int64(16 << 20)
	for index, id := range ids {
		if scanned >= 50 || time.Now().After(deadline) || budget <= 0 {
			more = true
			break
		}
		scanned++
		sum, err := s.ResolveControlChat(c, id, archived)
		if err != nil && target != "" {
			return nil, err
		}
		if err != nil || (target == "" && scope == "agent" && sum.AgentKey != c.AgentKey) {
			cur.After = id
			cur.Offset = 0
			continue
		}
		var historyStore any = s.Chats
		if archived {
			historyStore = s.Archives
		}
		if reader, ok := historyStore.(interface{ ControlHistorySize(string) (int64, error) }); ok {
			size, err := reader.ControlHistorySize(id)
			if err != nil {
				return nil, err
			}
			if size > 8<<20 {
				skipped = append(skipped, id)
				cur.After = id
				cur.Offset = 0
				continue
			}
			if size > budget {
				more = true
				break
			}
			budget -= size
		}
		_, messages, err := s.ControlMessages(c, id, archived)
		if err != nil {
			return nil, err
		}
		if cur.ChatID != id {
			cur.Offset = 0
		} else if cur.History != "" && cur.History != controlDigest(messages) {
			return nil, fmt.Errorf("history changed; restart searching")
		}
		cur.History = controlDigest(messages)
		cur.ChatID = id
		for cur.Offset < len(messages) && len(hits) < limit {
			m := messages[cur.Offset]
			cur.Offset++
			lower := strings.ToLower(m.Content)
			at := strings.Index(lower, strings.ToLower(query))
			if at >= 0 {
				chars := []rune(lower)
				position := len([]rune(lower[:at]))
				start := max(0, position-150)
				end := min(len(chars), start+500)
				original := []rune(m.Content)
				end = min(end, len(original))
				start = min(start, end)
				hits = append(hits, map[string]any{"chatId": id, "runId": m.RunID, "role": m.Role, "snippet": string(original[start:end])})
			}
		}
		if cur.Offset < len(messages) {
			more = true
			break
		}
		cur.After = id
		cur.ChatID = ""
		cur.History = ""
		cur.Offset = 0
		if len(hits) >= limit {
			more = index < len(ids)-1 || target == "" && len(ids) == 100
			break
		}
	}
	if target == "" && len(ids) == 100 {
		more = true
	}
	next := ""
	if more {
		next, err = controlNext(cur)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"hits": hits, "nextCursor": next, "incomplete": more || len(skipped) > 0, "skippedChatIds": skipped, "scanLimit": 50, "maxChatBytes": 8 << 20}, nil
}
func (s *Service) ControlDeleteRevision(c ControlCaller, id string, archived bool) (string, error) {
	sum, e := s.ResolveControlChat(c, id, archived)
	if e != nil {
		return "", e
	}
	if e = s.EnsureControlIdle(sum); e != nil {
		return "", e
	}
	if sum.ChatID == c.ChatID {
		return "", fmt.Errorf("current Chat has an active Run")
	}
	var detail chat.Detail
	if archived {
		a, err := s.Archives.LoadArchived(sum.ChatID)
		if err != nil {
			return "", err
		}
		detail = a.Detail
	} else {
		var err error
		detail, err = s.Chats.LoadChat(sum.ChatID)
		if err != nil {
			return "", err
		}
	}
	return controlDigest([]any{sum, detail}), nil
}
func (s *Service) EnsureControlIdle(sum *chat.Summary) error {
	if sum.PendingAwaiting != nil {
		return chat.ErrChatPendingAwaiting
	}
	return s.EnsureNoActiveRun(sum.ChatID)
}
func (s *Service) ControlManage(c ControlCaller, action string, args map[string]any, revision string) (out map[string]any, err error) {
	state := "not_started"
	defer func() {
		if err != nil {
			err = &contracts.MutationError{State: state, Err: err}
		}
	}()
	if action == "archive" || action == "restore" {
		ids, batch, e := ControlArchiveIDs(args)
		if e != nil {
			return nil, e
		}
		if batch {
			return s.controlArchiveBatch(c, action, ids), nil
		}
	}
	// Serializes deterministic fork/export receipts and tool mutations. Store locks
	// remain responsible for persistence; Run admission has its existing boundary.
	release := s.LockMutation()
	defer release()
	id := controlString(args, "chatId")
	if action == "fork" {
		id = controlString(args, "sourceChatId")
	}
	archived := controlBool(args, "archived") || action == "restore"
	sum, e := s.ResolveControlChat(c, id, archived)
	if e != nil {
		return nil, e
	}
	id = sum.ChatID
	if action == "archive" || action == "delete" || action == "fork" {
		if id == c.ChatID {
			return nil, fmt.Errorf("current Chat has an active Run")
		}
		if e = s.EnsureControlIdle(sum); e != nil {
			return nil, e
		}
	}
	result := map[string]any{"chatId": id}
	switch action {
	case "rename":
		state = "unknown"
		v, e := s.Chats.RenameChat(id, controlString(args, "chatName"))
		if e != nil {
			return nil, e
		}
		result["chat"] = v
	case "setPinned":
		state = "unknown"
		v, e := s.setChatPinned(id, controlBool(args, "pinned"))
		if e != nil {
			return nil, e
		}
		result["pinned"] = v.Pinned
		result["changed"] = v.Changed
	case "archive":
		state = "unknown"
		items, e := s.archiveChats([]string{id})
		if e != nil {
			return nil, e
		}
		if len(items) != 1 || !items[0].Success {
			return nil, fmt.Errorf("archive failed: %v", items)
		}
	case "restore":
		state = "unknown"
		items, e := s.restoreArchives([]string{id})
		if e != nil {
			return nil, e
		}
		if len(items) != 1 || !items[0].Success {
			return nil, fmt.Errorf("restore failed: %v", items)
		}
	case "delete":
		current, e := s.ControlDeleteRevision(c, id, archived)
		if e != nil {
			return nil, e
		}
		if revision == "" || current != revision {
			return nil, fmt.Errorf("approval_stale")
		}
		state = "unknown"
		if archived {
			e = s.Archives.DeleteArchived(id)
		} else {
			e = s.Chats.DeleteChat(id)
		}
		if e != nil {
			return nil, e
		}
	case "fork", "export":
		if c.RunID == "" || c.ToolID == "" {
			return nil, fmt.Errorf("missing invocation identity")
		}
		digest := controlDigest(map[string]any{"action": action, "args": args, "subject": c.Subject})
		stem := "control-" + controlDigest([]string{c.RunID, c.ToolID})[:32]
		if s.ControlStateDir == "" {
			return nil, fmt.Errorf("private receipt storage unavailable")
		}
		receiptDir := s.ControlStateDir
		for _, path := range []string{s.Chats.ChatDir(c.ChatID), receiptDir} {
			if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
				return nil, fmt.Errorf("invalid Chat storage directory")
			} else if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
		if e = os.MkdirAll(s.Chats.ChatDir(c.ChatID), 0700); e != nil {
			return nil, e
		}
		if e = os.MkdirAll(receiptDir, 0700); e != nil {
			return nil, e
		}
		receiptPath := filepath.Join(receiptDir, stem+".json")
		var receipt struct {
			Digest  string         `json:"digest"`
			Result  map[string]any `json:"result"`
			Started bool           `json:"started"`
		}
		if b, e := os.ReadFile(receiptPath); e == nil {
			if json.Unmarshal(b, &receipt) != nil || receipt.Digest != digest {
				return nil, fmt.Errorf("idempotency_conflict")
			}
			if receipt.Result != nil {
				return receipt.Result, nil
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		resuming := receipt.Started
		if !receipt.Started {
			receipt.Digest = digest
			receipt.Started = true
			b, _ := json.Marshal(receipt)
			if e = controlAtomicFile(receiptPath, b); e != nil {
				return nil, e
			}
		}
		if action == "fork" {
			if existing, e := s.Chats.Summary(stem); e != nil {
				return nil, e
			} else if existing != nil {
				// An unfinished receipt alone cannot prove who created this target.
				return nil, fmt.Errorf("idempotency_conflict: existing fork has no completed provenance receipt")
			}
			state = "unknown"
			v, e := s.Chats.DeriveChat(chat.DeriveChatRequest{SourceChatID: id, SourceRunID: controlString(args, "sourceRunId"), ChatID: stem, ChatName: controlString(args, "chatName")})
			if e != nil {
				return nil, e
			}
			result = map[string]any{"chatId": v.Summary.ChatID, "sourceChatId": id, "sourceRunId": v.SourceRunID, "copiedRuns": v.CopiedRuns}
		} else {
			if resuming {
				format := controlString(args, "format")
				ext := ".snapshot.json"
				if format == "markdown" {
					ext = ".md"
				}
				filename := stem + ext
				if info, err := os.Lstat(filepath.Join(s.Chats.ChatDir(c.ChatID), filename)); err == nil {
					if !info.Mode().IsRegular() {
						return nil, fmt.Errorf("export output is not a regular file")
					}
					result = map[string]any{"chatId": id, "url": filename, "format": format, "sizeBytes": info.Size()}
					receipt.Result = result
					b, _ := json.Marshal(receipt)
					if e = controlAtomicFile(receiptPath, b); e != nil {
						return nil, e
					}
					return result, nil
				} else if !os.IsNotExist(err) {
					return nil, err
				}
			}
			_, messages, e := s.ControlMessages(c, id, archived)
			if e != nil {
				return nil, e
			}
			format := controlString(args, "format")
			var body []byte
			ext := ".snapshot.json"
			if format == "snapshot" {
				var detail chat.Detail
				if archived {
					var a *chat.ArchivedChat
					a, e = s.Archives.LoadArchived(id)
					if e == nil && a != nil {
						detail = a.Detail
					}
				} else {
					detail, e = s.Chats.LoadChat(id)
				}
				if e != nil {
					return nil, e
				}
				var attachments []conversationexport.AttachmentV1
				if reader, ok := s.Chats.(interface {
					PublishedArtifacts(string) ([]chat.ArtifactManifestItem, error)
				}); ok && !archived {
					items, err := reader.PublishedArtifacts(id)
					if err != nil {
						return nil, err
					}
					attachments, _ = conversationexport.BuildSnapshotAttachments(id, items)
				}
				var doc conversationexport.SnapshotDocument
				doc, e = conversationexport.BuildSnapshotDocument(sum, detail.Events, attachments, time.Now().UnixMilli(), "zh-CN", nil)
				body = doc.JSON
			} else if format == "markdown" {
				ext = ".md"
				var b strings.Builder
				fmt.Fprintf(&b, "# %s\n\n", sum.ChatName)
				for _, m := range messages {
					fmt.Fprintf(&b, "## %s\n\n%s\n\n", m.Role, m.Content)
				}
				body = []byte(b.String())
			} else {
				return nil, fmt.Errorf("format must be markdown or snapshot")
			}
			if e != nil {
				return nil, e
			}
			filename := stem + ext
			path := filepath.Join(s.Chats.ChatDir(c.ChatID), filename)
			state = "unknown"
			if e = controlAtomicFile(path, body); e != nil {
				return nil, e
			}
			result = map[string]any{"chatId": id, "url": filename, "format": format, "sizeBytes": len(body)}
		}
		state = "committed"
		receipt.Digest = digest
		receipt.Result = result
		b, _ := json.Marshal(receipt)
		if e = controlAtomicFile(receiptPath, b); e != nil {
			return nil, e
		}
	default:
		return nil, fmt.Errorf("unsupported action")
	}
	if s.Notifications != nil {
		payload := map[string]any{"chatId": result["chatId"], "agentKey": sum.AgentKey}
		switch action {
		case "archive":
			s.Notifications.Broadcast("chat.archived", payload)
		case "delete":
			event := "chat.deleted"
			if archived {
				event = "archive.deleted"
			}
			s.Notifications.Broadcast(event, payload)
		case "restore":
			if restored, err := s.Chats.Summary(id); err == nil && restored != nil {
				payload["summary"] = restored
			}
			s.Notifications.Broadcast("archive.restored", payload)
		case "rename", "fork":
			actual, _ := s.Chats.Summary(controlString(result, "chatId"))
			if actual != nil {
				payload["chatName"] = actual.ChatName
				payload["agentKey"] = actual.AgentKey
				payload["source"] = actual.Source
				payload["createdAt"] = actual.CreatedAt
				if action == "fork" {
					s.Notifications.Broadcast("chat.created", payload)
				}
				update := map[string]any{"chatId": actual.ChatID, "chatName": actual.ChatName, "lastRunId": actual.LastRunID, "lastRunContent": actual.LastRunContent, "updatedAt": actual.UpdatedAt}
				s.Notifications.Broadcast("chat.updated", update)
			}
		}
	}

	return result, nil
}
func controlAtomicFile(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".control-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
