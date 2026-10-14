package filework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests use real parsers and image recompression. They are run in the
// Docker test target, not against a production bucket or application database.
func realPDFTools(t *testing.T) PDFCompressor {
	t.Helper()
	for _, tool := range []string{"qpdf", "gs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("real PDF runtime unavailable: use Docker test target")
		}
	}
	return PDFCompressor{QPDF: "qpdf", Ghostscript: "gs"}
}
func makeSyntheticPDF(t *testing.T, path string, withImage bool, pages int, active bool, metadata bool, jpegSource ...bool) {
	t.Helper()
	objects := []string{"", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	kids := []string{}
	for page := 0; page < pages; page++ {
		pageID := len(objects) + 1
		contentID := pageID + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageID))
		resource := "/Font << /F1 3 0 R >>"
		content := "BT /F1 18 Tf 72 740 Td (Homework readable text 12345) Tj ET\n"
		if withImage {
			imageID := pageID + 2
			resource += fmt.Sprintf(" /XObject << /Photo %d 0 R >>", imageID)
			content += "q 500 0 0 650 56 56 cm /Photo Do Q\n"
		}
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << %s >> /Contents %d 0 R >>", resource, contentID), fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
		if withImage {
			const width = 1200
			const height = 1800
			pixels := make([]byte, width*height*3)
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					i := (y*width + x) * 3
					pixels[i] = byte(x * 255 / width)
					pixels[i+1] = byte(y * 255 / height)
					pixels[i+2] = byte((x + y) % 256)
				}
			}
			filter := ""
			if len(jpegSource) > 0 && jpegSource[0] {
				rgba := image.NewRGBA(image.Rect(0, 0, width, height))
				for i := 0; i < width*height; i++ {
					rgba.Pix[4*i] = pixels[3*i]
					rgba.Pix[4*i+1] = pixels[3*i+1]
					rgba.Pix[4*i+2] = pixels[3*i+2]
					rgba.Pix[4*i+3] = 255
				}
				var encoded bytes.Buffer
				if err := jpeg.Encode(&encoded, rgba, &jpeg.Options{Quality: 95}); err != nil {
					t.Fatal(err)
				}
				pixels = encoded.Bytes()
				filter = " /Filter /DCTDecode"
			}
			objects = append(objects, fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8%s /Length %d >>\nstream\n%s\nendstream", width, height, filter, len(pixels), string(pixels)))
		}
	}
	objects[0] = "<< /Type /Catalog /Pages 2 0 R"
	if active {
		objects[0] += " /OpenAction << /S /JavaScript /JS (app.alert\\(1\\)) >>"
	}
	objects[0] += " >>"
	objects[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [ %s ] >>", pages, strings.Join(kids, " "))
	info := ""
	if metadata {
		objects = append(objects, "<< /Author (Secret Test Author) /Title (Test Private Title) /CreationDate (D:20261001000000) >>")
		info = fmt.Sprintf(" /Info %d 0 R", len(objects))
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.6\n%\xe2\xe3\xcf\xd3\n")
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
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, info, xref)
	if err := os.WriteFile(path, pdf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRealPDFAlreadyJPEGCompressedImagesShrinkAndPolicyChangesQuality(t *testing.T) {
	p := realPDFTools(t)
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "source.pdf")
	makeSyntheticPDF(t, source, true, 1, false, true, true)
	original, _ := os.Stat(source)
	var outputs [2]int64
	for index, quality := range []int{40, 90} {
		dir := t.TempDir()
		limits := validCommand().Limits
		limits.JPEGQuality = quality
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		a, err := p.Compress(ctx, source, dir, limits)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		outputs[index] = a.SizeBytes
		if a.SizeBytes >= original.Size()*3/4 {
			t.Fatalf("pre-JPEG input did not shrink enough: source%d output%d", original.Size(), a.SizeBytes)
		}
		t.Logf("JPEG source=%d quality=%d output=%d reduction=%.1f%%", original.Size(), quality, a.SizeBytes, 100*(1-float64(a.SizeBytes)/float64(original.Size())))
	}
	if outputs[0] >= outputs[1] {
		t.Fatalf("database quality policy not effective: low=%d high=%d", outputs[0], outputs[1])
	}
}

func TestRealPDFImageCompressionPreservesPagesTextAndRemovesMetadata(t *testing.T) {
	p := realPDFTools(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.pdf")
	makeSyntheticPDF(t, source, true, 1, false, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a, err := p.Compress(ctx, source, dir, validCommand().Limits)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.Stat(source)
	if a.SizeBytes >= original.Size()/3 {
		t.Fatalf("image did not materially shrink: source %d output %d", original.Size(), a.SizeBytes)
	}
	if a.PageCount != 1 || len(a.SHA256) != 64 {
		t.Fatal("invalid output metadata")
	}
	t.Logf("image source=%d output=%d reduction=%.1f%% pages=%d", original.Size(), a.SizeBytes, 100*(1-float64(a.SizeBytes)/float64(original.Size())), a.PageCount)
	_, doc, err := p.inspect(ctx, a.Path, 35)
	if err != nil || containsMetadata(doc["qpdf"]) {
		t.Fatal("output metadata survived", err)
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Fatal("PDF runtime tests need pdftotext")
	}
	text, err := exec.CommandContext(ctx, "pdftotext", a.Path, "-").Output()
	if err != nil || !bytes.Contains(text, []byte("Homework readable text 12345")) {
		t.Fatal("selectable text was lost", err)
	}
}

func TestRealPDFPolicyAndUnsafeInputs(t *testing.T) {
	p := realPDFTools(t)
	for _, tc := range []struct {
		name   string
		code   string
		setup  func(*testing.T, string)
		change func(*Limits)
	}{
		{"source_limit", "source_too_large", func(t *testing.T, path string) { makeSyntheticPDF(t, path, false, 1, false, false) }, func(l *Limits) { l.MaxSourceBytes = 5 }},
		{"output_limit", "processed_too_large", func(t *testing.T, path string) { makeSyntheticPDF(t, path, false, 1, false, false) }, func(l *Limits) { l.MaxOutputBytes = 5 }},
		{"signature", "invalid_pdf_signature", func(t *testing.T, path string) { os.WriteFile(path, []byte("not a pdf"), 0600) }, nil},
		{"corrupt", "corrupt_pdf", func(t *testing.T, path string) { os.WriteFile(path, []byte("%PDF-1.6 corrupt contents"), 0600) }, nil},
		{"pages", "too_many_pages", func(t *testing.T, path string) { makeSyntheticPDF(t, path, false, 2, false, false) }, func(l *Limits) { l.MaxPages = 1 }},
		{"active", "active_content", func(t *testing.T, path string) { makeSyntheticPDF(t, path, false, 1, true, false) }, nil},
		{"encrypted", "encrypted_pdf", func(t *testing.T, path string) {
			plain := path + ".plain"
			makeSyntheticPDF(t, plain, false, 1, false, false)
			if output, err := exec.Command("qpdf", "--encrypt", "test-password", "test-password", "256", "--", plain, path).CombinedOutput(); err != nil {
				t.Fatal(string(output), err)
			}
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.pdf")
			tc.setup(t, source)
			limits := validCommand().Limits
			if tc.change != nil {
				tc.change(&limits)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, err := p.Compress(ctx, source, dir, limits)
			code, ok := permanentCode(err)
			if !ok || code != tc.code {
				t.Fatalf("expected %s got %v", tc.code, err)
			}
		})
	}
}
func TestRealPDFMultiPageTextFallback(t *testing.T) {
	p := realPDFTools(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.pdf")
	makeSyntheticPDF(t, source, false, 3, false, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a, err := p.Compress(ctx, source, dir, validCommand().Limits)
	if err != nil || a.PageCount != 3 {
		t.Fatalf("text-only fallback failed: %+v %v", a, err)
	}
}

func TestRealPDFRecoverableWarningStillValidatesContent(t *testing.T) {
	p := realPDFTools(t)
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint("active=", active), func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.pdf")
			makeSyntheticPDF(t, source, false, 2, active, true)
			raw, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			// The cross-reference table and objects are intact, but the trailer
			// overstates /Size, as in the incident's mobile-generated PDFs.
			raw = bytes.Replace(raw, []byte("trailer\n<< /Size 9 "), []byte("trailer\n<< /Size 999 "), 1)
			if err = os.WriteFile(source, raw, 0600); err != nil {
				t.Fatal(err)
			}
			check, err := exec.Command("qpdf", "--json=2", "--json-stream-data=none", source).Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 || !json.Valid(check) {
				t.Fatalf("fixture must produce valid JSON with warning exit 3: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			a, err := p.Compress(ctx, source, dir, validCommand().Limits)
			if active {
				code, permanent := permanentCode(err)
				if !permanent || code != "active_content" {
					t.Fatalf("warning bypassed active-content policy: %v", err)
				}
				return
			}
			if err != nil || a.PageCount != 2 {
				t.Fatalf("recoverable PDF lost pages or failed: %+v %v", a, err)
			}
			text, err := exec.Command("pdftotext", a.Path, "-").Output()
			if err != nil || bytes.Count(text, []byte("Homework readable text 12345")) != 2 {
				t.Fatal("recoverable PDF lost selectable text", err)
			}
		})
	}
}
