package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (c *Config) applyEnv(options LoadOptions) {
	if strings.TrimSpace(options.Port) == "" {
		c.Server.Port = stringEnv("SERVER_PORT", c.Server.Port)
	}

	c.Paths.RegistriesDir = pathEnv("AP_RUNTIME_REGISTRIES_DIR", c.Paths.RegistriesDir)
	c.Paths.ChatsDir = pathEnv("AP_RUNTIME_CHATS_DIR", c.Paths.ChatsDir)
	c.Paths.MemoryDir = pathEnv("AP_RUNTIME_MEMORY_DIR", c.Paths.MemoryDir)
	c.Paths.KBaseDir = pathEnv("AP_RUNTIME_KBASE_DIR", c.Paths.KBaseDir)
	c.Paths.PanDir = pathEnv("AP_RUNTIME_PAN_DIR", c.Paths.PanDir)

	c.Providers.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "providers"))
	c.Models.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "models"))

	c.ResourceTicket.Secret = stringEnv("AP_CHAT_RESOURCE_TICKET_SECRET", c.ResourceTicket.Secret)

	c.Memory.StorageDir = pathEnv("AP_RUNTIME_MEMORY_DIR", c.Memory.StorageDir)
	c.Logging.LLMInteraction.ConsoleCategories = csvEnv("AP_DEBUG_LLM_CONSOLE", c.Logging.LLMInteraction.ConsoleCategories)
	c.Logging.LLMInteraction.RecordEnabled = boolEnv("AP_DEBUG_LLM_CHAT_RECORD", c.Logging.LLMInteraction.RecordEnabled)

	c.ContainerHub.BaseURL = stringEnv("AP_CONTAINER_HUB_BASE_URL", c.ContainerHub.BaseURL)
}

func stringEnv(key string, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return fallback
}

func pathEnv(key string, fallback string) string {
	value := stringEnv(key, fallback)
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return filepath.Clean(value)
}

func validateExplicitDirEnv(key string, path string) error {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s does not exist: %s", key, path)
		}
		return fmt.Errorf("stat %s (%s): %w", key, path, err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", key, path)
	}
	return nil
}

func boolEnv(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return parseBool(strings.TrimSpace(raw), fallback)
}

func csvEnv(key string, fallback []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return splitCSV(raw)
}

func splitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
	}
	parts := strings.Split(raw, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.Trim(strings.TrimSpace(part), `"'`)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func parseBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func parseInt(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	var value int
	sign := 1
	for i, ch := range raw {
		if i == 0 && ch == '-' {
			sign = -1
			continue
		}
		if ch < '0' || ch > '9' {
			return fallback
		}
		value = value*10 + int(ch-'0')
	}
	return sign * value
}

func anyValue(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func stringValue(value any, fallback string) string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case int64:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	default:
		return fallback
	}
}

func boolValue(value any, fallback bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return parseBool(v, fallback)
	default:
		return fallback
	}
}

func intValue(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		return parseInt(v, fallback)
	default:
		return fallback
	}
}

func int64Value(value any, fallback int64) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		return int64(parseInt(v, int(fallback)))
	default:
		return fallback
	}
}

func floatValue(value any, fallback float64) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		return parseFloat(v, fallback)
	default:
		return fallback
	}
}

func listValue(value any, fallback []string) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			text := stringValue(item, "")
			if text != "" {
				items = append(items, text)
			}
		}
		return items
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return []string{strings.TrimSpace(v)}
	default:
		return fallback
	}
}

func parseFloat(raw string, fallback float64) float64 {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	var parsed float64
	_, err := fmt.Sscanf(value, "%f", &parsed)
	if err != nil {
		return fallback
	}
	return parsed
}

func csvOrList(value any, fallback []string) []string {
	switch v := value.(type) {
	case string:
		items := splitCSV(v)
		if len(items) == 0 {
			return fallback
		}
		return items
	case []any, []string:
		return listValue(v, fallback)
	default:
		return fallback
	}
}
