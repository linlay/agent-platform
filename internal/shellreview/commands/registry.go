package commands

import (
	"agent-platform/internal/shellreview/registry"
	"strings"
)

type command struct {
	analyze func([]string) effectDraft
	config  func([]string) []string
}

var analyzers = map[registry.ID]command{
	registry.CmdEcho:      {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdEcho, args) }},
	registry.CmdPrintf:    {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdPrintf, args) }},
	registry.CmdPwd:       {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdPwd, args) }},
	registry.CmdTrue:      {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdTrue, args) }},
	registry.CmdFalse:     {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdFalse, args) }},
	registry.CmdDate:      {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdDate, args) }},
	registry.CmdSleep:     {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdSleep, args) }},
	registry.CmdUname:     {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdUname, args) }},
	registry.CmdWhoami:    {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdWhoami, args) }},
	registry.CmdId:        {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdId, args) }},
	registry.CmdBasename:  {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdBasename, args) }},
	registry.CmdDirname:   {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdDirname, args) }},
	registry.CmdWhich:     {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdWhich, args) }},
	registry.CmdPrintenv:  {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdPrintenv, args) }},
	registry.CmdTest:      {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdTest, args) }},
	registry.CmdTr:        {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdTr, args) }},
	registry.CmdCd:        {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdCd, args) }},
	registry.CmdTop:       {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdTop, args) }},
	registry.CmdFree:      {analyze: func(args []string) effectDraft { return simpleOperands(registry.CmdFree, args) }},
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
	registry.CmdCat:       {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdCat, catSpec, args) }},
	registry.CmdChmod:     {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdChmod, chmodSpec, args) }},
	registry.CmdCp:        {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdCp, cpSpec, args) }},
	registry.CmdCurl:      {analyze: curlOperands, config: curlConfig},
	registry.CmdCut:       {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdCut, cutSpec, args) }},
	registry.CmdDiff:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdDiff, diffSpec, args) }},
	registry.CmdDu:        {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdDu, duSpec, args) }},
	registry.CmdFile:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdFile, fileSpec, args) }},
	registry.CmdGrep:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdGrep, grepSpec, args) }},
	registry.CmdHead:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdHead, headSpec, args) }},
	registry.CmdLn:        {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdLn, lnSpec, args) }},
	registry.CmdLs:        {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdLs, lsSpec, args) }},
	registry.CmdMkdir:     {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdMkdir, mkdirSpec, args) }},
	registry.CmdMv:        {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdMv, mvSpec, args) }},
	registry.CmdReadlink:  {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdReadlink, readlinkSpec, args) }},
	registry.CmdRealpath:  {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdRealpath, realpathSpec, args) }},
	registry.CmdRg:        {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdRg, rgSpec, args) }, config: rgConfig},
	registry.CmdRm:        {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdRm, rmSpec, args) }},
	registry.CmdRmdir:     {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdRmdir, rmdirSpec, args) }},
	registry.CmdSort:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdSort, sortSpec, args) }},
	registry.CmdStat:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdStat, statSpec, args) }},
	registry.CmdTail:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdTail, tailSpec, args) }},
	registry.CmdTee:       {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdTee, teeSpec, args) }},
	registry.CmdTouch:     {analyze: func(args []string) effectDraft { return fileOperationOperands(registry.CmdTouch, touchSpec, args) }},
	registry.CmdUniq:      {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdUniq, uniqSpec, args) }},
	registry.CmdWc:        {analyze: func(args []string) effectDraft { return fileOperands(registry.CmdWc, wcSpec, args) }},
	registry.CmdWget:      {analyze: wgetOperands, config: wgetConfig},
}

func operands(base string, args []string) effectDraft {
	if base != strings.ToLower(base) {
		return unknownOperands(args)
	}
	entry, ok := registry.Lookup(base)
	if ok {
		if c, ok := analyzers[entry.ID]; ok {
			return c.analyze(args)
		}
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
