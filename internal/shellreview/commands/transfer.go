package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
	"path/filepath"
	"strings"
)

// transferOperands shares positional binding, not command-name dispatch.
// Both possible destination shapes are resource facts: a prior shell command
// can turn an absent/file destination into a directory before this call runs.
func transferOperands(spec types.OptionSpec, args []string, kind types.OpKind) effectDraft {
	p := options.Parse(spec, args)
	e := effectDraft{Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	for _, file := range p.Files {
		targetOption := false
		for _, key := range []string{"t", "target-directory"} {
			for _, ref := range p.ValueRefs[key] {
				targetOption = targetOption || file.Ref == ref
			}
		}
		if !targetOption {
			e.Files = append(e.Files, file)
		}
	}
	positions, refs := p.Positional, p.PositionalRefs
	target, ref := "", types.ValueRef{}
	placement := types.FileOrDirectory
	if p.Has("t") || p.Has("target-directory") {
		key := "t"
		if p.Has("target-directory") {
			key = "target-directory"
		}
		values := p.Values[key]
		if len(values) > 0 {
			target = values[len(values)-1]
			ref = p.ValueRefs[key][len(values)-1]
		}
		placement = types.IntoDirectory
	} else if len(positions) >= 2 {
		target = positions[len(positions)-1]
		ref = refs[len(refs)-1]
		positions = positions[:len(positions)-1]
		refs = refs[:len(refs)-1]
	} else if kind == types.Link && len(positions) == 1 {
		target = "."
		placement = types.IntoDirectory
	} else {
		e.Incomplete = true
	}
	if p.Has("T") || p.Has("no-target-directory") {
		if placement == types.IntoDirectory {
			e.Incomplete = true
		}
		placement = types.Exact
	}
	recursive := p.Recursive || p.Has("r") || p.Has("R") || p.Has("recursive") || p.Has("a") || p.Has("archive") || kind == types.Rename
	symbolic := kind == types.Link && (p.Has("s") || p.Has("symbolic")) || kind == types.Create && (p.Has("s") || p.Has("symbolic-link"))
	if kind == types.Create && (p.Has("l") || p.Has("link") || symbolic) {
		kind = types.Link
	}
	for i, source := range positions {
		if source == "" {
			e.Incomplete = true
			continue
		}
		group := i + 1
		role := types.Source
		if kind == types.Link {
			role = types.LinkTarget
		}
		child := filepath.Base(strings.TrimRight(source, "/"))
		if p.Has("parents") {
			child = strings.TrimLeft(source, "/")
		}
		if strings.ContainsAny(source, "*?[") {
			e.Incomplete = true
		}
		e.Files = append(e.Files, types.FileAccess{Path: source, Write: kind != types.Create, Kind: kind, Role: role, Recursive: recursive, Ref: refs[i], RelativeToDestination: symbolic, Group: group})
		if target != "" {
			for _, role := range []types.Role{types.Destination, types.Replaced} {
				e.Files = append(e.Files, types.FileAccess{Path: target, Write: true, Kind: kind, Role: role, Recursive: recursive, Ref: ref, Derived: true, Placement: placement, Child: child, Group: group})
			}
			// Backup numbering and suffixes add targets beyond the named destination.
			// Keep the parent subtree protected until exact backup paths are modeled.
			if p.Has("b") || p.Has("backup") {
				e.Incomplete = true
				e.Files = append(e.Files, types.FileAccess{Path: target, Write: true, Kind: types.UnknownWrite, Recursive: true, Placement: types.ParentDirectory, Derived: true})
				for _, suffix := range append(p.Values["S"], p.Values["suffix"]...) {
					e.Files = append(e.Files, types.FileAccess{Path: target + suffix, Write: true, Kind: types.UnknownWrite, Derived: true})
				}
			}
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
