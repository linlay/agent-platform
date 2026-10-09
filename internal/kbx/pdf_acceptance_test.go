package kbx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/builtins"
	"agent-platform/internal/knowledge"
)

func TestLivePlatformPDFAndExistingDataPreservation(t *testing.T) {
	bin := os.Getenv("KBX_ACCEPTANCE_BIN")
	if bin == "" {
		t.Skip("set KBX_ACCEPTANCE_BIN")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	manager, library := newTestManager(t)
	if err := os.Remove(library.database); err != nil {
		t.Fatal(err)
	}
	pdf := textLayerPDF("OrchardPDF approval requires two reviewers.", "OrchardPDF second page retains evidence.")
	sourcePath := filepath.Join(library.spec.WorkspaceRoot, "manual.pdf")
	if err := os.WriteFile(sourcePath, pdf, 0600); err != nil {
		t.Fatal(err)
	}
	// Existing unrelated files must remain untouched by maintenance.
	sentinels := []string{
		filepath.Join(filepath.Dir(filepath.Dir(library.database)), "docs", "control.db"),
		filepath.Join(filepath.Dir(filepath.Dir(library.database)), "docs", "generations", "legacy", "manifest.json"),
		filepath.Join(filepath.Dir(filepath.Dir(library.database)), "docs", "kbx", "previous-scope", "index.sqlite"),
	}
	for _, path := range sentinels {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("preserve-existing-data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewCenterEngine().Update(context.Background(), library.database, library.definition.Collections); err != nil {
		t.Fatal(err)
	}
	hits, err := manager.Search(context.Background(), "docs", "OrchardPDF", knowledge.SearchOptions{})
	if err != nil || len(hits.Results) == 0 {
		t.Fatalf("PDF search: %+v, %v", hits, err)
	}
	evidence, err := manager.Read("docs", knowledge.ReadOptions{Path: "workspace/manual.pdf", Limit: 100})
	if err != nil || !strings.Contains(evidence.Content, "two reviewers") || !strings.Contains(evidence.Content, "second page retains evidence") {
		t.Fatalf("PDF page evidence: %+v, %v", evidence, err)
	}
	for _, path := range sentinels {
		if b, err := os.ReadFile(path); err != nil || string(b) != "preserve-existing-data" {
			t.Fatalf("existing knowledge data changed: %s, %v", path, err)
		}
	}
	if b, err := os.ReadFile(sourcePath); err != nil || !bytes.Equal(b, pdf) {
		t.Fatalf("PDF source changed: %v", err)
	}
}

// Produce a valid two-page text-layer fixture without depending on a PDF SDK.
func textLayerPDF(first, second string) []byte {
	stream := func(text string) string {
		body := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET\n"
		return fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(body), body)
	}
	page := func(content int) string {
		return fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents %d 0 R >>", content)
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 >>",
		page(5), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		stream(first), page(7), stream(second),
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return out.Bytes()
}
