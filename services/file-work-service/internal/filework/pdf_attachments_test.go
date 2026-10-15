package filework

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentPolicyStillRejectsActions(t *testing.T) {
	for _, key := range []string{"/AF", "/EF", "/EmbeddedFiles"} {
		benign := map[string]any{key: []any{map[string]any{"/Type": "/EmbeddedFile"}}}
		if !dangerous(benign) || dangerousContent(benign, true) {
			t.Fatal("input/output attachment policies not separated", key)
		}
		for action := range dangerousActions {
			if !dangerousContent(map[string]any{key: map[string]any{"/S": action}}, true) {
				t.Fatal("attachment bypassed action checks", key, action)
			}
		}
	}
	local := map[string]any{"/OpenAction": []any{"4 0 R", "/FitH", nil}}
	if !dangerous(local) || dangerousContent(local, true) {
		t.Fatal("local opening view not accepted for removal")
	}
	for _, value := range []any{map[string]any{"/S": "/JavaScript", "/JS": "script"}, []any{"4 0 R", "/FitH", map[string]any{"/S": "/Launch"}}, []any{"4 0 R", "/JavaScript"}} {
		if !dangerousContent(map[string]any{"/OpenAction": value}, true) {
			t.Fatal("non-local opening action allowed")
		}
	}
}

func attachmentFixture(t *testing.T, path string, large, orphan, scripted, localOpen bool) {
	t.Helper()
	content := "BT /F1 18 Tf 72 740 Td (Homework attachment text 12345) Tj ET\n"
	if large {
		content += strings.Repeat("0 0 m 10 10 l S\n", 4000)
	}
	payload := `{"type":"Document","usedOnDeviceOCR":true,"private_attachment_marker":"removed"}`
	root := "<< /Type /Catalog /Pages 2 0 R"
	if localOpen {
		root += " /OpenAction [4 0 R /FitH null]"
	}
	if !orphan {
		root += " /AF [6 0 R] /Names << /EmbeddedFiles << /Names [(scan.json) 6 0 R] >> >>"
	}
	root += " >>"
	fileSpec := "<< /Type /Filespec /F (scan.json) /AFRelationship /ADBE_Contact_Private /EF << /F 7 0 R >>"
	if scripted {
		fileSpec += " /AA << /O << /S /JavaScript /JS (app.alert(1)) >> >>"
	}
	fileSpec += " >>"
	page := "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R"
	if !orphan {
		page += " /Annots [8 0 R]"
	}
	page += " >>"
	objects := []string{root, "<< /Type /Pages /Count 1 /Kids [4 0 R] >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", page, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content), fileSpec, fmt.Sprintf("<< /Type /EmbeddedFile /Subtype /application#2fjson /Length %d >>\nstream\n%sendstream", len(payload), payload), "<< /Type /Annot /Subtype /FileAttachment /Rect [10 10 20 20] /FS 6 0 R >>"}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.6\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	if err := os.WriteFile(path, pdf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRealPDFAttachmentsAreRemovedBeforeRenderingAndFallback(t *testing.T) {
	p := realPDFTools(t)
	for _, tc := range []struct {
		name                             string
		large, orphan, script, localOpen bool
	}{{"text-fallback", false, false, false, false}, {"compressed", true, false, false, false}, {"orphan-stream", false, true, false, false}, {"script-still-rejected", false, false, true, false}, {"local-opening-view", false, false, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source.pdf")
			attachmentFixture(t, source, tc.large, tc.orphan, tc.script, tc.localOpen)
			original, _ := os.ReadFile(source)
			ctx := context.Background()
			artifact, err := p.Compress(ctx, source, t.TempDir(), validCommand().Limits)
			if tc.script {
				if err == nil || err.Error() != "active_content" {
					t.Fatal("script allowed", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pages, doc, err := p.inspect(ctx, artifact.Path, 35)
			if err != nil || pages != 1 || dangerous(doc["qpdf"]) || containsMetadata(doc["qpdf"]) {
				t.Fatal("output retained attachment or changed pages", err)
			}
			attachments, ok := doc["attachments"].(map[string]any)
			if !ok || len(attachments) != 0 {
				t.Fatal("output has attachments")
			}
			text, err := p.runner().Run(ctx, "pdftotext", artifact.Path, "-")
			if err != nil || !bytes.Contains(text, []byte("Homework attachment text 12345")) {
				t.Fatal("visible text lost", err)
			}
			if tc.name == "text-fallback" && filepath.Base(artifact.Path) != "fallback.pdf" {
				t.Fatal("structural fallback was not exercised")
			}
			after, _ := os.ReadFile(source)
			if sha256.Sum256(original) != sha256.Sum256(after) {
				t.Fatal("source modified")
			}
		})
	}
}
