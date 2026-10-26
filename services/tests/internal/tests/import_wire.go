package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
)

type importWire struct {
	Type, Stage, Path, SHA256 string
	Body                      []byte
}

// Read the exact bytes before native decoding. Only registered import stages
// are representable; a query, encoded route or ambiguous JSON cannot become
// an independently authorized request after normalization by another layer.
func readImportWire(r *http.Request) (importWire, error) {
	var out importWire
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.EscapedPath() != r.URL.Path || r.Header.Get("Content-Encoding") != "" {
		return out, conflict("operation_request_mismatch")
	}
	path := r.URL.Path
	var prefix string
	switch {
	case strings.HasPrefix(path, "/internal/tests/imports/json/"):
		out.Type, prefix = "tests.json", "/internal/tests/imports/json/"
	case strings.HasPrefix(path, "/internal/tests/external-results/"):
		out.Type, prefix = "tests.external-results", "/internal/tests/external-results/"
	default:
		return out, conflict("operation_request_mismatch")
	}
	switch strings.TrimPrefix(path, prefix) {
	case "validate":
		out.Stage = "preview"
	case "commit":
		out.Stage = "apply"
	case "receipt":
		out.Stage = "receipt"
	case "prepare":
		out.Stage = "prepare"
	default:
		return out, conflict("operation_request_mismatch")
	}
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return out, &APIError{415, "json_required"}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, testBodyLimit+1))
	if err != nil {
		return out, bad("invalid_request")
	}
	if len(raw) > testBodyLimit {
		return out, &APIError{413, "import_request_too_large"}
	}
	if err = uniqueImportJSON(raw); err != nil {
		return out, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return out, bad("invalid_request")
	}
	allowed := map[string]bool{"organization_id": true, "division_id": true}
	if out.Type == "tests.json" {
		allowed["direction_id"], allowed["payload"] = true, true
	} else {
		allowed["test_id"], allowed["expected_test_revision"], allowed["rows"] = true, true, true
	}
	if out.Stage != "preview" {
		allowed["command_id"], allowed["preview_hash"] = true, true
		if out.Type == "tests.external-results" {
			allowed["upload_id"], allowed["upload_session_id"], allowed["upload_preview_revision"] = true, true, true
		}
	}
	for name := range fields {
		if !allowed[name] {
			return out, bad("invalid_request")
		}
	}
	var dto any = JSONImportCommitDTO{}
	if out.Type == "tests.json" && out.Stage == "preview" {
		dto = JSONImportPreviewDTO{}
	}
	if out.Type == "tests.external-results" {
		dto = ExternalImportCommitDTO{}
		if out.Stage == "preview" {
			dto = ExternalImportValidateDTO{}
		}
	}
	if exactImportJSONKeys(raw, reflect.TypeOf(dto), false) != nil {
		return out, bad("invalid_request")
	}
	sum := sha256.Sum256(raw)
	out.Path, out.Body, out.SHA256 = path, raw, hex.EncodeToString(sum[:])
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return out, nil
}

// The standard decoder accepts wrong-case aliases for tagged struct fields.
// Auth metadata must have exact names; raw business payload remains raw JSON.
func exactImportJSONKeys(raw []byte, t reflect.Type, require bool) error {
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if t.Kind() == reflect.Pointer || !require && (t.Kind() == reflect.Map || t.Kind() == reflect.Slice) {
			return nil
		}
		return bad("invalid_request")
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		props := map[string]reflect.Type{}
		required := map[string]bool{}
		var collect func(reflect.Type)
		collect = func(current reflect.Type) {
			for i := 0; i < current.NumField(); i++ {
				f := current.Field(i)
				if f.Anonymous {
					collect(f.Type)
					continue
				}
				tag := f.Tag.Get("json")
				name := strings.Split(tag, ",")[0]
				if name == "" || name == "-" {
					continue
				}
				props[name] = f.Type
				if !strings.Contains(tag, ",omitempty") {
					required[name] = true
				}
			}
		}
		collect(t)
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return bad("invalid_request")
		}
		for name, value := range fields {
			child, found := props[name]
			if !found {
				return bad("invalid_request")
			}
			if e := exactImportJSONKeys(value, child, require); e != nil {
				return e
			}
		}
		if require {
			for name := range required {
				if _, exists := fields[name]; !exists {
					return bad("invalid_request")
				}
			}
		}
	case reflect.Slice, reflect.Array:
		var rows []json.RawMessage
		if json.Unmarshal(raw, &rows) != nil {
			return bad("invalid_request")
		}
		for _, value := range rows {
			if e := exactImportJSONKeys(value, t.Elem(), require); e != nil {
				return e
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return bad("invalid_request")
		}
		for _, value := range fields {
			if e := exactImportJSONKeys(value, t.Elem(), require); e != nil {
				return e
			}
		}
	}
	return nil
}

func uniqueImportJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		start, container := token.(json.Delim)
		if !container {
			return nil
		}
		switch start {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				token, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := token.(string)
				if !ok || seen[name] {
					return bad("duplicate_json_field")
				}
				seen[name] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return bad("invalid_request")
		}
		end, err := d.Token()
		if err != nil {
			return err
		}
		if start == '{' && end != json.Delim('}') || start == '[' && end != json.Delim(']') {
			return bad("invalid_request")
		}
		return nil
	}
	if err := value(); err != nil {
		return bad("invalid_request")
	}
	if _, err := d.Token(); err != io.EOF {
		return bad("invalid_request")
	}
	return nil
}
