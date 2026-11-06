package commands

import (
	"slices"
	"strings"

	"agent-platform/internal/shellreview/registry"
)

type command struct {
	analyze func([]string) effectDraft
	config  func([]string) []string
}

var analyzers = map[registry.ID]command{
	registry.CmdEcho:      {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdEcho, args) }},
	registry.CmdPrintf:    {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdPrintf, args) }},
	registry.CmdTrue:      {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdTrue, args) }},
	registry.CmdFalse:     {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdFalse, args) }},
	registry.CmdTest:      {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdTest, args) }},
	registry.CmdCd:        {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdCd, args) }},
	registry.CmdGit:       {analyze: gitOperands},
	registry.CmdSed:       {analyze: sedOperands},
	registry.CmdAwk:       {analyze: awkOperands},
	registry.CmdGawk:      {analyze: awkOperands},
	registry.CmdMawk:      {analyze: awkOperands},
	registry.CmdNawk:      {analyze: awkOperands},
	registry.CmdFind:      {analyze: findOperands},
	registry.CmdJq:        {analyze: jqOperands},
	registry.CmdPdftotext: {analyze: pdftotextOperands},
	registry.CmdLsof:      {analyze: lsofOperands},
	registry.CmdTar:       {analyze: tarOperands, config: tarConfig},
	registry.CmdBsdtar:    {analyze: tarOperands},
	registry.CmdGtar:      {analyze: tarOperands, config: tarConfig},
	registry.CmdChmod:     {analyze: chmodOperands},
	registry.CmdCp:        {analyze: cpOperands},
	registry.CmdCurl:      {analyze: curlOperands, config: curlConfig},
	registry.CmdDu:        {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdDu, duSpec, args) }},
	registry.CmdGrep:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdGrep, grepSpec, args) }},
	registry.CmdLn:        {analyze: lnOperands},
	registry.CmdLs:        {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdLs, lsSpec, args) }},
	registry.CmdMv:        {analyze: mvOperands},
	registry.CmdRg:        {analyze: rgOperands, config: rgConfig},
	registry.CmdUniq:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdUniq, uniqSpec, args) }},
	registry.CmdWget:      {analyze: wgetOperands, config: wgetConfig},
}

// programs are Go analyzers for names outside the registry. Like commands.yml
// names, identity trusts them only as the system installation on PATH.
var programs = map[string]command{
	"diskutil": {analyze: diskutilOperands},
	"sips":     {analyze: sipsOperands},
	"sysctl":   {analyze: sysctlOperands},
}

// Programs lists analyzed names absent from the registry, from commands.yml
// or Go. Identity captures them as ordinary system programs.
func Programs() []string {
	var names []string
	for name := range specCommands {
		if IsProgram(name) {
			names = append(names, name)
		}
	}
	for name := range programs {
		if IsProgram(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func IsProgram(name string) bool {
	_, table := specCommands[name]
	_, program := programs[name]
	_, registered := registry.Lookup(name)
	return (table || program) && !registered
}

func operands(base string, args []string) effectDraft {
	if base != strings.ToLower(base) {
		return unknownOperands(args)
	}
	if entry, ok := registry.Lookup(base); ok {
		if c, ok := analyzers[entry.ID]; ok {
			return c.analyze(args)
		}
	}
	if c, ok := programs[base]; ok {
		return c.analyze(args)
	}
	if c, ok := specCommands[base]; ok {
		return c.analyze(args)
	}
	return unknownOperands(args)
}
func ConfigVariables(base string, args []string) []string {
	if base != strings.ToLower(base) {
		return nil
	}
	entry, ok := registry.Lookup(base)
	if !ok {
		return nil
	}
	if c, ok := analyzers[entry.ID]; ok && c.config != nil {
		return c.config(args)
	}
	return nil
}
