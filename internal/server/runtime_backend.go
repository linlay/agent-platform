package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

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
		OriginalQuery: append([]byte(nil), cmd.OriginalQuery...),
		RequestID:     cmd.RequestID, RunID: cmd.RunID, ChatID: cmd.ChatID, AgentKey: cmd.AgentKey,
		Role: cmd.Role, Hidden: cmd.Hidden, Message: cmd.Message, SourceUser: cmd.SourceUser, References: references,
		Params: contracts.CloneMap(cmd.Params), Scene: scene, Stream: cmd.Stream, IncludeUsage: cmd.IncludeUsage,
		IncludeFullText: cmd.IncludeFullText, PlanningMode: cmd.PlanningMode, EditingMode: cmd.EditingMode,
		AccessLevel: cmd.AccessLevel, Model: model, Primary: cmd.Primary,
		SyntheticQueryBootstrapped: cmd.SyntheticQueryBootstrapped, ChatSource: cmd.ChatSource,
		Origin: cmd.Origin.Clone(),
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
		OriginalQuery: append([]byte(nil), req.OriginalQuery...),
		RequestID:     req.RequestID, RunID: req.RunID, ChatID: req.ChatID, AgentKey: req.AgentKey,
		Role: req.Role, Hidden: req.Hidden, Message: req.Message, SourceUser: req.SourceUser,
		References: runtimeReferencesFromAPI(req.References), Params: contracts.CloneMap(req.Params), Scene: scene,
		Stream: req.Stream, IncludeUsage: req.IncludeUsage, IncludeFullText: req.IncludeFullText,
		PlanningMode: req.PlanningMode, EditingMode: req.EditingMode, AccessLevel: req.AccessLevel, Model: model, Primary: req.Primary, SyntheticQueryBootstrapped: req.SyntheticQueryBootstrapped,
		ChatSource: req.ChatSource, Origin: req.Origin.Clone(),
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
