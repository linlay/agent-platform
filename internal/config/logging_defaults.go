package config

const (
	defaultLoggingEnabled                   = true
	defaultSSELoggingEnabled                = false
	defaultLLMInteractionMaskSensitive      = false
	defaultLLMInteractionRecordEnabled      = false
	defaultLLMInteractionConsoleCategoryReq = "request"
	defaultLLMInteractionConsoleCategoryUse = "usage"
)

func defaultLoggingConfig(chatsDir string, memoryDir string) LoggingConfig {
	return LoggingConfig{
		Request:   ToggleConfig{Enabled: defaultLoggingEnabled},
		Auth:      ToggleConfig{Enabled: defaultLoggingEnabled},
		Exception: ToggleConfig{Enabled: defaultLoggingEnabled},
		Tool:      ToggleConfig{Enabled: defaultLoggingEnabled},
		Action:    ToggleConfig{Enabled: defaultLoggingEnabled},
		Viewport:  ToggleConfig{Enabled: defaultLoggingEnabled},
		SSE:       ToggleConfig{Enabled: defaultSSELoggingEnabled},
		Memory: MemoryLoggingConfig{
			Enabled: defaultLoggingEnabled,
			File:    memoryLogFileDefault(memoryDir),
		},
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
