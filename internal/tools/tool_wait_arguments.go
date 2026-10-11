package tools

import (
	. "agent-platform/internal/contracts"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var waitOffsetToken = regexp.MustCompile(`([0-9]+)([SmHD])`)

type waitArguments struct {
	deadline                     time.Time
	timezone, description, match string
	dateOnly                     bool
	conditions                   []WaitCondition
}

func parseWaitArguments(args map[string]any, now time.Time) (waitArguments, error) {
	a := waitArguments{match: "any"}
	for key := range args {
		switch key {
		case "offset", "base", "timezone", "description", "conditions", "match":
		default:
			return a, fmt.Errorf("unknown wait argument %q", key)
		}
	}
	for _, key := range []string{"offset", "base", "timezone", "description", "match"} {
		if v, ok := args[key]; ok {
			if _, valid := v.(string); !valid {
				return a, fmt.Errorf("%s must be a string", key)
			}
		}
	}
	a.description = strings.TrimSpace(stringArg(args, "description"))
	if a.description == "" {
		return a, fmt.Errorf("description is required and must explain what is being awaited")
	}
	if utf8.RuneCountInString(a.description) > 200 {
		return a, fmt.Errorf("description must not exceed 200 characters")
	}
	if value, ok := args["offset"]; ok {
		raw := strings.TrimPrefix(value.(string), "+")
		tokens := waitOffsetToken.FindAllStringSubmatch(raw, -1)
		consumed := ""
		var duration time.Duration
		for _, token := range tokens {
			consumed += token[0]
			n, err := strconv.ParseInt(token[1], 10, 64)
			if err != nil || n > 86400 {
				return a, fmt.Errorf("offset exceeds 24 hours")
			}
			unit := map[string]time.Duration{"S": time.Second, "m": time.Minute, "H": time.Hour, "D": 24 * time.Hour}[token[2]]
			if n > int64((24*time.Hour-duration)/unit) {
				return a, fmt.Errorf("offset exceeds 24 hours; use automation")
			}
			duration += time.Duration(n) * unit
		}
		if raw == "" || consumed != raw || duration <= 0 || duration > 24*time.Hour {
			return a, fmt.Errorf("offset must be a positive duration up to 24 hours using S, m, H or D (e.g. +5m); M, w and y are not supported")
		}
		_, zone, err := parseDateTimeZone(stringArg(args, "timezone"), now)
		if err != nil {
			return a, err
		}
		a.timezone = zone
		a.deadline = now.Add(duration)
	} else {
		base := stringArg(args, "base")
		if base == "" {
			return a, fmt.Errorf("offset or base is required")
		}
		anchor, err := parseDateTimeBase(base, stringArg(args, "timezone"), now)
		if err != nil {
			parsed, parseErr := time.Parse(time.RFC3339Nano, base)
			if parseErr != nil || !strings.Contains(base, ".") {
				return a, err
			}
			_, zone, zoneErr := parseDateTimeZone(stringArg(args, "timezone"), now)
			if zoneErr != nil {
				return a, zoneErr
			}
			anchor = &dateTimeAnchor{instant: parsed, zoneID: zone}
		}
		a.deadline = anchor.instant
		a.timezone = anchor.zoneID
		a.dateOnly = len(base) == 10
		if a.deadline.Sub(now) > 24*time.Hour {
			return a, fmt.Errorf("base exceeds 24 hours; use automation for longer scheduling")
		}
	}
	if raw, ok := args["conditions"]; ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return a, err
		}
		var objects []map[string]any
		if err := json.Unmarshal(data, &objects); err != nil {
			return a, fmt.Errorf("conditions must be an array of objects")
		}
		for _, object := range objects {
			for key := range object {
				switch key {
				case "type", "runId", "filePath", "after", "connectorId", "authorizationId":
				default:
					return a, fmt.Errorf("unknown condition field %q; use camelCase parameter names", key)
				}
			}
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&a.conditions); err != nil {
			return a, err
		}
		if len(a.conditions) < 1 || len(a.conditions) > 8 {
			return a, fmt.Errorf("conditions must contain 1 to 8 events")
		}
		for _, c := range a.conditions {
			valid := false
			allowed := WaitCondition{Type: c.Type}
			switch c.Type {
			case "run.terminal":
				valid = strings.TrimSpace(c.RunID) != ""
				allowed.RunID = c.RunID
			case "file.exists":
				valid = strings.TrimSpace(c.FilePath) != ""
				allowed.FilePath = c.FilePath
			case "file.modifiedAfter":
				valid = strings.TrimSpace(c.FilePath) != "" && c.After > 0
				allowed.FilePath = c.FilePath
				allowed.After = c.After
			case "connector.authorizationTerminal":
				valid = strings.TrimSpace(c.ConnectorID) != "" && strings.TrimSpace(c.AuthorizationID) != ""
				allowed.ConnectorID = c.ConnectorID
				allowed.AuthorizationID = c.AuthorizationID
			}
			if !valid || c != allowed {
				return a, fmt.Errorf("invalid condition %q or missing target fields", c.Type)
			}
		}
	}
	if raw, ok := args["match"]; ok {
		a.match = raw.(string)
		if len(a.conditions) == 0 {
			return a, fmt.Errorf("match requires conditions")
		}
	}
	if a.match != "any" && a.match != "all" {
		return a, fmt.Errorf("match must be any or all")
	}
	return a, nil
}
