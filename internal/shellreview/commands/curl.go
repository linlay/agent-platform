package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
	"net/url"
	"strings"
)

var curlSpec = types.OptionSpec{ShortFlags: "sSfILlvkiNgGOJR46qZ#", ShortArgs: map[byte]types.OptionRole{'o': types.OptionWrite, 'T': types.OptionRead, 'd': types.OptionRemoteData, 'F': types.OptionRemoteData, 'H': types.OptionValue, 'X': types.OptionValue, 'u': types.OptionValue, 'A': types.OptionValue, 'e': types.OptionValue, 'b': types.OptionValue, 'c': types.OptionWrite, 'K': types.OptionRead, 'm': types.OptionValue, 'x': types.OptionValue, 'w': types.OptionValue, 'r': types.OptionValue, 'C': types.OptionValue, 'E': types.OptionRead, 'U': types.OptionValue, 'z': types.OptionValue, 'Y': types.OptionValue, 'y': types.OptionValue, 'D': types.OptionWrite, 'Q': types.OptionValue, 't': types.OptionValue, 'P': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "silent", "show-error", "fail", "fail-with-body", "include", "head", "location", "location-trusted", "insecure", "verbose", "compressed", "http1.0", "http1.1", "http2", "http2-prior-knowledge", "http3", "ipv4", "ipv6", "get", "globoff", "progress-bar", "no-progress-meter", "no-buffer", "remote-name", "remote-name-all", "remote-header-name", "create-dirs", "netrc", "netrc-optional", "list-only", "disable", "tcp-nodelay", "no-keepalive", "path-as-is", "raw", "styled-output", "no-styled-output", "ssl-reqd", "tlsv1.2", "tlsv1.3", "anyauth", "basic", "digest", "ntlm", "negotiate", "junk-session-cookies", "retry-connrefused", "retry-all-errors", "no-sessionid", "fail-early", "parallel", "version"), options.Longs(types.OptionWrite, "output", "cookie-jar", "trace", "trace-ascii", "dump-header", "stderr", "etag-save", "libcurl"), options.Longs(types.OptionWriteDir, "output-dir"), options.Longs(types.OptionRead, "upload-file", "config", "netrc-file", "cacert", "cert", "key", "capath", "etag-compare", "crlfile", "pinnedpubkey"), options.Longs(types.OptionRemoteData, "data", "data-binary", "data-raw", "data-ascii", "data-urlencode", "json", "form", "form-string"), options.Longs(types.OptionValue, "header", "request", "user", "user-agent", "referer", "cookie", "max-time", "connect-timeout", "proxy", "write-out", "range", "continue-at", "retry", "retry-delay", "retry-max-time", "max-redirs", "limit-rate", "speed-limit", "speed-time", "resolve", "connect-to", "interface", "oauth2-bearer", "aws-sigv4", "url", "proxy-user", "time-cond", "max-filesize", "keepalive-time", "dns-servers", "cert-type", "key-type", "ciphers", "tls-max", "unix-socket", "abstract-unix-socket", "noproxy", "variable", "expand-url", "url-query", "parallel-max", "rate", "socks5-hostname"))}

func curlOperands(args []string) types.Effects {
	spec := curlSpec
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
			if file := curlRemoteDataFile(key, value); file != "" {
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
	if p.Has("K") || p.Has("config") {
		// A config file can add outputs, uploads and URLs that are not reviewed here.
		e.ExecutesCode = true
	}
	if p.Has("O") || p.Has("remote-name") || p.Has("remote-name-all") {
		// The output file name comes from the URL and lands in cwd.
		add(".", true, false)
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

func curlRemoteDataFile(key, value string) string {
	switch key {
	case "data-raw", "form-string":
		return ""
	case "F", "form":
		_, v, ok := strings.Cut(value, "=")
		if ok && (strings.HasPrefix(v, "@") || strings.HasPrefix(v, "<")) {
			file, _, _ := strings.Cut(v[1:], ";")
			return file
		}
		return ""
	case "data-urlencode":
		if _, v, ok := strings.Cut(value, "@"); ok {
			return v
		}
		return ""
	}
	if strings.HasPrefix(value, "@") {
		return value[1:]
	}
	return ""
}

func curlConfig(args []string) []string {
	if len(args) > 0 && (args[0] == "-q" || args[0] == "--disable") {
		return nil
	}
	return []string{"CURL_HOME"}
}
