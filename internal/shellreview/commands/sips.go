package commands

import (
	"strings"

	"agent-platform/internal/shellreview/types"
)

type sipsValue int

const (
	sipsData sipsValue = iota
	sipsRead
	sipsWrite
)

type sipsOption struct {
	modifies bool
	values   []sipsValue
}

// sipsOptions follows `sips --help`. The JavaScript option (-j/--js) and
// --man are absent, so they stay opaque like any unlisted option.
var sipsOptions = func() map[string]sipsOption {
	out := map[string]sipsOption{}
	add := func(o sipsOption, names ...string) {
		for _, name := range names {
			out[name] = o
		}
	}
	data, two := []sipsValue{sipsData}, []sipsValue{sipsData, sipsData}
	add(sipsOption{}, "-1", "--oneLine", "--verify", "--debug", "-h", "--help", "-H", "--helpProperties", "-v", "--version", "--formats")
	add(sipsOption{values: data}, "-g", "--getProperty")
	add(sipsOption{values: []sipsValue{sipsWrite}}, "-x", "--extractProfile", "-o", "--out")
	add(sipsOption{values: []sipsValue{sipsData, sipsWrite}}, "-X", "--extractTag")
	add(sipsOption{modifies: true}, "--repair", "--deleteColorManagementProperties", "-i", "--addIcon", "--optimizeColorForSharing")
	add(sipsOption{modifies: true, values: data}, "-d", "--deleteProperty", "--deleteTag", "-r", "--rotate", "-f", "--flip", "--padColor", "--resampleWidth", "--resampleHeight", "-Z", "--resampleHeightWidthMax")
	add(sipsOption{modifies: true, values: two}, "-s", "--setProperty", "--copyTag", "-c", "--cropToHeightWidth", "--cropOffset", "-p", "--padToHeightWidth", "-z", "--resampleHeightWidth")
	add(sipsOption{modifies: true, values: []sipsValue{sipsRead}}, "-e", "--embedProfile", "-E", "--embedProfileIfNone", "-m", "--matchTo")
	add(sipsOption{modifies: true, values: []sipsValue{sipsRead, sipsData}}, "-M", "--matchToWithIntent")
	add(sipsOption{modifies: true, values: []sipsValue{sipsData, sipsRead}}, "--loadTag")
	return out
}()

// sipsOperands separates queries from modifications. A modification rewrites
// the operands in place unless --out names a file, or a directory that then
// receives every result under a possibly different extension.
func sipsOperands(args []string) effectDraft {
	var e effectDraft
	modifies, out := false, false
	var operands []int
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-" || !strings.HasPrefix(a, "-") {
			operands = append(operands, i)
			continue
		}
		option, ok := sipsOptions[a]
		if !ok || i+len(option.values) >= len(args) {
			return unknownOperands(args)
		}
		modifies = modifies || option.modifies
		out = out || a == "-o" || a == "--out"
		for _, value := range option.values {
			i++
			switch value {
			case sipsRead:
				e.Files = append(e.Files, types.FileAccess{Path: args[i], Ref: types.ArgumentRef(i, args[i]), Kind: types.Read})
			case sipsWrite:
				e.Files = append(e.Files, types.FileAccess{Path: args[i], Write: true, Recursive: a == "-o" || a == "--out", Ref: types.ArgumentRef(i, args[i]), Kind: types.Output})
			}
		}
	}
	inPlace := modifies && !out
	for _, i := range operands {
		kind := types.Read
		if inPlace {
			kind = types.Output
		}
		e.Files = append(e.Files, types.FileAccess{Path: args[i], Write: inPlace, Ref: types.ArgumentRef(i, args[i]), Kind: kind})
	}
	return e
}
