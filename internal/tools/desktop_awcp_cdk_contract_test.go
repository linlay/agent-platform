//go:build awcp_cdk_contract

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopAwcpCurrentCDKContract(t *testing.T) {
	root := os.Getenv("AWCP_CDK_SOURCE")
	if root == "" {
		t.Fatal("AWCP_CDK_SOURCE must point to the CDK source directory")
	}
	for _, relative := range []string{
		"docs/awcp/awcp.schema.json",
		"packages/core/src/lib/awcp/registry.ts",
	} {
		if info, err := os.Stat(filepath.Join(root, relative)); err != nil || info.IsDir() {
			t.Fatalf("missing CDK contract source %s: %v", relative, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "docs/awcp/examples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var examples struct {
		ManualIndex     map[string]any `json:"manualIndex"`
		ManualSection   map[string]any `json:"manualSection"`
		ManualRequest   map[string]any `json:"manualRequest"`
		InvokeRequest   map[string]any `json:"invokeRequest"`
		SuccessResponse map[string]any `json:"successResponse"`
	}
	if err := json.Unmarshal(raw, &examples); err != nil {
		t.Fatal(err)
	}
	examples.ManualIndex["ok"] = true
	examples.ManualIndex["method"] = desktopAwcpGetManualMethod
	if err := validateDesktopAwcpManualResponse(examples.ManualIndex, map[string]any{}); err != nil {
		t.Fatalf("CDK directory rejected: %v", err)
	}
	examples.ManualSection["ok"] = true
	examples.ManualSection["method"] = desktopAwcpGetManualMethod
	if err := validateDesktopAwcpManualResponse(examples.ManualSection, examples.ManualRequest); err != nil {
		t.Fatalf("CDK section rejected: %v", err)
	}
	requestID, _ := examples.InvokeRequest["requestId"].(string)
	failed, err := validateDesktopAwcpResponse(examples.SuccessResponse, requestID, examples.InvokeRequest)
	if err != nil || failed {
		t.Fatalf("CDK response rejected: failed=%v err=%v", failed, err)
	}
}
