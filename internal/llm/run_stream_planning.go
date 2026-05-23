package llm

import (
	"encoding/json"
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
	started              bool
	planningID           string
	planningFile         string
	title                string
	sentMarkdown         string
	draftFileInitialized bool
}

type planningDraftString struct {
	Value   string
	Present bool
	Closed  bool
}

type planningDraftArray struct {
	Items   []planningDraftString
	Present bool
	Closed  bool
}

type planningDraftArgs struct {
	Title                  planningDraftString
	Summary                planningDraftString
	PublicEventsAndStorage planningDraftArray
	ImplementationChanges  planningDraftArray
	Interfaces             planningDraftArray
	TestPlan               planningDraftArray
	Assumptions            planningDraftArray
}

func (s *llmRunStream) appendToolCallDeltas(deltas []AgentDelta) {
	for _, delta := range deltas {
		s.pending = append(s.pending, delta)
		toolCall, ok := delta.(DeltaToolCall)
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
	if !draftArgs.Title.Closed {
		return nil
	}
	title := strings.TrimSpace(draftArgs.Title.Value)
	if title == "" {
		return nil
	}
	state.title = title
	if state.planningID == "" {
		state.planningID = planutil.PlanningID(title, s.planningRunID())
	}
	if state.planningFile == "" {
		if chatsDir := s.planningChatsDir(); chatsDir != "" {
			state.planningFile = planutil.PlanningFile(chatsDir, state.planningID)
		}
	}
	events := make([]AgentDelta, 0)
	if !state.started {
		state.started = true
		events = append(events, s.planningStartDelta(state, "started"))
	}
	draft := renderPlanningDraftMarkdown(draftArgs)
	events = append(events, s.planningMarkdownDeltas(state, draft, true)...)
	return events
}

func (s *llmRunStream) appendFinalPlanningDeltas(toolID string, result ToolExecutionResult) {
	planningID := strings.TrimSpace(AnyStringNode(result.Structured["planningId"]))
	planningFile := strings.TrimSpace(AnyStringNode(result.Structured["planningFile"]))
	title := strings.TrimSpace(AnyStringNode(result.Structured["title"]))
	status := strings.TrimSpace(AnyStringNode(result.Structured["status"]))
	markdown := AnyStringNode(result.Structured["markdown"])
	if status == "" {
		status = "ready"
	}
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
	if state.title == "" {
		state.title = title
	}
	if !state.started {
		state.started = true
		s.pending = append(s.pending, s.planningStartDelta(state, "started"))
	}
	s.pending = append(s.pending, s.planningMarkdownDeltas(state, markdown, false)...)
	s.pending = append(s.pending, DeltaPlanningEnd{
		PlanningID:   planningID,
		PlanningFile: planningFile,
		ChatID:       s.session.ChatID,
		RunID:        s.session.RunID,
		RequestID:    s.session.RequestID,
		AgentKey:     s.session.AgentKey,
		Title:        title,
		Status:       status,
		Markdown:     markdown,
	})
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
	events := make([]AgentDelta, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		if writeDraft {
			state.appendPlanningDraftFile(chunk)
		}
		events = append(events, DeltaPlanningDelta{
			PlanningID:   state.planningID,
			PlanningFile: state.planningFile,
			ChatID:       s.session.ChatID,
			RunID:        s.session.RunID,
			RequestID:    s.session.RequestID,
			AgentKey:     s.session.AgentKey,
			Title:        state.title,
			Status:       "writing",
			Delta:        chunk,
		})
	}
	return events
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

func (s *llmRunStream) planningStartDelta(state *planningWriteStreamState, status string) DeltaPlanningStart {
	return DeltaPlanningStart{
		PlanningID:   state.planningID,
		PlanningFile: state.planningFile,
		ChatID:       s.session.ChatID,
		RunID:        s.session.RunID,
		RequestID:    s.session.RequestID,
		AgentKey:     s.session.AgentKey,
		Title:        state.title,
		Status:       status,
	}
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
		Title:                  parsePlanningStringField(buffer, "title"),
		Summary:                parsePlanningStringField(buffer, "summary"),
		PublicEventsAndStorage: parsePlanningArrayField(buffer, "publicEventsAndStorage"),
		ImplementationChanges:  parsePlanningArrayField(buffer, "implementationChanges"),
		Interfaces:             parsePlanningArrayField(buffer, "interfaces"),
		TestPlan:               parsePlanningArrayField(buffer, "testPlan"),
		Assumptions:            parsePlanningArrayField(buffer, "assumptions"),
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

func parsePlanningArrayField(buffer string, key string) planningDraftArray {
	valueOffset := findJSONObjectValueOffset(buffer, key)
	if valueOffset < 0 || valueOffset >= len(buffer) || buffer[valueOffset] != '[' {
		return planningDraftArray{}
	}
	return parsePlanningStringArrayAt(buffer, valueOffset)
}

func renderPlanningDraftMarkdown(args planningDraftArgs) string {
	if !args.Title.Closed {
		return ""
	}
	title := strings.TrimSpace(args.Title.Value)
	if title == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")
	if !args.Summary.Present {
		return b.String()
	}
	b.WriteString("## Summary\n")
	summary := strings.TrimSpace(args.Summary.Value)
	if summary == "" {
		return b.String()
	}
	b.WriteString(summary)
	if !args.Summary.Closed {
		return b.String()
	}
	b.WriteString("\n\n")
	if !appendPlanningDraftSection(&b, "Public Events And Storage", args.PublicEventsAndStorage, false) {
		return b.String()
	}
	if !appendPlanningDraftSection(&b, "Implementation Changes", args.ImplementationChanges, false) {
		return b.String()
	}
	if !appendPlanningDraftSection(&b, "Interfaces", args.Interfaces, false) {
		return b.String()
	}
	if !appendPlanningDraftSection(&b, "Test Plan", args.TestPlan, false) {
		return b.String()
	}
	_ = appendPlanningDraftSection(&b, "Assumptions", args.Assumptions, true)
	return b.String()
}

func appendPlanningDraftSection(b *strings.Builder, title string, section planningDraftArray, last bool) bool {
	if !section.Present {
		return false
	}
	b.WriteString("## ")
	b.WriteString(title)
	b.WriteByte('\n')
	wroteItem := false
	for _, item := range section.Items {
		line := cleanPlanningDraftLine(item.Value)
		if line == "" {
			if !item.Closed {
				return false
			}
			continue
		}
		wroteItem = true
		b.WriteString("- ")
		b.WriteString(line)
		if !item.Closed {
			return false
		}
		b.WriteByte('\n')
	}
	if !section.Closed {
		return false
	}
	if !wroteItem {
		b.WriteString("- None specified.\n")
	}
	if !last {
		b.WriteByte('\n')
	}
	return true
}

func cleanPlanningDraftLine(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
}

func partialPlanningWriteArgs(buffer string) map[string]any {
	var full map[string]any
	if err := json.Unmarshal([]byte(buffer), &full); err == nil && len(full) > 0 {
		return full
	}
	out := map[string]any{}
	draft := parsePlanningDraftArgs(buffer)
	if draft.Title.Closed {
		out["title"] = draft.Title.Value
	}
	if draft.Summary.Closed {
		out["summary"] = draft.Summary.Value
	}
	if draft.PublicEventsAndStorage.Closed {
		out["publicEventsAndStorage"] = closedPlanningDraftItems(draft.PublicEventsAndStorage)
	}
	if draft.ImplementationChanges.Closed {
		out["implementationChanges"] = closedPlanningDraftItems(draft.ImplementationChanges)
	}
	if draft.Interfaces.Closed {
		out["interfaces"] = closedPlanningDraftItems(draft.Interfaces)
	}
	if draft.TestPlan.Closed {
		out["testPlan"] = closedPlanningDraftItems(draft.TestPlan)
	}
	if draft.Assumptions.Closed {
		out["assumptions"] = closedPlanningDraftItems(draft.Assumptions)
	}
	return out
}

func closedPlanningDraftItems(section planningDraftArray) []any {
	items := make([]any, 0, len(section.Items))
	for _, item := range section.Items {
		if item.Closed {
			items = append(items, item.Value)
		}
	}
	return items
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

func parsePlanningStringArrayAt(text string, start int) planningDraftArray {
	array := planningDraftArray{Present: true}
	if start < 0 || start >= len(text) || text[start] != '[' {
		return planningDraftArray{}
	}
	for i := start + 1; i < len(text); {
		i = skipJSONSpaces(text, i)
		if i >= len(text) {
			return array
		}
		switch text[i] {
		case ']':
			array.Closed = true
			return array
		case ',':
			i++
			continue
		case '"':
			value, end, closed, ok := parseJSONStringFragmentAt(text, i)
			if !ok {
				return array
			}
			array.Items = append(array.Items, planningDraftString{
				Value:   value,
				Present: true,
				Closed:  closed,
			})
			if !closed {
				return array
			}
			i = end + 1
		default:
			return array
		}
	}
	return array
}

func parsePartialJSONStringArray(text string, start int) ([]string, bool) {
	if start < 0 || start >= len(text) || text[start] != '[' {
		return nil, false
	}
	items := make([]string, 0)
	for i := start + 1; i < len(text); {
		i = skipJSONSpaces(text, i)
		if i >= len(text) {
			return items, false
		}
		switch text[i] {
		case ']':
			return items, true
		case ',':
			i++
			continue
		case '"':
			value, end, ok := parseJSONStringAt(text, i)
			if !ok {
				return items, false
			}
			items = append(items, value)
			i = end + 1
		default:
			return items, false
		}
	}
	return items, false
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
