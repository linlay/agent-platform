package stream

import "strings"

const rejectedMarkdownResourceText = "资源地址不合规"

// markdownDestinationGuard buffers only Markdown link/image destinations so a
// forbidden transport URL split across provider deltas is never emitted.
// Ordinary text and code spans/fences continue streaming immediately.
type MarkdownDestinationGuard struct {
	chatID             string
	codeDelimiter      byte
	codeDelimiterWidth int
	pendingBang        bool
	inLabel            bool
	labelDepth         int
	labelEscaped       bool
	awaitDestination   bool
	label              strings.Builder
	inDestination      bool
	destinationDepth   int
	destinationEscaped bool
	destination        strings.Builder
}

func NewMarkdownDestinationGuard(chatID string) *MarkdownDestinationGuard {
	return &MarkdownDestinationGuard{chatID: strings.TrimSpace(chatID)}
}

func newMarkdownDestinationGuard(chatID string) *MarkdownDestinationGuard {
	return NewMarkdownDestinationGuard(chatID)
}

func (g *MarkdownDestinationGuard) Write(chunk string) string {
	if g == nil || chunk == "" {
		return chunk
	}
	var out strings.Builder
	for index := 0; index < len(chunk); {
		char := chunk[index]
		if g.inDestination {
			index++
			if g.destinationEscaped {
				g.destination.WriteByte(char)
				g.destinationEscaped = false
				continue
			}
			switch char {
			case '\\':
				g.destination.WriteByte(char)
				g.destinationEscaped = true
			case '(':
				g.destinationDepth++
				g.destination.WriteByte(char)
			case ')':
				g.destinationDepth--
				if g.destinationDepth == 0 {
					g.finishDestination(&out, true)
					continue
				}
				g.destination.WriteByte(char)
			default:
				g.destination.WriteByte(char)
			}
			continue
		}
		if g.inLabel {
			index++
			g.label.WriteByte(char)
			if g.labelEscaped {
				g.labelEscaped = false
				continue
			}
			switch char {
			case '\\':
				g.labelEscaped = true
			case '[':
				g.labelDepth++
			case ']':
				g.labelDepth--
				if g.labelDepth == 0 {
					g.inLabel = false
					g.awaitDestination = true
				}
			}
			continue
		}
		if g.awaitDestination {
			g.awaitDestination = false
			if char == '(' {
				g.label.WriteByte('(')
				g.inDestination = true
				g.destinationDepth = 1
				g.destinationEscaped = false
				g.destination.Reset()
				index++
				continue
			}
			out.WriteString(g.label.String())
			g.label.Reset()
			continue
		}

		if char == '`' || char == '~' {
			runEnd := index + 1
			for runEnd < len(chunk) && chunk[runEnd] == char {
				runEnd++
			}
			runWidth := runEnd - index
			isDelimiter := char == '`' || runWidth >= 3
			if isDelimiter {
				if g.pendingBang {
					out.WriteByte('!')
					g.pendingBang = false
				}
				out.WriteString(chunk[index:runEnd])
				if g.codeDelimiter == 0 {
					g.codeDelimiter = char
					g.codeDelimiterWidth = runWidth
				} else if g.codeDelimiter == char && g.codeDelimiterWidth == runWidth {
					g.codeDelimiter = 0
					g.codeDelimiterWidth = 0
				}
				index = runEnd
				continue
			}
		}

		if g.codeDelimiter != 0 {
			out.WriteByte(char)
			index++
			continue
		}
		if g.pendingBang {
			g.pendingBang = false
			if char == '[' {
				g.label.WriteString("![")
				g.inLabel = true
				g.labelDepth = 1
				index++
				continue
			}
			out.WriteByte('!')
		}
		if char == '!' {
			g.pendingBang = true
			index++
			continue
		}
		if char == '[' {
			g.label.WriteByte('[')
			g.inLabel = true
			g.labelDepth = 1
			g.labelEscaped = false
			index++
			continue
		}
		out.WriteByte(char)
		index++
	}
	return out.String()
}

func (g *MarkdownDestinationGuard) Flush() string {
	if g == nil {
		return ""
	}
	var out strings.Builder
	if g.pendingBang {
		out.WriteByte('!')
		g.pendingBang = false
	}
	if g.inDestination {
		g.finishDestination(&out, false)
	} else if g.inLabel || g.awaitDestination {
		out.WriteString(g.label.String())
		g.resetLabel()
	}
	return out.String()
}

func (g *MarkdownDestinationGuard) finishDestination(out *strings.Builder, closed bool) {
	destination := g.destination.String()
	if g.destinationForbidden(destination) {
		out.WriteString(rejectedMarkdownResourceText)
	} else {
		out.WriteString(g.label.String())
		out.WriteString(destination)
		if closed {
			out.WriteByte(')')
		}
	}
	g.destination.Reset()
	g.inDestination = false
	g.destinationDepth = 0
	g.destinationEscaped = false
	g.resetLabel()
}

func (g *MarkdownDestinationGuard) resetLabel() {
	g.label.Reset()
	g.inLabel = false
	g.labelDepth = 0
	g.labelEscaped = false
	g.awaitDestination = false
}

func (g *MarkdownDestinationGuard) destinationForbidden(destination string) bool {
	target := strings.TrimSpace(destination)
	if strings.HasPrefix(target, "<") {
		if end := strings.Index(target, ">"); end > 1 {
			target = strings.TrimSpace(target[1:end])
		}
	} else if index := strings.IndexAny(target, " \t\r\n"); index >= 0 {
		target = target[:index]
	}
	if target == "/api/resource" || strings.HasPrefix(target, "/api/resource?") {
		return true
	}
	return g.chatID != "" && (target == g.chatID || strings.HasPrefix(target, g.chatID+"/"))
}
