package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
	sandboxservice "agent-platform/internal/sandbox"
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/types"
)

type opaqueFailureSandbox struct {
	err             error
	opens, attempts int
}

func (s *opaqueFailureSandbox) OpenIfNeeded(context.Context, *ExecutionContext) error {
	s.opens++
	return nil
}
func (s *opaqueFailureSandbox) CloseQuietly(*ExecutionContext) {}
func (s *opaqueFailureSandbox) Execute(context.Context, *ExecutionContext, string, string, int64, map[string]string) (SandboxExecutionResult, error) {
	s.attempts++
	return SandboxExecutionResult{Stdout: "approved"}, s.err
}
func opaqueGuestExecutor(client SandboxClient) *RuntimeToolExecutor {
	return &RuntimeToolExecutor{cfg: config.Config{Bash: config.BashConfig{AllowedCommands: []string{"*"}, ShellFeaturesEnabled: true}}, sandbox: client}
}

func TestUnprovenGuestTransportNeverStartsAProbeBeforeHumanReview(t *testing.T) {
	client := &opaqueFailureSandbox{}
	executor := opaqueGuestExecutor(client)
	ctx := sandboxBashExecutionContext(t)
	args := map[string]any{"command": "curl -q -o /dev/null https://example.invalid"}
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove} {
		ctx.Session.AccessLevel = level
		p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
		if !p.UnknownDialect || !p.RequiresApproval() {
			t.Fatalf("unproven guest borrowed Bash: %+v", p)
		}
		result, err := executor.invokeSandboxBash(context.Background(), args, ctx)
		if err != nil || result.Error != "bash_access_approval_required" {
			t.Fatalf("unapproved guest executed: %+v %v", result, err)
		}
	}
	if client.opens != 0 || client.attempts != 0 {
		t.Fatal("guest resolver or command started before consent")
	}
	ctx.Session.AccessLevel = AccessLevelFullAccess
	if p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy); !p.UnknownDialect || !p.Allowed() {
		t.Fatal(p)
	}
	ctx.Session.WorkspaceReadOnly = true
	args["cwd"] = "@workspace"
	if p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy); !p.Blocked() {
		t.Fatalf("readonly lost priority: %+v", p)
	}
	ctx.Session.WorkspaceReadOnly = false
	executor.cfg.Bash.ShellFeaturesEnabled = false
	if p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy); !p.Blocked() {
		t.Fatalf("strict mode borrowed unknown grammar: %+v", p)
	}
	if client.opens != 0 || client.attempts != 0 {
		t.Fatal("preflight started guest code")
	}
}

func TestGuestApprovalRefundNeedsPositiveNoStartEvidence(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			failure := fmt.Errorf("transport result unknown")
			if known {
				failure = fmt.Errorf("%w: local preparation failed", ErrSandboxCommandNotStarted)
			}
			client := &opaqueFailureSandbox{err: failure}
			executor := opaqueGuestExecutor(client)
			ctx := sandboxBashExecutionContext(t)
			ctx.Session.AccessLevel = AccessLevelAskForApproval
			args := map[string]any{"command": "echo approved"}
			p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
			shellplan.RegisterExactApproval(ctx, p.Fingerprint)
			result, err := executor.invokeSandboxBash(context.Background(), args, ctx)
			if err != nil || result.Error != "sandbox_execute_failed" || client.attempts != 1 {
				t.Fatalf("dispatch %+v %v", result, err)
			}
			if remaining := ctx.AccessPolicyApprovals[p.Fingerprint]; (remaining == 1) != known {
				t.Fatalf("refund without correct start evidence: %d", remaining)
			}
		})
	}
}

type mutateAfterLaunchReview struct {
	*sandboxservice.ContainerHubSandboxService
	scope  *runenv.Scope
	mutate bool
	t      *testing.T
}

func (s *mutateAfterLaunchReview) ReviewShellLaunch(c *ExecutionContext, command, cwd string, env map[string]string) (types.LaunchBinding, error) {
	binding, err := s.ContainerHubSandboxService.ReviewShellLaunch(c, command, cwd, env)
	if s.mutate {
		if _, mutationErr := s.scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationSet, Name: "REVIEW_VALUE", Value: "after"}); mutationErr != nil {
			s.t.Fatal(mutationErr)
		}
	}
	return binding, err
}

func TestContainerHubLoginShellReviewIsOpaqueAndFreezesPayloadEnvironment(t *testing.T) {
	var requests atomic.Int32
	payloads := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "decode", 400)
			return
		}
		payloads <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"exit_code":0,"stdout":"approved"}`))
	}))
	defer server.Close()
	cfg := config.ContainerHubConfig{Enabled: true, BaseURL: server.URL, DefaultEnvironmentID: "fixture", RequestTimeout: 1}
	service := sandboxservice.NewContainerHubSandboxService(cfg, config.PathsConfig{})
	scope := runenv.NewScope(runenv.Limits{})
	defer scope.Destroy()
	if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationSet, Name: "REVIEW_VALUE", Value: "before"}); err != nil {
		t.Fatal(err)
	}
	adapter := &mutateAfterLaunchReview{ContainerHubSandboxService: service, scope: scope, t: t}
	executor := opaqueGuestExecutor(adapter)
	ctx := sandboxBashExecutionContext(t)
	ctx.Session.AccessLevel = AccessLevelAskForApproval
	ctx.Session.RunID = "run"
	ctx.Session.AgentKey = "agent"
	ctx.SandboxSession = &SandboxSession{SessionID: "existing", EnvironmentID: "fixture", Level: "run"}
	ctx.RunEnvironment = scope
	command := " \n echo approved\n "
	args := map[string]any{"command": command}
	p := executor.ReviewBashAccess(context.Background(), args, ctx, executor.cfg.AccessPolicy)
	if !p.UnknownDialect || !p.RequiresApproval() || p.Launch == nil || p.Launch.Executable != "/bin/sh" || len(p.Launch.Template) != 2 || p.Launch.Template[0] != "-lc" || !strings.Contains(p.Launch.Reason, "not attested") {
		t.Fatalf("actual transport misclassified: %+v", p)
	}
	if requests.Load() != 0 {
		t.Fatal("review opened a session or ran a guest probe")
	}
	shellplan.RegisterExactApproval(ctx, p.Fingerprint)
	adapter.mutate = true
	result, err := executor.invokeSandboxBash(context.Background(), args, ctx)
	if err != nil || result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("approved opaque launch %+v %v", result, err)
	}
	payload := <-payloads
	argv, _ := payload["args"].([]any)
	env, _ := payload["env"].(map[string]any)
	if payload["command"] != "/bin/sh" || len(argv) != 2 || argv[0] != "-lc" || argv[1] != command || env["REVIEW_VALUE"] != "before" {
		t.Fatalf("actual payload diverged from approved source/environment: %#v", payload)
	}
	if len(ctx.AccessPolicyApprovals) != 0 || requests.Load() != 1 {
		t.Fatal("opaque exact approval was not spent once")
	}
}
