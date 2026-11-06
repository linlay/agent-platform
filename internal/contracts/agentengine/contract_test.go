package agentengine

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestSharedContractExamples(t *testing.T) {
	data, err := os.ReadFile("contract_cases_test.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string          `json:"name"`
		Contract string          `json:"contract"`
		Valid    bool            `json:"valid"`
		Value    json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, example := range cases {
		t.Run(example.Name, func(t *testing.T) {
			err := Validate(example.Contract, example.Value)
			if (err == nil) != example.Valid {
				t.Fatalf("valid=%v, got %v", example.Valid, err)
			}
			if !example.Valid {
				return
			}
			value, err := roundTrip(example.Contract, example.Value)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			if err := json.Unmarshal(example.Value, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("DTO lost or changed public fields: before=%s after=%s", example.Value, encoded)
			}
		})
	}
}

// Decode positive fixtures into concrete DTOs to catch lost JSON fields.
// Construction is test-only; production validation remains Validate.
func roundTrip(name string, raw json.RawMessage) (any, error) {
	targets := map[string]any{
		"PlatformTarget":        new(PlatformTarget),
		"EngineDependency":      new(EngineDependency),
		"EngineLaunch":          new(EngineLaunch),
		"AdapterVersion":        new(AdapterVersion),
		"EngineResource":        new(EngineResource),
		"EnginePluginManifest":  new(EnginePluginManifest),
		"EngineInstallation":    new(EngineInstallation),
		"EngineUpsert":          new(EngineUpsert),
		"EngineSnapshot":        new(EngineSnapshot),
		"EngineRevoke":          new(EngineRevoke),
		"ACPProject":            new(ACPProject),
		"ConfigSelection":       new(ConfigSelection),
		"SessionBinding":        new(SessionBinding),
		"ConfigChoice":          new(ConfigChoice),
		"ExternalConfigOption":  new(ExternalConfigOption),
		"PreparedSession":       new(PreparedSession),
		"ConfigUpdate":          new(ConfigUpdate),
		"TerminalAuthLaunch":    new(TerminalAuthLaunch),
		"AuthMethod":            new(AuthMethod),
		"AuthStatus":            new(AuthStatus),
		"AuthStart":             new(AuthStart),
		"ElicitationRequest":    new(ElicitationRequest),
		"QuestionOption":        new(QuestionOption),
		"ExternalQuestion":      new(ExternalQuestion),
		"EngineError":           new(EngineError),
		"ManagementBootstrap":   new(ManagementBootstrap),
		"ManagementStatus":      new(ManagementStatus),
		"SyncAck":               new(SyncAck),
		"ExecutionBackend":      new(ExecutionBackend),
		"EngineCapabilities":    new(EngineCapabilities),
		"EngineDirectoryEntry":  new(EngineDirectoryEntry),
		"EngineDirectory":       new(EngineDirectory),
		"DirectoryChanged":      new(DirectoryChanged),
		"EpochMillis":           new(EpochMillis),
		"QuestionAnswer":        new(QuestionAnswer),
		"DeliverySource":        new(DeliverySource),
		"TrustedDeliveryRecord": new(TrustedDeliveryRecord),
		"SessionPrepare":        new(SessionPrepare),
		"AuthCancel":            new(AuthCancel),
	}
	target, ok := targets[name]
	if !ok {
		return nil, fmt.Errorf("unknown contract %s", name)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, err
	}
	return target, nil
}

func TestLinuxEngineTargetUsesExistingManifestContract(t *testing.T) {
	if err := Validate("PlatformTarget", []byte(`{"os":"linux","arch":"amd64"}`)); err != nil {
		t.Fatal(err)
	}
}
