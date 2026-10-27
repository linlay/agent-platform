package contracts

import "agent-platform/internal/contracts/queryinput"

func (c *RunControl) MailHop() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mailHop
}
func (c *RunControl) RaiseMailHop(hop int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.mailHop = max(c.mailHop, hop)
	c.mu.Unlock()
}
func (c *RunControl) ConsumeMailInput(m *queryinput.MailInput, origin *queryinput.Origin, refs ...queryinput.Reference) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if m != nil {
		c.mailHop = max(c.mailHop, m.Hop)
	} else if origin == nil || origin.Type == queryinput.OriginUser {
		c.mailHop = 0
		c.userChatReferences = queryinput.ResourceIDs(refs, "chat")
	}
}

func (c *RunControl) UserChatReferences() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.userChatReferences...)
}
func (c *RunControl) QueuedMail() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.steerQueue) > 0 && c.steerQueue[0].Mail != nil
}
