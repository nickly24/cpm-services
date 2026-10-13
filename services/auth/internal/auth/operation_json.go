package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

var errOperationJSON = errors.New("invalid_operation_request")

// This decoder is deliberately separate from ordinary login DTO decoding.
// The new immutable operation protocol rejects duplicate/aliased fields before
// unmarshalling can discard them, including escaped duplicate object names.
func decodeOperationJSON(data []byte, target any) error {
	if !utf8.Valid(data) || len(bytes.TrimSpace(data)) == 0 {
		return errOperationJSON
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := operationJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errOperationJSON
	}
	t := reflect.TypeOf(target)
	if t == nil || t.Kind() != reflect.Pointer {
		return errOperationJSON
	}
	if err := operationJSONFields(data, t.Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errOperationJSON
	}
	return nil
}

func operationJSONValue(d *json.Decoder, depth int) error {
	if depth > 24 {
		return errOperationJSON
	}
	token, err := d.Token()
	if err != nil {
		return errOperationJSON
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return errOperationJSON
			}
			seen[key] = true
			if err := operationJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return errOperationJSON
		}
	case '[':
		for d.More() {
			if err := operationJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return errOperationJSON
		}
	default:
		return errOperationJSON
	}
	return nil
}

func operationJSONFields(raw []byte, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil // The discriminated target is checked by its registered type.
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errOperationJSON
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return errOperationJSON
		}
		known := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			name := tag[0]
			if name == "" {
				name = field.Name
			}
			known[name] = true
			value, present := fields[name]
			optional := false
			for _, option := range tag[1:] {
				optional = optional || option == "omitempty"
			}
			if !present {
				if !optional {
					return errOperationJSON
				}
				continue
			}
			if err := operationJSONFields(value, field.Type); err != nil {
				return err
			}
		}
		for name := range fields {
			if !known[name] {
				return errOperationJSON
			}
		}
	case reflect.Array, reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return errOperationJSON
		}
		for _, value := range values {
			if err := operationJSONFields(value, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return errOperationJSON
		}
		for _, value := range values {
			if err := operationJSONFields(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
