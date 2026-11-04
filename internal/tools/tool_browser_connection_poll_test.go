package tools

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	. "agent-platform/internal/contracts"
)

func TestBrowserConnectionPollingWaitsOnlyForRemainingInterval(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pendingDelay time.Duration
		wantGap      time.Duration
	}{
		{"immediate pending", 0, time.Second},
		{"short pending", 250 * time.Millisecond, time.Second},
		{"normal long poll", 20 * time.Second, 20 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var starts []time.Time
				openCalls := 0
				executor, execCtx, _ := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
					if request.Payload["method"] == "Browser.awaitConnection" {
						starts = append(starts, time.Now())
						if len(starts) <= 2 {
							time.Sleep(tc.pendingDelay)
							return map[string]any{"ok": true, "result": map[string]any{"status": "pending"}}
						}
						return map[string]any{"ok": true, "result": map[string]any{"status": "connected"}}
					}
					openCalls++
					if openCalls == 1 {
						return map[string]any{"ok": false, "error": map[string]any{"code": "browser_connection_required", "message": "Connect Chrome."}}
					}
					return map[string]any{"ok": true, "result": map[string]any{"surfaceId": "chrome:1", "provider": "chrome"}}
				})
				before := time.Now()
				result, err := executor.Invoke(context.Background(), "surface_open", map[string]any{"url": "https://example.com/"}, execCtx)
				if err != nil || result.ExitCode != 0 || openCalls != 2 || len(starts) != 3 {
					t.Fatalf("result=%#v err=%v openCalls=%d polls=%d", result, err, openCalls, len(starts))
				}
				for index := 1; index < len(starts); index++ {
					if gap := starts[index].Sub(starts[index-1]); gap != tc.wantGap {
						t.Errorf("poll %d gap=%s, want %s", index, gap, tc.wantGap)
					}
				}
				if elapsed := time.Since(before); elapsed != 2*tc.wantGap {
					t.Errorf("connection wait=%s, want %s", elapsed, 2*tc.wantGap)
				}
			})
		})
	}
}

func TestBrowserConnectionPollingCancellationInterruptsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		openCalls, polls := 0, 0
		executor, execCtx, _ := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
			if request.Payload["method"] == "Browser.awaitConnection" {
				polls++
				go func() {
					time.Sleep(100 * time.Millisecond)
					cancel()
				}()
				return map[string]any{"ok": true, "result": map[string]any{"status": "pending"}}
			}
			openCalls++
			return map[string]any{"ok": false, "error": map[string]any{"code": "browser_connection_required", "message": "Connect Chrome."}}
		})
		before := time.Now()
		result, err := executor.Invoke(ctx, "surface_open", map[string]any{"url": "https://example.com/"}, execCtx)
		if err != nil || result.Error != "browser_connection_required" || openCalls != 1 || polls != 1 {
			t.Fatalf("result=%#v err=%v openCalls=%d polls=%d", result, err, openCalls, polls)
		}
		if elapsed := time.Since(before); elapsed != 100*time.Millisecond {
			t.Fatalf("cancelled connection wait=%s, want immediate cancellation at 100ms", elapsed)
		}
	})
}

func TestBrowserConnectionPollingTerminalStatusReturnsImmediately(t *testing.T) {
	for _, status := range []string{"connected", "failed", "unavailable"} {
		t.Run(status, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				polls := 0
				executor, execCtx, _ := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
					polls++
					return map[string]any{"ok": true, "result": map[string]any{"status": status}}
				})
				before := time.Now()
				connected := executor.awaitWebControlConnection(context.Background(), execCtx)
				if connected != (status == "connected") || polls != 1 || time.Since(before) != 0 {
					t.Fatalf("status=%s connected=%t polls=%d elapsed=%s", status, connected, polls, time.Since(before))
				}
			})
		})
	}
}

func TestBrowserConnectionPollingKeepsItsOverallDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		polls := 0
		executor, execCtx, _ := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
			polls++
			if polls > 300 {
				t.Error("immediate pending replies exceeded the bounded polling rate")
				return map[string]any{"ok": true, "result": map[string]any{"status": "failed"}}
			}
			return map[string]any{"ok": true, "result": map[string]any{"status": "pending"}}
		})
		before := time.Now()
		if executor.awaitWebControlConnection(context.Background(), execCtx) {
			t.Fatal("pending connection was reported as connected")
		}
		if elapsed := time.Since(before); elapsed != 5*time.Minute || polls != 300 {
			t.Fatalf("pending wait=%s polls=%d, want five minutes and 300 polls", elapsed, polls)
		}
	})
}
