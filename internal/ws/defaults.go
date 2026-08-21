package ws

import "time"

const (
	defaultMaxMessageSizeBytes  = 1 << 20
	defaultPingInterval         = 30
	defaultPongTimeout          = 60
	defaultHeartbeatInterval    = 30
	defaultClientSilenceTimeout = 100
	defaultWriteTimeout         = 15
	defaultWriteQueueSize       = 256
	defaultMaxObservesPerConn   = 8
	defaultWriteQueueFullGrace  = 5 * time.Second
)

const ProtocolVersion = 2
