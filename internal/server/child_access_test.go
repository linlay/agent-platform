package server

import (
	"context"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	orchestration "agent-platform/internal/runtime/orchestration"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type accessCheckingEngine struct {
	check func(context.Context, api.QueryRequest, contracts.QuerySession)
}

func (e accessCheckingEngine) Stream(ctx context.Context, req api.QueryRequest, session contracts.QuerySession) (contracts.AgentStream, error) {
	e.check(ctx, req, session)
	return &stubOrchestratableStream{finalText: "checked"}, nil
}

func TestChildExecutorUsesOwnPermissionControlAndFrozenTeamDefault(t *testing.T) {
	for _, tc := range []struct {
		name, parent, child string
		team                bool
	}{
		{"ordinary lower", "full_access", "ask_for_approval", false},
		{"ordinary higher", "ask_for_approval", "full_access", false},
		{"frozen member", "ask_for_approval", "full_access", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := contracts.NewRunControl(context.Background(), "root")
			parent.SetInitialAccessLevel(tc.parent)
			defer parent.Finish()
			def := catalog.AgentDefinition{Key: "writer", Mode: "GENERAL", AccessConfig: contracts.AgentAccessConfig{Default: tc.child}}
			engine := accessCheckingEngine{check: func(ctx context.Context, req api.QueryRequest, session contracts.QuerySession) {
				control := contracts.RunControlFromContext(ctx)
				if control == nil || control == parent {
					t.Fatal("child borrowed parent permission control")
				}
				level, _ := control.AccessLevelSnapshot()
				if level != tc.child || req.AccessLevel != level || session.AccessLevel != level {
					t.Fatalf("control=%s req=%s session=%s", level, req.AccessLevel, session.AccessLevel)
				}
				parent.UpdateAccessLevel("auto_approve")
				if after, _ := control.AccessLevelSnapshot(); after != tc.child {
					t.Fatal("parent switch changed member permission")
				}
				plan, err := accesspolicy.BuildPathPlan(config.DefaultAccessPolicyConfig(), session, accesspolicy.WriteAccess, t.TempDir()+"/output.txt")
				if err != nil || (plan.Decision == accesspolicy.DecisionAllow) != (tc.child == "full_access") {
					t.Fatalf("wrong actual tool policy: %+v %v", plan, err)
				}
			}}
			o := newTestFrameOrchestratorWithContext(contracts.WithRunControl(parent.Context(), parent), engine, map[string]catalog.AgentDefinition{"writer": def}, nil, nil)
			o.BuildQuerySession = func(_ context.Context, req runtimetypes.QueryCommand, _ chat.Summary, def catalog.AgentDefinition, _ querySessionBuildOptions) (contracts.QuerySession, error) {
				return contracts.QuerySession{AgentKey: def.Key, AccessLevel: req.AccessLevel}, nil
			}
			if tc.team {
				snapshot := catalog.NewTeamSnapshot(catalog.AgentDefinition{Key: "lead", Mode: "TEAM", TeamConfig: &catalog.TeamConfig{Members: []string{"writer"}}}, map[string]catalog.AgentDefinition{"writer": def})
				o.TeamSnapshot = &snapshot
				// Current registry now disagrees with the member frozen at root start.
				o.Registry = orchestratorRegistry{agents: map[string]catalog.AgentDefinition{"writer": {Key: "writer", Mode: "GENERAL"}}}
			}
			result := o.RunChildTaskWithOptions(0, orchestration.PreparedSubTask{Spec: contracts.SubAgentTaskSpec{TaskAgentKey: "writer", TaskText: "test"}, AgentDef: def, TaskID: "task", RequestID: "request"}, nil, func(stream.StreamInput) {}, orchestration.ChildRunOptions{})
			if result.Status != "completed" {
				t.Fatalf("child failed: %+v", result)
			}
		})
	}
}
