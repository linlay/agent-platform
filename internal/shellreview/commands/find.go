package commands

import (
	"strings"

	"agent-platform/internal/shellreview/types"
)

func findOperands(args []string) types.Effects {
	var e types.Effects
	i := 0
	for i < len(args) && (args[i] == "-L" || args[i] == "-H" || args[i] == "-P") {
		i++
	}
	for i < len(args) && !strings.HasPrefix(args[i], "-") && args[i] != "!" && args[i] != "(" {
		e.Files = append(e.Files, types.FileAccess{Path: args[i], Recursive: true})
		i++
	}
	if len(e.Files) == 0 {
		e.Files = append(e.Files, types.FileAccess{Path: ".", Recursive: true})
	}
	roots := len(e.Files)
	for ; i < len(args); i++ {
		switch args[i] {
		case "-name", "-iname", "-path", "-ipath", "-wholename", "-iwholename", "-lname", "-ilname", "-regex", "-iregex", "-regextype", "-type", "-xtype", "-maxdepth", "-mindepth", "-size", "-mtime", "-mmin", "-atime", "-amin", "-ctime", "-cmin", "-perm", "-user", "-group", "-uid", "-gid", "-links", "-inum", "-printf", "-used":
			i++
			if i >= len(args) {
				e.Incomplete = true
			}
		case "-newer", "-anewer", "-cnewer", "-samefile":
			i++
			if i < len(args) {
				e.Files = append(e.Files, types.FileAccess{Path: args[i]})
			} else {
				e.Incomplete = true
			}
		case "-fprint", "-fprint0", "-fls":
			i++
			if i < len(args) {
				e.Files = append(e.Files, types.FileAccess{Path: args[i], Write: true})
			} else {
				e.Incomplete = true
			}
		case "-fprintf":
			i += 2
			if i-1 < len(args) {
				e.Files = append(e.Files, types.FileAccess{Path: args[i-1], Write: true})
			} else {
				e.Incomplete = true
			}
		case "-print", "-print0", "-ls", "-empty", "-a", "-o", "-and", "-or", "!", "-not", "(", ")", "-prune", "-true", "-false", "-depth", "-d", "-follow", "-xdev", "-mount", "-readable", "-writable", "-executable", "-nouser", "-nogroup", "-quit", "-noleaf", "-daystart":
		case "-delete":
			for j := 0; j < roots; j++ {
				e.Files[j].Write = true
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
