package ws

import "strings"

const streamKindTerminalStatus = "terminal-status"

func (c *Conn) ReserveTerminalStatusStream(requestID string) error {
	return c.reserveNamedStream(requestID, "terminal-status", streamKindTerminalStatus, "")
}

func (c *Conn) ReleaseTerminalStatusStream(requestID string) (DetachedStream, bool) {
	if c == nil {
		return DetachedStream{}, false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return DetachedStream{}, false
	}
	c.mu.Lock()
	entry := c.activeStreams[requestID]
	matches := entry != nil && entry.kind == streamKindTerminalStatus
	c.mu.Unlock()
	if !matches {
		return DetachedStream{}, false
	}
	return c.releaseStream(requestID, false, "", 0)
}
