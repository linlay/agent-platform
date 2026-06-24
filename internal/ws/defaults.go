package ws

import "time"

const (
	defaultMaxMessageSizeBytes = 1 << 20
	defaultPingInterval        = 30
	defaultWriteTimeout        = 15
	defaultWriteQueueSize      = 256
	defaultMaxObservesPerConn  = 8
	defaultWriteQueueFullGrace = 5 * time.Second
)
