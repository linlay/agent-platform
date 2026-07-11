package team

import (
	"reflect"
	"testing"
)

func TestDescriptorDefinesInternalTeamMode(t *testing.T) {
	descriptor := Descriptor()
	if descriptor.Mode != Mode || descriptor.MainStage != MainStage || descriptor.MainCacheKey != MainCacheKey {
		t.Fatalf("unexpected descriptor identity %#v", descriptor)
	}
	if descriptor.CreatePrefix != "" {
		t.Fatalf("TEAM must not be creatable as an ordinary agent, prefix=%q", descriptor.CreatePrefix)
	}
	if !descriptor.Capabilities.InvokeChildren || descriptor.Capabilities.RunAsChild || descriptor.Capabilities.FileChangeHooks {
		t.Fatalf("unexpected TEAM capabilities %#v", descriptor.Capabilities)
	}
	if !reflect.DeepEqual(descriptor.Profile.ToolNames, []string{ToolDelegate, ToolInvoke}) {
		t.Fatalf("unexpected TEAM tools %#v", descriptor.Profile.ToolNames)
	}

	descriptor.Profile.ToolNames[0] = "changed"
	descriptor.Profile.Budget["timeout"] = 1
	if got := Descriptor(); got.Profile.ToolNames[0] != ToolDelegate || got.Profile.Budget["timeout"] != 3600 {
		t.Fatalf("descriptor defaults were mutated: %#v", got.Profile)
	}
}

func TestNormalizeMaxParallel(t *testing.T) {
	for _, tc := range []struct {
		input int
		want  int
	}{{0, DefaultMaxParallel}, {-1, DefaultMaxParallel}, {1, 1}, {4, 4}, {6, MaxParallel}} {
		if got := NormalizeMaxParallel(tc.input); got != tc.want {
			t.Fatalf("NormalizeMaxParallel(%d)=%d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestMainSystemInitSpec(t *testing.T) {
	spec := MainSystemInitSpec()
	if spec.CacheKey != MainCacheKey || spec.FingerprintStage != MainStage || spec.PromptStage != MainStage || spec.Mode != MainStage || spec.Stage != "main" {
		t.Fatalf("unexpected system-init spec %#v", spec)
	}
	if !spec.Initial || !spec.UseSharedSystemPrompt || spec.IncludeAfterCallHints {
		t.Fatalf("unexpected system-init flags %#v", spec)
	}
	if !reflect.DeepEqual(spec.ToolNames, DefaultToolNames()) {
		t.Fatalf("system-init tools=%#v", spec.ToolNames)
	}
}
