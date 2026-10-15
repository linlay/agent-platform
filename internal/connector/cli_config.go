package connector

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// cli.json follows the WorkBuddy field set. authQrModal and platform are accepted
// but ignored; connector.json authBrowser controls authorization presentation.
// Other unknown fields are rejected so misspelled declarations never silently
// change behavior.
var cliFields = map[string]bool{
	"runtime": true, "init": true, "npmRegistry": true, "npmRegistries": true, "versionCheck": true,
	"auth": true, "status": true, "unAuth": true, "statusMatch": true, "statusMatchJson": true, "env": true,
	"authUrlDomain": true, "authWaitForExit": true, "authSuppressBrowser": true, "authDeviceFlow": true,
	"authQrModal": true, "platform": true,
}

// Per-step auth fields override the top-level defaults of the same name.
var cliAuthStepFields = map[string]bool{
	"command": true, "skipIf": true, "authUrlDomain": true, "authWaitForExit": true, "authSuppressBrowser": true, "authDeviceFlow": true,
}

var cliDomain = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)

// MCPPreAuthCLI starts the package's MCP components only after CLI login.
const MCPPreAuthCLI = "cli"

func validateCLIConfig(p Package) error {
	if p.CLI == nil {
		return nil
	}
	if err := knownFields("cli.json", p.CLI, cliFields); err != nil {
		return err
	}
	if runtime, exists := p.CLI["runtime"]; exists {
		fields, ok := runtime.(map[string]any)
		if !ok {
			return fmt.Errorf("cli.json runtime must be an object")
		}
		if err := knownFields("cli.json runtime", fields, map[string]bool{"type": true, "version": true}); err != nil {
			return err
		}
	}
	if raw, exists := p.CLI["versionCheck"]; exists {
		fields, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("cli.json versionCheck must be an object")
		}
		if err := knownFields("cli.json versionCheck", fields, map[string]bool{"command": true, "minVersion": true, "versionPattern": true}); err != nil {
			return err
		}
	} else if _, exists := p.CLI["init"]; exists {
		return fmt.Errorf("cli.json init requires versionCheck")
	}
	if err := validateNPMRegistries(p.CLI); err != nil {
		return err
	}
	if err := validateAuthBehavior("cli.json", p.CLI); err != nil {
		return err
	}
	auth, managed := p.CLI["auth"]
	if !managed {
		for _, key := range []string{"unAuth", "authUrlDomain", "authWaitForExit", "authSuppressBrowser", "authDeviceFlow"} {
			if _, exists := p.CLI[key]; exists {
				return fmt.Errorf("cli.json %s requires auth", key)
			}
		}
		// Token packages may verify a candidate credential with status alone.
		_, status := p.CLI["status"]
		for _, key := range []string{"statusMatch", "statusMatchJson"} {
			if _, exists := p.CLI[key]; exists && !status {
				return fmt.Errorf("cli.json %s requires status", key)
			}
		}
		if status && p.AuthMode != AuthToken {
			return fmt.Errorf("cli.json status without auth requires authMode token")
		}
		return nil
	}
	if p.AuthMode != AuthDelegated {
		return fmt.Errorf("cli.json auth requires authMode self")
	}
	if steps, ok := auth.([]any); ok {
		if len(steps) == 0 || len(steps) > 16 {
			return fmt.Errorf("cli.json auth requires 1 to 16 steps")
		}
		for i, raw := range steps {
			step, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("cli.json auth[%d] must be an object", i)
			}
			name := fmt.Sprintf("cli.json auth[%d]", i)
			if err := knownFields(name, step, cliAuthStepFields); err != nil {
				return err
			}
			if _, exists := step["command"]; !exists {
				return fmt.Errorf("%s requires command", name)
			}
			if err := validateAuthBehavior(name, step); err != nil {
				return err
			}
		}
	} else if _, ok := auth.(map[string]any); !ok {
		return fmt.Errorf("cli.json auth must be an OS command map or an array of steps")
	}
	for _, key := range []string{"status", "unAuth"} {
		if _, exists := p.CLI[key]; !exists {
			return fmt.Errorf("cli.json auth requires %s", key)
		}
	}
	return nil
}

func validateNPMRegistries(cli map[string]any) error {
	var registries []any
	if raw, exists := cli["npmRegistry"]; exists {
		if _, both := cli["npmRegistries"]; both {
			return fmt.Errorf("cli.json declares both npmRegistry and npmRegistries")
		}
		registries = []any{raw}
	} else if raw, exists := cli["npmRegistries"]; exists {
		list, ok := raw.([]any)
		if !ok || len(list) == 0 || len(list) > 8 {
			return fmt.Errorf("cli.json npmRegistries must list 1 to 8 registries")
		}
		registries = list
	}
	if len(registries) > 0 {
		if _, exists := cli["init"]; !exists {
			return fmt.Errorf("cli.json npm registries require init")
		}
	}
	for _, raw := range registries {
		value, _ := raw.(string)
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("cli.json npm registry must be an https URL")
		}
	}
	return nil
}

func validateAuthBehavior(name string, fields map[string]any) error {
	if raw, exists := fields["authUrlDomain"]; exists {
		if domain, _ := raw.(string); !cliDomain.MatchString(domain) {
			return fmt.Errorf("%s authUrlDomain must be a lowercase domain", name)
		}
	}
	for _, key := range []string{"authWaitForExit", "authSuppressBrowser"} {
		if raw, exists := fields[key]; exists {
			if _, ok := raw.(bool); !ok {
				return fmt.Errorf("%s %s must be boolean", name, key)
			}
		}
	}
	raw, exists := fields["authDeviceFlow"]
	if !exists {
		return nil
	}
	flow, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s authDeviceFlow must be an object", name)
	}
	if err := knownFields(name+" authDeviceFlow", flow, map[string]bool{"uriPattern": true, "codePattern": true, "defaultExpiresInSeconds": true, "codeEmbeddedInUri": true}); err != nil {
		return err
	}
	for _, key := range []string{"uriPattern", "codePattern"} {
		value, exists := flow[key]
		if !exists && key == "codePattern" {
			continue
		}
		pattern, _ := value.(string)
		if pattern == "" {
			return fmt.Errorf("%s authDeviceFlow.%s must be a regular expression", name, key)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s authDeviceFlow.%s is invalid: %v", name, key, err)
		}
	}
	if raw, exists := flow["defaultExpiresInSeconds"]; exists {
		seconds, ok := raw.(float64)
		if !ok || seconds < 30 || seconds > 3600 || seconds != float64(int(seconds)) {
			return fmt.Errorf("%s authDeviceFlow.defaultExpiresInSeconds must be 30 to 3600", name)
		}
	}
	if raw, exists := flow["codeEmbeddedInUri"]; exists {
		if _, ok := raw.(bool); !ok {
			return fmt.Errorf("%s authDeviceFlow.codeEmbeddedInUri must be boolean", name)
		}
	}
	return nil
}

func validateMCPPreAuth(p Package) error {
	switch p.MCPPreAuth {
	case "":
		return nil
	case MCPPreAuthCLI:
		if p.AuthMode != AuthDelegated || !p.ManagedCLI() {
			return fmt.Errorf("mcp.json preAuth cli requires authMode self and cli.json auth")
		}
		return nil
	}
	return fmt.Errorf("mcp.json preAuth must be cli")
}

func knownFields(name string, fields map[string]any, allowed map[string]bool) error {
	var unknown []string
	for key := range fields {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("%s has unsupported field %s", name, strings.Join(unknown, ", "))
}
