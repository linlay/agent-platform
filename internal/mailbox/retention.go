package mailbox

import (
	"context"
	"errors"
	"log"
	"time"
)

const DefaultRetentionDays = 30

// StartRetention runs once at startup and daily afterwards. Existence includes
// archived Chats: archiving is not deletion. Only App supplies this lookup.
func (s *Service) StartRetention(days int, exists func(string) (bool, error)) {
	if days <= 0 {
		days = DefaultRetentionDays
	}
	s.mu.Lock()
	if s.closed || s.retentionStarted {
		s.mu.Unlock()
		return
	}
	s.retentionStarted = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			if err := s.Maintain(time.Now(), days, exists); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[mailbox] retention: %v", err)
			}
			select {
			case <-s.stop:
				return
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Maintain repairs missed deletion hooks before clearing expired terminal
// bodies. It never redelivers suspended mail or deletes Chat-owned resources.
func (s *Service) Maintain(at time.Time, days int, exists func(string) (bool, error)) error {
	if days <= 0 || days > 365000 {
		return failure("invalid_request", "mail retention days must be between 1 and 365000")
	}
	if exists != nil {
		// Read only distinct addresses, not every retained message body.
		ids, err := s.Store.retentionIDs(`SELECT FROM_CHAT_ID_ FROM MAILS WHERE FROM_CHAT_ID_<>'' AND FROM_CHAT_DELETED_AT_=0
            UNION SELECT TO_CHAT_ID_ FROM MAILS WHERE TO_CHAT_DELETED_AT_=0 AND NOT
            (OPENED_CHAT_=1 AND (STATUS_='accepting' OR STATUS_='suspended' AND RESUME_PHASE_='accepting'))`)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.maintenanceActive(); err != nil {
				return err
			}
			present, err := exists(id)
			if err != nil {
				return err
			}
			if !present {
				if err := s.PurgeChat(id); err != nil {
					return err
				}
			}
		}
	}
	cutoff := at.AddDate(0, 0, -days).UnixMilli()
	// Bounded batches avoid loading all expired bodies into memory on startup.
	for {
		if err := s.maintenanceActive(); err != nil {
			return err
		}
		ids, err := s.Store.retentionIDs("SELECT MAIL_ID_ FROM MAILS WHERE TERMINAL_AT_>0 AND TERMINAL_AT_<=? AND BODY_PURGED_AT_=0 ORDER BY TERMINAL_AT_,MAIL_ID_ LIMIT 100", cutoff)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			if err := s.maintenanceActive(); err != nil {
				return err
			}
			_, err := s.change(id, func(row *Mail) error {
				if !row.Terminal() || row.TerminalAt == 0 || row.TerminalAt > cutoff || row.PurgedAt != 0 {
					return errStale
				}
				clearBody(row, at.UnixMilli())
				return nil
			})
			if err != nil && !errors.Is(err, errStale) && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
	}
	return s.Store.deleteOrphans()
}

func clearBody(m *Mail, at int64) {
	m.Input, m.Message, m.References, m.Files, m.Error = nil, "", nil, nil, nil
	if m.PurgedAt == 0 {
		m.PurgedAt = at
	}
}

func (s *Store) deleteOrphans() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM MAILS WHERE FROM_CHAT_DELETED_AT_>0 AND TO_CHAT_DELETED_AT_>0 AND TERMINAL_AT_>0")
	return err
}

func (s *Service) maintenanceActive() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	select {
	case <-s.stop:
		return context.Canceled
	default:
		return nil
	}
}

func (s *Store) retentionIDs(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
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
