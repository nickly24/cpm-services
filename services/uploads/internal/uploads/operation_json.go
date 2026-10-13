package uploads

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

var errOperationJSON = errors.New("invalid operation JSON")

// Decode exact public and Auth contracts: JSON aliases, duplicates (including
// escaped names), missing fields and null scalars must not change an intent.
func decodeOperationJSON(raw []byte, dst any) error {
	value, err := operationJSONValue(raw)
	t := reflect.TypeOf(dst)
	if err != nil || value == nil || t == nil || t.Kind() != reflect.Pointer || operationJSONShape(value, t.Elem()) != nil {
		return errOperationJSON
	}
	if json.Unmarshal(raw, dst) != nil {
		return errOperationJSON
	}
	return nil
}

// Native receipts may include unrelated owner fields, but every known proof
// field must retain its canonical name, presence and non-null scalar type.
func decodeNativeOperationJSON(raw []byte, dst any) error {
	value, err := operationJSONValue(raw)
	t := reflect.TypeOf(dst)
	if err != nil || value == nil || t == nil || t.Kind() != reflect.Pointer || operationJSONShapeMode(value, t.Elem(), true) != nil || json.Unmarshal(raw, dst) != nil {
		return errOperationJSON
	}
	return nil
}

func decodeOperationRequest(r *http.Request, dst any) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return api(415, "json_required")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (32<<10)+1))
	if err != nil || len(raw) > 32<<10 || decodeOperationJSON(raw, dst) != nil {
		return api(400, "invalid_json")
	}
	return nil
}

func operationJSONValue(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errOperationJSON
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := readOperationValue(d, 0)
	if err != nil {
		return nil, errOperationJSON
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errOperationJSON
	}
	return value, nil
}

func readOperationValue(d *json.Decoder, depth int) (any, error) {
	if depth > 24 {
		return nil, errOperationJSON
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		out := map[string]any{}
		for d.More() {
			name, e := d.Token()
			key, ok := name.(string)
			if e != nil || !ok {
				return nil, errOperationJSON
			}
			if _, duplicate := out[key]; duplicate {
				return nil, errOperationJSON
			}
			value, e := readOperationValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			out[key] = value
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, errOperationJSON
		}
		return out, nil
	case '[':
		out := []any{}
		for d.More() {
			value, e := readOperationValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			out = append(out, value)
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, errOperationJSON
		}
		return out, nil
	}
	return nil, errOperationJSON
}

type operationJSONField struct {
	typeOf   reflect.Type
	optional bool
}

func operationJSONFields(t reflect.Type) map[string]operationJSONField {
	fields := map[string]operationJSONField{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")
		if tag[0] == "-" {
			continue
		}
		if f.Anonymous && tag[0] == "" && f.Type.Kind() == reflect.Struct {
			for key, nested := range operationJSONFields(f.Type) {
				fields[key] = nested
			}
			continue
		}
		name := tag[0]
		if name == "" {
			name = f.Name
		}
		fields[name] = operationJSONField{typeOf: f.Type, optional: strings.Contains(f.Tag.Get("json"), ",omitempty")}
	}
	return fields
}

func operationJSONShape(value any, t reflect.Type) error {
	return operationJSONShapeMode(value, t, false)
}

func operationJSONShapeMode(value any, t reflect.Type, allowExtra bool) error {
	if t == nil {
		return errOperationJSON
	}
	if t.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		return operationJSONShapeMode(value, t.Elem(), allowExtra)
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil // Its discriminator is checked by the binding validator.
	}
	if value == nil {
		return errOperationJSON
	}
	if t == reflect.TypeOf(time.Time{}) {
		if _, ok := value.(string); !ok {
			return errOperationJSON
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return errOperationJSON
		}
		fields := operationJSONFields(t)
		for key, child := range object {
			f, known := fields[key]
			if !known && allowExtra {
				for canonical := range fields {
					if strings.EqualFold(key, canonical) {
						return errOperationJSON
					}
				}
				continue
			}
			if !known || operationJSONShapeMode(child, f.typeOf, allowExtra) != nil {
				return errOperationJSON
			}
		}
		for key, f := range fields {
			if _, present := object[key]; !present && !f.optional {
				return errOperationJSON
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return errOperationJSON
		}
		for _, child := range object {
			if operationJSONShapeMode(child, t.Elem(), allowExtra) != nil {
				return errOperationJSON
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return errOperationJSON
		}
		for _, child := range array {
			if operationJSONShapeMode(child, t.Elem(), allowExtra) != nil {
				return errOperationJSON
			}
		}
	}
	return nil // The typed decoder validates scalar values and numeric ranges.
}
