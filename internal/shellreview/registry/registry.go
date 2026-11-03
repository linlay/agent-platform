// Package registry owns immutable shared command metadata. It contains no
// analyzers or approval decisions, so syntax and wrapper layers can read it.
package registry

import "strings"

type ID uint16
type Flag uint16

const (
	Arguments Flag = 1 << iota
	Builtin
	Wrapper
	Interpreter
	BuildTool
	ShellInterpreter
	PlatformBuiltin
	Capture
	HardBlocked
	StrictUnsupported
	ShellWrapper
)

const (
	Unknown ID = iota
	CmdDot
	CmdColon
	CmdAlias
	CmdAwk
	CmdBasename
	CmdBash
	CmdBg
	CmdBreak
	CmdBsdtar
	CmdBuiltin
	CmdCat
	CmdCd
	CmdChmod
	CmdCmd
	CmdCommand
	CmdCompgen
	CmdComplete
	CmdCompopt
	CmdContinue
	CmdCoproc
	CmdCp
	CmdCurl
	CmdCut
	CmdDash
	CmdDate
	CmdDbx
	CmdDeclare
	CmdDf
	CmdDiff
	CmdDirname
	CmdDu
	CmdEcho
	CmdEmulate
	CmdEnable
	CmdEnv
	CmdEval
	CmdExec
	CmdExport
	CmdFalse
	CmdFg
	CmdFc
	CmdFile
	CmdFind
	CmdFree
	CmdGawk
	CmdGetChildItem
	CmdGetContent
	CmdGit
	CmdGo
	CmdGrep
	CmdGtar
	CmdHash
	CmdHead
	CmdHttpx
	CmdId
	CmdJobs
	CmdJq
	CmdKsh
	CmdLn
	CmdLocal
	CmdLs
	CmdLsof
	CmdMake
	CmdMapfile
	CmdMawk
	CmdMkdir
	CmdMv
	CmdNawk
	CmdNice
	CmdNode
	CmdNohup
	CmdNpm
	CmdNpx
	CmdPdftotext
	CmdPerl
	CmdPip
	CmdPnpm
	CmdPowershell
	CmdPrintenv
	CmdPrintf
	CmdPwd
	CmdPwsh
	CmdPython
	CmdPython3
	CmdRead
	CmdReadarray
	CmdReadlink
	CmdRealpath
	CmdRemoveItem
	CmdReturn
	CmdRg
	CmdRm
	CmdRmdir
	CmdRuby
	CmdSed
	CmdSet
	CmdSh
	CmdShift
	CmdShopt
	CmdSleep
	CmdSort
	CmdSource
	CmdStat
	CmdStdbuf
	CmdSysopen
	CmdSysread
	CmdSysseek
	CmdSyswrite
	CmdTail
	CmdTar
	CmdTee
	CmdTest
	CmdTimeout
	CmdTop
	CmdTouch
	CmdTr
	CmdTrap
	CmdTrue
	CmdType
	CmdTypeset
	CmdUnalias
	CmdUname
	CmdUniq
	CmdUnset
	CmdWc
	CmdWget
	CmdWhich
	CmdWhoami
	CmdXargs
	CmdYarn
	CmdZfChgrp
	CmdZfChmod
	CmdZfChown
	CmdZfLn
	CmdZfMkdir
	CmdZfMv
	CmdZfRm
	CmdZfRmdir
	CmdZmodload
	CmdZpty
	CmdZsh
	CmdZsocket
	CmdZtcp
)

type Entry struct {
	ID    ID
	Name  string
	Flags Flag
}

var entries = [...]Entry{
	{CmdDot, ".", HardBlocked | StrictUnsupported},
	{CmdColon, ":", Builtin},
	{CmdAlias, "alias", HardBlocked},
	{CmdAwk, "awk", Arguments | Capture},
	{CmdBasename, "basename", Arguments | Capture},
	{CmdBash, "bash", Interpreter | ShellInterpreter | Capture},
	{CmdBg, "bg", HardBlocked | StrictUnsupported},
	{CmdBreak, "break", Builtin},
	{CmdBsdtar, "bsdtar", 0},
	{CmdBuiltin, "builtin", Wrapper | ShellWrapper},
	{CmdCat, "cat", Arguments | Capture},
	{CmdCd, "cd", Arguments | Builtin | Capture},
	{CmdChmod, "chmod", Arguments | Capture},
	{CmdCmd, "cmd", Interpreter | Capture},
	{CmdCommand, "command", Wrapper | ShellWrapper},
	{CmdCompgen, "compgen", HardBlocked},
	{CmdComplete, "complete", HardBlocked},
	{CmdCompopt, "compopt", HardBlocked},
	{CmdContinue, "continue", Builtin},
	{CmdCoproc, "coproc", HardBlocked | StrictUnsupported},
	{CmdCp, "cp", Arguments | Capture},
	{CmdCurl, "curl", Arguments | Capture},
	{CmdCut, "cut", Arguments | Capture},
	{CmdDash, "dash", Interpreter | ShellInterpreter | Capture},
	{CmdDate, "date", Arguments | Capture},
	{CmdDbx, "dbx", Arguments | PlatformBuiltin | Capture},
	{CmdDeclare, "declare", Builtin},
	{CmdDf, "df", Arguments | Capture},
	{CmdDiff, "diff", Arguments | Capture},
	{CmdDirname, "dirname", Arguments | Capture},
	{CmdDu, "du", Arguments | Capture},
	{CmdEcho, "echo", Arguments | Builtin | Capture},
	{CmdEmulate, "emulate", HardBlocked},
	{CmdEnable, "enable", HardBlocked},
	{CmdEnv, "env", Wrapper | Capture},
	{CmdEval, "eval", HardBlocked | StrictUnsupported},
	{CmdExec, "exec", HardBlocked | StrictUnsupported},
	{CmdExport, "export", Builtin},
	{CmdFalse, "false", Arguments | Builtin | Capture},
	{CmdFg, "fg", HardBlocked | StrictUnsupported},
	{CmdFc, "fc", 0},
	{CmdFile, "file", Arguments | Capture},
	{CmdFind, "find", Arguments | Capture},
	{CmdFree, "free", Arguments | Capture},
	{CmdGawk, "gawk", Arguments | Capture},
	{CmdGetChildItem, "Get-ChildItem", Arguments | Capture},
	{CmdGetContent, "Get-Content", Arguments | Capture},
	{CmdGit, "git", Arguments | Capture},
	{CmdGo, "go", BuildTool | Capture},
	{CmdGrep, "grep", Arguments | Capture},
	{CmdGtar, "gtar", 0},
	{CmdHash, "hash", HardBlocked},
	{CmdHead, "head", Arguments | Capture},
	{CmdHttpx, "httpx", Arguments | PlatformBuiltin | Capture},
	{CmdId, "id", Arguments | Capture},
	{CmdJobs, "jobs", HardBlocked | StrictUnsupported},
	{CmdJq, "jq", Arguments | Capture},
	{CmdKsh, "ksh", Interpreter | ShellInterpreter | Capture},
	{CmdLn, "ln", Arguments | Capture},
	{CmdLocal, "local", Builtin},
	{CmdLs, "ls", Arguments | Capture},
	{CmdLsof, "lsof", Arguments | Capture},
	{CmdMake, "make", BuildTool | Capture},
	{CmdMapfile, "mapfile", HardBlocked},
	{CmdMawk, "mawk", 0},
	{CmdMkdir, "mkdir", Arguments | Capture},
	{CmdMv, "mv", Arguments | Capture},
	{CmdNawk, "nawk", 0},
	{CmdNice, "nice", Wrapper | Capture},
	{CmdNode, "node", Interpreter | Capture},
	{CmdNohup, "nohup", Wrapper | Capture},
	{CmdNpm, "npm", BuildTool | Capture},
	{CmdNpx, "npx", BuildTool | Capture},
	{CmdPdftotext, "pdftotext", Arguments | PlatformBuiltin | Capture},
	{CmdPerl, "perl", Interpreter | Capture},
	{CmdPip, "pip", BuildTool | Capture},
	{CmdPnpm, "pnpm", BuildTool | Capture},
	{CmdPowershell, "powershell", Interpreter | Capture},
	{CmdPrintenv, "printenv", Arguments | Capture},
	{CmdPrintf, "printf", Arguments | Builtin | Capture},
	{CmdPwd, "pwd", Arguments | Builtin | Capture},
	{CmdPwsh, "pwsh", Interpreter | Capture},
	{CmdPython, "python", Interpreter | Capture},
	{CmdPython3, "python3", Interpreter | Capture},
	{CmdRead, "read", Builtin | HardBlocked},
	{CmdReadarray, "readarray", HardBlocked},
	{CmdReadlink, "readlink", Arguments | Capture},
	{CmdRealpath, "realpath", Arguments | Capture},
	{CmdRemoveItem, "Remove-Item", Arguments | Capture},
	{CmdReturn, "return", Builtin},
	{CmdRg, "rg", Arguments | PlatformBuiltin | Capture},
	{CmdRm, "rm", Arguments | Capture},
	{CmdRmdir, "rmdir", Arguments | Capture},
	{CmdRuby, "ruby", Interpreter | Capture},
	{CmdSed, "sed", Arguments | Capture},
	{CmdSet, "set", Builtin | HardBlocked},
	{CmdSh, "sh", Interpreter | ShellInterpreter | Capture},
	{CmdShift, "shift", Builtin},
	{CmdShopt, "shopt", HardBlocked},
	{CmdSleep, "sleep", Arguments | Capture},
	{CmdSort, "sort", Arguments | Capture},
	{CmdSource, "source", HardBlocked | StrictUnsupported},
	{CmdStat, "stat", Arguments | Capture},
	{CmdStdbuf, "stdbuf", Wrapper | Capture},
	{CmdSysopen, "sysopen", HardBlocked},
	{CmdSysread, "sysread", HardBlocked},
	{CmdSysseek, "sysseek", HardBlocked},
	{CmdSyswrite, "syswrite", HardBlocked},
	{CmdTail, "tail", Arguments | Capture},
	{CmdTar, "tar", Arguments | Capture},
	{CmdTee, "tee", Arguments | Capture},
	{CmdTest, "test", Arguments | Builtin | Capture},
	{CmdTimeout, "timeout", Wrapper | Capture},
	{CmdTop, "top", Arguments | Capture},
	{CmdTouch, "touch", Arguments | Capture},
	{CmdTr, "tr", Arguments | Capture},
	{CmdTrap, "trap", HardBlocked},
	{CmdTrue, "true", Arguments | Builtin | Capture},
	{CmdType, "type", Builtin},
	{CmdTypeset, "typeset", Builtin},
	{CmdUnalias, "unalias", HardBlocked},
	{CmdUname, "uname", Arguments | Capture},
	{CmdUniq, "uniq", Arguments | Capture},
	{CmdUnset, "unset", Builtin | HardBlocked},
	{CmdWc, "wc", Arguments | Capture},
	{CmdWget, "wget", Arguments | Capture},
	{CmdWhich, "which", Arguments | Capture},
	{CmdWhoami, "whoami", Arguments | Capture},
	{CmdXargs, "xargs", 0},
	{CmdYarn, "yarn", BuildTool | Capture},
	{CmdZfChgrp, "zf_chgrp", HardBlocked},
	{CmdZfChmod, "zf_chmod", HardBlocked},
	{CmdZfChown, "zf_chown", HardBlocked},
	{CmdZfLn, "zf_ln", HardBlocked},
	{CmdZfMkdir, "zf_mkdir", HardBlocked},
	{CmdZfMv, "zf_mv", HardBlocked},
	{CmdZfRm, "zf_rm", HardBlocked},
	{CmdZfRmdir, "zf_rmdir", HardBlocked},
	{CmdZmodload, "zmodload", HardBlocked},
	{CmdZpty, "zpty", HardBlocked},
	{CmdZsh, "zsh", Interpreter | ShellInterpreter | Capture},
	{CmdZsocket, "zsocket", HardBlocked},
	{CmdZtcp, "ztcp", HardBlocked},
}

var byName = func() map[string]Entry {
	out := make(map[string]Entry, len(entries))
	for _, e := range entries {
		key := strings.ToLower(e.Name)
		if _, exists := out[key]; exists {
			panic("duplicate command metadata: " + key)
		}
		out[key] = e
	}
	return out
}()

func Lookup(name string) (Entry, bool) { e, ok := byName[strings.ToLower(name)]; return e, ok }
func Is(name string, flag Flag) bool   { e, ok := Lookup(name); return ok && e.Flags&flag != 0 }
func Names(flag Flag) []string {
	var names []string
	for _, e := range entries {
		if e.Flags&flag != 0 {
			names = append(names, e.Name)
		}
	}
	return names
}
func Entries() []Entry { return append([]Entry(nil), entries[:]...) }

var byID = func() map[ID]Entry {
	out := map[ID]Entry{}
	for _, e := range entries {
		if _, exists := out[e.ID]; exists {
			panic("duplicate command ID")
		}
		out[e.ID] = e
	}
	return out
}()

func Name(id ID) string   { return byID[id].Name }
func IDOf(name string) ID { e, _ := Lookup(name); return e.ID }
