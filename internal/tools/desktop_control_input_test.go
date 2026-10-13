package tools

import (
	. "agent-platform/internal/contracts"
	"context"
	"fmt"
	"reflect"
	"strconv"
	"testing"
)

func TestDesktopPinBooleanCompatibilityBeforeReviewAndDispatch(t *testing.T) {
	for _, pinned := range []bool{true, false} {
		for _, reviewString := range []bool{false, true} {
			for _, executionString := range []bool{false, true} {
				t.Run(fmt.Sprintf("pinned=%t/reviewString=%t/executionString=%t", pinned, reviewString, executionString), func(t *testing.T) {
					executor, e, invoker := desktopCDPParamsTestRuntime(t.TempDir())
					e.CurrentToolID = "pin-call"
					entryKey := "webapp:webapp-0123456789abcdef"
					if !pinned {
						entryKey = "website:docs"
					}
					pinArgs := func(value any) map[string]any {
						return map[string]any{"action": "site.setPinned", "args": map[string]any{"entryKey": entryKey, "pinned": value}}
					}
					var reviewValue, executionValue any = pinned, pinned
					if reviewString {
						reviewValue = strconv.FormatBool(pinned)
					}
					if executionString {
						executionValue = strconv.FormatBool(pinned)
					}
					plan, err := executor.PrepareToolApproval(context.Background(), "desktop_site", pinArgs(reviewValue), e)
					if err != nil || plan == nil {
						t.Fatalf("pin review: %#v %v", plan, err)
					}
					formArgs, _ := plan.Form["args"].(map[string]any)
					if formArgs["pinned"] != pinned || plan.Fingerprint != desktopControlFingerprint(e, "desktop_site", pinArgs(pinned)) {
						t.Fatalf("review did not use native boolean: %#v", plan)
					}
					result, err := executor.Invoke(context.Background(), "desktop_site", pinArgs(executionValue), e)
					if err != nil || result.Error != "approval_required" {
						t.Fatalf("unapproved: %#v %v", result, err)
					}
					e.ToolApprovals = map[string]bool{plan.Fingerprint: true}
					result, err = executor.Invoke(context.Background(), "desktop_site", pinArgs(!pinned), e)
					if err != nil || result.Error != "approval_required" {
						t.Fatalf("opposite pin state borrowed approval: %#v %v", result, err)
					}
					// Use fresh arguments so execution cannot rely on review's mutation.
					result, err = executor.Invoke(context.Background(), "desktop_site", pinArgs(executionValue), e)
					if err != nil || result.Error != "" {
						t.Fatalf("approved pin: %#v %v", result, err)
					}
					result, err = executor.Invoke(context.Background(), "desktop_site", pinArgs(executionValue), e)
					if err != nil || result.Error != "approval_required" {
						t.Fatalf("approval reused: %#v %v", result, err)
					}
					_, requests := invoker.snapshots()
					writes := 0
					for _, request := range requests {
						if request.Type == "desktop.site.setPinned" {
							writes++
							if request.Payload["pinned"] != pinned || request.Payload["entryKey"] != entryKey {
								t.Fatalf("dispatch did not use reviewed boolean: %#v", request.Payload)
							}
						}
					}
					if writes != 1 {
						t.Fatalf("pin dispatches: %d", writes)
					}
				})
			}
		}
	}
}

func TestDesktopPinBooleanCompatibilityPreservesInvalidValues(t *testing.T) {
	for _, value := range []any{"TRUE", "False", " true", "false ", "yes", "1", float64(1), nil} {
		t.Run(fmt.Sprintf("%T/%v", value, value), func(t *testing.T) {
			executor, e, invoker := desktopCDPParamsTestRuntime(t.TempDir())
			e.CurrentToolID = "pin-call"
			e.AccessLevel = AccessLevelAutoApprove
			args := map[string]any{"action": "site.setPinned", "args": map[string]any{"entryKey": "website:docs", "pinned": value}}
			plan, err := executor.PrepareToolApproval(context.Background(), "desktop_site", args, e)
			if err != nil || plan == nil {
				t.Fatalf("pin review: %#v %v", plan, err)
			}
			formArgs, _ := plan.Form["args"].(map[string]any)
			if !reflect.DeepEqual(formArgs["pinned"], value) {
				t.Fatalf("invalid value converted: %#v", formArgs)
			}
			native := map[string]any{"action": "site.setPinned", "args": map[string]any{"entryKey": "website:docs", "pinned": true}}
			e.ToolApprovals = map[string]bool{desktopControlFingerprint(e, "desktop_site", native): true}
			result, err := executor.Invoke(context.Background(), "desktop_site", args, e)
			if err != nil || result.Error != "approval_required" {
				t.Fatalf("invalid value borrowed native approval: %#v %v", result, err)
			}
			_, requests := invoker.snapshots()
			if len(requests) != 0 {
				t.Fatalf("invalid invocation dispatched: %#v", requests)
			}
		})
	}
}

func TestDesktopControlBooleanCompatibilityPreservesOpenArgs(t *testing.T) {
	executor, e, invoker := desktopCDPParamsTestRuntime(t.TempDir())
	e.CurrentToolID = "website-call"
	e.AccessLevel = AccessLevelAutoApprove
	params := map[string]any{"id": "docs", "patch": map[string]any{"label": "true", "pinned": "false"}}
	args := map[string]any{"action": "website.update", "args": params}
	plan, err := executor.PrepareToolApproval(context.Background(), "desktop_site", args, e)
	if err != nil || plan == nil || !reflect.DeepEqual(plan.Form["args"], params) {
		t.Fatalf("opaque review arguments changed: %#v %v", plan, err)
	}
	e.ToolApprovals = map[string]bool{plan.Fingerprint: true}
	result, err := executor.Invoke(context.Background(), "desktop_site", args, e)
	if err != nil || result.Error != "" {
		t.Fatalf("approved website update: %#v %v", result, err)
	}
	_, requests := invoker.snapshots()
	want := map[string]any{"id": "docs", "patch": map[string]any{"label": "true", "pinned": "false"}}
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Payload, want) {
		t.Fatalf("opaque dispatch arguments changed: %#v", requests)
	}
}
