package shellanalysis

import "strings"

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

func gitOperands(args []string) Effects {
	var e Effects
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
			e.Files = append(e.Files, FileAccess{Path: args[i]})
			i++
		case strings.HasPrefix(a, "-C") && len(a) > 2:
			e.Files = append(e.Files, FileAccess{Path: a[2:]})
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
	sub := args[i]
	rest := args[i+1:]
	flags := map[string]bool{}
	var positional, afterDashes []string
	dashes := false
	for _, a := range rest {
		if dashes {
			afterDashes = append(afterDashes, a)
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
	}
	for _, p := range afterDashes {
		e.Files = append(e.Files, FileAccess{Path: p})
	}
	switch {
	case gitReadCommands[sub] || isGitListing(sub, flags, positional):
		if flags["--ext-diff"] || flags["--show-signature"] || flags["-O"] || flags["--open-files-in-pager"] {
			e.ExecutesCode = true
		}
		if sub == "diff" && flags["--no-index"] {
			for _, p := range positional {
				e.Files = append(e.Files, FileAccess{Path: p})
			}
		}
		e.GitCheck = GitCheckRead
	case gitWriteCommands[sub]:
		e.RepoWrite = true
		e.GitCheck = GitCheckWrite
		gitWriteEffects(&e, sub, flags, positional, afterDashes)
	case gitNetworkCommands[sub]:
		e.SSHAgent = true
		e.GitCheck = GitCheckNetwork
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
				e.Files = append(e.Files, FileAccess{Path: positional[len(positional)-1], Write: true, Recursive: true})
			} else {
				// The destination is derived from the URL inside cwd.
				e.Files = append(e.Files, FileAccess{Path: ".", Write: true})
			}
		}
	default:
		// Aliases, external subcommands and tools such as bisect run or difftool.
		e.ExecutesCode = true
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

func gitWriteEffects(e *Effects, sub string, flags map[string]bool, positional, afterDashes []string) {
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
		for _, p := range positional {
			e.Files = append(e.Files, FileAccess{Path: p, Write: true})
		}
	case "rm":
		for _, p := range append(positional, afterDashes...) {
			e.Files = append(e.Files, FileAccess{Path: p, Write: true, Recursive: flags["-r"]})
		}
	case "init":
		if len(positional) > 0 {
			e.Files = append(e.Files, FileAccess{Path: positional[0], Write: true})
		}
	case "worktree":
		if len(positional) > 1 && positional[0] == "add" {
			e.Files = append(e.Files, FileAccess{Path: positional[len(positional)-1], Write: true, Recursive: true})
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
		for _, p := range positional {
			e.Files = append(e.Files, FileAccess{Path: p})
		}
	}
}

// ExecutableGitKey reports configuration keys under which Git runs a program
// implicitly for the commands modeled as non-executing.
func ExecutableGitKey(key string) bool { return isExecutableGitKey(key) }

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
