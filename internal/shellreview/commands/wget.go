package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
	"net/url"
	"strings"
)

var wgetSpec = types.OptionSpec{ShortFlags: "qcvNrpkxbKmnEFS", ShortArgs: map[byte]types.OptionRole{'O': types.OptionWrite, 'o': types.OptionWrite, 'a': types.OptionWrite, 'P': types.OptionWriteDir, 'i': types.OptionRead, 'e': types.OptionExec, 'U': types.OptionValue, 'T': types.OptionValue, 't': types.OptionValue, 'w': types.OptionValue, 'l': types.OptionValue, 'Q': types.OptionValue, 'A': types.OptionValue, 'R': types.OptionValue, 'D': types.OptionValue, 'I': types.OptionValue, 'X': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "quiet", "continue", "no-clobber", "timestamping", "recursive", "no-parent", "no-check-certificate", "verbose", "no-verbose", "server-response", "spider", "mirror", "page-requisites", "convert-links", "adjust-extension", "no-directories", "force-directories", "content-disposition", "trust-server-names", "ignore-case", "inet4-only", "inet6-only", "show-progress", "no-host-directories", "no-config"), options.Longs(types.OptionWrite, "output-document", "output-file", "append-output", "save-cookies"), options.Longs(types.OptionWriteDir, "directory-prefix"), options.Longs(types.OptionRead, "input-file", "load-cookies", "ca-certificate", "certificate", "private-key", "config"), options.Longs(types.OptionExec, "execute", "use-askpass"), options.Longs(types.OptionRemoteData, "post-data", "post-file", "body-data", "body-file"), options.Longs(types.OptionValue, "user-agent", "timeout", "tries", "wait", "level", "header", "user", "password", "method", "limit-rate", "accept", "reject", "domains", "include-directories", "exclude-directories", "referer", "quota", "max-redirect", "progress", "restrict-file-names", "cut-dirs", "waitretry", "dns-timeout", "connect-timeout", "read-timeout", "https-only"))}

func wgetOperands(args []string) types.Effects {
	spec := wgetSpec
	p := options.Parse(spec, args)
	e := types.Effects{Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	e.Files = append(e.Files, p.Files...)
	add := func(path string, write, recursive bool) {
		if path != "" && path != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: path, Write: write, Recursive: recursive})
		}
	}
	for key, values := range p.Values {
		role := spec.Long[key]
		if len(key) == 1 {
			role = spec.ShortArgs[key[0]]
		}
		if role != types.OptionRemoteData {
			continue
		}
		e.RemoteMutation = true
		for _, value := range values {
			if file := wgetRemoteDataFile(key, value); file != "" {
				add(file, false, false)
			}
		}
	}
	for _, method := range append(p.Values["X"], p.Values["request"]...) {
		if upper := strings.ToUpper(method); upper != "GET" && upper != "HEAD" && upper != "OPTIONS" {
			e.RemoteMutation = true
		}
	}
	for _, method := range p.Values["method"] {
		if upper := strings.ToUpper(method); upper != "GET" && upper != "HEAD" {
			e.RemoteMutation = true
		}
	}
	if p.Has("T") || p.Has("upload-file") {
		e.RemoteMutation = true
	}
	if p.Has("config") {
		// A config file can add outputs, uploads and URLs that are not reviewed here.
		e.ExecutesCode = true
	}
	if p.Has("O") || p.Has("remote-name") || p.Has("remote-name-all") || !p.Has("O") && !p.Has("output-document") && !p.Has("spider") {
		// The output file name comes from the URL and lands in cwd.
		add(".", true, (p.Has("r") || p.Has("recursive") || p.Has("m") || p.Has("mirror")))
	}
	for _, raw := range p.Positional {
		if u, err := url.Parse(raw); err == nil && strings.EqualFold(u.Scheme, "file") {
			add(u.Path, false, false)
		} else if !strings.Contains(raw, "://") && strings.ContainsAny(raw, "/\\") {
			// A bare host/path is a URL to curl; only obvious local paths are reviewed.
			e.Incomplete = true
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}

func wgetRemoteDataFile(key, value string) string {
	if strings.HasPrefix(value, "@") {
		return value[1:]
	}
	return ""
}
func wgetConfig(args []string) []string {
	if configDisabled(args) {
		return nil
	}
	return []string{"WGETRC"}
}
