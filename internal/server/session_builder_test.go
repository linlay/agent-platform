package server

import (
	"reflect"
	"testing"

	"agent-platform-runner-go/internal/contracts"
)

func TestBuildSessionToolNamesDoesNotAutoAddInvokeAgents(t *testing.T) {
	got := buildSessionToolNames([]string{"_datetime_"}, true)
	want := []string{"_datetime_"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildSessionToolNamesKeepsExplicitInvokeAgents(t *testing.T) {
	got := buildSessionToolNames([]string{"_datetime_", contracts.InvokeAgentsToolName}, true)
	want := []string{"_datetime_", contracts.InvokeAgentsToolName}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildSessionToolNamesFiltersInvokeAgentsWhenDisallowed(t *testing.T) {
	got := buildSessionToolNames([]string{"_datetime_", contracts.InvokeAgentsToolName}, false)
	want := []string{"_datetime_"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}
