package agent

import "testing"

func TestRejectRetiredEditingTemplateVariable(t *testing.T) {
	for _, prompt := range []string{"{{editing_mode}}", "editing={{ editing_mode }}", "{{\nediting_mode\t}}"} {
		if err := ValidatePromptTemplate(prompt); err == nil {
			t.Fatalf("retired editing placeholder accepted: %q", prompt)
		}
	}
	if err := ValidatePromptTemplate("Workspace {{workspace_dir}}"); err != nil {
		t.Fatal(err)
	}
	if _, exists := CommonPromptValues(PromptContext{})["editing_mode"]; exists {
		t.Fatal("Run editing state is still exposed to system templates")
	}
}
