package config

const (
	defaultLoggingEnabled                   = true
	defaultSSELoggingEnabled                = false
	defaultLLMInteractionMaskSensitive      = false
	defaultLLMInteractionRecordEnabled      = false
	defaultLLMInteractionConsoleCategoryReq = "request"
	defaultLLMInteractionConsoleCategoryUse = "usage"
)

func defaultLoggingConfig(chatsDir string) LoggingConfig {
	return LoggingConfig{
		Request:   ToggleConfig{Enabled: defaultLoggingEnabled},
		Auth:      ToggleConfig{Enabled: defaultLoggingEnabled},
		Exception: ToggleConfig{Enabled: defaultLoggingEnabled},
		Tool:      ToggleConfig{Enabled: defaultLoggingEnabled},
		Action:    ToggleConfig{Enabled: defaultLoggingEnabled},
		View:      ToggleConfig{Enabled: defaultLoggingEnabled},
		SSE:       ToggleConfig{Enabled: defaultSSELoggingEnabled},
		LLMInteraction: LLMInteractionLoggingConfig{
			Enabled: defaultLoggingEnabled,
			ConsoleCategories: []string{
				defaultLLMInteractionConsoleCategoryReq,
				defaultLLMInteractionConsoleCategoryUse,
			},
			MaskSensitive: defaultLLMInteractionMaskSensitive,
			RecordEnabled: defaultLLMInteractionRecordEnabled,
			RecordDir:     chatsDir,
		},
	}
}
