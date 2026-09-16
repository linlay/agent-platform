package connector

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ValidateExternalCLI enforces the published component contract for new packages.
// Legacy loading remains separate, so upgrades never silently rewrite installed bundles.
func ValidateExternalCLI(pkg Package) error {
	if pkg.Builtin {
		return nil
	}
	if pkg.Type != "cli" && pkg.Type != "mcp" {
		return fmt.Errorf("connector spec requires type cli or mcp")
	}
	if pkg.CLI == nil {
		return nil
	}
	allowed := map[string]bool{}
	for _, key := range []string{"runtime", "init", "versionCheck", "auth", "status", "unAuth", "statusMatchJson", "statusMatch", "env", "staticEnv", "authWaitForExit", "authUrlDomain", "authSuppressBrowser"} {
		allowed[key] = true
	}
	for key := range pkg.CLI {
		if !allowed[key] {
			return fmt.Errorf("unknown CLI field %s", key)
		}
	}
	present := 0
	for _, key := range []string{"auth", "status", "unAuth"} {
		if _, ok := pkg.CLI[key]; ok {
			present++
		}
	}
	if pkg.AuthMode == AuthDelegated && present != 0 && present != 3 {
		return fmt.Errorf("null authentication requires auth, status and unAuth together")
	}
	if pkg.AuthMode != AuthDelegated {
		for _, key := range []string{"auth", "unAuth", "authWaitForExit", "authUrlDomain", "authSuppressBrowser"} {
			if _, ok := pkg.CLI[key]; ok {
				return fmt.Errorf("CLI field %s requires null authentication", key)
			}
		}
	}
	for _, key := range []string{"init", "status", "unAuth"} {
		if value, ok := pkg.CLI[key]; ok {
			if err := validateOSCommands(value); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	for _, key := range []string{"authWaitForExit", "authSuppressBrowser"} {
		if value, ok := pkg.CLI[key]; ok {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be boolean", key)
			}
		}
	}
	for _, key := range []string{"env", "staticEnv"} {
		if raw, ok := pkg.CLI[key]; ok {
			values, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be object", key)
			}
			for name, value := range values {
				text, ok := value.(string)
				if !ok {
					return fmt.Errorf("%s values must be strings", key)
				}
				if !regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`).MatchString(name) || text == "" {
					return fmt.Errorf("invalid CLI environment entry")
				}
				normalized := strings.ToUpper(name)
				if normalized == "CONNECTOR_BIN_DIR" || normalized == "CONNECTOR_ARCH" || normalized == "HOME" || normalized == "USERPROFILE" || strings.HasPrefix(normalized, "XDG_") || normalized == "PATH" {
					return fmt.Errorf("%s cannot override private runtime environment", key)
				}
				if key == "env" {
					var fields []TokenField
					if pkg.AuthMode == AuthToken {
						fields, _ = TokenFields(pkg.Manifest)
					}
					allowed := map[string]bool{}
					for _, field := range fields {
						allowed[field.Key] = true
					}
					if pkg.AuthMode == AuthOneID {
						allowed["ONEID_TOKEN"] = true
					}
					if pkg.AuthMode == AuthOAuth {
						allowed["OAUTH_ACCESS_TOKEN"] = true
					}
					matches := regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`).FindAllStringSubmatch(text, -1)
					if len(matches) == 0 {
						return fmt.Errorf("dynamic CLI value requires credential placeholders")
					}
					for _, match := range matches {
						if !allowed[match[1]] {
							return fmt.Errorf("invalid dynamic CLI credential placeholder")
						}
					}

				}
			}
		}
	}
	return nil
}
func validateOSCommands(raw any) error {
	values, ok := raw.(map[string]any)
	if !ok || len(values) == 0 {
		return fmt.Errorf("OS command object is required")
	}
	for key, value := range values {
		if key != "darwin" && key != "linux" && key != "win32" {
			return fmt.Errorf("unknown OS command %s", key)
		}
		if text, ok := value.(string); !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("OS command must be nonempty string")
		}
	}
	return nil
}

// Required auth_mode is checked against the original JSON before legacy normalizing.
func ValidateSpecManifest(data []byte) error {
	var values map[string]json.RawMessage
	if err := DecodeJSON(data, &values); err != nil {
		return err
	}
	if _, ok := values["auth_mode"]; !ok {
		return fmt.Errorf("auth_mode is required")
	}
	var mode any
	if err := json.Unmarshal(values["auth_mode"], &mode); err != nil {
		return err
	}
	if mode != nil && mode != "token" && mode != "oneid-token" && mode != "oauth" && mode != "mcp" {
		return fmt.Errorf("invalid auth_mode")
	}
	return nil
}
