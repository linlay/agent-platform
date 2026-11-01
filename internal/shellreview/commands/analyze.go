package commands

import (
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
	"strings"
)

// Analyze consumes a proven identity in an explicitly supported grammar.
// PowerShell aliases, cmdlets, functions and unverified native argv never enter
// the Unix analyzers. Later Windows profiles must opt in with actual evidence.
func Analyze(input types.CommandInput) types.Effects {
	entry, exists := registry.Lookup(input.Name)
	valid := exists && input.Name == strings.ToLower(input.Name) && input.Dialect == dialect.Bash
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
			if f.Write {
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
		e.Ops = append(e.Ops, types.Op{Kind: kind, Targets: []types.Target{{Path: f.Path, Role: role, Word: word}}, Recursive: f.Recursive, Arg: word.Arg, Origin: origin, Rule: input.Name + ":" + string(kind)})
		// Unknown options add a write constraint; known Delete/Rename/Chmod/etc
		// remain intact, and cannot later be mistaken for byte-output operations.
		if f.Write && kind == types.Read {
			e.Ops = append(e.Ops, types.Op{Kind: types.UnknownWrite, Targets: []types.Target{{Path: f.Path, Role: role, Word: word}}, Recursive: f.Recursive, Arg: word.Arg, Origin: origin, Rule: input.Name + ":unknown-option"})
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
			e.Deps = append(e.Deps, types.Dep{Kind: types.Environment, Variable: name})
		}
	}
	e.Deps = append(e.Deps, draft.Deps...)
	return e
}
