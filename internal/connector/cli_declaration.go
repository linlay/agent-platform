package connector

import (
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/shellenv"
)

// ErrInvalidPackage distinguishes declaration errors from unsupported runtime
// capabilities and failures while executing an otherwise valid package.
var ErrInvalidPackage = errors.New("INVALID_PACKAGE")

// CLIEnvironmentDeclaration contains public declarations only. Templates are
// normalized in memory; the installed package is never rewritten.
type CLIEnvironmentDeclaration struct {
	Env       map[string]string
	StaticEnv map[string]string
}

// CLIExecutionEnvironment accepts the component-local protocol and the earlier
// auth_bindings input. Neither input takes precedence over a duplicate name.
func (p Package) CLIExecutionEnvironment() (CLIEnvironmentDeclaration, error) {
	var result CLIEnvironmentDeclaration
	if p.CLI == nil {
		return result, nil
	}
	dynamic, err := cliStringEnvironment(p.CLI, "env")
	if err != nil {
		return result, err
	}
	static, err := cliStringEnvironment(p.CLI, "staticEnv")
	if err != nil {
		return result, err
	}
	allowed := map[string]string{}
	if len(dynamic) > 0 {
		switch p.AuthMode {
		case AuthToken:
			fields, err := TokenFields(p.Manifest)
			if err != nil {
				return result, err
			}
			for _, field := range fields {
				allowed[field.Key] = "configured"
			}
		case AuthOneID:
			allowed["ONEID_TOKEN"] = "configured"
		case AuthOAuth:
			allowed["OAUTH_ACCESS_TOKEN"] = "configured"
		default:
			return result, cliDeclarationError("env", "is not supported by this authentication mode")
		}
	}
	for name, template := range dynamic {
		if err := validateAuthTemplate(template, allowed); err != nil {
			return result, cliDeclarationError("env."+name, "requires a template referencing declared credentials")
		}
		dynamic[name] = p.normalizeCLIAuthTemplate(template)
	}
	for name, template := range p.AuthBindings["cli"].Env {
		if _, exists := dynamic[name]; exists {
			return result, cliDeclarationError("env."+name, "conflicts with auth_bindings.cli.env")
		}
		if !validCLIEnvironmentName(name) {
			return result, cliDeclarationError("auth_bindings.cli.env."+name, "uses an invalid or platform-owned variable")
		}
		if err := validateAuthTemplate(template, p.compatibleAuthVariables()); err != nil {
			return result, cliDeclarationError("auth_bindings.cli.env."+name, "requires a template referencing declared credentials")
		}
		if dynamic == nil {
			dynamic = map[string]string{}
		}
		dynamic[name] = p.normalizeCLIAuthTemplate(template)
	}
	for name, value := range static {
		if strings.Contains(value, "${") {
			return result, cliDeclarationError("staticEnv."+name, "must be a literal value without credential templates")
		}
		if _, exists := dynamic[name]; exists {
			return result, cliDeclarationError("staticEnv."+name, "conflicts with a dynamic environment variable")
		}
	}
	result.Env, result.StaticEnv = dynamic, static
	return result, nil
}

func (p Package) normalizeCLIAuthTemplate(template string) string {
	switch p.AuthMode {
	case AuthOneID:
		return strings.ReplaceAll(template, "${ONEID_TOKEN}", "${AP_ACCESS_TOKEN}")
	case AuthOAuth, AuthMCP:
		return strings.ReplaceAll(template, "${OAUTH_ACCESS_TOKEN}", "${ACCESS_TOKEN}")
	default:
		return template
	}
}

// compatibleAuthVariables keeps published auth_bindings aliases readable while
// cli.json.env itself follows the protocol's public variable names.
func (p Package) compatibleAuthVariables() map[string]string {
	allowed := map[string]string{}
	switch p.AuthMode {
	case AuthToken:
		fields, _ := TokenFields(p.Manifest)
		for _, field := range fields {
			allowed[field.Key] = "configured"
		}
	case AuthOAuth, AuthMCP:
		allowed["ACCESS_TOKEN"], allowed["OAUTH_ACCESS_TOKEN"] = "configured", "configured"
	case AuthOneID:
		allowed["AP_ACCESS_TOKEN"], allowed["ONEID_TOKEN"] = "configured", "configured"
	}
	return allowed
}

func validCLIEnvironmentName(name string) bool {
	if !envName.MatchString(name) || strings.HasPrefix(name, "AP_") || strings.HasSuffix(name, "_CONFIG_DIR") || shellenv.UnsafeOverride(name) {
		return false
	}
	switch name {
	case "CONNECTOR_BIN_DIR", "CONNECTOR_ARCH", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "NPM_CONFIG_PREFIX":
		return false
	}
	return true
}

func cliStringEnvironment(cli map[string]any, key string) (map[string]string, error) {
	raw, exists := cli[key]
	if !exists {
		return nil, nil
	}
	object, ok := cliObject(raw)
	if !ok {
		return nil, cliDeclarationError(key, "must be an object of nonempty strings")
	}
	values := make(map[string]string, len(object))
	for name, rawValue := range object {
		value, ok := rawValue.(string)
		if !ok || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
			return nil, cliDeclarationError(key+"."+name, "must be a nonempty single-line string")
		}
		if !validCLIEnvironmentName(name) {
			return nil, cliDeclarationError(key+"."+name, "uses an invalid or platform-owned variable")
		}
		values[name] = value
	}
	return values, nil
}

// ValidateCLI checks structure without selecting an OS, starting a program or
// reading credentials. Runtime support remains the execution adapter's concern.
func (p Package) ValidateCLI() error {
	if p.CLI == nil {
		return nil
	}
	if err := cliAllowedFields(p.CLI, "", "runtime", "init", "versionCheck", "auth", "status", "unAuth", "statusMatchJson", "statusMatch", "env", "staticEnv", "authWaitForExit", "authUrlDomain", "authSuppressBrowser", "platform", "authQrModal"); err != nil {
		return err
	}
	if raw, exists := p.CLI["runtime"]; exists {
		object, ok := cliObject(raw)
		if !ok {
			return cliDeclarationError("runtime", "must be an object")
		}
		if err := cliAllowedFields(object, "runtime", "type", "version"); err != nil {
			return err
		}
		for _, key := range []string{"type", "version"} {
			if err := cliNonemptyString(object[key], "runtime."+key); err != nil {
				return err
			}
		}
	}
	version, ok := cliObject(p.CLI["versionCheck"])
	if !ok {
		return cliDeclarationError("versionCheck", "must be an object")
	}
	if err := cliAllowedFields(version, "versionCheck", "command", "minVersion", "versionPattern"); err != nil {
		return err
	}
	if err := cliCommandMap(version["command"], "versionCheck.command", false); err != nil {
		return err
	}
	minimum, ok := version["minVersion"].(string)
	if !ok || !validVersion(minimum) {
		return cliDeclarationError("versionCheck.minVersion", "must be a SemVer string")
	}
	if pattern, exists := version["versionPattern"]; exists {
		if err := cliNonemptyString(pattern, "versionCheck.versionPattern"); err != nil {
			return err
		}
	}
	platform, err := cliPlatformExtension(p.CLI)
	if err != nil {
		return err
	}
	deleteConfig := platform["logoutMode"] == "delete-config"
	for _, key := range []string{"init", "status", "unAuth"} {
		if raw, exists := p.CLI[key]; exists {
			// Published delete-config packages use an empty unAuth mapping. They
			// never execute it; the manager removes the isolated config instead.
			if err := cliCommandMap(raw, key, key == "unAuth" && deleteConfig); err != nil {
				return err
			}
		}
	}
	if raw, exists := p.CLI["auth"]; exists {
		if p.AuthMode != AuthDelegated {
			return cliDeclarationError("auth", "is only allowed with auth_mode null")
		}
		if steps, ok := raw.([]any); ok {
			if len(steps) == 0 || len(steps) > 16 {
				return cliDeclarationError("auth", "must contain 1 to 16 steps")
			}
			for _, rawStep := range steps {
				step, ok := cliObject(rawStep)
				if !ok {
					return cliDeclarationError("auth step", "must be an object")
				}
				if err := cliAllowedFields(step, "auth step", "command", "skipIf", "authWaitForExit", "authUrlDomain", "authSuppressBrowser"); err != nil {
					return err
				}
				if err := cliCommandMap(step["command"], "auth step.command", false); err != nil {
					return err
				}
				if skip, exists := step["skipIf"]; exists {
					if err := cliCommandMap(skip, "auth step.skipIf", false); err != nil {
						return err
					}
				}
				if err := cliLoginOptions(step, "auth step"); err != nil {
					return err
				}
			}
		} else if err := cliCommandMap(raw, "auth", false); err != nil {
			return err
		}
		if _, exists := p.CLI["status"]; !exists {
			return cliDeclarationError("auth", "requires status")
		}
		if _, exists := p.CLI["unAuth"]; !exists && !deleteConfig {
			return cliDeclarationError("auth", "requires unAuth")
		}
	}
	if p.AuthMode != AuthDelegated {
		for _, key := range []string{"unAuth", "authWaitForExit", "authUrlDomain", "authSuppressBrowser"} {
			if _, exists := p.CLI[key]; exists {
				return cliDeclarationError(key, "is only allowed with auth_mode null")
			}
		}
	} else if _, auth := p.CLI["auth"]; !auth {
		if _, unAuth := p.CLI["unAuth"]; unAuth {
			return cliDeclarationError("unAuth", "requires auth and status")
		}
	}
	if err := cliLoginOptions(p.CLI, ""); err != nil {
		return err
	}
	if raw, exists := p.CLI["statusMatch"]; exists {
		if err := cliNonemptyString(raw, "statusMatch"); err != nil {
			return err
		}
	}
	if raw, exists := p.CLI["statusMatchJson"]; exists {
		if _, ok := cliObject(raw); !ok {
			return cliDeclarationError("statusMatchJson", "must be an object")
		}
	}
	// authQrModal and platform are published presentation/configuration extensions.
	if raw, exists := p.CLI["authQrModal"]; exists {
		if _, ok := raw.(bool); !ok {
			return cliDeclarationError("authQrModal", "must be a boolean")
		}
	}
	_, err = p.CLIExecutionEnvironment()
	return err
}

func cliPlatformExtension(cli map[string]any) (map[string]any, error) {
	raw, exists := cli["platform"]
	if !exists {
		return nil, nil
	}
	object, ok := cliObject(raw)
	if !ok {
		return nil, cliDeclarationError("platform", "must be an object")
	}
	if err := cliAllowedFields(object, "platform", "command", "configEnv", "logoutMode", "npmPackage", "npmVersion", "entry", "nativeEntry"); err != nil {
		return nil, err
	}
	for key, raw := range object {
		if _, ok := raw.(string); !ok {
			return nil, cliDeclarationError("platform."+key, "must be a string")
		}
	}
	return object, nil
}

func cliLoginOptions(object map[string]any, path string) error {
	for _, key := range []string{"authWaitForExit", "authSuppressBrowser"} {
		if raw, exists := object[key]; exists {
			if _, ok := raw.(bool); !ok {
				return cliDeclarationError(cliFieldPath(path, key), "must be a boolean")
			}
		}
	}
	if raw, exists := object["authUrlDomain"]; exists {
		if err := cliNonemptyString(raw, cliFieldPath(path, "authUrlDomain")); err != nil {
			return err
		}
		domain := raw.(string)
		if domain != strings.TrimSpace(domain) || strings.ContainsAny(domain, ":/?#@*\\ \t\r\n") {
			return cliDeclarationError(cliFieldPath(path, "authUrlDomain"), "must be a single hostname without scheme, port or wildcard")
		}
	}
	return nil
}

func cliCommandMap(raw any, path string, allowEmpty bool) error {
	object, ok := cliObject(raw)
	if !ok || len(object) == 0 {
		return cliDeclarationError(path, "must be a nonempty OS command mapping")
	}
	if err := cliAllowedFields(object, path, "darwin", "linux", "win32"); err != nil {
		return err
	}
	for os, raw := range object {
		if allowEmpty {
			if value, ok := raw.(string); ok && value == "" {
				continue
			}
		}
		if err := cliNonemptyString(raw, cliFieldPath(path, os)); err != nil {
			return err
		}
	}
	return nil
}

func cliAllowedFields(object map[string]any, path string, fields ...string) error {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	for key := range object {
		if !allowed[key] {
			return cliDeclarationError(cliFieldPath(path, key), "is not a supported field")
		}
	}
	return nil
}

func cliObject(raw any) (map[string]any, bool) {
	switch object := raw.(type) {
	case map[string]any:
		return object, object != nil
	case map[string]string:
		if object == nil {
			return nil, false
		}
		result := make(map[string]any, len(object))
		for key, value := range object {
			result[key] = value
		}
		return result, true
	default:
		return nil, false
	}
}

func cliNonemptyString(raw any, path string) error {
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
		return cliDeclarationError(path, "must be a nonempty string")
	}
	return nil
}

func cliFieldPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func cliDeclarationError(path, message string) error {
	return fmt.Errorf("%w: cli.json %s %s", ErrInvalidPackage, path, message)
}
