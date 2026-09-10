package coder

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

// prepareSteeredPlanning runs only at a completed tool/model boundary. The
// obsolete finalize_planning call gets its unique result before user messages
// are appended, preserving a valid tool-call/result history for the next model.
func (s *coderPlanningStream) prepareSteeredPlanning(steers []api.SteerRequest) {
	if s.execCtx != nil && s.execCtx.PlanningState != nil {
		plan := s.execCtx.PlanningState
		answer := contracts.AwaitingErrorAnswer("planning", contracts.PlanningSuperseded, "Planning superseded by a new user instruction")
		if s.confirmationAsked {
			s.pending = append(s.pending, contracts.DeltaAwaitingAnswer{
				AwaitingID: s.planningConfirmationAwaitingID(), Answer: answer,
			})
		}
		s.pending = append(s.pending, contracts.DeltaPlanningSuperseded{
			PlanningID: plan.PlanningID, PlanningFile: plan.PlanningFile,
			AwaitingID: s.planningConfirmationAwaitingID(),
		})
		s.appendPlanningConfirmationToolResult(answer)
	}
	s.preparePlanningFeedback(nil)
	s.planningSuperseded = true
	s.confirmationAsked = false
	s.summaryDone = false
	s.completed = false
	for _, steer := range steers {
		if len(steer.PreparedMessages) == 0 {
			steer.PreparedMessages = []map[string]any{{"role": "user", "content": steer.Message}}
		}
		s.pending = append(s.pending, contracts.DeltaRequestSteer{
			RequestID: steer.RequestID, ChatID: steer.ChatID, RunID: steer.RunID,
			SteerID: steer.SteerID, Message: steer.Message, References: steer.References,
			Messages: steer.PreparedMessages,
		})
		for _, message := range steer.PreparedMessages {
			s.executeMessages = append(s.executeMessages, contracts.ModelMessage{Role: "user", Content: message["content"]})
		}
	}
}
