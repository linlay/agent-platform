package commands

import (
	"strings"

	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

// Analyze consumes a proven identity in an explicitly supported grammar.
// PowerShell aliases, cmdlets, functions and unverified native argv never enter
// the Unix analyzers. Later Windows profiles must opt in with actual evidence.
func Analyze(input types.CommandInput) types.Effects {
	entry, exists := registry.Lookup(input.Name)
	valid := (exists || IsProgram(input.Name)) && input.Name == strings.ToLower(input.Name) && input.Dialect == dialect.Bash
	switch input.Identity.Domain {
	case types.ShellBuiltin:
		valid = valid && input.Identity.Source == types.BuiltinProgram && entry.Flags&registry.Builtin != 0
	case types.NativeCLI:
		valid = valid && (input.Identity.Source == types.SystemProgram || input.Identity.Source == types.ContainerSystemProgram || input.Identity.Source == types.PlatformProgram)
	default:
		valid = false
	}
	var draft effectDraft
	if valid {
		draft = operands(input.Name, input.Args)
	} else {
		draft = unknownOperands(input.Args)
	}
	e := types.Effects{SSHAgent: draft.SSHAgent}
	if !valid {
		e.Unknowns = append(e.Unknowns, types.Unknown{Kind: types.UnknownDialect, Arg: -1, Rule: "identity:domain-source-dialect"})
	}
	groups := map[int]int{}
	for _, f := range draft.Files {
		kind := f.Kind
		if kind == "" {
			if f.Write {
				kind = types.UnknownWrite
			} else {
				kind = types.Read
			}
		}
		role := f.Role
		if role == "" {
			if f.Write && kind != types.Read {
				role = types.Destination
			} else {
				role = types.Source
			}
		}
		word := types.ValueSource(input, f.Ref, f.Derived)
		origin := types.Implicit
		if f.Ref.Known {
			origin = types.Argument
		}
		target := types.Target{Path: f.Path, Role: role, Word: word, Placement: f.Placement, Child: f.Child, RelativeToDestination: f.RelativeToDestination, Walk: literalWalk(input, f.Walk)}
		op := types.Op{Kind: kind, Targets: []types.Target{target}, Recursive: f.Recursive, Arg: word.Arg, Origin: origin, Rule: input.Name + ":" + string(kind)}
		if index, ok := groups[f.Group]; f.Group != 0 && ok {
			e.Ops[index].Targets = append(e.Ops[index].Targets, target)
		} else {
			if f.Group != 0 {
				groups[f.Group] = len(e.Ops)
			}
			e.Ops = append(e.Ops, op)
		}
		// Unknown options add a write constraint; known Delete/Rename/Chmod/etc
		// remain intact, and cannot later be mistaken for byte-output operations.
		if f.Write && !op.WritesTarget(target) {
			e.Ops = append(e.Ops, types.Op{Kind: types.UnknownWrite, Targets: []types.Target{target}, Recursive: f.Recursive, Arg: word.Arg, Origin: origin, Rule: input.Name + ":unknown-option"})
		}
	}
	if draft.Incomplete {
		e.Unknowns = append(e.Unknowns, types.Unknown{Kind: types.UnknownOption, Arg: -1, Rule: input.Name + ":grammar"})
	}
	if draft.ExecutesCode {
		e.Ops = append(e.Ops, types.Op{Kind: types.Exec, Arg: -1, Origin: types.Implicit, Rule: input.Name + ":program-or-unread-behavior"})
		e.Unknowns = append(e.Unknowns, types.Unknown{Kind: types.UnknownBehavior, Arg: -1, Rule: input.Name + ":behavior"})
	}
	for _, fact := range []struct {
		on   bool
		kind types.OpKind
	}{{draft.RepoWrite, types.RepoWrite}, {draft.Destructive, types.Discard}, {draft.RemoteMutation, types.RemoteMutation}} {
		if fact.on {
			e.Ops = append(e.Ops, types.Op{Kind: fact.kind, Arg: -1, Origin: types.Implicit, Rule: input.Name + ":" + string(fact.kind)})
		}
	}
	if draft.GitCheck != types.GitCheckNone {
		e.Deps = append(e.Deps, types.Dep{Kind: types.GitConfig, Check: string(draft.GitCheck), Path: draft.RepoPath})
	}
	if valid {
		for _, name := range ConfigVariables(input.Name, input.Args) {
			if name != "TAR_OPTIONS" {
				continue
			}
			e.Deps = append(e.Deps, types.Dep{Kind: types.Environment, Variable: name})
		}
	}
	e.Deps = append(e.Deps, draft.Deps...)
	return e
}

// literalWalk keeps the prune tests only when each pattern reaches the
// program as written; the shell rewrites an unquoted glob or a leading tilde.
func literalWalk(input types.CommandInput, w *types.Walk) *types.Walk {
	if w == nil {
		return nil
	}
	out := *w
	for _, alt := range w.Prune {
		for _, test := range alt {
			if test.Primary != "-type" && !types.ValueSource(input, test.Ref, false).Literal {
				out.Prune = nil
			}
		}
	}
	if out.MaxDepth < 0 && out.Prune == nil {
		return nil
	}
	return &out
}
