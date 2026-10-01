package catalog

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSkillPackageMemberIDCompatibility(t *testing.T) {
	for _, input := range []string{`{"id":"pdf","extension":{"x":1}}`, `{"key":"pdf","extension":{"x":1}}`, `{"id":"pdf","key":"pdf","extension":{"x":1}}`} {
		var member SkillPackageMember
		if err := json.Unmarshal([]byte(input), &member); err != nil {
			t.Fatal(err)
		}
		if member.ID != "pdf" {
			t.Fatalf("identity lost: %#v", member)
		}
		encoded, err := json.Marshal(member)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"key"`) || !strings.Contains(string(encoded), `"id":"pdf"`) || !strings.Contains(string(encoded), `"extension"`) {
			t.Fatalf("noncanonical member: %s", encoded)
		}
	}
	for _, input := range []string{`{"id":"pdf","key":"other"}`, `{"id":"pdf","key":1}`} {
		var member SkillPackageMember
		if json.Unmarshal([]byte(input), &member) == nil {
			t.Fatalf("accepted ambiguous member: %s", input)
		}
	}
}

func TestSkillArchiveIDCompatibility(t *testing.T) {
	for _, fields := range []string{"id: pdf", "key: pdf", "id: pdf\nkey: pdf"} {
		archive := nestedPackageZIP(t, map[string]string{"SKILL.md": "---\nname: old-name\n" + fields + "\n---\nBody"})
		id, err := DetectEditableSkillArchiveID(bytes.NewReader(archive), int64(len(archive)))
		if err != nil || id != "pdf" {
			t.Fatalf("%s: id=%s err=%v", fields, id, err)
		}
	}
	archive := nestedPackageZIP(t, map[string]string{"SKILL.md": "---\nname: pdf\nid: pdf\nkey: other\n---\nBody"})
	if _, err := DetectEditableSkillArchiveID(bytes.NewReader(archive), int64(len(archive))); err == nil {
		t.Fatal("accepted conflicting archive identity")
	}
}
