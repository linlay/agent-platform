package contracts

type NotificationSink interface {
	Broadcast(eventType string, data map[string]any)
}

type noopNotificationSink struct{}

func NewNoopNotificationSink() NotificationSink {
	return noopNotificationSink{}
}

func (noopNotificationSink) Broadcast(string, map[string]any) {}
