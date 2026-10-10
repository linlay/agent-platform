package knowledge

import "fmt"

// ChunkSettings is a sparse source override; pointers distinguish omission from
// explicit zero overlap. ChunkConfig is the fully resolved, character-based value.
type ChunkSettings struct {
	Unit         string `json:"unit,omitempty"`
	Strategy     string `json:"strategy,omitempty"`
	MaxChars     *int   `json:"maxChars,omitempty"`
	OverlapChars *int   `json:"overlapChars,omitempty"`
}

func ChunkSettingsFrom(c ChunkConfig) ChunkSettings {
	return ChunkSettings{Unit: c.Unit, Strategy: c.Strategy, MaxChars: &c.MaxChars, OverlapChars: &c.OverlapChars}
}
func ResolveSourceChunk(library, collection ChunkSettings) (ChunkConfig, error) {
	c := DefaultChunkConfig()
	for _, override := range []ChunkSettings{library, collection} {
		if override.Unit != "" {
			c.Unit = override.Unit
		}
		if override.Strategy != "" {
			c.Strategy = override.Strategy
		}
		if override.MaxChars != nil {
			c.MaxChars = *override.MaxChars
		}
		if override.OverlapChars != nil {
			c.OverlapChars = *override.OverlapChars
		}
	}
	if err := ValidateSourceChunk(c); err != nil {
		return ChunkConfig{}, err
	}
	return c, nil
}
func ValidateChunkSettings(c ChunkSettings) error {
	if c.Unit != "" && c.Unit != ChunkUnitChars {
		return fmt.Errorf("chunk.unit must be chars")
	}
	switch c.Strategy {
	case "", "window", "regex", "structural":
	default:
		return fmt.Errorf("chunk.strategy must be window, regex or structural")
	}
	if c.MaxChars != nil && *c.MaxChars <= 0 {
		return fmt.Errorf("chunk.maxChars must be positive")
	}
	if c.OverlapChars != nil && *c.OverlapChars < 0 {
		return fmt.Errorf("chunk.overlapChars must be nonnegative")
	}
	return nil
}
