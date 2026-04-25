package bashast

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type walkError struct {
	reason   string
	nodeType string
}

func (e *walkError) Error() string { return e.reason }

func tooComplex(nodeType, format string, args ...any) *walkError {
	return &walkError{nodeType: nodeType, reason: fmt.Sprintf(format, args...)}
}

type varScope struct {
	vars map[string]string
}

func newVarScope() *varScope {
	scope := &varScope{vars: map[string]string{}}
	for _, name := range safeEnvironmentVariables {
		scope.vars[name] = TrackedVariablePlaceholder
	}
	return scope
}

func (s *varScope) snapshot() *varScope {
	next := &varScope{vars: make(map[string]string, len(s.vars))}
	for key, value := range s.vars {
		next.vars[key] = value
	}
	return next
}

func (s *varScope) set(name, value string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	s.vars[name] = value
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
	commands []SimpleCommand
}

const maxExtractedCommands = 256

func newWalker(source string) *walker {
	return &walker{source: source, scope: newVarScope()}
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
	if stmt == nil || stmt.Cmd == nil {
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
		next, err := w.walkStmt(cmd.X, scope)
		if err != nil {
			return scope, err
		}
		return w.walkStmt(cmd.Y, next)
	case syntax.OrStmt, syntax.Pipe, syntax.PipeAll:
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
		next := scope.snapshot()
		for _, envVar := range envVars {
			next.set(envVar.Name, envVar.Value)
		}
		return next, nil
	}
	argv := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		value, err := w.parseWord(word, scope)
		if err != nil {
			return scope, err
		}
		argv = append(argv, value)
	}
	redirects, err := w.parseRedirects(redirs, scope)
	if err != nil {
		return scope, err
	}
	if err := w.appendCommand(SimpleCommand{
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
	envVars := make([]EnvVar, 0, len(decl.Args))
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
			envVars = append(envVars, EnvVar{Name: assign.Name.Value, Value: value})
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
	if err := w.appendCommand(SimpleCommand{
		Argv:      argv,
		EnvVars:   envVars,
		Redirects: redirects,
		Text:      w.sourceForNode(stmt),
	}); err != nil {
		return scope, err
	}
	return next, nil
}

func (w *walker) appendCommand(cmd SimpleCommand) *walkError {
	if len(w.commands) >= maxExtractedCommands {
		return tooComplex("syntax.File", "too many commands to analyze")
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
	if _, err := w.walkStmts(clause.Cond, scope.snapshot()); err != nil {
		return scope, err
	}
	if _, err := w.walkStmts(clause.Then, scope.snapshot()); err != nil {
		return scope, err
	}
	if clause.Else != nil {
		if _, err := w.walkIfClause(clause.Else, scope.snapshot()); err != nil {
			return scope, err
		}
	}
	return scope, nil
}

func (w *walker) walkWhileClause(clause *syntax.WhileClause, scope *varScope) (*varScope, *walkError) {
	if _, err := w.walkStmts(clause.Cond, scope.snapshot()); err != nil {
		return scope, err
	}
	if _, err := w.walkStmts(clause.Do, scope.snapshot()); err != nil {
		return scope, err
	}
	return scope, nil
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
	if _, err := w.walkStmts(clause.Do, loopScope); err != nil {
		return scope, err
	}
	return scope, nil
}

func (w *walker) parseAssignments(assigns []*syntax.Assign, scope *varScope) ([]EnvVar, *walkError) {
	envVars := make([]EnvVar, 0, len(assigns))
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
		envVars = append(envVars, EnvVar{Name: assign.Name.Value, Value: value})
	}
	return envVars, nil
}

func (w *walker) parseRedirects(redirs []*syntax.Redirect, scope *varScope) ([]Redirect, *walkError) {
	out := make([]Redirect, 0, len(redirs))
	for _, redir := range redirs {
		if redir == nil {
			continue
		}
		if redir.Hdoc != nil {
			return nil, tooComplex("syntax.Redirect", "unsupported heredoc redirection")
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
		out = append(out, Redirect{Op: redir.Op.String(), Target: target, Fd: fd})
	}
	return out, nil
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

func (w *walker) parseWordPart(part syntax.WordPart, scope *varScope, insideDoubleQuote bool) (string, *walkError) {
	switch node := part.(type) {
	case *syntax.Lit:
		return node.Value, nil
	case *syntax.SglQuoted:
		if node.Dollar {
			return "", tooComplex("syntax.SglQuoted", "unsupported ANSI-C quoted string")
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
		value, ok := scope.get(node.Param.Value)
		if !ok {
			return "", tooComplex("syntax.ParamExp", "unknown variable %s", node.Param.Value)
		}
		if !insideDoubleQuote && hasBareVarUnsafeChars(value) {
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
