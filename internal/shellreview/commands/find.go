package commands

import (
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
	for i < len(args) && (args[i] == "-L" || args[i] == "-H" || args[i] == "-P") {
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
	}
	return e
}
