package commands

import (
	"strings"

	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

// Git read commands whose only implicit program execution comes from
// repository configuration (fsmonitor, diff drivers, filters, signing).
var gitReadCommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "rev-parse": true, "ls-files": true,
	"ls-tree": true, "blame": true, "annotate": true, "describe": true, "shortlog": true, "cat-file": true,
	"show-ref": true, "merge-base": true, "for-each-ref": true, "grep": true, "rev-list": true, "name-rev": true,
	"count-objects": true, "check-ignore": true, "check-attr": true, "whatchanged": true, "version": true,
}

// Local repository mutations: they may run hooks in addition to configuration.
var gitWriteCommands = map[string]bool{
	"add": true, "commit": true, "mv": true, "rm": true, "restore": true, "reset": true, "stash": true,
	"switch": true, "checkout": true, "branch": true, "tag": true, "merge": true, "rebase": true,
	"cherry-pick": true, "revert": true, "am": true, "apply": true, "init": true, "notes": true,
	"clean": true, "gc": true, "worktree": true, "reflog": true, "config": true, "remote": true,
}

var gitNetworkCommands = map[string]bool{"fetch": true, "pull": true, "push": true, "clone": true, "ls-remote": true, "submodule": true}

func gitOperands(args []string) effectDraft {
	var e effectDraft
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		a := args[i]
		i++
		switch {
		case a == "-C":
			if i == len(args) {
				e.Incomplete, e.ExecutesCode = true, true
				return e
			}
			e.RepoPath = joinDirectory(e.RepoPath, args[i])
			e.Files = append(e.Files, types.FileAccess{Path: e.RepoPath, Ref: types.ArgumentRef(i, args[i]), Derived: true})
			i++
		case strings.HasPrefix(a, "-C") && len(a) > 2:
			e.RepoPath = joinDirectory(e.RepoPath, a[2:])
			e.Files = append(e.Files, types.FileAccess{Path: e.RepoPath, Ref: types.ValueRef{Index: i - 1, Start: 2, End: len(a), Known: true}, Derived: true})
		case a == "--no-pager" || a == "-P" || a == "--no-optional-locks" || a == "--literal-pathspecs" || a == "--glob-pathspecs" || a == "--noglob-pathspecs" || a == "--icase-pathspecs" || a == "--no-replace-objects" || a == "-p" || a == "--paginate":
		default:
			// -c/--config-env inject configuration; --git-dir/--work-tree/--exec-path
			// relocate the repository or Git's helpers. Their programs are not frozen.
			e.ExecutesCode = true
			if a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--exec-path" || a == "--config-env" || a == "--namespace" {
				i++
			}
		}
	}
	if i >= len(args) {
		return e
	}
	globalFiles := len(e.Files)
	sub := args[i]
	rest := args[i+1:]
	parsed := options.Parse(gitOptions(sub), rest)
	for j := range parsed.Files {
		parsed.Files[j].Ref.Index += i + 1
	}
	e.Files = append(e.Files, parsed.Files...)
	e.Incomplete = e.Incomplete || parsed.Incomplete
	if parsed.Incomplete {
		for j, path := range parsed.Positional {
			if path != "-" && !strings.Contains(path, "://") {
				ref := parsed.PositionalRefs[j]
				ref.Index += i + 1
				e.Files = append(e.Files, types.FileAccess{Path: path, Write: true, Recursive: true, Ref: ref})
			}
		}
	}
	flags := map[string]bool{}
	var positional, afterDashes []string
	var positionalRefs, afterRefs []types.ValueRef
	dashes := false
	for j, a := range rest {
		ref := types.ArgumentRef(i+1+j, a)
		if dashes {
			afterDashes = append(afterDashes, a)
			afterRefs = append(afterRefs, ref)
			continue
		}
		if a == "--" {
			dashes = true
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags[a] = true
			if name, _, ok := strings.Cut(a, "="); ok {
				flags[name] = true
			}
			continue
		}
		positional = append(positional, a)
		positionalRefs = append(positionalRefs, ref)
	}
	for j, p := range afterDashes {
		e.Files = append(e.Files, types.FileAccess{Path: p, Ref: afterRefs[j]})
	}
	switch {
	case gitReadCommands[sub] || isGitListing(sub, flags, positional):
		if flags["--ext-diff"] || flags["--show-signature"] || flags["-O"] || flags["--open-files-in-pager"] {
			e.ExecutesCode = true
		}
		if sub == "diff" && flags["--no-index"] {
			for j, p := range positional {
				e.Files = append(e.Files, types.FileAccess{Path: p, Ref: positionalRefs[j]})
			}
		}
		e.GitCheck = types.GitCheckRead
	case gitWriteCommands[sub]:
		e.RepoWrite = true
		e.GitCheck = types.GitCheckWrite
		gitWriteEffects(&e, sub, flags, positional, afterDashes, positionalRefs, afterRefs)
	case gitNetworkCommands[sub]:
		e.SSHAgent = true
		e.GitCheck = types.GitCheckNetwork
		switch sub {
		case "push":
			e.RemoteMutation = true
			if flags["--force"] || flags["-f"] || flags["--force-with-lease"] || flags["--delete"] || flags["-d"] || flags["--mirror"] || flags["--prune"] {
				e.Destructive = true
			}
			for _, p := range positional {
				if strings.HasPrefix(p, "+") || strings.HasPrefix(p, ":") {
					e.Destructive = true
				}
			}
		case "fetch", "pull", "submodule":
			e.RepoWrite = true
		case "clone":
			if len(positional) >= 2 {
				e.Files = append(e.Files, types.FileAccess{Path: positional[len(positional)-1], Write: true, Recursive: true, Ref: positionalRefs[len(positional)-1], Kind: types.Create})
			} else {
				// The destination is derived from the URL inside cwd.
				e.Files = append(e.Files, types.FileAccess{Path: ".", Write: true, Recursive: true, Kind: types.Create})
			}
		}
	default:
		// Aliases, external subcommands and tools such as bisect run or difftool.
		e.ExecutesCode = true
		e.GitCheck = types.GitCheckNetwork
	}
	if e.Incomplete || sub == "config" && e.RepoWrite {
		escalateWrites(&e)
	}
	for j := globalFiles; j < len(e.Files); j++ {
		if e.RepoPath != "" && !strings.HasPrefix(e.Files[j].Path, "/") {
			e.Files[j].Path = joinDirectory(e.RepoPath, e.Files[j].Path)
			e.Files[j].Derived = true
		}
	}
	if e.GitCheck != types.GitCheckNone {
		root := e.RepoPath
		if root == "" {
			root = "."
		}
		e.Files = append(e.Files, types.FileAccess{Path: root, Recursive: true, Kind: types.Read, Derived: true})
	}
	return e
}

func isGitListing(sub string, flags map[string]bool, positional []string) bool {
	switch sub {
	case "branch":
		if flags["-d"] || flags["-D"] || flags["--delete"] || flags["-m"] || flags["-M"] || flags["--move"] || flags["-c"] || flags["-C"] || flags["--copy"] || flags["-u"] || flags["--set-upstream-to"] || flags["--unset-upstream"] || flags["--edit-description"] || flags["-f"] || flags["--force"] {
			return false
		}
		return len(positional) == 0 || flags["--list"] || flags["-l"] || flags["--contains"] || flags["--merged"] || flags["--no-merged"] || flags["--points-at"]
	case "tag":
		if flags["-d"] || flags["--delete"] || flags["-a"] || flags["-s"] || flags["-f"] || flags["-m"] || flags["-F"] {
			return false
		}
		return len(positional) == 0 || flags["-l"] || flags["--list"]
	case "remote":
		return len(positional) == 0 || positional[0] == "get-url"
	case "stash":
		return len(positional) > 0 && (positional[0] == "list" || positional[0] == "show")
	case "worktree":
		return len(positional) > 0 && positional[0] == "list"
	case "reflog":
		return len(positional) == 0 || positional[0] == "show"
	case "config":
		return flags["--get"] || flags["--get-all"] || flags["--get-regexp"] || flags["--list"] || flags["-l"] || (len(positional) == 1 && !flags["--unset"] && !flags["--unset-all"] && !flags["--add"] && !flags["--replace-all"] && !flags["-e"] && !flags["--edit"])
	}
	return false
}

func gitWriteEffects(e *effectDraft, sub string, flags map[string]bool, positional, afterDashes []string, positionalRefs, afterRefs []types.ValueRef) {
	switch sub {
	case "reset":
		e.Destructive = flags["--hard"] || flags["--merge"] || flags["--keep"]
	case "clean":
		e.Destructive = !(flags["-n"] || flags["--dry-run"])
	case "checkout":
		e.Destructive = flags["-f"] || flags["--force"] || len(afterDashes) > 0 || (len(positional) == 1 && positional[0] == ".")
	case "restore":
		e.Destructive = !(flags["--staged"] || flags["-S"]) || flags["--worktree"] || flags["-W"]
	case "stash":
		e.Destructive = len(positional) > 0 && (positional[0] == "drop" || positional[0] == "clear")
	case "branch":
		e.Destructive = flags["-D"] || (flags["-d"] || flags["--delete"]) && (flags["-f"] || flags["--force"])
	case "mv":
		for j, p := range positional {
			e.Files = append(e.Files, types.FileAccess{Path: p, Write: true, Ref: positionalRefs[j], Kind: types.Rename})
		}
	case "rm":
		refs := append(append([]types.ValueRef(nil), positionalRefs...), afterRefs...)
		for j, p := range append(append([]string(nil), positional...), afterDashes...) {
			e.Files = append(e.Files, types.FileAccess{Path: p, Write: true, Recursive: flags["-r"], Ref: refs[j], Kind: types.Delete})
		}
	case "init":
		if len(positional) > 0 {
			e.Files = append(e.Files, types.FileAccess{Path: positional[0], Write: true, Ref: positionalRefs[0], Kind: types.Create})
		}
	case "worktree":
		if len(positional) > 1 && positional[0] == "add" {
			e.Files = append(e.Files, types.FileAccess{Path: positional[len(positional)-1], Write: true, Recursive: true, Ref: positionalRefs[len(positional)-1], Kind: types.Create})
		} else if len(positional) > 0 && positional[0] != "prune" {
			e.ExecutesCode = true
		}
	case "config":
		if flags["--global"] || flags["--system"] || flags["--file"] || flags["-f"] || flags["--blob"] {
			// Configuration outside the repository is not modeled as a file effect.
			e.ExecutesCode = true
		}
		if len(positional) > 0 && isExecutableGitKey(positional[0]) {
			e.ExecutesCode = true
		}
	case "apply", "am":
		for j, p := range positional {
			e.Files = append(e.Files, types.FileAccess{Path: p, Ref: positionalRefs[j]})
		}
	}
}

func isExecutableGitKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "core.fsmonitor", "diff.external", "core.hookspath", "gpg.program", "commit.gpgsign", "tag.gpgsign", "log.showsignature", "core.sshcommand", "core.askpass", "core.editor", "sequence.editor", "core.pager", "core.alternaterefscommand", "uploadpack.packobjectshook", "credential.helper":
		return true
	}
	if strings.HasPrefix(key, "filter.") || strings.HasPrefix(key, "alias.") || strings.HasPrefix(key, "pager.") || strings.HasPrefix(key, "gpg.") && strings.HasSuffix(key, ".program") {
		return true
	}
	if strings.HasPrefix(key, "diff.") && (strings.HasSuffix(key, ".textconv") || strings.HasSuffix(key, ".command")) {
		return true
	}
	if strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver") {
		return true
	}
	return false
}

func joinDirectory(base, next string) string {
	if strings.HasPrefix(next, "/") || base == "" {
		return next
	}
	return strings.TrimRight(base, "/") + "/" + next
}

// Common flags cover the explicitly modeled subset. Unknown options remain
// UnknownOption even when the subcommand has a known read/write classification.
var gitOptionSpec = types.OptionSpec{
	ShortFlags: "aAbBdDlnNpPqrsStuvzMRG", NumericShort: true,
	ShortArgs: map[byte]types.OptionRole{'m': types.OptionValue, 'C': types.OptionValue, 'U': types.OptionValue, 'O': types.OptionRead},
	Long: options.Merge(
		options.Longs(types.OptionNone, "stat", "short", "porcelain", "oneline", "all", "force", "force-with-lease", "delete", "mirror", "prune", "hard", "merge", "keep", "dry-run", "staged", "worktree", "no-index", "ext-diff", "show-signature", "open-files-in-pager", "list", "contains", "merged", "no-merged", "points-at", "move", "copy", "unset-upstream", "edit-description", "get", "get-all", "get-regexp", "unset", "unset-all", "add", "replace-all", "edit", "global", "system", "cached", "name-only", "name-status", "quiet", "verbose", "help", "version", "no-ext-diff", "no-textconv", "no-renames", "check", "exit-code", "no-color"),
		options.Longs(types.OptionOptionalValue, "color", "pretty", "format", "decorate"),
		options.Longs(types.OptionValue, "message", "max-count", "since", "until", "author", "grep", "set-upstream-to"),
		options.Longs(types.OptionRead, "file"), options.Longs(types.OptionWrite, "output")),
}

func gitOptions(sub string) types.OptionSpec {
	spec := gitOptionSpec
	spec.ShortArgs = map[byte]types.OptionRole{}
	for key, value := range gitOptionSpec.ShortArgs {
		spec.ShortArgs[key] = value
	}
	if sub == "config" || sub == "grep" {
		spec.ShortArgs['f'] = types.OptionRead
	} else {
		spec.ShortFlags += "f"
	}
	if sub == "tag" || sub == "commit" {
		spec.ShortArgs['F'] = types.OptionRead
	} else if sub == "grep" {
		spec.ShortFlags += "F"
	}
	return spec
}
