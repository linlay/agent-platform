package commands

import (
	"strconv"
	"strings"

	"agent-platform/internal/shellreview/types"
)

// findDataPrimaries take one argument that is data, not a file.
var findDataPrimaries = map[string]bool{
	"-name": true, "-iname": true, "-path": true, "-ipath": true, "-wholename": true, "-iwholename": true, "-lname": true, "-ilname": true,
	"-regex": true, "-iregex": true, "-regextype": true, "-type": true, "-xtype": true, "-maxdepth": true, "-mindepth": true, "-size": true,
	"-mtime": true, "-mmin": true, "-atime": true, "-amin": true, "-ctime": true, "-cmin": true, "-perm": true, "-user": true, "-group": true,
	"-uid": true, "-gid": true, "-links": true, "-inum": true, "-printf": true, "-used": true,
}

// findToken undoes the shell escape the parser keeps on find punctuation:
// \; \( \) and \! are the usual way to type them.
func findToken(arg string) string {
	switch arg {
	case `\;`, `\(`, `\)`, `\!`:
		return arg[1:]
	}
	return arg
}

// FindExec splits find arguments into its search roots, the commands run by
// -exec (up to ";" or "{} +") and the indices of the remaining arguments. ok
// is false for -execdir, -ok, -okdir or an unterminated -exec, which keep the
// whole call opaque.
func FindExec(args []string) (roots []string, execs [][]string, keep []int, ok bool) {
	i := 0
	for i < len(args) && (args[i] == "-L" || args[i] == "-H" || args[i] == "-P") {
		keep = append(keep, i)
		i++
	}
	for i < len(args) && !strings.HasPrefix(args[i], "-") && findToken(args[i]) != "!" && findToken(args[i]) != "(" {
		roots = append(roots, args[i])
		keep = append(keep, i)
		i++
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}
	for ; i < len(args); i++ {
		values := 0
		switch a := args[i]; {
		case findDataPrimaries[a], a == "-newer", a == "-anewer", a == "-cnewer", a == "-samefile", a == "-fprint", a == "-fprint0", a == "-fls":
			values = 1
		case a == "-fprintf":
			values = 2
		case a == "-execdir", a == "-ok", a == "-okdir":
			return nil, nil, nil, false
		case a == "-exec":
			end := i + 1
			for end < len(args) && findToken(args[end]) != ";" && !(args[end] == "+" && args[end-1] == "{}") {
				end++
			}
			if end >= len(args) || end == i+1 {
				return nil, nil, nil, false
			}
			execs = append(execs, args[i+1:end])
			i = end
			continue
		}
		for j := i; j <= i+values && j < len(args); j++ {
			keep = append(keep, j)
		}
		i += values
	}
	return roots, execs, keep, true
}

func findOperands(args []string) effectDraft {
	var e effectDraft
	i := 0
	follow := false
	for i < len(args) && (args[i] == "-L" || args[i] == "-H" || args[i] == "-P") {
		follow = follow || args[i] == "-L"
		i++
	}
	for i < len(args) && !strings.HasPrefix(args[i], "-") && findToken(args[i]) != "!" && findToken(args[i]) != "(" {
		e.Files = append(e.Files, types.FileAccess{Path: args[i], Recursive: true, Ref: types.ArgumentRef(i, args[i])})
		i++
	}
	if len(e.Files) == 0 {
		e.Files = append(e.Files, types.FileAccess{Path: ".", Recursive: true})
	}
	roots := len(e.Files)
	walk := findWalk(args, i, follow)
	for ; i < len(args); i++ {
		if findDataPrimaries[args[i]] {
			if i++; i >= len(args) {
				e.Incomplete = true
			}
			continue
		}
		switch findToken(args[i]) {
		case "-newer", "-anewer", "-cnewer", "-samefile":
			i++
			if i < len(args) {
				e.Files = append(e.Files, types.FileAccess{Path: args[i], Ref: types.ArgumentRef(i, args[i])})
			} else {
				e.Incomplete = true
			}
		case "-fprint", "-fprint0", "-fls":
			i++
			if i < len(args) {
				e.Files = append(e.Files, types.FileAccess{Path: args[i], Write: true, Kind: types.Output, Ref: types.ArgumentRef(i, args[i])})
			} else {
				e.Incomplete = true
			}
		case "-fprintf":
			i += 2
			if i-1 < len(args) {
				e.Files = append(e.Files, types.FileAccess{Path: args[i-1], Write: true, Kind: types.Output, Ref: types.ArgumentRef(i-1, args[i-1])})
			} else {
				e.Incomplete = true
			}
		case "-print", "-print0", "-ls", "-empty", "-a", "-o", "-and", "-or", "!", "-not", "(", ")", "-prune", "-true", "-false", "-depth", "-d", "-follow", "-xdev", "-mount", "-readable", "-writable", "-executable", "-nouser", "-nogroup", "-quit", "-noleaf", "-daystart":
		case "-delete":
			for j := 0; j < roots; j++ {
				e.Files[j].Write = true
				e.Files[j].Kind = types.Delete
			}
			e.Destructive = true
		default:
			// -exec/-execdir/-ok run programs over runtime-selected paths.
			e.ExecutesCode = true
			e.Incomplete = true
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	} else if walk != nil {
		// Programs run by the walk could reach a skipped directory from an
		// ancestor, so only complete walks without them keep the bound.
		for j := 0; j < roots; j++ {
			e.Files[j].Walk = walk
		}
	}
	return e
}

// findWalk bounds the walk below find's roots by -maxdepth and by a leading
// `<tests> -prune -o` clause over -name, -path and -type d. Following
// symlinks reaches directories by other paths and depths, -depth and -delete
// disable -prune, and -mindepth skips the tests near the roots.
func findWalk(args []string, start int, follow bool) *types.Walk {
	w := &types.Walk{MaxDepth: -1}
	maxdepths := 0
	prune := !follow
	for i := start; i < len(args); i++ {
		switch a := findToken(args[i]); a {
		case "-maxdepth":
			maxdepths++
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n >= 0 {
					w.MaxDepth = n
				}
			}
		case "-follow":
			return nil
		case "-mindepth", "-depth", "-d", "-delete":
			prune = false
		}
		i += findValueCount(args[i])
	}
	if follow || maxdepths != 1 {
		w.MaxDepth = -1
	}
	if prune {
		w.Prune = findPruneClause(args[start:], start)
	}
	if w.MaxDepth < 0 && w.Prune == nil {
		return nil
	}
	return w
}

func findValueCount(arg string) int {
	switch {
	case findDataPrimaries[arg], arg == "-newer", arg == "-anewer", arg == "-cnewer", arg == "-samefile", arg == "-fprint", arg == "-fprint0", arg == "-fls":
		return 1
	case arg == "-fprintf":
		return 2
	}
	return 0
}

// findPruneClause returns the prune clause as alternatives of tests, or nil
// unless the expression starts with tests joined by -a and parenthesized -o
// groups, ending in -prune and followed by -o or nothing. offset locates expr
// in the analyzer's arguments.
func findPruneClause(expr []string, offset int) [][]types.WalkTest {
	alts := [][]types.WalkTest{nil}
	for i := 0; i < len(expr); i++ {
		switch a := findToken(expr[i]); a {
		case "-a", "-and", "-xdev", "-mount", "-noleaf", "-daystart":
		case "-maxdepth":
			i++
		case "-prune":
			if i+1 == len(expr) || findToken(expr[i+1]) == "-o" || findToken(expr[i+1]) == "-or" {
				return alts
			}
			return nil
		case "(":
			var group [][]types.WalkTest
			current := []types.WalkTest{}
			for i++; i < len(expr) && findToken(expr[i]) != ")"; i++ {
				switch findToken(expr[i]) {
				case "-a", "-and":
				case "-o", "-or":
					group, current = append(group, current), []types.WalkTest{}
				default:
					test, ok := findPruneTest(expr, i, offset)
					if !ok {
						return nil
					}
					current = append(current, test)
					i++
				}
			}
			if i >= len(expr) || len(current) == 0 {
				return nil
			}
			group = append(group, current)
			var next [][]types.WalkTest
			for _, alt := range alts {
				for _, tests := range group {
					next = append(next, append(append([]types.WalkTest(nil), alt...), tests...))
				}
			}
			if alts = next; len(alts) > 16 {
				return nil
			}
		default:
			test, ok := findPruneTest(expr, i, offset)
			if !ok {
				return nil
			}
			for j := range alts {
				alts[j] = append(alts[j], test)
			}
			i++
		}
	}
	return nil
}

func findPruneTest(expr []string, i, offset int) (types.WalkTest, bool) {
	if i+1 >= len(expr) {
		return types.WalkTest{}, false
	}
	switch a, value := findToken(expr[i]), expr[i+1]; a {
	case "-name", "-path", "-wholename":
		if a == "-wholename" {
			a = "-path"
		}
		return types.WalkTest{Primary: a, Value: value, Ref: types.ArgumentRef(offset+i+1, value)}, true
	case "-type":
		return types.WalkTest{Primary: a, Value: value}, value == "d"
	}
	return types.WalkTest{}, false
}
