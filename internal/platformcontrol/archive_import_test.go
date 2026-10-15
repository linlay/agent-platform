package platformcontrol

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func importZIP(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveImportReceiptIsReleasedWhenCallerRunFinishes(t *testing.T) {
	h, e, path := importFixture(t)
	e.RunControl = contracts.NewRunControl(t.Context(), e.Session.RunID)
	defer e.RunControl.Finish()
	importZIP(t, path, map[string]string{"SKILL.md": "---\nname: demo\ndescription: Demo\n---\nUse it."})
	args := importArgs("skill", path, false)
	approveImport(t, h, e, args)
	r, err := h.Invoke(t.Context(), "catalog_manage", args, e)
	if err != nil || r.Error != "" {
		t.Fatalf("import: %#v %v", r, err)
	}
	key := archiveImportInvocation(e)
	request := archiveImportRequestDigest(args["args"].(map[string]any))
	if _, _, found := h.sources.ImportReceipt(key, request); !found {
		t.Fatal("active Run lost its receipt")
	}
	e.RunControl.Finish()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, found := h.sources.ImportReceipt(key, request); !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finished Run retained receipt")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Paths.SkillsCenterDir, "demo", "SKILL.md")); err != nil {
		t.Fatal("receipt cleanup removed installed package")
	}
}

func importFixture(t *testing.T) (*ToolHandler, *contracts.ExecutionContext, string) {
	t.Helper()
	h := connectorControlFixture(t)
	h.cfg.AccessPolicy = config.DefaultAccessPolicyConfig()
	h.sources.Config = h.cfg
	e := controlExecution()
	path := filepath.Join(t.TempDir(), "download.zip")
	e.Session.WorkspaceRoot = filepath.Dir(path)
	return h, e, path
}

func importArgs(typ, path string, overwrite any) map[string]any {
	return map[string]any{"action": "import", "args": map[string]any{"resourceType": typ, "archivePath": path, "overwrite": overwrite}}
}

func approveImport(t *testing.T, h *ToolHandler, e *contracts.ExecutionContext, args map[string]any) *contracts.ToolApproval {
	t.Helper()
	approval, err := h.PrepareToolApproval(t.Context(), "catalog_manage", args, e)
	if err != nil || approval == nil {
		t.Fatalf("prepare import: %#v %v", approval, err)
	}
	e.ToolApprovals = map[string]bool{approval.Fingerprint: true}
	return approval
}

func TestArchiveImportPublishesDetectedIdentityToItsCenter(t *testing.T) {
	for _, typ := range []string{"connector", "skill", "skill-package"} {
		t.Run(typ, func(t *testing.T) {
			h, e, path := importFixture(t)
			files := map[string]string{}
			resourceType, id, target := "skill", "demo", h.cfg.Paths.SkillsCenterDir
			switch typ {
			case "connector":
				resourceType, target = "connector", h.cfg.Paths.EffectiveConnectorsCenterDir()
				files["connector.json"] = `{"id":"demo","name":"Demo","version":"0.1.0","type":"cli","authMode":"self"}`
				files["cli.json"] = `{}`
				files["skills/attached/SKILL.md"] = "---\nname: attached\ndescription: Attached\n---\nUse it."
			case "skill":
				files["wrapper/SKILL.md"] = "---\nid: demo\nname: demo\ndescription: Demo\nversion: 1.0.0\n---\nUse it."
				files["wrapper/assets/data.txt"] = "retained"
			case "skill-package":
				id = "suite"
				files["package.json"] = `{"name":"suite","version":"1.0.0","skills":[{"id":"member"}]}`
				files["member/SKILL.md"] = "---\nname: member\ndescription: Member\n---\nUse it."
			}
			importZIP(t, path, files)
			preparations := 0
			h.sources.PrepareConnector = func(id string) (connectorauth.Preparation, error) {
				preparations++
				return connectorauth.Preparation{ConnectorID: id, Status: "pending"}, nil
			}
			args := importArgs(resourceType, path, "false")
			approval := approveImport(t, h, e, args)
			declared, declareErr := h.ToolApprovalInputPath("catalog_manage", args, e)
			if declareErr != nil || declared != path {
				t.Fatalf("review input: %s %v", declared, declareErr)
			}
			if !approval.AllowAutoApprove || approval.Form["resourceKey"] != id || approval.Form["kind"] != typ {
				t.Fatalf("bad review: %#v", approval)
			}
			if destination, ok := approval.Form["targetPath"].(string); !ok || filepath.Base(destination) != id || filepath.Base(filepath.Dir(destination)) != filepath.Base(target) {
				t.Fatalf("review destination was not backend-selected: %#v", approval.Form["targetPath"])
			}
			if _, err := os.Stat(filepath.Join(target, id)); !os.IsNotExist(err) {
				t.Fatal("review published candidate")
			}
			result, err := h.Invoke(t.Context(), "catalog_manage", args, e)
			if err != nil || result.Error != "" || result.Structured["installed"] != true || result.Structured["id"] != id {
				t.Fatalf("import: %#v %v", result, err)
			}
			primary := "SKILL.md"
			if typ == "connector" {
				primary = "connector.json"
			}
			if typ == "skill-package" {
				primary = "package.json"
			}
			if _, err := os.Stat(filepath.Join(target, id, primary)); err != nil {
				t.Fatal(err)
			}
			if typ == "connector" {
				if _, err := os.Stat(filepath.Join(target, id, "skills", "attached", "SKILL.md")); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(h.cfg.Paths.SkillsCenterDir, "attached")); !os.IsNotExist(err) {
					t.Fatal("connector skill was split into skills-center")
				}
				manifest, err := os.ReadFile(filepath.Join(target, id, "connector.json"))
				if err != nil || !bytes.Contains(manifest, []byte(`"authMode":"self"`)) {
					t.Fatal("delegated authentication changed")
				}
			}
			// The original call can replay its receipt even after its source goes
			// away, without executing package preparation a second time.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			replayed, err := h.Invoke(t.Context(), "catalog_manage", args, e)
			if err != nil || replayed.Error != "" || replayed.Structured["id"] != id {
				t.Fatalf("replay: %#v %v", replayed, err)
			}
			if typ == "connector" && preparations != 1 {
				t.Fatalf("preparations=%d", preparations)
			}
			args["args"].(map[string]any)["overwrite"] = true
			conflict, _ := h.Invoke(t.Context(), "catalog_manage", args, e)
			if conflict.Error == "" || !strings.Contains(conflict.Output, "idempotency_conflict") {
				t.Fatalf("changed arguments replayed: %#v", conflict)
			}
		})
	}
}

func TestArchiveImportCannotReuseChangedContentOrSiblingApproval(t *testing.T) {
	h, e, path := importFixture(t)
	files := map[string]string{"SKILL.md": "---\nname: demo\ndescription: Demo\n---\nOriginal."}
	importZIP(t, path, files)
	args := importArgs("skill", path, false)
	approveImport(t, h, e, args)
	e.CurrentToolID = "sibling"
	result, _ := h.Invoke(t.Context(), "catalog_manage", args, e)
	if result.Error != "approval_required" {
		t.Fatalf("sibling: %#v", result)
	}
	e.CurrentToolID = "call"
	files["SKILL.md"] += " changed"
	importZIP(t, path, files)
	result, _ = h.Invoke(t.Context(), "catalog_manage", args, e)
	if result.Error != "approval_required" {
		t.Fatalf("changed ZIP: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Paths.SkillsCenterDir, "demo")); !os.IsNotExist(err) {
		t.Fatal("unreviewed package published")
	}
}

func TestArchiveImportRechecksWholeInstalledTree(t *testing.T) {
	h, e, path := importFixture(t)
	files := map[string]string{"SKILL.md": "---\nname: demo\ndescription: Demo\n---\nOriginal.", "old.txt": "old"}
	importZIP(t, path, files)
	args := importArgs("skill", path, false)
	approveImport(t, h, e, args)
	if r, _ := h.Invoke(t.Context(), "catalog_manage", args, e); r.Error != "" {
		t.Fatal(r)
	}
	e.CurrentToolID = "replace"
	args = importArgs("skill", path, true)
	approveImport(t, h, e, args)
	if err := os.WriteFile(filepath.Join(h.cfg.Paths.SkillsCenterDir, "demo", "old.txt"), []byte("external edit"), 0644); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Invoke(t.Context(), "catalog_manage", args, e)
	if r.Error != "approval_required" {
		t.Fatalf("stale installed tree: %#v", r)
	}
	delete(files, "old.txt")
	files["new.txt"] = "new"
	importZIP(t, path, files)
	approveImport(t, h, e, args)
	r, _ = h.Invoke(t.Context(), "catalog_manage", args, e)
	if r.Error != "" {
		t.Fatal(r)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Paths.SkillsCenterDir, "demo", "old.txt")); !os.IsNotExist(err) {
		t.Fatal("replacement retained removed files")
	}
}

func TestArchiveImportRollbackAndPreparationFailureAreDistinct(t *testing.T) {
	for _, typ := range []string{"skill", "connector"} {
		t.Run(typ, func(t *testing.T) {
			h, e, path := importFixture(t)
			files := map[string]string{"SKILL.md": "---\nname: demo\ndescription: Demo\n---\nUse it."}
			if typ == "connector" {
				files = map[string]string{"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","authMode":"self"}`, "cli.json": `{}`}
			}
			importZIP(t, path, files)
			args := importArgs(typ, path, false)
			approveImport(t, h, e, args)
			calls := 0
			h.sources.Reload = func(context.Context, string) error {
				calls++
				if calls == 1 {
					return errors.New("reload failed")
				}
				return nil
			}
			r, _ := h.Invoke(t.Context(), "catalog_manage", args, e)
			if r.Error != "control_rolled_back" || r.Structured["executionState"] != "rolled_back" || calls != 2 {
				t.Fatalf("rollback: %#v calls=%d", r, calls)
			}
			e.CurrentToolID = "fresh"
			h.sources.PrepareConnector = func(string) (connectorauth.Preparation, error) {
				return connectorauth.Preparation{}, errors.New("prepare failed")
			}
			approveImport(t, h, e, args)
			r, _ = h.Invoke(t.Context(), "catalog_manage", args, e)
			if r.Error != "" || r.Structured["installed"] != true {
				t.Fatalf("import after rollback: %#v", r)
			}
			if typ == "connector" && r.Structured["preparation"].(map[string]any)["status"] != "failed" {
				t.Fatal("preparation failure lost")
			}
		})
	}
}

func TestArchiveImportEnforcesReadAndRootStageAdmission(t *testing.T) {
	h, e, path := importFixture(t)
	importZIP(t, path, map[string]string{"SKILL.md": "---\nname: demo\ndescription: Demo\n---\nUse it."})
	e.AccessLevel, e.Session.AccessLevel = "default", "default"
	e.Session.WorkspaceRoot = t.TempDir()
	// Remove @temp from this test policy so an external temporary fixture really
	// exercises the same read approval that a user's Documents path requires.
	policy := h.cfg.AccessPolicy.Levels["default"]
	policy.ReadRoots = []string{"@workspace"}
	h.cfg.AccessPolicy.Levels["default"] = policy
	args := importArgs("skill", path, false)
	if _, err := h.PrepareToolApproval(t.Context(), "catalog_manage", args, e); err == nil || !strings.Contains(err.Error(), "archive_read_approval_required") {
		t.Fatalf("unapproved read: %v", err)
	}
	access, err := filetools.BuildAccessPlanFromPolicy(h.cfg.AccessPolicy, e.Session, filetools.ReadAccess, path)
	if err != nil {
		t.Fatal(err)
	}
	filetools.RegisterExactReadApproval(e, access.Fingerprint)
	approveImport(t, h, e, args)
	r, _ := h.Invoke(t.Context(), "catalog_manage", args, e)
	if r.Error != "" || filetools.HasReadApproval(e, access) {
		t.Fatalf("read grant: %#v %#v", r, e.FileReadApprovals)
	}
	for _, alter := range []func(*contracts.ExecutionContext){func(e *contracts.ExecutionContext) { e.Session.SubTaskID = "child" }, func(e *contracts.ExecutionContext) { e.ToolExecutionPolicy = "read_only" }, func(e *contracts.ExecutionContext) { e.Session.NativeConnectorTools = nil }} {
		x := controlExecution()
		alter(x)
		if _, err := h.PrepareToolApproval(t.Context(), "catalog_manage", args, x); err == nil {
			t.Fatal("invalid caller reached import review")
		}
	}
	statePath := filepath.Join(h.cfg.Paths.EffectiveStateDir(), "credentials.zip")
	importZIP(t, statePath, map[string]string{"SKILL.md": "---\nname: secret\ndescription: Secret\n---\nUse it."})
	x := controlExecution()
	x.CurrentToolID = "protected-read"
	if _, err := h.PrepareToolApproval(t.Context(), "catalog_manage", importArgs("skill", statePath, false), x); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("protected source read: %v", err)
	}
	for _, params := range []map[string]any{{"resourceType": "agent", "archivePath": path}, {"resourceType": "skill", "archivePath": path, "targetDir": "/tmp/other"}} {
		if _, err := h.PrepareToolApproval(t.Context(), "catalog_manage", map[string]any{"action": "import", "args": params}, controlExecution()); err == nil {
			encoded, _ := json.Marshal(params)
			t.Fatalf("invalid input accepted: %s", encoded)
		}
	}
}
