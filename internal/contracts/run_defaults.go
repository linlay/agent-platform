package contracts

import "time"

const (
	defaultRunReaperInterval        = 30 * time.Second
	defaultRunMaxBackgroundDuration = 0
	defaultRunCompletedRetention    = 10 * time.Second
	defaultRunEventBusMaxEvents     = 10000
	defaultRunMaxDisconnectedWait   = 0
	defaultRunMaxObserversPerRun    = 8
)
