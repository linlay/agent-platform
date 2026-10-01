package shellanalysis

import (
	"strconv"
	"strings"
)

// optionRole describes the local effect of an option value.
type optionRole int

const (
	optNone          optionRole = iota // flag without a value
	optValue                           // value is not a local resource
	optRead                            // value is a file that is read
	optWrite                           // value is a file that is written
	optReadDir                         // value is a directory that is read
	optWriteDir                        // value is a directory that is written
	optExec                            // value is a program or command to run
	optRemote                          // flag causes a remote mutation
	optRemoteData                      // value is sent to a remote endpoint (may be @file)
	optTwoValues                       // option consumes two non-resource values
	optNameFile                        // option consumes a name and a file that is read
	optRecursive                       // flag makes operands recursive
	optOptionalValue                   // --long[=value]; never consumes the next argument
)

// commandSpec is the grammar used to separate options, their values and file
// operands. It never decides permissions: it only reports local effects.
type commandSpec struct {
	shortFlags string                // letters without values
	shortArgs  map[byte]optionRole   // letters taking a value
	long       map[string]optionRole // long options by name, without leading --
	// numericShort accepts -NUM forms (head -20, tail -200).
	numericShort bool
}

type parsedOptions struct {
	Positional []string
	Files      []FileAccess
	Incomplete bool
	Exec       bool
	Remote     bool
	Recursive  bool
	flags      map[string]bool
	values     map[string][]string
}

func (p *parsedOptions) has(name string) bool { return p.flags[name] }

func parseOptions(spec commandSpec, args []string) parsedOptions {
	p := parsedOptions{flags: map[string]bool{}, values: map[string][]string{}}
	apply := func(key string, role optionRole, value string) {
		p.flags[key] = true
		p.values[key] = append(p.values[key], value)
		switch role {
		case optRead:
			p.Files = append(p.Files, FileAccess{Path: value})
		case optWrite:
			p.Files = append(p.Files, FileAccess{Path: value, Write: true})
		case optReadDir:
			p.Files = append(p.Files, FileAccess{Path: value, Recursive: true})
		case optWriteDir:
			p.Files = append(p.Files, FileAccess{Path: value, Write: true})
		case optExec:
			p.Exec = true
		case optRemote:
			p.Remote = true
		case optRecursive:
			p.Recursive = true
		}
	}
	end := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if end || a == "-" || !strings.HasPrefix(a, "-") {
			p.Positional = append(p.Positional, a)
			continue
		}
		if a == "--" {
			end = true
			continue
		}
		if strings.HasPrefix(a, "--") {
			name, value, hasValue := strings.Cut(a[2:], "=")
			role, known := spec.long[name]
			if !known {
				p.Incomplete = true
				if hasValue && strings.ContainsAny(value, "/\\") {
					p.Files = append(p.Files, FileAccess{Path: value, Write: true})
				}
				continue
			}
			switch role {
			case optNone, optRecursive, optRemote:
				if hasValue {
					p.Incomplete = true
				}
				apply(name, role, "")
			case optOptionalValue:
				apply(name, optValue, value)
			case optTwoValues, optNameFile:
				if hasValue || i+2 >= len(args) {
					p.Incomplete = true
					continue
				}
				first, second := args[i+1], args[i+2]
				i += 2
				if role == optNameFile {
					apply(name, optRead, second)
				} else {
					apply(name, optValue, first+"\x00"+second)
				}
			default:
				if !hasValue {
					if i+1 >= len(args) {
						p.Incomplete = true
						continue
					}
					i++
					value = args[i]
				}
				apply(name, role, value)
			}
			continue
		}
		letters := a[1:]
		if spec.numericShort {
			if _, err := strconv.Atoi(letters); err == nil {
				continue
			}
		}
		for j := 0; j < len(letters); j++ {
			c := letters[j]
			if role, ok := spec.shortArgs[c]; ok {
				value := letters[j+1:]
				if value == "" {
					if i+1 >= len(args) {
						p.Incomplete = true
						break
					}
					i++
					value = args[i]
				}
				apply(string(c), role, value)
				break
			}
			if strings.IndexByte(spec.shortFlags, c) >= 0 {
				apply(string(c), optNone, "")
				continue
			}
			p.Incomplete = true
			break
		}
	}
	return p
}

func longs(role optionRole, names ...string) map[string]optionRole {
	m := make(map[string]optionRole, len(names))
	for _, name := range names {
		m[name] = role
	}
	return m
}

func merge(maps ...map[string]optionRole) map[string]optionRole {
	out := map[string]optionRole{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

var specs = map[string]commandSpec{
	"cat": {shortFlags: "AbenstuvET", long: longs(optNone, "number", "number-nonblank", "show-all", "show-ends", "show-tabs", "squeeze-blank", "show-nonprinting")},
	"ls": {shortFlags: "aAbBcCdDfFghHiklLmNnopqQrRsStuUvxXZ1", shortArgs: map[byte]optionRole{'I': optValue, 'w': optValue, 'T': optValue},
		long: merge(longs(optNone, "all", "almost-all", "human-readable", "recursive", "reverse", "classify", "directory", "group-directories-first", "inode", "size", "full-time", "numeric-uid-gid", "dereference", "si", "literal", "quote-name", "escape", "hide-control-chars", "show-control-chars", "no-group", "author"),
			longs(optOptionalValue, "color", "classify", "hyperlink", "indicator-style"), longs(optValue, "sort", "time", "format", "time-style", "ignore", "hide", "width", "tabsize", "block-size", "quoting-style"))},
	"head": {shortFlags: "qvz", shortArgs: map[byte]optionRole{'n': optValue, 'c': optValue}, numericShort: true,
		long: merge(longs(optNone, "quiet", "silent", "verbose", "zero-terminated"), longs(optValue, "lines", "bytes"))},
	"tail": {shortFlags: "qvfFz", shortArgs: map[byte]optionRole{'n': optValue, 'c': optValue, 's': optValue}, numericShort: true,
		long: merge(longs(optNone, "quiet", "silent", "verbose", "retry", "zero-terminated"), longs(optOptionalValue, "follow"), longs(optValue, "lines", "bytes", "pid", "sleep-interval", "max-unchanged-stats"))},
	"wc": {shortFlags: "clLmw", long: merge(longs(optNone, "lines", "words", "chars", "bytes", "max-line-length"), longs(optRead, "files0-from"), longs(optOptionalValue, "total"))},
	"grep": {shortFlags: "abcEFGhHiIlLnoqrRsUvwxyzZP", shortArgs: map[byte]optionRole{'e': optValue, 'f': optRead, 'm': optValue, 'A': optValue, 'B': optValue, 'C': optValue, 'd': optValue, 'D': optValue},
		long: merge(longs(optNone, "count", "files-with-matches", "files-without-match", "word-regexp", "line-regexp", "fixed-strings", "extended-regexp", "perl-regexp", "basic-regexp", "invert-match", "only-matching", "no-filename", "with-filename", "quiet", "silent", "no-messages", "text", "null", "null-data", "line-number", "ignore-case", "no-ignore-case", "byte-offset", "initial-tab", "line-buffered"),
			longs(optRecursive, "recursive", "dereference-recursive"), longs(optOptionalValue, "color", "colour"),
			longs(optValue, "include", "exclude", "exclude-dir", "binary-files", "max-count", "after-context", "before-context", "context", "regexp", "label", "devices", "directories"), longs(optRead, "file", "exclude-from"))},
	"rg": {shortFlags: "abcFhHiIlLnNopqsSuUvwxz0", shortArgs: map[byte]optionRole{'e': optValue, 'f': optRead, 'g': optValue, 't': optValue, 'T': optValue, 'm': optValue, 'A': optValue, 'B': optValue, 'C': optValue, 'j': optValue, 'M': optValue, 'r': optValue, 'E': optValue, 'd': optValue},
		long: merge(longs(optNone, "hidden", "no-ignore", "no-ignore-vcs", "smart-case", "case-sensitive", "ignore-case", "json", "vimgrep", "heading", "no-heading", "follow", "fixed-strings", "multiline", "pcre2", "stats", "files", "files-with-matches", "files-without-match", "count", "count-matches", "line-number", "no-line-number", "only-matching", "invert-match", "word-regexp", "line-regexp", "search-zip", "text", "null", "no-filename", "with-filename", "no-messages", "quiet", "trim", "type-list", "no-config", "one-file-system", "unrestricted", "binary", "column", "byte-offset", "passthru", "max-columns-preview"),
			longs(optValue, "glob", "iglob", "type", "type-not", "type-add", "sort", "sortr", "max-count", "max-depth", "max-filesize", "threads", "after-context", "before-context", "context", "regexp", "replace", "encoding", "max-columns", "path-separator", "colors", "color", "pre-glob", "field-match-separator", "field-context-separator", "context-separator"),
			longs(optRead, "file", "ignore-file"), longs(optExec, "pre"))},
	"mkdir": {shortFlags: "pv", shortArgs: map[byte]optionRole{'m': optValue}, long: merge(longs(optNone, "parents", "verbose"), longs(optValue, "mode", "context"))},
	"touch": {shortFlags: "acmfh", shortArgs: map[byte]optionRole{'r': optRead, 'd': optValue, 't': optValue},
		long: merge(longs(optNone, "no-create", "no-dereference"), longs(optValue, "date", "time"), longs(optRead, "reference"))},
	"rm":    {shortFlags: "fiIdvRr", long: merge(longs(optNone, "force", "dir", "verbose", "one-file-system", "preserve-root"), longs(optRecursive, "recursive"), longs(optOptionalValue, "interactive"))},
	"rmdir": {shortFlags: "pv", long: longs(optNone, "parents", "ignore-fail-on-non-empty", "verbose")},
	"chmod": {shortFlags: "Rfvc", long: merge(longs(optNone, "silent", "quiet", "verbose", "changes", "preserve-root", "no-preserve-root"), longs(optRecursive, "recursive"), longs(optRead, "reference"))},
	"tee":   {shortFlags: "aip", long: merge(longs(optNone, "append", "ignore-interrupts"), longs(optOptionalValue, "output-error"))},
	"cp": {shortFlags: "afinpPRrsTvuLHlxb", shortArgs: map[byte]optionRole{'t': optWriteDir, 'S': optValue},
		long: merge(longs(optNone, "archive", "force", "interactive", "no-clobber", "recursive", "symbolic-link", "verbose", "no-target-directory", "parents", "dereference", "no-dereference", "link", "remove-destination", "strip-trailing-slashes", "one-file-system", "attributes-only"),
			longs(optOptionalValue, "preserve", "update", "backup", "reflink", "sparse", "no-preserve"), longs(optValue, "suffix"), longs(optWriteDir, "target-directory"))},
	"mv": {shortFlags: "finvuTb", shortArgs: map[byte]optionRole{'t': optWriteDir, 'S': optValue},
		long: merge(longs(optNone, "force", "interactive", "no-clobber", "verbose", "no-target-directory", "strip-trailing-slashes"), longs(optOptionalValue, "update", "backup"), longs(optValue, "suffix"), longs(optWriteDir, "target-directory"))},
	"ln": {shortFlags: "sfnvTbrLPi", shortArgs: map[byte]optionRole{'t': optWriteDir, 'S': optValue},
		long: merge(longs(optNone, "symbolic", "force", "no-dereference", "verbose", "relative", "logical", "physical", "interactive", "no-target-directory"), longs(optOptionalValue, "backup"), longs(optValue, "suffix"), longs(optWriteDir, "target-directory"))},
	"sort": {shortFlags: "bdfginruMhsVzcCmR", shortArgs: map[byte]optionRole{'k': optValue, 't': optValue, 'o': optWrite, 'S': optValue, 'T': optWriteDir},
		long: merge(longs(optNone, "unique", "reverse", "numeric-sort", "general-numeric-sort", "human-numeric-sort", "version-sort", "month-sort", "ignore-case", "ignore-leading-blanks", "dictionary-order", "stable", "merge", "random-sort", "zero-terminated"),
			longs(optOptionalValue, "check"), longs(optValue, "key", "field-separator", "buffer-size", "parallel", "sort", "batch-size"), longs(optWrite, "output"), longs(optWriteDir, "temporary-directory"), longs(optRead, "files0-from"))},
	"uniq": {shortFlags: "cduizD", shortArgs: map[byte]optionRole{'f': optValue, 's': optValue, 'w': optValue},
		long: merge(longs(optNone, "count", "repeated", "unique", "ignore-case", "zero-terminated"), longs(optOptionalValue, "all-repeated", "group"), longs(optValue, "skip-fields", "skip-chars", "check-chars"))},
	"du": {shortFlags: "achksxLHbmPl0", shortArgs: map[byte]optionRole{'d': optValue, 'B': optValue, 't': optValue, 'X': optRead},
		long: merge(longs(optNone, "summarize", "human-readable", "all", "total", "apparent-size", "si", "bytes", "dereference", "one-file-system", "count-links", "null", "inodes"), longs(optValue, "max-depth", "exclude", "threshold", "block-size"), longs(optOptionalValue, "time"), longs(optRead, "exclude-from", "files0-from"))},
	"stat": {shortFlags: "fLtx", shortArgs: map[byte]optionRole{'c': optValue}, long: merge(longs(optNone, "dereference", "file-system", "terse"), longs(optValue, "format", "printf"))},
	"file": {shortFlags: "bhiLszkNrp0", shortArgs: map[byte]optionRole{'m': optRead, 'f': optRead, 'F': optValue},
		long: merge(longs(optNone, "brief", "mime", "mime-type", "mime-encoding", "dereference", "no-dereference", "special-files", "uncompress", "keep-going"), longs(optRead, "files-from", "magic-file"), longs(optValue, "separator"))},
	"readlink": {shortFlags: "efmnqsvz", long: longs(optNone, "canonicalize", "canonicalize-existing", "canonicalize-missing", "no-newline", "quiet", "silent", "verbose", "zero")},
	"realpath": {shortFlags: "emLPqsz", long: merge(longs(optNone, "canonicalize-existing", "canonicalize-missing", "logical", "physical", "quiet", "strip", "no-symlinks", "zero"), longs(optReadDir, "relative-to", "relative-base"))},
	"cut":      {shortFlags: "snz", shortArgs: map[byte]optionRole{'b': optValue, 'c': optValue, 'd': optValue, 'f': optValue}, long: merge(longs(optNone, "only-delimited", "complement", "zero-terminated"), longs(optValue, "bytes", "characters", "delimiter", "fields", "output-delimiter"))},
	"diff": {shortFlags: "uNqsbwBiaytcpTdreEZ", shortArgs: map[byte]optionRole{'U': optValue, 'C': optValue, 'x': optValue, 'X': optRead, 'L': optValue, 'W': optValue, 'I': optValue, 'F': optValue, 'S': optValue},
		long: merge(longs(optNone, "brief", "report-identical-files", "new-file", "unidirectional-new-file", "ignore-case", "ignore-space-change", "ignore-all-space", "ignore-blank-lines", "ignore-tab-expansion", "ignore-trailing-space", "text", "side-by-side", "expand-tabs", "initial-tab", "minimal", "speed-large-files", "strip-trailing-cr", "suppress-common-lines", "show-c-function", "left-column", "suppress-blank-empty", "no-dereference"),
			longs(optRecursive, "recursive"), longs(optOptionalValue, "unified", "context", "color", "normal"), longs(optValue, "exclude", "label", "width", "ignore-matching-lines", "show-function-line", "starting-file", "horizon-lines", "tabsize", "palette", "line-format", "old-line-format", "new-line-format", "unchanged-line-format", "old-group-format", "new-group-format", "changed-group-format", "unchanged-group-format"),
			longs(optRead, "exclude-from", "from-file", "to-file"))},
	"curl": {shortFlags: "sSfILlvkiNgGOJR46qZ#", shortArgs: map[byte]optionRole{'o': optWrite, 'T': optRead, 'd': optRemoteData, 'F': optRemoteData, 'H': optValue, 'X': optValue, 'u': optValue, 'A': optValue, 'e': optValue, 'b': optValue, 'c': optWrite, 'K': optRead, 'm': optValue, 'x': optValue, 'w': optValue, 'r': optValue, 'C': optValue, 'E': optRead, 'U': optValue, 'z': optValue, 'Y': optValue, 'y': optValue, 'D': optWrite, 'Q': optValue, 't': optValue, 'P': optValue},
		long: merge(longs(optNone, "silent", "show-error", "fail", "fail-with-body", "include", "head", "location", "location-trusted", "insecure", "verbose", "compressed", "http1.0", "http1.1", "http2", "http2-prior-knowledge", "http3", "ipv4", "ipv6", "get", "globoff", "progress-bar", "no-progress-meter", "no-buffer", "remote-name", "remote-name-all", "remote-header-name", "create-dirs", "netrc", "netrc-optional", "list-only", "disable", "tcp-nodelay", "no-keepalive", "path-as-is", "raw", "styled-output", "no-styled-output", "ssl-reqd", "tlsv1.2", "tlsv1.3", "anyauth", "basic", "digest", "ntlm", "negotiate", "junk-session-cookies", "retry-connrefused", "retry-all-errors", "no-sessionid", "fail-early", "parallel"),
			longs(optWrite, "output", "cookie-jar", "trace", "trace-ascii", "dump-header", "stderr", "etag-save", "libcurl"), longs(optWriteDir, "output-dir"), longs(optRead, "upload-file", "config", "netrc-file", "cacert", "cert", "key", "capath", "etag-compare", "crlfile", "pinnedpubkey"),
			longs(optRemoteData, "data", "data-binary", "data-raw", "data-ascii", "data-urlencode", "json", "form", "form-string"),
			longs(optValue, "header", "request", "user", "user-agent", "referer", "cookie", "max-time", "connect-timeout", "proxy", "write-out", "range", "continue-at", "retry", "retry-delay", "retry-max-time", "max-redirs", "limit-rate", "speed-limit", "speed-time", "resolve", "connect-to", "interface", "oauth2-bearer", "aws-sigv4", "url", "proxy-user", "time-cond", "max-filesize", "keepalive-time", "dns-servers", "cert-type", "key-type", "ciphers", "tls-max", "unix-socket", "abstract-unix-socket", "noproxy", "variable", "expand-url", "url-query", "parallel-max", "rate"))},
	"wget": {shortFlags: "qcvNrpkxbKmnEFS", shortArgs: map[byte]optionRole{'O': optWrite, 'o': optWrite, 'a': optWrite, 'P': optWriteDir, 'i': optRead, 'e': optExec, 'U': optValue, 'T': optValue, 't': optValue, 'w': optValue, 'l': optValue, 'Q': optValue, 'A': optValue, 'R': optValue, 'D': optValue, 'I': optValue, 'X': optValue},
		long: merge(longs(optNone, "quiet", "continue", "no-clobber", "timestamping", "recursive", "no-parent", "no-check-certificate", "verbose", "no-verbose", "server-response", "spider", "mirror", "page-requisites", "convert-links", "adjust-extension", "no-directories", "force-directories", "content-disposition", "trust-server-names", "ignore-case", "inet4-only", "inet6-only", "show-progress", "no-host-directories"),
			longs(optWrite, "output-document", "output-file", "append-output", "save-cookies"), longs(optWriteDir, "directory-prefix"), longs(optRead, "input-file", "load-cookies", "ca-certificate", "certificate", "private-key"),
			longs(optExec, "execute", "use-askpass"), longs(optRemoteData, "post-data", "post-file", "body-data", "body-file"),
			longs(optValue, "user-agent", "timeout", "tries", "wait", "level", "header", "user", "password", "method", "limit-rate", "accept", "reject", "domains", "include-directories", "exclude-directories", "referer", "quota", "max-redirect", "progress", "restrict-file-names", "cut-dirs", "waitretry", "dns-timeout", "connect-timeout", "read-timeout", "https-only"))},
	"jq": {shortFlags: "rcsnejaSCMRh", shortArgs: map[byte]optionRole{'f': optRead, 'L': optReadDir},
		long: merge(longs(optNone, "raw-output", "compact-output", "slurp", "null-input", "exit-status", "join-output", "ascii-output", "sort-keys", "color-output", "monochrome-output", "tab", "raw-input", "seq", "stream", "stream-errors", "raw-output0", "unbuffered", "args", "jsonargs"),
			longs(optValue, "indent"), longs(optRead, "from-file"), longs(optTwoValues, "arg", "argjson"), longs(optNameFile, "slurpfile", "rawfile"))},
	"pdftotext": {shortFlags: "", shortArgs: map[byte]optionRole{}, long: map[string]optionRole{}},
}
