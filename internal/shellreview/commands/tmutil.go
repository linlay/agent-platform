package commands

import (
	"strings"

	"agent-platform/internal/shellreview/types"
)

// tmutilOperands accepts the local snapshot listings. Their only operand is a
// mount point, read as a single path; nothing beneath it is walked. Every
// other verb can start, delete, restore or reconfigure backups, so it stays
// opaque.
func tmutilOperands(args []string) effectDraft {
	if len(args) == 0 {
		return unknownOperands(args)
	}
	operands := args[1:]
	switch args[0] {
	case "listlocalsnapshots":
		if len(operands) != 1 {
			return unknownOperands(args)
		}
	case "listlocalsnapshotdates":
		if len(operands) > 1 {
			return unknownOperands(args)
		}
	default:
		return unknownOperands(args)
	}
	var e effectDraft
	for i, a := range operands {
		if strings.HasPrefix(a, "-") {
			return unknownOperands(args)
		}
		e.Files = append(e.Files, types.FileAccess{Path: a, Ref: types.ArgumentRef(i+1, a), Kind: types.Read})
	}
	return e
}
