package bash

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"agent-platform/internal/shellreview/types"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

type walkError struct {
	reason   string
	nodeType string
}

func tooComplex(nodeType, format string, args ...any) *walkError {
	return &walkError{nodeType: nodeType, reason: fmt.Sprintf(format, args...)}
}

type varScope struct {
	vars     map[string]string
	assigned map[string]bool
}

func newVarScope() *varScope {
	scope := &varScope{vars: map[string]string{}, assigned: map[string]bool{}}
	for _, name := range safeEnvironmentVariables {
		scope.vars[name] = TrackedVariablePlaceholder
	}
	return scope
}

func newVarScopeWithKnownVariables(variables map[string]string) *varScope {
	scope := newVarScope()
	for name, value := range variables {
		if isShellIdentifier(name) {
			scope.vars[name] = value
		}
	}
	return scope
}

func (s *varScope) snapshot() *varScope {
	next := &varScope{vars: make(map[string]string, len(s.vars)), assigned: make(map[string]bool, len(s.assigned))}
	for key, value := range s.vars {
		next.vars[key] = value
	}
	for key, value := range s.assigned {
		next.assigned[key] = value
	}
	return next
}

func (s *varScope) set(name, value string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	s.vars[name] = value
	s.assigned[name] = true
}

// Branch joins must not restore the initial value of PATH or a script operand
// when a conditional assignment may have changed it.
func joinScopes(scopes ...*varScope) *varScope {
	joined := scopes[0].snapshot()
	for _, scope := range scopes[1:] {
		for key, value := range joined.vars {
			if other, exists := scope.vars[key]; !exists || value != other {
				joined.set(key, TrackedVariablePlaceholder)
			}
		}
		for key, value := range scope.vars {
			if previous, exists := joined.vars[key]; !exists || previous != value {
				joined.set(key, TrackedVariablePlaceholder)
			}
			if scope.assigned[key] {
				joined.assigned[key] = true
			}
		}
	}
	return joined
}

func (w *walker) markLoopMutation(start int, before, after *varScope) {
	mark := func() {
		for i := start; i < len(w.commands); i++ {
			w.commands[i].Uncertain = true
		}
	}
	for _, cmd := range w.commands[start:] {
		argv := cmd.Argv
		if len(argv) > 1 && (argv[0] == "command" || argv[0] == "builtin") {
			argv = argv[1:]
		}
		if len(argv) > 0 && argv[0] == "cd" {
			// A subsequent iteration may resolve the same script under a new cwd.
			mark()
			return
		}
	}
	joined := joinScopes(before, after)
	for key, value := range joined.vars {
		if original, exists := before.vars[key]; !exists || original != value {
			mark()
			return
		}
	}
}

func (s *varScope) get(name string) (string, bool) {
	value, ok := s.vars[name]
	return value, ok
}

var safeEnvironmentVariables = []string{
	"HOME", "PWD", "OLDPWD", "USER", "LOGNAME", "PATH", "SHELL",
	"TERM", "TMPDIR", "LANG", "LC_ALL", "BASH_VERSION",
}

var unsafeDeclFlagRe = regexp.MustCompile(`^[+-][A-Za-z]*[niaA]`)

type walker struct {
	source   string
	scope    *varScope
	commands []types.Invocation
	globs    GlobExpander
	// cwds lists the directories a later command may run in, relative to the
	// review cwd ("" is that cwd); every cd adds one. cwdUnknown records a cd
	// whose target is not static.
	cwds       []string
	cwdUnknown bool
	// cdTargets holds the directories of the latest cd with a static target.
	cdTargets []string
}

const maxExtractedCommands = 256

func newWalkerWithKnownVariables(source string, variables map[string]string) *walker {
	return &walker{source: source, scope: newVarScopeWithKnownVariables(variables), cwds: []string{""}}
}

func (w *walker) walkFile(file *syntax.File) *walkError {
	scope, err := w.walkStmts(file.Stmts, w.scope)
	if err != nil {
		return err
	}
	w.scope = scope
	return nil
}

func (w *walker) walkStmts(stmts []*syntax.Stmt, scope *varScope) (*varScope, *walkError) {
	current := scope
	for _, stmt := range stmts {
		next, err := w.walkStmt(stmt, current)
		if err != nil {
			return current, err
		}
		current = next
	}
	return current, nil
}

func (w *walker) walkStmt(stmt *syntax.Stmt, scope *varScope) (*varScope, *walkError) {
	if stmt == nil {
		return scope, nil
	}
	if stmt.Background {
		foreground := *stmt
		foreground.Background = false
		_, err := w.walkStmt(&foreground, scope.snapshot())
		return scope, err
	}
	if stmt.Cmd == nil {
		redirects, err := w.parseRedirects(stmt.Redirs, scope)
		if err != nil {
			return scope, err
		}
		if len(redirects) > 0 {
			if err := w.appendCommand(types.Invocation{Redirects: redirects, Text: w.sourceForNode(stmt), Variables: scope.knownValues()}); err != nil {
				return scope, err
			}
		}
		return scope, nil
	}
	if stmt.Coprocess || stmt.Disown {
		return scope, tooComplex("syntax.Stmt", "unsupported statement modifier")
	}
	stmtScope := scope
	if stmt.Background {
		stmtScope = scope.snapshot()
	}
	next, err := w.walkCommand(stmt.Cmd, stmt.Redirs, stmtScope, stmt)
	if err != nil {
		return scope, err
	}
	if stmt.Background {
		return scope, nil
	}
	return next, nil
}

func (w *walker) walkCommand(cmd syntax.Command, redirs []*syntax.Redirect, scope *varScope, stmt *syntax.Stmt) (*varScope, *walkError) {
	switch node := cmd.(type) {
	case *syntax.CallExpr:
		return w.walkCallExpr(node, redirs, scope, stmt)
	case *syntax.BinaryCmd:
		return w.walkBinaryCmd(node, scope)
	case *syntax.DeclClause:
		return w.walkDeclClause(node, redirs, scope, stmt)
	case *syntax.IfClause:
		return w.walkIfClause(node, scope)
	case *syntax.WhileClause:
		return w.walkWhileClause(node, scope)
	case *syntax.ForClause:
		return w.walkForClause(node, scope)
	case *syntax.Subshell:
		return scope, tooComplex("syntax.Subshell", "unsupported subshell")
	case *syntax.Block:
		return scope, tooComplex("syntax.Block", "unsupported block")
	case *syntax.FuncDecl:
		return scope, tooComplex("syntax.FuncDecl", "unsupported function definition")
	case *syntax.CaseClause:
		return scope, tooComplex("syntax.CaseClause", "unsupported case statement")
	case *syntax.ArithmCmd:
		return scope, tooComplex("syntax.ArithmCmd", "unsupported arithmetic command")
	case *syntax.TestClause:
		return scope, tooComplex("syntax.TestClause", "unsupported test command")
	case *syntax.LetClause:
		return scope, tooComplex("syntax.LetClause", "unsupported let clause")
	case *syntax.TimeClause:
		return scope, tooComplex("syntax.TimeClause", "unsupported time clause")
	case *syntax.CoprocClause:
		return scope, tooComplex("syntax.CoprocClause", "unsupported coproc clause")
	default:
		return scope, tooComplex(fmt.Sprintf("%T", cmd), "unsupported command node %T", cmd)
	}
}

func (w *walker) walkBinaryCmd(cmd *syntax.BinaryCmd, scope *varScope) (*varScope, *walkError) {
	switch cmd.Op {
	case syntax.AndStmt:
		start := len(w.commands)
		w.cdTargets = nil
		next, err := w.walkStmt(cmd.X, scope)
		if err != nil {
			return scope, err
		}
		// The right side runs only after a left-side cd succeeded.
		saved := slices.Clone(w.cwds)
		if len(w.commands) == start+1 && w.cdTargets != nil {
			w.cwds = w.cdTargets
		}
		last, err := w.walkStmt(cmd.Y, next.snapshot())
		for _, dir := range w.cwds {
			if !slices.Contains(saved, dir) {
				saved = append(saved, dir)
			}
		}
		w.cwds = saved
		return joinScopes(next, last), err
	case syntax.OrStmt:
		left, err := w.walkStmt(cmd.X, scope.snapshot())
		if err != nil {
			return scope, err
		}
		right, err := w.walkStmt(cmd.Y, left.snapshot())
		return joinScopes(left, right), err
	case syntax.Pipe, syntax.PipeAll:
		if _, err := w.walkStmt(cmd.X, scope.snapshot()); err != nil {
			return scope, err
		}
		if _, err := w.walkStmt(cmd.Y, scope.snapshot()); err != nil {
			return scope, err
		}
		return scope, nil
	default:
		return scope, tooComplex("syntax.BinaryCmd", "unsupported binary operator %s", cmd.Op.String())
	}
}

func (w *walker) walkCallExpr(call *syntax.CallExpr, redirs []*syntax.Redirect, scope *varScope, stmt *syntax.Stmt) (*varScope, *walkError) {
	envVars, err := w.parseAssignments(call.Assigns, scope)
	if err != nil {
		return scope, err
	}
	if len(call.Args) == 0 {
		if len(redirs) > 0 {
			redirects, err := w.parseRedirects(redirs, scope)
			if err != nil {
				return scope, err
			}
			if err := w.appendCommand(types.Invocation{EnvVars: envVars, Redirects: redirects, Text: w.sourceForNode(stmt), Variables: scope.knownValues()}); err != nil {
				return scope, err
			}
		}
		next := scope.snapshot()
		for _, envVar := range envVars {
			next.set(envVar.Name, envVar.Value)
		}
		return next, nil
	}
	argv := make([]string, 0, len(call.Args))
	words := make([]types.WordSpan, 0, len(call.Args))
	for _, word := range call.Args {
		expanded, err := expandBraceWord(word)
		if err != nil {
			return scope, err
		}
		span := wordSpan(word)
		// Brace expansion yields several argv entries from one source span;
		// the span stays shared so verified-connector projection remains exact.
		span.Expansion = span.Expansion || len(expanded) > 1 || expanded[0] != word
		span.Literal = span.Literal && !span.Expansion
		for _, item := range expanded {
			value, err := w.parseWord(item, scope)
			if err != nil {
				return scope, err
			}
			argv = append(argv, value)
			words = append(words, span)
		}
	}
	redirects, err := w.parseRedirects(redirs, scope)
	if err != nil {
		return scope, err
	}
	w.trackCd(argv, scope)
	if err := w.appendCommand(types.Invocation{
		Words:     words,
		Variables: scope.knownValues(),
		Argv:      argv,
		EnvVars:   envVars,
		Redirects: redirects,
		Text:      w.sourceForNode(stmt),
	}); err != nil {
		return scope, err
	}
	return scope, nil
}

func (w *walker) walkDeclClause(decl *syntax.DeclClause, redirs []*syntax.Redirect, scope *varScope, stmt *syntax.Stmt) (*varScope, *walkError) {
	if decl.Variant == nil {
		return scope, tooComplex("syntax.DeclClause", "declaration without variant")
	}
	next := scope.snapshot()
	argv := []string{decl.Variant.Value}
	envVars := make([]types.EnvVar, 0, len(decl.Args))
	restrictedDecl := isRestrictedDeclVariant(decl.Variant.Value)
	for _, assign := range decl.Args {
		if assign == nil {
			continue
		}
		if assign.Index != nil || assign.Array != nil || assign.Append {
			return scope, tooComplex("syntax.Assign", "unsupported declaration assignment")
		}
		if assign.Name != nil && assign.Value != nil {
			value, err := w.parseWord(assign.Value, scope)
			if err != nil {
				return scope, err
			}
			envVars = append(envVars, types.EnvVar{Name: assign.Name.Value, Value: value})
			next.set(assign.Name.Value, value)
			argv = append(argv, assign.Name.Value+"="+value)
			continue
		}
		if assign.Value != nil {
			value, err := w.parseWord(assign.Value, scope)
			if err != nil {
				return scope, err
			}
			if restrictedDecl {
				if unsafeDeclFlagRe.MatchString(value) {
					return scope, tooComplex("syntax.DeclClause", "unsupported declaration flag %s", value)
				}
				if strings.Contains(value, "[") {
					return scope, tooComplex("syntax.DeclClause", "unsupported declaration argument with array subscript")
				}
			}
			argv = append(argv, value)
			continue
		}
		if assign.Name != nil {
			argv = append(argv, assign.Name.Value)
			continue
		}
		return scope, tooComplex("syntax.Assign", "unsupported declaration argument")
	}
	redirects, err := w.parseRedirects(redirs, scope)
	if err != nil {
		return scope, err
	}
	if err := w.appendCommand(types.Invocation{
		Variables: scope.knownValues(),
		Argv:      argv,
		EnvVars:   envVars,
		Redirects: redirects,
		Text:      w.sourceForNode(stmt),
	}); err != nil {
		return scope, err
	}
	return next, nil
}

func (s *varScope) knownValues() map[string]string {
	var values map[string]string
	for key, value := range s.vars {
		if value == TrackedVariablePlaceholder && !s.assigned[key] {
			continue
		}
		if values == nil {
			values = map[string]string{}
		}
		values[key] = value
	}
	return values
}

func (w *walker) appendCommand(cmd types.Invocation) *walkError {
	if len(w.commands) >= maxExtractedCommands {
		return tooComplex("syntax.File", "too many commands to analyze")
	}
	for i := range cmd.Words {
		span := &cmd.Words[i]
		if span.Start >= 0 && span.End <= len(w.source) && span.Start < span.End {
			span.Raw = w.source[span.Start:span.End]
		}
	}
	for i := range cmd.Redirects {
		span := &cmd.Redirects[i].TargetWord
		if span.Start >= 0 && span.End <= len(w.source) && span.Start < span.End {
			span.Raw = w.source[span.Start:span.End]
		}
	}
	w.commands = append(w.commands, cmd)
	return nil
}

func isRestrictedDeclVariant(variant string) bool {
	switch strings.TrimSpace(variant) {
	case "declare", "typeset", "local":
		return true
	default:
		return false
	}
}

func (w *walker) walkIfClause(clause *syntax.IfClause, scope *varScope) (*varScope, *walkError) {
	condition, err := w.walkStmts(clause.Cond, scope.snapshot())
	if err != nil {
		return scope, err
	}
	then, err := w.walkStmts(clause.Then, condition.snapshot())
	if err != nil {
		return scope, err
	}
	otherwise := condition
	if clause.Else != nil {
		otherwise, err = w.walkIfClause(clause.Else, condition.snapshot())
		if err != nil {
			return scope, err
		}
	}
	return joinScopes(then, otherwise), nil
}

func (w *walker) walkWhileClause(clause *syntax.WhileClause, scope *varScope) (*varScope, *walkError) {
	start := len(w.commands)
	condition, err := w.walkStmts(clause.Cond, scope.snapshot())
	if err != nil {
		return scope, err
	}
	body, err := w.walkStmts(clause.Do, condition.snapshot())
	if err != nil {
		return scope, err
	}
	w.markLoopMutation(start, scope, joinScopes(condition, body))
	return joinScopes(condition, body), nil
}

func (w *walker) walkForClause(clause *syntax.ForClause, scope *varScope) (*varScope, *walkError) {
	iter, ok := clause.Loop.(*syntax.WordIter)
	if !ok {
		return scope, tooComplex("syntax.ForClause", "unsupported for loop")
	}
	loopScope := scope.snapshot()
	if iter.Name != nil {
		loopScope.set(iter.Name.Value, TrackedVariablePlaceholder)
	}
	for _, item := range iter.Items {
		if _, err := w.parseWord(item, scope); err != nil {
			return scope, err
		}
	}
	start := len(w.commands)
	body, err := w.walkStmts(clause.Do, loopScope.snapshot())
	if err != nil {
		return scope, err
	}
	if iter.Name != nil {
		values, ok := w.loopValues(iter.Items, scope)
		if ok && start+(len(w.commands)-start)*len(values) <= maxExtractedCommands {
			// Review the body once per value the loop will see.
			w.commands = w.commands[:start]
			bodies := []*varScope{scope}
			for _, value := range values {
				itemScope := scope.snapshot()
				itemScope.set(iter.Name.Value, value)
				next, err := w.walkStmts(clause.Do, itemScope.snapshot())
				if err != nil {
					return scope, err
				}
				w.markLoopMutation(start, itemScope, next)
				bodies = append(bodies, next)
			}
			return joinScopes(bodies...), nil
		}
	}
	w.markLoopMutation(start, loopScope, body)
	return joinScopes(scope, body), nil
}

const maxLoopValues = 64

// loopValues lists the words a for loop iterates over when every item is
// static: plain or quoted text, known variables, braces, a leading ~ with a
// known HOME, and globs the expander resolves.
func (w *walker) loopValues(items []*syntax.Word, scope *varScope) ([]string, bool) {
	if w.globs == nil {
		return nil, false
	}
	var values []string
	for _, item := range items {
		words, err := expandBraceWord(item)
		if err != nil {
			return nil, false
		}
		for _, word := range words {
			value, glob, ok := staticLoopWord(word, scope)
			if !ok {
				return nil, false
			}
			if !glob {
				values = append(values, value)
				continue
			}
			if w.cwdUnknown && !filepath.IsAbs(value) {
				return nil, false
			}
			matches, ok := w.globs(value, w.cwds)
			if !ok {
				return nil, false
			}
			values = append(values, matches...)
		}
		if len(values) > maxLoopValues {
			return nil, false
		}
	}
	return values, true
}

// staticLoopWord returns a loop item's value and whether it holds an unquoted
// glob. Backslashes and glob characters inside quotes are not modeled.
func staticLoopWord(word *syntax.Word, scope *varScope) (string, bool, bool) {
	var b strings.Builder
	glob := false
	known := func(name string) (string, bool) {
		value, ok := scope.get(name)
		return value, ok && value != TrackedVariablePlaceholder && value != CommandSubstitutionPlaceholder
	}
	quoted := func(text string) bool {
		b.WriteString(text)
		return !strings.ContainsAny(text, "*?[\\")
	}
	for i, part := range word.Parts {
		switch node := part.(type) {
		case *syntax.Lit:
			text := node.Value
			if strings.Contains(text, `\`) {
				return "", false, false
			}
			if i == 0 && (text == "~" || strings.HasPrefix(text, "~/")) {
				home, ok := known("HOME")
				if !ok || !filepath.IsAbs(home) {
					return "", false, false
				}
				text = home + text[1:]
			} else if i == 0 && strings.HasPrefix(text, "~") {
				return "", false, false
			}
			glob = glob || strings.ContainsAny(text, "*?[")
			b.WriteString(text)
		case *syntax.SglQuoted:
			if node.Dollar || !quoted(node.Value) {
				return "", false, false
			}
		case *syntax.DblQuoted:
			if node.Dollar {
				return "", false, false
			}
			for _, inner := range node.Parts {
				switch inner := inner.(type) {
				case *syntax.Lit:
					if !quoted(inner.Value) {
						return "", false, false
					}
				case *syntax.ParamExp:
					value, ok := known(inner.Param.Value)
					if !isSimpleParamExp(inner) || !ok || !quoted(value) {
						return "", false, false
					}
				default:
					return "", false, false
				}
			}
		case *syntax.ParamExp:
			value, ok := known(node.Param.Value)
			if !isSimpleParamExp(node) || !ok || hasBareVarUnsafeChars(value) {
				return "", false, false
			}
			b.WriteString(value)
		default:
			return "", false, false
		}
	}
	return b.String(), glob, true
}

// trackCd adds the target of each cd as a possible cwd. Any cd, even one in a
// pipeline or branch that might not change this shell, widens the set.
func (w *walker) trackCd(argv []string, scope *varScope) {
	if len(argv) > 1 && (argv[0] == "command" || argv[0] == "builtin") {
		argv = argv[1:]
	}
	if len(argv) == 0 || argv[0] != "cd" {
		w.cdTargets = nil
		return
	}
	target := ""
	if len(argv) == 2 {
		target = argv[1]
	}
	if home, ok := scope.get("HOME"); ok && (target == "~" || strings.HasPrefix(target, "~/")) && filepath.IsAbs(home) {
		target = home + target[1:]
	}
	_, cdpath := scope.get("CDPATH")
	if target == "" || strings.HasPrefix(target, "-") || strings.HasPrefix(target, "~") || strings.Contains(target, TrackedVariablePlaceholder) || strings.Contains(target, CommandSubstitutionPlaceholder) || cdpath && !filepath.IsAbs(target) {
		w.cwdUnknown = true
		return
	}
	var targets []string
	for _, dir := range w.cwds {
		next := target
		if !filepath.IsAbs(target) {
			next = filepath.Join(dir, target)
		}
		if !slices.Contains(targets, next) {
			targets = append(targets, next)
		}
	}
	for _, next := range targets {
		if !slices.Contains(w.cwds, next) {
			w.cwds = append(w.cwds, next)
		}
	}
	w.cdTargets = targets
}

func (w *walker) parseAssignments(assigns []*syntax.Assign, scope *varScope) ([]types.EnvVar, *walkError) {
	envVars := make([]types.EnvVar, 0, len(assigns))
	for _, assign := range assigns {
		if assign == nil {
			continue
		}
		if assign.Name == nil || assign.Index != nil || assign.Array != nil || assign.Append || assign.Naked {
			return nil, tooComplex("syntax.Assign", "unsupported assignment")
		}
		value := ""
		if assign.Value != nil {
			parsed, err := w.parseWord(assign.Value, scope)
			if err != nil {
				return nil, err
			}
			value = parsed
		}
		envVars = append(envVars, types.EnvVar{Name: assign.Name.Value, Value: value})
	}
	return envVars, nil
}

func (w *walker) parseRedirects(redirs []*syntax.Redirect, scope *varScope) ([]types.Redirect, *walkError) {
	out := make([]types.Redirect, 0, len(redirs))
	for _, redir := range redirs {
		if redir == nil {
			continue
		}
		target := ""
		if redir.Word != nil {
			parsed, err := w.parseWord(redir.Word, scope)
			if err != nil {
				return nil, err
			}
			target = parsed
		}
		fd := -1
		if redir.N != nil {
			parsed, err := strconv.Atoi(redir.N.Value)
			if err != nil {
				return nil, tooComplex("syntax.Redirect", "unsupported redirection fd")
			}
			fd = parsed
		}
		next := types.Redirect{Op: redir.Op.String(), Target: target, TargetWord: wordSpan(redir.Word), Fd: fd}
		if redir.Hdoc != nil {
			if _, err := w.parseWord(redir.Hdoc, scope); err != nil {
				return nil, err
			}
			bodyStart, bodyEnd := w.heredocBodyRange(redir, target)
			next.IsHeredoc = true
			next.HeredocBodyStart = bodyStart
			next.HeredocBodyEnd = bodyEnd
			next.HeredocLiteral = heredocLiteral(redir)
		}
		out = append(out, next)
	}
	return out, nil
}

// heredocLiteral reports a body the shell passes on unchanged: any quoting in
// the delimiter disables expansion, otherwise the body must be plain text.
func heredocLiteral(redir *syntax.Redirect) bool {
	if redir.Word != nil {
		for _, part := range redir.Word.Parts {
			switch p := part.(type) {
			case *syntax.SglQuoted, *syntax.DblQuoted:
				return true
			case *syntax.Lit:
				if strings.Contains(p.Value, `\`) {
					return true
				}
			}
		}
	}
	for _, part := range redir.Hdoc.Parts {
		if lit, ok := part.(*syntax.Lit); !ok || strings.Contains(lit.Value, `\`) {
			return false
		}
	}
	return true
}

func (w *walker) heredocBodyRange(redir *syntax.Redirect, delimiter string) (int, int) {
	start := int(redir.Hdoc.Pos().Offset())
	end := int(redir.Hdoc.End().Offset())
	if start < 0 || end < start || start > len(w.source) {
		return start, end
	}
	if end > len(w.source) {
		end = len(w.source)
	}

	scan := start
	for scan <= end {
		lineStart := scan
		lineEnd := strings.IndexByte(w.source[scan:end], '\n')
		next := end + 1
		rawLine := w.source[scan:end]
		if lineEnd >= 0 {
			rawLine = w.source[scan : scan+lineEnd]
			next = scan + lineEnd + 1
		}
		candidate := strings.TrimSuffix(rawLine, "\r")
		if redir.Op.String() == "<<-" {
			candidate = strings.TrimLeft(candidate, "\t")
		}
		if candidate == delimiter {
			return start, lineStart
		}
		if next <= scan || next > len(w.source) {
			break
		}
		scan = next
	}
	return start, end
}

func (w *walker) parseWord(word *syntax.Word, scope *varScope) (string, *walkError) {
	if word == nil {
		return "", nil
	}
	var b strings.Builder
	for _, part := range word.Parts {
		value, err := w.parseWordPart(part, scope, false)
		if err != nil {
			return "", err
		}
		b.WriteString(value)
	}
	return b.String(), nil
}

// removeBackslashes applies Bash quote removal to literal text, which the
// parser keeps verbatim: unquoted, a backslash yields the next character;
// inside double quotes it escapes only $ ` " and \. The parser already drops
// line continuations. Reviewing the raw text would check a different path
// than the one the shell opens.
func removeBackslashes(text string, insideDoubleQuote bool) string {
	if !strings.Contains(text, `\`) {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\\' && i+1 < len(text) {
			if next := text[i+1]; !insideDoubleQuote || strings.IndexByte("$`\"\\", next) >= 0 {
				i++
				c = next
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

func (w *walker) parseWordPart(part syntax.WordPart, scope *varScope, insideDoubleQuote bool) (string, *walkError) {
	switch node := part.(type) {
	case *syntax.Lit:
		return removeBackslashes(node.Value, insideDoubleQuote), nil
	case *syntax.SglQuoted:
		if node.Dollar {
			value, err := expand.Literal(&expand.Config{}, &syntax.Word{Parts: []syntax.WordPart{node}})
			if err != nil {
				return "", tooComplex("syntax.SglQuoted", "invalid ANSI-C quoted string")
			}
			if strings.IndexByte(value, 0) >= 0 {
				return "", tooComplex("syntax.SglQuoted", "NUL in quoted string")
			}
			return value, nil
		}
		return node.Value, nil
	case *syntax.DblQuoted:
		if node.Dollar {
			return "", tooComplex("syntax.DblQuoted", "unsupported locale quoted string")
		}
		var b strings.Builder
		for _, child := range node.Parts {
			value, err := w.parseWordPart(child, scope, true)
			if err != nil {
				return "", err
			}
			b.WriteString(value)
		}
		return b.String(), nil
	case *syntax.ParamExp:
		if !isSimpleParamExp(node) {
			return "", tooComplex("syntax.ParamExp", "unsupported parameter expansion")
		}
		if value, ok := safeSpecialParamValue(node.Param.Value); ok {
			return value, nil
		}
		value, ok := scope.get(node.Param.Value)
		if !ok {
			return "", tooComplex("syntax.ParamExp", "unknown variable %s", node.Param.Value)
		}
		if !insideDoubleQuote && (hasBareVarUnsafeChars(value) || value == TrackedVariablePlaceholder && scope.assigned[node.Param.Value]) {
			return "", tooComplex("syntax.ParamExp", "variable %s expands to unsafe unquoted value", node.Param.Value)
		}
		return value, nil
	case *syntax.CmdSubst:
		if node.Backquotes {
			return "", tooComplex("syntax.CmdSubst", "unsupported backtick command substitution")
		}
		if node.TempFile || node.ReplyVar {
			return "", tooComplex("syntax.CmdSubst", "unsupported command substitution form")
		}
		if _, err := w.walkStmts(node.Stmts, scope.snapshot()); err != nil {
			return "", err
		}
		return CommandSubstitutionPlaceholder, nil
	case *syntax.ArithmExp:
		return "", tooComplex("syntax.ArithmExp", "unsupported arithmetic expansion")
	case *syntax.ProcSubst:
		return "", tooComplex("syntax.ProcSubst", "unsupported process substitution")
	case *syntax.ExtGlob:
		return "", tooComplex("syntax.ExtGlob", "unsupported extended glob")
	case *syntax.BraceExp:
		return "", tooComplex("syntax.BraceExp", "unsupported brace expansion")
	default:
		return "", tooComplex(fmt.Sprintf("%T", part), "unsupported word part %T", part)
	}
}

func hasBareVarUnsafeChars(value string) bool {
	return strings.ContainsAny(value, " \t\n*?[]")
}

func safeSpecialParamValue(name string) (string, bool) {
	switch name {
	case "?":
		return "0", true
	default:
		return "", false
	}
}

func isShellIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for idx, r := range name {
		if idx == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func isSimpleParamExp(exp *syntax.ParamExp) bool {
	return exp != nil && exp.Param != nil && exp.Flags == nil &&
		!exp.Excl && !exp.Length && !exp.Width && !exp.IsSet &&
		exp.NestedParam == nil && exp.Index == nil &&
		len(exp.Modifiers) == 0 && exp.Slice == nil &&
		exp.Repl == nil && exp.Names == 0 && exp.Exp == nil
}

func (w *walker) sourceForNode(node syntax.Node) string {
	if node == nil {
		return ""
	}
	start := int(node.Pos().Offset())
	end := int(node.End().Offset())
	if start < 0 || end < start || end > len(w.source) {
		return ""
	}
	return strings.TrimSpace(w.source[start:end])
}
