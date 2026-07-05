package llm

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"

	. "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

const planningDeltaChunkRunes = 96

type planningWriteStreamState struct {
	toolID               string
	argsBuffer           string
	planningID           string
	planningFile         string
	sentMarkdown         string
	started              bool
	ended                bool
	draftFileInitialized bool
}

type planningDraftString struct {
	Value   string
	Present bool
	Closed  bool
}

type planningDraftArgs struct {
	Markdown planningDraftString
}

func (s *llmRunStream) appendToolCallDeltas(deltas []AgentDelta) {
	for _, delta := range deltas {
		toolCall, ok := delta.(DeltaToolCall)
		if ok && strings.TrimSpace(toolCall.ArgsDelta) != "" {
			s.markFirstVisibleDelta()
		}
		s.pending = append(s.pending, delta)
		if !ok {
			continue
		}
		s.pending = append(s.pending, s.planningDeltasFromToolCall(toolCall)...)
	}
}

func (s *llmRunStream) planningDeltasFromToolCall(delta DeltaToolCall) []AgentDelta {
	toolID := strings.TrimSpace(delta.ID)
	if toolID == "" {
		return nil
	}
	if !isPlanningWriteTool(delta.Name) {
		if s == nil || s.planningWrites == nil || s.planningWrites[toolID] == nil {
			return nil
		}
	}
	state := s.ensurePlanningWriteState(toolID)
	if state == nil {
		return nil
	}
	state.argsBuffer += delta.ArgsDelta
	draftArgs := parsePlanningDraftArgs(state.argsBuffer)
	if !draftArgs.Markdown.Present {
		return nil
	}
	if state.planningID == "" {
		state.planningID = planutil.PlanningIDForRevision(s.planningRunID(), s.planningRevision())
	}
	if state.planningFile == "" {
		if chatsDir := s.planningChatsDir(); chatsDir != "" {
			state.planningFile = planutil.PlanningFileForChat(chatsDir, s.session.ChatID, state.planningID)
		}
	}
	draft := renderPlanningDraftMarkdown(draftArgs)
	return s.planningMarkdownDeltas(state, draft, true)
}

func (s *llmRunStream) appendFinalPlanningDeltas(toolID string, result ToolExecutionResult) {
	planningID := strings.TrimSpace(AnyStringNode(result.Structured["planningId"]))
	planningFile := strings.TrimSpace(AnyStringNode(result.Structured["planningFile"]))
	markdown := AnyStringNode(result.Structured["markdown"])
	if planningID == "" || strings.TrimSpace(markdown) == "" {
		return
	}
	state := s.ensurePlanningWriteState(toolID)
	if state == nil {
		state = &planningWriteStreamState{toolID: strings.TrimSpace(toolID)}
	}
	if state.planningID == "" {
		state.planningID = planningID
	}
	if state.planningFile == "" {
		state.planningFile = planningFile
	}
	s.pending = append(s.pending, s.planningMarkdownDeltas(state, markdown, false)...)
	if end := state.planningEndDelta(); end != nil {
		s.pending = append(s.pending, end)
	}
	if s.planningWrites != nil {
		delete(s.planningWrites, strings.TrimSpace(toolID))
	}
}

func (s *llmRunStream) planningMarkdownDeltas(state *planningWriteStreamState, markdown string, writeDraft bool) []AgentDelta {
	if state == nil || markdown == "" || markdown == state.sentMarkdown {
		return nil
	}
	if !strings.HasPrefix(markdown, state.sentMarkdown) {
		if state.sentMarkdown != "" {
			return nil
		}
	}
	suffix := markdown[len(state.sentMarkdown):]
	if suffix == "" {
		return nil
	}
	state.sentMarkdown += suffix
	chunks := splitPlanningDeltaChunks(suffix)
	events := make([]AgentDelta, 0, len(chunks)+1)
	if !state.started {
		state.started = true
		events = append(events, DeltaPlanningStart{
			PlanningID: state.planningID,
		})
	}
	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		if writeDraft {
			state.appendPlanningDraftFile(chunk)
		}
		events = append(events, DeltaPlanningDelta{
			PlanningID: state.planningID,
			Delta:      chunk,
		})
	}
	return events
}

func (state *planningWriteStreamState) planningEndDelta() AgentDelta {
	if state == nil || !state.started || state.ended || strings.TrimSpace(state.planningID) == "" {
		return nil
	}
	state.ended = true
	return DeltaPlanningEnd{PlanningID: state.planningID}
}

func splitPlanningDeltaChunks(text string) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if len(runes) <= planningDeltaChunkRunes {
		return []string{text}
	}
	chunks := make([]string, 0, (len(runes)/planningDeltaChunkRunes)+1)
	for start := 0; start < len(runes); start += planningDeltaChunkRunes {
		end := start + planningDeltaChunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func (state *planningWriteStreamState) appendPlanningDraftFile(chunk string) {
	if state == nil || strings.TrimSpace(state.planningFile) == "" || chunk == "" {
		return
	}
	if !state.draftFileInitialized {
		if err := os.MkdirAll(filepath.Dir(state.planningFile), 0o755); err != nil {
			return
		}
		if err := os.WriteFile(state.planningFile, nil, 0o644); err != nil {
			return
		}
		state.draftFileInitialized = true
	}
	file, err := os.OpenFile(state.planningFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(chunk)
}

func (s *llmRunStream) ensurePlanningWriteState(toolID string) *planningWriteStreamState {
	if s == nil {
		return nil
	}
	toolID = strings.TrimSpace(toolID)
	if toolID == "" {
		return nil
	}
	if s.planningWrites == nil {
		s.planningWrites = map[string]*planningWriteStreamState{}
	}
	state := s.planningWrites[toolID]
	if state == nil {
		state = &planningWriteStreamState{toolID: toolID}
		s.planningWrites[toolID] = state
	}
	return state
}

func (s *llmRunStream) planningRunID() string {
	if s == nil {
		return ""
	}
	runID := strings.TrimSpace(s.session.RunID)
	if runID == "" {
		runID = strings.TrimSpace(s.req.RunID)
	}
	if runID == "" {
		runID = strings.TrimSpace(s.session.RequestID)
	}
	return runID
}

func (s *llmRunStream) planningRevision() int {
	if s == nil || s.execCtx == nil || s.execCtx.PlanningRevision <= 0 {
		return 1
	}
	return s.execCtx.PlanningRevision
}

func (s *llmRunStream) planningChatsDir() string {
	if s == nil {
		return ""
	}
	chatsDir := ""
	if s.engine != nil {
		chatsDir = strings.TrimSpace(s.engine.cfg.Paths.ChatsDir)
	}
	if chatsDir == "" {
		chatsDir = strings.TrimSpace(s.session.RuntimeContext.LocalPaths.ChatsDir)
	}
	return chatsDir
}

func parsePlanningDraftArgs(buffer string) planningDraftArgs {
	return planningDraftArgs{
		Markdown: parsePlanningStringField(buffer, "markdown"),
	}
}

func parsePlanningStringField(buffer string, key string) planningDraftString {
	valueOffset := findJSONObjectValueOffset(buffer, key)
	if valueOffset < 0 || valueOffset >= len(buffer) || buffer[valueOffset] != '"' {
		return planningDraftString{}
	}
	value, _, closed, ok := parseJSONStringFragmentAt(buffer, valueOffset)
	if !ok {
		return planningDraftString{}
	}
	return planningDraftString{Value: value, Present: true, Closed: closed}
}

func renderPlanningDraftMarkdown(args planningDraftArgs) string {
	if !args.Markdown.Present {
		return ""
	}
	return args.Markdown.Value
}

func findJSONObjectValueOffset(text string, key string) int {
	for i := 0; i < len(text); i++ {
		if text[i] != '"' {
			continue
		}
		value, end, ok := parseJSONStringAt(text, i)
		if !ok {
			return -1
		}
		i = end
		if value != key {
			continue
		}
		j := skipJSONSpaces(text, end+1)
		if j >= len(text) || text[j] != ':' {
			continue
		}
		return skipJSONSpaces(text, j+1)
	}
	return -1
}

func parseJSONStringAt(text string, start int) (string, int, bool) {
	value, end, closed, ok := parseJSONStringFragmentAt(text, start)
	return value, end, ok && closed
}

func parseJSONStringFragmentAt(text string, start int) (string, int, bool, bool) {
	if start < 0 || start >= len(text) || text[start] != '"' {
		return "", start, false, false
	}
	var b strings.Builder
	for i := start + 1; i < len(text); {
		ch := text[i]
		if ch == '"' {
			return b.String(), i, true, true
		}
		if ch != '\\' {
			b.WriteByte(ch)
			i++
			continue
		}
		if i+1 >= len(text) {
			return b.String(), len(text), false, true
		}
		escaped := text[i+1]
		switch escaped {
		case '"', '\\', '/':
			b.WriteByte(escaped)
			i += 2
		case 'b':
			b.WriteByte('\b')
			i += 2
		case 'f':
			b.WriteByte('\f')
			i += 2
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case 'u':
			r, next, ok := parseJSONUnicodeEscape(text, i)
			if !ok {
				return b.String(), len(text), false, true
			}
			b.WriteRune(r)
			i = next
		default:
			return b.String(), i, false, true
		}
	}
	return b.String(), len(text), false, true
}

func parseJSONUnicodeEscape(text string, slash int) (rune, int, bool) {
	if slash+6 > len(text) || text[slash] != '\\' || text[slash+1] != 'u' {
		return 0, slash, false
	}
	value, err := strconv.ParseInt(text[slash+2:slash+6], 16, 32)
	if err != nil {
		return 0, slash, false
	}
	r := rune(value)
	next := slash + 6
	if 0xD800 <= r && r <= 0xDBFF && next+6 <= len(text) && text[next] == '\\' && text[next+1] == 'u' {
		lowValue, lowErr := strconv.ParseInt(text[next+2:next+6], 16, 32)
		low := rune(lowValue)
		if lowErr == nil && 0xDC00 <= low && low <= 0xDFFF {
			return utf16.DecodeRune(r, low), next + 6, true
		}
	}
	return r, next, true
}

func skipJSONSpaces(text string, start int) int {
	for start < len(text) {
		switch text[start] {
		case ' ', '\n', '\r', '\t':
			start++
		default:
			return start
		}
	}
	return start
}
