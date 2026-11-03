package resources

import (
	"agent-platform/internal/shellreview/types"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// DeviceInspector belongs to one review and one execution environment. It
// caches even failures only for that review; callers construct another for the
// pre-launch review. No process/Run/container-wide device cache exists.
type DeviceInspector struct {
	once   sync.Once
	probe  func() types.ResourceResult
	result types.ResourceResult
}

func NewDeviceInspector(sandbox bool, env *types.BashEnvironment) *DeviceInspector {
	inspector := &DeviceInspector{probe: inspectHostNullDevice}
	if sandbox {
		inspector.probe = func() types.ResourceResult {
			if env == nil || env.InspectNullDevice == nil {
				return unresolvedDevice("guest null-device probe unavailable")
			}
			raw, err := env.InspectNullDevice()
			if err != nil {
				return unresolvedDevice("guest null-device probe failed")
			}
			result, err := ParseLinuxNullDevice(raw)
			if err != nil {
				return unresolvedDevice("guest null-device response invalid")
			}
			return result
		}
	}
	return inspector
}
func unresolvedDevice(reason string) types.ResourceResult {
	return types.ResourceResult{State: types.Unresolved, Reason: reason}
}

// NullOffset considers only literal exact spellings and supported byte access.
// Redirections are opened by the shell even for opaque/unknown-option calls;
// argument effects require complete analysis of this invocation alone.
func (i *DeviceInspector) NullOffset(op types.Op, target types.Target, complete bool) types.ResourceResult {
	if target.Path != "/dev/null" || !target.Word.Literal || target.Word.Origin != types.WordOriginSyntax || target.Placement != types.Exact || (op.Kind != types.Read && op.Kind != types.Output) {
		return types.ResourceResult{State: types.Known}
	}
	if op.Origin != types.ShellRedirect && (op.Origin != types.Argument || !complete) {
		return types.ResourceResult{State: types.Known}
	}
	i.once.Do(func() { i.result = i.probe() })
	return i.result
}

// ParseLinuxNullDevice accepts stat's numeric mode|major|minor protocol without
// localized type text or following a final symlink. Numbers are hexadecimal.
func ParseLinuxNullDevice(raw string) (types.ResourceResult, error) {
	if len(raw) > 64 {
		return types.ResourceResult{}, errors.New("oversized device response")
	}
	fields := strings.Split(strings.TrimSpace(raw), "|")
	if len(fields) != 3 {
		return types.ResourceResult{}, errors.New("invalid device response")
	}
	var values [3]uint64
	for i, field := range fields {
		if field == "" {
			return types.ResourceResult{}, errors.New("empty device field")
		}
		for _, c := range field {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return types.ResourceResult{}, errors.New("nonnumeric device field")
			}
		}
		bits := 32
		if i == 0 {
			bits = 16
		}
		value, err := strconv.ParseUint(field, 16, bits)
		if err != nil {
			return types.ResourceResult{}, err
		}
		values[i] = value
	}
	return types.ResourceResult{State: types.Known, NullDevice: values[0]&0xf000 == 0x2000 && values[1] == 1 && values[2] == 3}, nil
}
