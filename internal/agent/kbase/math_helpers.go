package kbase

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

type sqliteOpenMode int

const (
	sqliteOpenWrite sqliteOpenMode = iota
	sqliteOpenRead

	kbaseSQLiteBusyTimeoutMS = 5000
)

// kbaseSQLiteDSN is used exclusively for the SQLite control plane.
func kbaseSQLiteDSN(dbPath string, mode sqliteOpenMode) string {
	path := filepath.ToSlash(dbPath)
	if filepath.IsAbs(dbPath) && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	uri := url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", kbaseSQLiteBusyTimeoutMS))
	if mode == sqliteOpenRead {
		query.Add("_pragma", "query_only(1)")
	} else {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Add("_pragma", "synchronous(NORMAL)")
	}
	uri.RawQuery = query.Encode()
	return uri.String()
}

func stableIDDigest(ids []string) string {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	hash := sha256.New()
	var length [8]byte
	previous := ""
	for index, id := range ids {
		if index > 0 && id == previous {
			continue
		}
		previous = id
		binary.BigEndian.PutUint64(length[:], uint64(len([]byte(id))))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(id))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func lanceErrorCode(err error) string {
	var engineErr *LanceEngineError
	if errors.As(err, &engineErr) && engineErr.Code != "" {
		return engineErr.Code
	}
	return "engine_internal"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
