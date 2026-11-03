package resources

import (
	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellreview/types"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

type DependencyPermission struct {
	Writable   bool
	Readonly   bool
	HardReason string
}
type DependencyPermissionFunc func(path string) (DependencyPermission, error)
type DependencyReview struct {
	State     types.ResourceState
	Unsafe    bool
	HasConfig bool
	SHA256    string
	Reason    string
}
type dependencyVersion struct {
	Path  string
	Info  types.FileInspection
	State string
}
type inspectedDependency struct {
	info types.FileInspection
	err  error
}

// DependencyInspector caches filesystem/account observations only within this
// review and actual execution environment. Paths remain data, never shell code.
type DependencyInspector struct {
	sandbox     bool
	env         *types.BashEnvironment
	permission  DependencyPermissionFunc
	files       map[string]inspectedDependency
	homeChecked bool
	home        string
	homeErr     error
}

func NewDependencyInspector(sandbox bool, env *types.BashEnvironment, permission DependencyPermissionFunc) *DependencyInspector {
	if permission == nil {
		permission = func(string) (DependencyPermission, error) {
			return DependencyPermission{}, errors.New("dependency permission unavailable")
		}
	}
	return &DependencyInspector{sandbox: sandbox, env: env, permission: permission, files: map[string]inspectedDependency{}}
}
func dependencyPath(raw, cwd string) string {
	if filepath.IsAbs(raw) {
		return raw
	}
	return pathutil.JoinUnclean(cwd, raw)
}
func (i *DependencyInspector) inspect(path string) (types.FileInspection, error) {
	if cached, ok := i.files[path]; ok {
		return cached.info, cached.err
	}
	var info types.FileInspection
	var err error
	if i.env != nil && i.env.InspectConfig != nil {
		info, err = i.env.InspectConfig(path)
	} else if i.sandbox {
		err = errors.New("guest dependency probe unavailable")
	} else {
		info, err = inspectHostConfig(path)
	}
	if err == nil && info.Exists {
		if !filepath.IsAbs(info.Canonical) || strings.ContainsRune(info.Canonical, 0) || len(info.SHA256) != 64 {
			err = errors.New("dependency fingerprint incomplete")
		} else {
			_, err = hex.DecodeString(info.SHA256)
		}
	}
	i.files[path] = inspectedDependency{info: info, err: err}
	return info, err
}
func (i *DependencyInspector) account() (string, error) {
	if !i.homeChecked {
		i.homeChecked = true
		if i.env != nil && i.env.AccountHome != nil {
			i.home, i.homeErr = i.env.AccountHome()
		} else if i.sandbox {
			i.homeErr = errors.New("guest account home unavailable")
		} else {
			i.home, i.homeErr = accountHome()
		}
	}
	return i.home, i.homeErr
}
func (i *DependencyInspector) Review(deps []types.Dep, x types.BashExecution, priorMutation bool) DependencyReview {
	result := DependencyReview{State: types.Known}
	var versions []dependencyVersion
	unresolved := func(path, reason string) {
		if result.State == types.HardViolation {
			return
		}
		result.State = types.Unresolved
		result.Unsafe = true
		result.Reason = reason
		versions = append(versions, dependencyVersion{Path: path, State: "unresolved"})
	}
	inspect := func(path string, explicit bool) (exists bool, stop bool) {
		path = dependencyPath(path, x.Cwd)
		if path == "" || strings.ContainsRune(path, 0) {
			unresolved(path, "configuration path cannot be resolved")
			return false, true
		}
		permission, err := i.permission(path)
		if err != nil {
			unresolved(path, "configuration permission cannot be confirmed")
			return false, true
		}
		if permission.HardReason != "" {
			result.State = types.HardViolation
			result.Reason = permission.HardReason
			return false, true
		}
		if strings.TrimSpace(path) != path {
			unresolved(path, "configuration path spelling is unsupported")
			return false, true
		}
		info, err := i.inspect(path)
		if err != nil {
			unresolved(path, "configuration dependency cannot be inspected: "+path)
			return false, true
		}
		versions = append(versions, dependencyVersion{Path: path, Info: info, State: "known"})
		if explicit {
			result.Unsafe = true
			result.Reason = "command loads an unfrozen configuration or program file"
		}
		aliasWritable := false
		if info.Canonical != "" {
			actual, err := i.permission(info.Canonical)
			if err != nil {
				unresolved(path, "canonical configuration permission cannot be confirmed")
				return info.Exists, true
			}
			if actual.HardReason != "" {
				result.State = types.HardViolation
				result.Reason = actual.HardReason
				return info.Exists, true
			}
			if strings.TrimSpace(info.Canonical) != info.Canonical {
				unresolved(path, "canonical configuration path spelling is unsupported")
				return info.Exists, true
			}
			permission.Writable = permission.Writable || actual.Writable
			permission.Readonly = permission.Readonly || actual.Readonly
		}
		if info.Alias && !permission.Readonly && (info.Exists || priorMutation) {
			for parent := physicalParent(path); parent != ""; parent = physicalParent(parent) {
				p, err := i.permission(parent)
				if err != nil {
					unresolved(path, "configuration alias permission cannot be confirmed")
					return info.Exists, true
				}
				aliasWritable = aliasWritable || p.Writable
				if parent == "/" || parent == "." {
					break
				}
			}
		}
		if info.Exists {
			result.HasConfig = true
			if permission.Writable || aliasWritable {
				result.Unsafe = true
				result.Reason = "command loads configuration writable without approval: " + path
			}
			return true, true
		}
		// A previous write/opaque call can create a config before execution reaches
		// this invocation. Absence at review time alone does not prove absence then.
		if priorMutation && (permission.Writable || aliasWritable) {
			result.Unsafe = true
			result.Reason = "configuration may be created by an earlier command: " + path
		}
		return false, false
	}
	for _, dep := range deps {
		if result.State == types.HardViolation {
			break
		}
		switch dep.Kind {
		case types.Environment:
			if dep.Check == "startup" {
				unresolved(dep.Variable+"="+dep.Path, "process startup can execute additional code through "+dep.Variable)
				continue
			}
			if x.Env[dep.Variable] != "" {
				result.Unsafe = true
				result.Reason = "command behavior is supplied by " + dep.Variable
			}
		case types.Script:
			result.Unsafe = true
			if dep.Path != "" {
				inspect(dep.Path, true)
			} else {
				result.Reason = "inline program may load unreviewed modules"
			}
		case types.ConfigFile:
			if len(dep.Candidates) == 0 {
				if dep.Path != "" {
					inspect(dep.Path, true)
				}
				continue
			}
			for _, candidate := range dep.Candidates {
				raw := candidate.Path
				set := false
				if candidate.NativeHome {
					home, err := i.account()
					if err != nil {
						unresolved("account-home", "native account home cannot be confirmed")
						break
					}
					if home == "" {
						continue
					}
					raw = pathutil.JoinUnclean(home, candidate.Relative)
				} else if candidate.Variable != "" {
					value, exists := x.Env[candidate.Variable]
					set = exists
					if value == "" {
						if !set || candidate.EmptyPath == "" {
							continue
						}
						raw = candidate.EmptyPath
					} else {
						raw = value
						if candidate.Relative != "" {
							raw = pathutil.JoinUnclean(raw, candidate.Relative)
						}
					}
				}
				if raw == "" {
					continue
				}
				exists, stop := inspect(raw, false)
				if exists || stop || candidate.StopWhenSet && set {
					break
				}
			}
		}
	}
	if len(versions) > 0 {
		data, _ := json.Marshal(versions)
		sum := sha256.Sum256(data)
		result.SHA256 = hex.EncodeToString(sum[:])
	}
	return result
}
