package server

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/runtime/runexec"
)

type RunExecutorParams = runexec.NativeOptions

var broadcastRunCompletion = runexec.BroadcastCompletion
var persistedRunMode = runexec.PersistedRunMode

func (s *Server) broadcastRunCompletionNotifications(completion chat.RunCompletion) {
	if s == nil {
		return
	}
	broadcastRunCompletion(RunExecutorParams{
		Chats:         s.deps.Chats,
		Notifications: s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
	}, completion)
}

var handleAwaitingLifecycle = runexec.HandleAwaitingLifecycle
var rawAwaitingIDForTask = runexec.RawAwaitingIDForTask
var maybeBroadcastInterruptedAwaiting = runexec.MaybeBroadcastInterruptedAwaiting

type awaitingTracker = runexec.AwaitingTracker
