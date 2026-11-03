package commands

import (
	_ "embed"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

//go:embed commands.yml
var commandsYAML []byte

// The file is embedded, so a malformed entry fails every test run rather than
// reaching production.
var specCommands = mustSpecCommands(commandsYAML)

type specCommand struct {
	operands string
	spec     types.OptionSpec
	// Short flags with the recursive role; long ones use OptionRecursive.
	recursiveShort string
	examples       []specExample
}

type specExample struct {
	args   []string
	expect map[string][]string
	exec   bool
}

var operandKinds = map[string]types.OpKind{"read": types.Read, "create": types.Create, "output": types.Output, "delete": types.Delete}

var specRoles = map[string]types.OptionRole{
	"flag": types.OptionNone, "value": types.OptionValue, "optional": types.OptionOptionalValue,
	"read": types.OptionRead, "write": types.OptionWrite, "read-dir": types.OptionReadDir, "write-dir": types.OptionWriteDir,
	"exec": types.OptionExec, "recursive": types.OptionRecursive,
}

func mustSpecCommands(data []byte) map[string]specCommand {
	parsed, err := parseSpecCommands(data)
	if err != nil {
		panic("commands.yml: " + err.Error())
	}
	for name := range parsed {
		if !programName.MatchString(name) {
			panic(fmt.Sprintf("commands.yml: %q is not a lowercase program name", name))
		}
		entry, registered := registry.Lookup(name)
		if !registered {
			continue
		}
		// Builtins such as pwd qualify; wrappers, interpreters and build tools
		// carry identity rules a table cannot express.
		if entry.Flags&^(registry.Builtin|registry.Capture) != registry.Arguments {
			panic(fmt.Sprintf("commands.yml: %s is not an ordinary command", name))
		}
		if _, ok := analyzers[entry.ID]; ok {
			panic(fmt.Sprintf("commands.yml: %s also has a Go analyzer", name))
		}
	}
	return parsed
}

var programName = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]*$`)

// TablePrograms lists commands.yml names absent from the registry. Identity
// treats them as ordinary programs, still trusted only as system installations.
func TablePrograms() []string {
	var names []string
	for name := range specCommands {
		if IsTableProgram(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func IsTableProgram(name string) bool {
	_, table := specCommands[name]
	_, registered := registry.Lookup(name)
	return table && !registered
}

func parseSpecCommands(data []byte) (map[string]specCommand, error) {
	tree, err := config.LoadYAMLTreeBytesWithOptions(data, config.YAMLTreeOptions{RejectDuplicateKeys: true, DisableEnvInterpolation: true})
	if err != nil {
		return nil, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("root must map command names")
	}
	result := make(map[string]specCommand, len(root))
	for name, raw := range root {
		c, err := parseSpecCommand(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		result[name] = c
	}
	return result, nil
}

func parseSpecCommand(raw any) (specCommand, error) {
	fields, ok := raw.(map[string]any)
	if !ok {
		return specCommand{}, fmt.Errorf("must be a map")
	}
	if _, ok := fields["operands"]; !ok {
		return specCommand{}, fmt.Errorf("operands is required")
	}
	c := specCommand{spec: types.OptionSpec{ShortArgs: map[byte]types.OptionRole{}, Long: map[string]types.OptionRole{}}}
	for key, value := range fields {
		var err error
		switch key {
		case "operands":
			c.operands, _ = value.(string)
			if _, ok := operandKinds[c.operands]; !ok && c.operands != "none" {
				err = fmt.Errorf("operands must be none, read, create, output or delete")
			}
		case "short":
			err = c.parseOptions(value, true)
		case "long":
			err = c.parseOptions(value, false)
		case "numeric":
			if c.spec.NumericShort, ok = value.(bool); !ok {
				err = fmt.Errorf("numeric must be true or false")
			}
		case "examples":
			c.examples, err = parseSpecExamples(value)
		default:
			err = fmt.Errorf("unknown field %q", key)
		}
		if err != nil {
			return specCommand{}, err
		}
	}
	recursiveLong := false
	for _, role := range c.spec.Long {
		recursiveLong = recursiveLong || role == types.OptionRecursive
	}
	if c.operands == "none" && (recursiveLong || c.recursiveShort != "") {
		return specCommand{}, fmt.Errorf("recursive options need file operands")
	}
	return c, nil
}

func (c *specCommand) parseOptions(raw any, short bool) error {
	groups, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("options must be grouped by role")
	}
	for roleName, rawNames := range groups {
		role, ok := specRoles[roleName]
		if !ok {
			return fmt.Errorf("unknown role %q", roleName)
		}
		names, err := stringList(rawNames)
		if err != nil {
			return fmt.Errorf("%s: %w", roleName, err)
		}
		for _, name := range names {
			if !short {
				if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "= \t") {
					return fmt.Errorf("long option %q must be a bare name", name)
				}
				if _, dup := c.spec.Long[name]; dup {
					return fmt.Errorf("long option %q listed twice", name)
				}
				c.spec.Long[name] = role
				continue
			}
			if len(name) != 1 || !isOptionLetter(name[0]) {
				return fmt.Errorf("short option %q must be one letter or digit", name)
			}
			letter := name[0]
			if _, dup := c.spec.ShortArgs[letter]; dup || strings.IndexByte(c.spec.ShortFlags, letter) >= 0 {
				return fmt.Errorf("short option %q listed twice", name)
			}
			switch role {
			case types.OptionNone:
				c.spec.ShortFlags += name
			case types.OptionRecursive:
				c.spec.ShortFlags += name
				c.recursiveShort += name
			default:
				c.spec.ShortArgs[letter] = role
			}
		}
	}
	return nil
}

func isOptionLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func parseSpecExamples(raw any) ([]specExample, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("examples must be a list")
	}
	result := make([]specExample, 0, len(items))
	for i, item := range items {
		fields, ok := item.(map[string]any)
		_, hasArgs := fields["args"]
		_, hasExpect := fields["expect"]
		if !ok || !hasArgs || !hasExpect {
			return nil, fmt.Errorf("example %d needs args and expect", i+1)
		}
		ex := specExample{expect: map[string][]string{}}
		for key, value := range fields {
			var err error
			switch key {
			case "args":
				ex.args, err = stringList(value)
			case "expect":
				err = ex.parseExpect(value)
			default:
				err = fmt.Errorf("unknown field %q", key)
			}
			if err != nil {
				return nil, fmt.Errorf("example %d: %w", i+1, err)
			}
		}
		result = append(result, ex)
	}
	return result, nil
}

func (ex *specExample) parseExpect(raw any) error {
	fields, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("expect must be a map")
	}
	for key, value := range fields {
		if key == "exec" {
			if ex.exec, ok = value.(bool); !ok {
				return fmt.Errorf("exec must be true or false")
			}
			continue
		}
		if _, ok := operandKinds[key]; !ok && key != "recursive" {
			return fmt.Errorf("unknown effect %q", key)
		}
		paths, err := stringList(value)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		ex.expect[key] = paths
	}
	return nil
}

func stringList(raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list")
	}
	result := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%v is not a string; quote numbers, true, false and null", item)
		}
		result[i] = s
	}
	return result, nil
}

func (c specCommand) analyze(args []string) effectDraft {
	p := options.Parse(c.spec, args)
	e := effectDraft{Files: p.Files, Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	if kind, files := operandKinds[c.operands]; files {
		recursive := p.Recursive
		for i := range len(c.recursiveShort) {
			recursive = recursive || p.Has(c.recursiveShort[i:i+1])
		}
		for i, path := range p.Positional {
			if path != "" && path != "-" {
				e.Files = append(e.Files, types.FileAccess{Path: path, Write: kind != types.Read, Recursive: recursive, Ref: p.PositionalRefs[i], Kind: kind})
			}
		}
		e.Destructive = kind == types.Delete && recursive
	} else if e.Incomplete {
		// Operands are data; only an unrecognized option can make them paths.
		for i, path := range p.Positional {
			if path != "-" && !strings.Contains(path, "://") {
				e.Files = append(e.Files, types.FileAccess{Path: path, Ref: p.PositionalRefs[i]})
			}
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
