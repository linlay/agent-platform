package tools

import (
	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"testing"
)

type approvalInputHandler struct {
	captureNamedToolHandler
	declarations int
}

func (h *approvalInputHandler) ToolApprovalInputPath(_ string, args map[string]any, _ *ExecutionContext) (string, error) {
	h.declarations++
	path, _ := args["input"].(string)
	return path, nil
}

func TestToolRouterDelegatesReviewInputAndEnforcesNativeMount(t *testing.T) {
	backend := &recordingPolicyBackend{defs: []api.ToolDetailResponse{{Name: "catalog_manage"}, {Name: "other"}}}
	router := mustNewToolRouter(t, backend, nil, nil, nil)
	handler := &approvalInputHandler{captureNamedToolHandler: captureNamedToolHandler{names: []string{"catalog_manage"}}}
	if err := router.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"input": "/source/package.zip"}
	if _, err := router.ToolApprovalInputPath("catalog_manage", args, &ExecutionContext{}); err == nil || handler.declarations != 0 {
		t.Fatal("unmounted handler declared input")
	}
	e := &ExecutionContext{Session: QuerySession{ToolNames: []string{"catalog_manage"}, NativeConnectorTools: map[string]string{"catalog_manage": connector.PlatformControlConnectorID}, ConnectorDirs: map[string]string{connector.PlatformControlConnectorID: "/mounted"}}}
	path, err := router.ToolApprovalInputPath("catalog_manage", args, e)
	if err != nil || path != args["input"] || handler.declarations != 1 {
		t.Fatalf("declaration: %s %v", path, err)
	}
	if path, err := router.ToolApprovalInputPath("other", args, e); err != nil || path != "" {
		t.Fatal("ordinary tool acquired a review input")
	}
}
