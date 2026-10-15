package filework

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Rejected struct{ Code string }

func (e *Rejected) Error() string { return e.Code }
func reject(code string) error    { return &Rejected{Code: code} }

type Artifact struct {
	Path      string
	SizeBytes int64
	PageCount int
	SHA256    string
}
type Compressor interface {
	Compress(context.Context, string, string, Limits) (Artifact, error)
}

// Runner never invokes a shell. Its output is bounded and child processes are
// killed by the processing deadline; Linux also bounds address space and files.
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("prlimit"); err != nil {
			return nil, errors.New("resource_limiter_unavailable")
		}
		args = append([]string{"--as=1073741824", "--fsize=134217728", "--cpu=240", "--", name}, args...)
		name = "prlimit"
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 5e9
	var out cappedBuffer
	out.limit = 64 * 1024 * 1024
	var stderr cappedBuffer
	stderr.limit = 16 * 1024
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, errors.New("pdf_tool_failed")
	}
	return out.Bytes(), nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("pdf_tool_output_limit")
	}
	return b.Buffer.Write(p)
}

type PDFCompressor struct {
	QPDF        string
	Ghostscript string
	Runner      Runner
}

func (p PDFCompressor) runner() Runner {
	if p.Runner != nil {
		return p.Runner
	}
	return ExecRunner{}
}

func (p PDFCompressor) inspect(ctx context.Context, path string, maxPages int) (int, map[string]any, error) {
	return p.inspectPolicy(ctx, path, maxPages, false)
}

func (p PDFCompressor) inspectPolicy(ctx context.Context, path string, maxPages int, allowAttachments bool) (int, map[string]any, error) {
	// is-encrypted returns 0 for every encrypted PDF, including files with an
	// empty user password. JSON alone cannot inspect password-protected files.
	_, encryptedErr := p.runner().Run(ctx, p.QPDF, "--is-encrypted", path)
	if encryptedErr == nil {
		return 0, nil, reject("encrypted_pdf")
	}
	if ctx.Err() != nil {
		return 0, nil, ctx.Err()
	}
	// qpdf uses exit 3 for recoverable warnings (for example an inaccurate
	// trailer /Size). Keep fatal parser errors failing, and validate the recovered
	// object graph below before passing anything to Ghostscript.
	raw, err := p.runner().Run(ctx, p.QPDF, "--json=2", "--json-stream-data=none", "--warning-exit-0", path)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, reject("corrupt_pdf")
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return 0, nil, reject("corrupt_pdf")
	}
	encrypt, ok := doc["encrypt"].(map[string]any)
	if !ok {
		return 0, nil, reject("corrupt_pdf")
	}
	if encrypted, _ := encrypt["encrypted"].(bool); encrypted {
		return 0, nil, reject("encrypted_pdf")
	}
	pages, ok := doc["pages"].([]any)
	if !ok || len(pages) == 0 {
		return 0, nil, reject("empty_pdf")
	}
	if len(pages) > maxPages {
		return 0, nil, reject("too_many_pages")
	}
	q, ok := doc["qpdf"].([]any)
	if !ok || len(q) != 2 {
		return 0, nil, reject("corrupt_pdf")
	}
	if dangerousContent(q[1], allowAttachments) {
		return 0, nil, reject("active_content")
	}
	return len(pages), doc, nil
}

var dangerousKeys = map[string]bool{"/JS": true, "/JavaScript": true, "/Launch": true, "/RichMedia": true, "/EmbeddedFile": true, "/EmbeddedFiles": true, "/EF": true, "/AF": true, "/AA": true, "/OpenAction": true, "/SubmitForm": true, "/ImportData": true, "/Movie": true, "/Sound": true, "/3D": true, "/Rendition": true, "/XFA": true}
var dangerousActions = map[string]bool{"/JavaScript": true, "/Launch": true, "/GoToR": true, "/SubmitForm": true, "/ImportData": true, "/Movie": true, "/Sound": true, "/Rendition": true}
var attachmentKeys = map[string]bool{"/EmbeddedFile": true, "/EmbeddedFiles": true, "/EF": true, "/AF": true}

func dangerous(v any) bool {
	return dangerousContent(v, false)
}

func dangerousContent(v any, allowAttachments bool) bool {
	switch x := v.(type) {
	case map[string]any:
		for key, value := range x {
			passive := attachmentKeys[key] || (key == "/OpenAction" && localOpenDestination(value))
			if dangerousKeys[key] && !(allowAttachments && passive) {
				return true
			}
			if key == "/S" || key == "/Type" || key == "/Subtype" {
				if s, ok := value.(string); ok && (dangerousActions[s] || (dangerousKeys[s] && !(allowAttachments && attachmentKeys[s]))) {
					return true
				}
			}
			if dangerousContent(value, allowAttachments) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if dangerousContent(value, allowAttachments) {
				return true
			}
		}
	}
	return false
}

// PDF destination arrays only select a local page/view; they are not actions.
// Accept the standard explicit forms for removal, with bounded numeric/null
// coordinates. Action dictionaries and named/remote destinations stay rejected.
func localOpenDestination(v any) bool {
	a, ok := v.([]any)
	if !ok || len(a) < 2 || len(a) > 6 {
		return false
	}
	ref, ok := a[0].(string)
	if !ok {
		return false
	}
	parts := strings.Fields(ref)
	if len(parts) != 3 || parts[2] != "R" {
		return false
	}
	object, e1 := strconv.Atoi(parts[0])
	generation, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || object <= 0 || generation < 0 {
		return false
	}
	mode, _ := a[1].(string)
	lengths := map[string]int{"/XYZ": 5, "/Fit": 2, "/FitH": 3, "/FitV": 3, "/FitR": 6, "/FitB": 2, "/FitBH": 3, "/FitBV": 3}
	if lengths[mode] != len(a) {
		return false
	}
	for _, coordinate := range a[2:] {
		if coordinate != nil {
			if _, ok := coordinate.(float64); !ok {
				return false
			}
		}
	}
	return true
}

// Discard attachments rather than treating their presence as an infection.
// Replace embedded streams themselves, including orphaned objects, so neither
// Ghostscript nor a structural fallback can preserve their payloads.
func removeAttachments(doc map[string]any) {
	q, _ := doc["qpdf"].([]any)
	if len(q) != 2 {
		return
	}
	objects, _ := q[1].(map[string]any)
	for id, object := range objects {
		obj, _ := object.(map[string]any)
		dict, _ := obj["value"].(map[string]any)
		stream, _ := obj["stream"].(map[string]any)
		streamDict, _ := stream["dict"].(map[string]any)
		if dict["/Type"] == "/Filespec" || dict["/Subtype"] == "/FileAttachment" || streamDict["/Type"] == "/EmbeddedFile" {
			objects[id] = map[string]any{"value": nil}
		}
	}
	removeAttachmentReferences(q)
}

func removeAttachmentReferences(v any) {
	switch x := v.(type) {
	case map[string]any:
		for key, value := range x {
			if attachmentKeys[key] || (key == "/OpenAction" && localOpenDestination(value)) {
				delete(x, key)
			} else {
				removeAttachmentReferences(value)
			}
		}
	case []any:
		for _, value := range x {
			removeAttachmentReferences(value)
		}
	}
}

func removeMetadata(v any) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "/Metadata")
		delete(x, "/Info")
		for _, value := range x {
			removeMetadata(value)
		}
	case []any:
		for _, value := range x {
			removeMetadata(value)
		}
	}
}

func (p PDFCompressor) sanitize(ctx context.Context, input, output string, doc map[string]any) error {
	removeMetadata(doc["qpdf"])
	patch, err := json.Marshal(map[string]any{"qpdf": doc["qpdf"]})
	if err != nil {
		return err
	}
	patchFile := output + ".json"
	if err = os.WriteFile(patchFile, patch, 0600); err != nil {
		return err
	}
	defer os.Remove(patchFile)
	_, err = p.runner().Run(ctx, p.QPDF, "--update-from-json="+patchFile, "--object-streams=generate", "--compress-streams=y", "--linearize", "--warning-exit-0", input, output)
	return err
}

func (p PDFCompressor) Compress(ctx context.Context, source, dir string, limits Limits) (Artifact, error) {
	var artifact Artifact
	if err := limits.Validate(); err != nil {
		return artifact, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return artifact, err
	}
	if info.Size() > limits.MaxSourceBytes {
		return artifact, reject("source_too_large")
	}
	file, err := os.Open(source)
	if err != nil {
		return artifact, err
	}
	sig := make([]byte, 5)
	_, readErr := io.ReadFull(file, sig)
	file.Close()
	if readErr != nil || string(sig) != "%PDF-" {
		return artifact, reject("invalid_pdf_signature")
	}
	pages, originalDoc, err := p.inspectPolicy(ctx, source, limits.MaxPages, true)
	if err != nil {
		return artifact, err
	}
	if dangerous(originalDoc["qpdf"]) {
		// The input policy has already rejected scripts/actions. The remaining
		// strict-policy flags are attachments or a passive local opening view;
		// remove them before rendering.
		clean := filepath.Join(dir, "input-clean.pdf")
		removeAttachments(originalDoc)
		if err = p.sanitize(ctx, source, clean, originalDoc); err != nil {
			return artifact, err
		}
		cleanPages, cleanDoc, cleanErr := p.inspect(ctx, clean, limits.MaxPages)
		if cleanErr != nil {
			return artifact, cleanErr
		}
		if cleanPages != pages {
			return artifact, reject("page_count_changed")
		}
		source, originalDoc = clean, cleanDoc
	}
	compressed := filepath.Join(dir, "compressed.pdf")
	args := []string{"-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-dCompatibilityLevel=1.6", "-dAutoRotatePages=/None", "-dDetectDuplicateImages=true", "-dCompressFonts=true", "-dSubsetFonts=true", "-dEmbedAllFonts=true", "-dPreserveAnnots=true", "-dPreserveMarkedContent=true", "-dPreserveDocumentProperties=false", "-dPassThroughJPEGImages=false", "-dPassThroughJPXImages=false", "-dAutoFilterColorImages=false", "-dColorImageFilter=/DCTEncode", "-dAutoFilterGrayImages=false", "-dGrayImageFilter=/DCTEncode", "-dDownsampleColorImages=true", "-dColorImageDownsampleType=/Bicubic", "-dColorImageDownsampleThreshold=1.0", "-dColorImageResolution=" + strconv.Itoa(limits.DPI), "-dDownsampleGrayImages=true", "-dGrayImageDownsampleType=/Bicubic", "-dGrayImageDownsampleThreshold=1.0", "-dGrayImageResolution=" + strconv.Itoa(limits.DPI), "-dDownsampleMonoImages=true", "-dMonoImageResolution=" + strconv.Itoa(limits.DPI*2), "-sOutputFile=" + compressed, "-c", jpegDistillerParams(limits.JPEGQuality), "-f", source}
	if _, err = p.runner().Run(ctx, p.Ghostscript, args...); err != nil {
		return artifact, err
	}
	outputPages, compressedDoc, err := p.inspect(ctx, compressed, limits.MaxPages)
	if err != nil {
		return artifact, err
	}
	if outputPages != pages {
		return artifact, reject("page_count_changed")
	}
	output := filepath.Join(dir, "output.pdf")
	if err = p.sanitize(ctx, compressed, output, compressedDoc); err != nil {
		return artifact, err
	}
	if err = os.Remove(compressed); err != nil {
		return artifact, err
	}
	outputInfo, err := os.Stat(output)
	if err != nil {
		return artifact, err
	}
	if outputInfo.Size() > info.Size() {
		// Photo-heavy files use the downsampled result. If a text-only PDF grew,
		// compare a metadata-clean structural rewrite of the verified source.
		fallback := filepath.Join(dir, "fallback.pdf")
		if err = p.sanitize(ctx, source, fallback, originalDoc); err != nil {
			return artifact, err
		}
		fallbackInfo, statErr := os.Stat(fallback)
		if statErr != nil {
			return artifact, statErr
		}
		if fallbackInfo.Size() < outputInfo.Size() {
			output = fallback
			outputInfo = fallbackInfo
		}
	}
	if outputInfo.Size() > limits.MaxOutputBytes {
		return artifact, reject("processed_too_large")
	}
	finalPages, finalDoc, err := p.inspect(ctx, output, limits.MaxPages)
	if err != nil {
		return artifact, err
	}
	if finalPages != pages {
		return artifact, reject("page_count_changed")
	}
	if containsMetadata(finalDoc["qpdf"]) {
		return artifact, reject("metadata_not_removed")
	}
	file, err = os.Open(output)
	if err != nil {
		return artifact, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return artifact, err
	}
	return Artifact{Path: output, SizeBytes: outputInfo.Size(), PageCount: pages, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func containsMetadata(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for key, value := range x {
			if key == "/Metadata" || key == "/Info" {
				return true
			}
			if containsMetadata(value) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if containsMetadata(value) {
				return true
			}
		}
	}
	return false
}

func artifactMetadata(c Command, a Artifact) map[string]string {
	// Use HTTP-safe metadata names: some S3 front proxies strip underscore headers,
	// which invalidates the signed request. Homework accepts both historical and hyphenated names.
	return map[string]string{"job-id": c.JobID, "generation": fmt.Sprint(c.Generation), "organization-id": fmt.Sprint(c.OrganizationID), "division-id": fmt.Sprint(c.DivisionID), "page-count": fmt.Sprint(a.PageCount), "sha256": a.SHA256}
}

// JPEG quality uses the IJG quantization scaling curve. pdfwrite image
// dictionaries override the generic JPEGQ device flag, so set them explicitly.
func jpegDistillerParams(quality int) string {
	scale := float64(200-2*quality) / 100
	if quality < 50 {
		scale = 50 / float64(quality)
	}
	return fmt.Sprintf("<< /ColorImageDict << /QFactor %.6f /Blend 1 /HSamples [2 1 1 2] /VSamples [2 1 1 2] >> /GrayImageDict << /QFactor %.6f /Blend 1 >> >> setdistillerparams", scale, scale)
}
