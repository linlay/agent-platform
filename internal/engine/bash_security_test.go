package engine

import "testing"

// Each test group covers one validator. Tests use table-driven format:
// blocked = should fail (ok=false), allowed = should pass (ok=true).

func TestBashSecurity_ControlCharacters(t *testing.T) {
	blocked := []string{
		"ls \x00",          // null byte
		"echo \x1b[31m",   // ESC sequence
		"cat \x07file",    // BEL
	}
	allowed := []string{
		"ls -la",
		"echo hello\tworld", // tab is allowed
	}
	runBashSecurityCases(t, "control_chars", blocked, allowed)
}

func TestBashSecurity_IncompleteCommands(t *testing.T) {
	blocked := []string{
		"\tls",          // starts with tab
		"-rf /",         // starts with flags
		"&& echo hi",   // starts with operator
		"|| true",       // starts with operator
		"; rm -rf /",    // starts with ;
		">> file",       // starts with >>
	}
	allowed := []string{
		"ls -la",
		"git status",
		"echo hello",
	}
	runBashSecurityCases(t, "incomplete", blocked, allowed)
}

func TestBashSecurity_ObfuscatedFlags(t *testing.T) {
	blocked := []string{
		`rm $'\x72\x6d'`,         // ANSI-C quoting on non-echo command
		`chmod $"hello"`,         // locale quoting on non-echo command
		`cmd ''""-rf"`,           // empty quote chain + quoted flag
		`ls """""""""""-la`,      // 3+ consecutive quotes
	}
	allowed := []string{
		"ls -la",
		`echo $'\x72\x6d'`,      // echo is exempted from obfuscated flag checks
		`echo 'hello world'`,
		`grep "pattern" file`,
	}
	runBashSecurityCases(t, "obfuscated_flags", blocked, allowed)
}

func TestBashSecurity_ShellMetacharacters(t *testing.T) {
	// NOTE: Generic ;|& detection is handled by validateStrictCommand (after
	// checkBashSecurity). This validator focuses on find -name/-path patterns
	// where metacharacters inside quotes can trick the shell.
	blocked := []string{
		`find . -name "*.go;rm -rf /"`,   // semicolon in find -name
		`find . -path "x|evil"`,          // pipe in find -path
		`find . -iname "x&evil"`,         // ampersand in find -iname
	}
	allowed := []string{
		"ls -la",
		`find . -name "*.go"`,
		`find . -type f`,
	}
	runBashSecurityCases(t, "metacharacters", blocked, allowed)
}

func TestBashSecurity_DangerousVariables(t *testing.T) {
	blocked := []string{
		"$HOME | cat",       // var in pipe
		"echo $PATH > file", // var in redirect
	}
	allowed := []string{
		"echo hello",
		"ls -la",
	}
	runBashSecurityCases(t, "dangerous_vars", blocked, allowed)
}

func TestBashSecurity_Newlines(t *testing.T) {
	blocked := []string{
		"ls\nrm -rf /",    // newline injection
		"echo hi\r\nwhoami", // CR+LF
	}
	allowed := []string{
		"ls -la",
		"echo hello",
	}
	runBashSecurityCases(t, "newlines", blocked, allowed)
}

func TestBashSecurity_CommandSubstitution(t *testing.T) {
	blocked := []string{
		"echo $(whoami)",    // $() substitution
		"echo ${HOME}",     // ${} substitution
		"echo `date`",      // backtick
		"cat <(ls)",        // process substitution
		"diff >(sort) file", // process substitution
	}
	allowed := []string{
		"ls -la",
		"echo hello",
	}
	runBashSecurityCases(t, "command_substitution", blocked, allowed)
}

func TestBashSecurity_Redirections(t *testing.T) {
	blocked := []string{
		"echo hi > /etc/passwd",  // output redirect
		"cat < /etc/shadow",     // input redirect
	}
	allowed := []string{
		"ls -la",
		"echo hi",
		// safe redirections are stripped first:
		"echo hi > /dev/null",
		"echo hi 2>&1",
	}
	runBashSecurityCases(t, "redirections", blocked, allowed)
}

func TestBashSecurity_IFSInjection(t *testing.T) {
	// Detects $IFS references (not assignment form IFS=).
	blocked := []string{
		"echo $IFS",         // IFS variable reference
		`echo ${IFS}cmd`,    // IFS in braces
		`cmd $IFS arg`,      // IFS in argument
	}
	allowed := []string{
		"ls -la",
		"echo hello",
	}
	runBashSecurityCases(t, "ifs_injection", blocked, allowed)
}

func TestBashSecurity_ProcEnviron(t *testing.T) {
	blocked := []string{
		"cat /proc/1/environ",       // direct
		"cat /proc/self/environ",    // self
		"head /proc/1234/environ",   // numeric PID
	}
	allowed := []string{
		"cat /proc/cpuinfo",
		"ls /proc",
	}
	runBashSecurityCases(t, "proc_environ", blocked, allowed)
}

func TestBashSecurity_BackslashEscapedWhitespace(t *testing.T) {
	blocked := []string{
		`echo\ test`,        // backslash-space
		"ls\\\tfile",        // backslash-tab
	}
	allowed := []string{
		"ls -la",
		`echo "hello world"`,
	}
	runBashSecurityCases(t, "backslash_whitespace", blocked, allowed)
}

func TestBashSecurity_BackslashEscapedOperators(t *testing.T) {
	blocked := []string{
		`ls \; rm`,          // escaped semicolon
		`echo \| cat`,       // escaped pipe
		`cmd \& bg`,         // escaped ampersand
	}
	allowed := []string{
		"ls -la",
		`echo "hello"`,
	}
	runBashSecurityCases(t, "backslash_operators", blocked, allowed)
}

func TestBashSecurity_UnicodeWhitespace(t *testing.T) {
	blocked := []string{
		"ls\u00A0-la",       // non-breaking space
		"echo\u2003hello",   // em space
	}
	allowed := []string{
		"ls -la",
		"echo hello",
	}
	runBashSecurityCases(t, "unicode_whitespace", blocked, allowed)
}

func TestBashSecurity_BraceExpansion(t *testing.T) {
	blocked := []string{
		"echo {a,b,c}",      // comma brace expansion
		"echo {1..10}",      // range brace expansion
	}
	allowed := []string{
		"ls -la",
		"echo hello",
	}
	runBashSecurityCases(t, "brace_expansion", blocked, allowed)
}

func TestBashSecurity_ZshDangerousCommands(t *testing.T) {
	blocked := []string{
		"zmodload zsh/system",
		"emulate -c 'evil'",
		"sysopen /etc/passwd",
		"zpty cmd evil",
		"ztcp 10.0.0.1 80",
		"zf_rm /etc/passwd",
	}
	allowed := []string{
		"ls -la",
		"echo zmodload",      // not at command position
	}
	runBashSecurityCases(t, "zsh_dangerous", blocked, allowed)
}

func TestBashSecurity_MalformedTokenInjection(t *testing.T) {
	blocked := []string{
		`echo "unclosed; rm -rf /`,  // unbalanced double quote + ;
		`echo 'unclosed; rm`,        // unbalanced single quote + ;
	}
	allowed := []string{
		"ls -la",
		`echo "balanced"`,
		`echo 'balanced'`,
	}
	runBashSecurityCases(t, "malformed_token", blocked, allowed)
}

func TestBashSecurity_NormalCommandsPass(t *testing.T) {
	// Regression: make sure common safe commands pass all checks.
	allowed := []string{
		"ls",
		"ls -la",
		"pwd",
		"cat file.txt",
		"head -n 10 file.txt",
		"tail -f log.txt",
		"git status",
		"git log --oneline -10",
		`git diff HEAD -- "path with spaces/file.go"`,
		"find . -name '*.go' -type f",
		"grep -rn pattern src/",
		"wc -l file.txt",
		"df -h",
		"free -m",
		"top -bn1",
		"echo hello world",
		`echo "hello world"`,
		`echo 'hello world'`,
		"rg pattern --type go",
	}
	for _, cmd := range allowed {
		ok, reason := checkBashSecurity(cmd)
		if !ok {
			t.Errorf("expected safe command to pass: %q, got blocked: %s", cmd, reason)
		}
	}
}

// helper
func runBashSecurityCases(t *testing.T, name string, blocked, allowed []string) {
	t.Helper()
	for _, cmd := range blocked {
		ok, reason := checkBashSecurity(cmd)
		if ok {
			t.Errorf("[%s] expected blocked: %q, but passed", name, cmd)
		}
		_ = reason
	}
	for _, cmd := range allowed {
		ok, reason := checkBashSecurity(cmd)
		if !ok {
			t.Errorf("[%s] expected allowed: %q, but blocked: %s", name, cmd, reason)
		}
	}
}
