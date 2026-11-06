package commands

import (
	"strings"

	"agent-platform/internal/shellreview/types"
)

// diskutilReads lists the verbs that only report. Every other verb can mount,
// erase, partition or repair, so it stays opaque.
var diskutilReads = map[string][]string{
	"list": nil, "info": nil, "information": nil, "listFilesystems": nil,
	"apfs":        {"list", "listSnapshots", "listVolumeGroups", "listCryptoUsers"},
	"cs":          {"list"},
	"coreStorage": {"list"},
	"appleRAID":   {"list"},
	"ar":          {"list"},
}

// diskutilOperands reads a device node or mount point named with a path
// separator; device identifiers such as disk3s5 and list filters are data.
func diskutilOperands(args []string) effectDraft {
	if len(args) == 0 {
		return effectDraft{} // prints usage
	}
	subverbs, ok := diskutilReads[args[0]]
	next := 1
	if ok && subverbs != nil {
		ok = len(args) > 1
		if ok {
			ok = false
			for _, verb := range subverbs {
				ok = ok || args[1] == verb
			}
		}
		next = 2
	}
	if !ok {
		return unknownOperands(args)
	}
	var e effectDraft
	for i := next; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-plist" || a == "-all":
		case strings.HasPrefix(a, "-"):
			return unknownOperands(args)
		case strings.Contains(a, "/"):
			e.Files = append(e.Files, types.FileAccess{Path: a, Ref: types.ArgumentRef(i, a), Kind: types.Read})
		}
	}
	return e
}
