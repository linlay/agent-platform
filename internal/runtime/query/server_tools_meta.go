package query

import (
	"crypto/rand"
	"fmt"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

func normalizeQueryRole(role string) (string, bool) {
	return queryinput.NormalizeQueryRole(role)
}

func defaultRole(role string) string {
	normalized, ok := queryinput.NormalizeQueryRole(role)
	if !ok {
		return queryinput.QueryRoleUser
	}
	return normalized
}

func newRunID() string {
	return chat.NewRunID()
}

func (s *Service) toolLookup() contracts.ToolDefinitionLookup {
	if tl, ok := s.deps.Tools.(contracts.ToolDefinitionLookup); ok {
		return contracts.NewCompositeToolLookup(tl, s.deps.Registry)
	}
	return s.deps.Registry
}

func newChatID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		data[0:4],
		data[4:6],
		data[6:8],
		data[8:10],
		data[10:16],
	)
}
