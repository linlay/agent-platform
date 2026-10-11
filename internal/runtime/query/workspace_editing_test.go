package query

import (
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestPlanningQueryPreservesWorkspaceEditingIntentForExecution(t *testing.T) {
	service := &Service{}
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		for _, explicit := range []*bool{nil, boolPointer(false), boolPointer(true)} {
			prepared := preparedQuery{
				Req:      runtimetypes.QueryCommand{Message: "plan", EditingMode: explicit},
				AgentDef: catalog.AgentDefinition{Mode: mode},
				Session:  contracts.QuerySession{Mode: mode, PlanningMode: true, EditingMode: false},
			}
			assembler, _ := service.newAssemblerAndMapper(prepared)
			payload := assembler.BootstrapEmissions()[0].Event.Data().Map()
			want := mode != "KBASE"
			if explicit != nil {
				want = *explicit
			}
			if payload["planningMode"] != true || payload["editingMode"] != want {
				t.Fatalf("%s editing=%v: planned execution intent lost: %#v", mode, explicit, payload)
			}
		}
	}
}

func boolPointer(value bool) *bool { return &value }
