package session

import (
	runtimetypes "agent-platform/internal/runtime/types"
	"context"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/reference"
	"agent-platform/internal/skillsexec"
)

// PrepareRunReferences resolves locator types using trusted catalog/storage
// metadata. Files keep their existing query/steer-specific validation boundary.
func (b *Builder) PrepareRunReferences(ctx context.Context, session contracts.QuerySession, def catalog.AgentDefinition, refs []queryinput.Reference, steer, scripts bool) (result contracts.ReferenceGrant, prepareErr error) {
	defer func() {
		if prepareErr != nil {
			var request *runtimetypes.RequestError
			if !errors.As(prepareErr, &request) {
				prepareErr = &runtimetypes.RequestError{Status: 400, Code: "reference_unavailable", Message: prepareErr.Error()}
			}
		}
	}()
	refs, err := queryinput.NormalizeResourceReferences(refs)
	if err != nil {
		return contracts.ReferenceGrant{}, err
	}
	for kind, limit := range map[string]int{"skill": contracts.MaxReferencedSkills, "kbase": contracts.MaxReferencedKbases, "chat": contracts.MaxReferencedChats} {
		if len(queryinput.ResourceIDs(refs, kind)) > limit {
			return contracts.ReferenceGrant{}, fmt.Errorf("Run accepts at most %d %s references", limit, kind)
		}
	}
	grant := contracts.ReferenceGrant{Scripts: map[string]*skillsexec.Scope{}}
	policy := def.Interaction()
	if session.InteractionConfig != nil {
		policy = *session.InteractionConfig
	}
	def.InteractionConfig = &policy
	remote := IsProxyRoutedAgent(def) || def.Engine == catalog.AgentEngineACP || catalog.AgentIsChannelMode(def.Mode)
	for _, ref := range refs {
		if err := policy.ValidateReference(ref.Type); err != nil {
			return grant, err
		}
		if remote && (ref.Type == "chat" || ref.Type == "kbase" || steer && ref.Type == "skill") {
			return grant, fmt.Errorf("%s references are unsupported by this execution backend", ref.Type)
		}
		if session.SubTaskID != "" && ref.Type == "chat" {
			return grant, fmt.Errorf("chat references are unsupported in child tasks")
		}
		switch ref.Type {
		case "skill":
			if remote {
				grant.References = append(grant.References, queryinput.Reference{Type: "skill", ID: ref.ID})
				continue
			}
			resolved, err := b.ResolveSkills(def, []string{ref.ID})
			if err != nil {
				return grant, err
			}
			roots, err := ReferencedSkillRunAccess(resolved.Skills)
			if err != nil {
				return grant, err
			}
			grant.Roots.ReadRoots = append(grant.Roots.ReadRoots, roots.ReadRoots...)
			grant.Roots.ReadonlyRoots = append(grant.Roots.ReadonlyRoots, roots.ReadonlyRoots...)
			for _, selected := range resolved.Skills {
				grant.References = append(grant.References, queryinput.Reference{Type: "skill", ID: selected.ID, Name: selected.Definition.Name, Path: selected.InstructionsPath, Meta: map[string]any{"description": selected.Definition.Description}})
				if scripts {
					grant.Scripts[selected.ID] = BuildSkillScriptScope(session, catalog.AgentDefinition{}, []ReferencedSkill{selected})
				}
			}
		case "kbase":
			libraries, err := b.ResolveKbases(def, []string{ref.ID})
			if err != nil {
				return grant, err
			}
			library := libraries[0]
			grant.References = append(grant.References, queryinput.Reference{Type: "kbase", ID: library.ID, Name: library.Name, Meta: map[string]any{"description": library.Description, "collections": library.Collections}})
		case "chat":
			if !session.AllowsTool("chat_query") || session.PlanningMode && ContainsTool(session.PlanningExcludeTools, "chat_query") || !contracts.OrdinaryNativeRoot(session) {
				return grant, fmt.Errorf("chat reference requires an available chat_query tool")
			}
			principalCtx := ctx
			if session.Subject != "" {
				principalCtx = WithReferenceSubject(ctx, session.Subject)
			}
			prepared, err := reference.New(b.deps.Chats).PrepareChatReference(principalCtx, session.ChatID, ref)
			if err != nil {
				return grant, err
			}
			grant.References = append(grant.References, prepared)
		case "site":
			prepared, err := reference.PrepareSiteReference(ref)
			if err != nil {
				return grant, err
			}
			grant.References = append(grant.References, prepared)
		default:
			grant.References = append(grant.References, ref)
		}
	}
	return grant, nil
}

// BindReferencePreparer captures the Run's frozen definition and interaction
// policy. The resulting callback prepares a transaction, never commits it.
func (b *Builder) BindReferencePreparer(session *contracts.QuerySession, def catalog.AgentDefinition, scripts bool) {
	frozen := *session
	session.PrepareReferenceSteer = func(req queryinput.SteerRequest) (queryinput.SteerRequest, error) {
		if req.RunID != frozen.RunID || req.ChatID != "" && req.ChatID != frozen.ChatID {
			return req, fmt.Errorf("steer does not match the active Run")
		}
		if strings.TrimSpace(req.Message) == "" && len(req.References) > 0 {
			content := false
			for _, ref := range req.References {
				switch strings.ToLower(strings.TrimSpace(ref.Type)) {
				case "chat", "file", "selection":
					content = true
				}
			}
			if !content {
				return req, fmt.Errorf("message is required for skill, kbase or site references")
			}
		}
		grant, err := b.PrepareRunReferences(context.Background(), frozen, def, req.References, true, scripts)
		if err != nil {
			return req, err
		}
		req.References = grant.References
		req.CommitReferences = func() error { return frozen.ReferencesScope.Commit(grant) }
		return req, nil
	}
}

func WithReferenceSubject(ctx context.Context, subject string) context.Context {
	return reference.WithSubject(ctx, strings.TrimSpace(subject))
}
