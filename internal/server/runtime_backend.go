package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

func runtimeLocalInterruptRequest(command runtimetypes.InterruptCommand, req api.InterruptRequest) api.InterruptRequest {
	if command.Caller.Scope == "server" {
		return interruptRequestWithCause(req, command.Source, command.Reason, command.Detail)
	}
	return httpAPIUserInterruptRequest(req)
}

func submitResultToRuntime(response api.SubmitResponse) runtimetypes.SubmitResult {
	return runtimetypes.SubmitResult{
		Accepted: response.Accepted, Status: response.Status, ChatID: response.ChatID, RunID: response.RunID,
		AwaitingID: response.AwaitingID, SubmitID: response.SubmitID, Continued: response.Continued,
		ErrorCode: response.ErrorCode, Detail: response.Detail,
	}
}

func queryRequestFromRuntime(cmd runtimetypes.QueryCommand) api.QueryRequest {
	references := apiReferencesFromRuntime(cmd.References)
	var scene *api.Scene
	if cmd.Scene != nil {
		scene = &api.Scene{URL: cmd.Scene.URL, Title: cmd.Scene.Title}
	}
	var model *api.QueryModelOptions
	if cmd.Model != nil {
		model = &api.QueryModelOptions{Key: cmd.Model.Key, ModelID: cmd.Model.ModelID, ReasoningEffort: cmd.Model.ReasoningEffort, ServiceTier: cmd.Model.ServiceTier}
	}
	return api.QueryRequest{
		RequestID: cmd.RequestID, RunID: cmd.RunID, ChatID: cmd.ChatID, AgentKey: cmd.AgentKey, TeamID: cmd.TeamID,
		Role: cmd.Role, Hidden: cmd.Hidden, Message: cmd.Message, SourceUser: cmd.SourceUser, References: references,
		Params: contracts.CloneMap(cmd.Params), Scene: scene, Stream: cmd.Stream, IncludeUsage: cmd.IncludeUsage,
		IncludeFullText: cmd.IncludeFullText, PlanningMode: cmd.PlanningMode, EditingMode: cmd.EditingMode,
		MustUseSkills: append([]string(nil), cmd.MustUseSkills...), AccessLevel: cmd.AccessLevel, Model: model,
		SyntheticQueryBootstrapped: cmd.SyntheticQueryBootstrapped, ChatSource: cmd.ChatSource,
		TrustedQueryMetadata: contracts.CloneMap(cmd.TrustedQueryMetadata),
	}
}

func queryCommandFromAPI(req api.QueryRequest) runtimetypes.QueryCommand {
	var scene *runtimetypes.Scene
	if req.Scene != nil {
		scene = &runtimetypes.Scene{URL: req.Scene.URL, Title: req.Scene.Title}
	}
	var model *runtimetypes.QueryModelOptions
	if req.Model != nil {
		model = &runtimetypes.QueryModelOptions{Key: req.Model.Key, ModelID: req.Model.ModelID, ReasoningEffort: req.Model.ReasoningEffort, ServiceTier: req.Model.ServiceTier}
	}
	return runtimetypes.QueryCommand{
		RequestID: req.RequestID, RunID: req.RunID, ChatID: req.ChatID, AgentKey: req.AgentKey, TeamID: req.TeamID,
		Role: req.Role, Hidden: req.Hidden, Message: req.Message, SourceUser: req.SourceUser,
		References: runtimeReferencesFromAPI(req.References), Params: contracts.CloneMap(req.Params), Scene: scene,
		Stream: req.Stream, IncludeUsage: req.IncludeUsage, IncludeFullText: req.IncludeFullText,
		PlanningMode: req.PlanningMode, EditingMode: req.EditingMode, MustUseSkills: append([]string(nil), req.MustUseSkills...),
		AccessLevel: req.AccessLevel, Model: model, SyntheticQueryBootstrapped: req.SyntheticQueryBootstrapped,
		ChatSource: req.ChatSource, TrustedQueryMetadata: contracts.CloneMap(req.TrustedQueryMetadata),
	}
}

func runtimeClientTarget(target contracts.WebClientTarget) runtimetypes.ClientTarget {
	return runtimetypes.ClientTarget{
		SessionID: target.SessionID, BoundaryKey: target.BoundaryKey,
		Subject: target.Subject, SurfaceID: target.SurfaceID,
	}
}

func runtimeReferencesFromAPI(references []api.Reference) []runtimetypes.Reference {
	converted := make([]runtimetypes.Reference, len(references))
	for index, reference := range references {
		converted[index] = runtimetypes.Reference{
			ID: reference.ID, Type: reference.Type, Name: reference.Name, Path: reference.Path,
			MimeType: reference.MimeType, SizeBytes: reference.SizeBytes, URL: reference.URL,
			Text: reference.Text, Annotation: reference.Annotation, AnnotationIndex: reference.AnnotationIndex, SHA256: reference.SHA256, Meta: contracts.CloneMap(reference.Meta),
		}
	}
	return converted
}

func apiReferencesFromRuntime(references []runtimetypes.Reference) []api.Reference {
	converted := make([]api.Reference, len(references))
	for index, reference := range references {
		converted[index] = api.Reference{
			ID: reference.ID, Type: reference.Type, Name: reference.Name, Path: reference.Path,
			MimeType: reference.MimeType, SizeBytes: reference.SizeBytes, URL: reference.URL,
			Text: reference.Text, Annotation: reference.Annotation, AnnotationIndex: reference.AnnotationIndex, SHA256: reference.SHA256, Meta: contracts.CloneMap(reference.Meta),
		}
	}
	return converted
}
