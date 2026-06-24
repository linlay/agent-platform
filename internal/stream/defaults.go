package stream

type RenderConfig struct {
	FlushInterval        int64 // seconds; 0 means disabled
	MaxBufferedChars     int
	MaxBufferedEvents    int
	HeartbeatPassThrough bool
}

func DefaultRenderConfig() RenderConfig {
	return RenderConfig{
		FlushInterval:        0,
		MaxBufferedChars:     0,
		MaxBufferedEvents:    0,
		HeartbeatPassThrough: true,
	}
}
