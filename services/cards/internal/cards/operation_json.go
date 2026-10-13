package cards

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// Auth v1 uses exact field names and rejects duplicate keys recursively, unlike
// encoding/json's case-insensitive struct matching and last-key-wins behavior.
func strictOperationJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid operation UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := readOperationJSON(d, 0)
	if err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing operation JSON")
	}
	t := reflect.TypeOf(out)
	if t == nil || t.Kind() != reflect.Pointer || !operationJSONShape(value, t.Elem()) {
		return errors.New("invalid operation JSON shape")
	}
	return json.Unmarshal(raw, out)
}

func readOperationJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, errors.New("operation JSON too deep")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return nil, e
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid key")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate operation key")
			}
			v, e := readOperationJSON(d, depth+1)
			if e != nil {
				return nil, e
			}
			object[name] = v
			if len(object) > 1000 {
				return nil, errors.New("operation object limit")
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, errors.New("invalid object")
		}
		return object, nil
	case json.Delim('['):
		array := []any{}
		for d.More() {
			v, e := readOperationJSON(d, depth+1)
			if e != nil {
				return nil, e
			}
			array = append(array, v)
			if len(array) > 1000 {
				return nil, errors.New("operation array limit")
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, errors.New("invalid array")
		}
		return array, nil
	}
	return token, nil
}

func operationJSONShape(v any, t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		return v == nil || operationJSONShape(v, t.Elem())
	}
	if t == reflect.TypeOf(time.Time{}) {
		s, ok := v.(string)
		if !ok || !strings.HasSuffix(s, "Z") {
			return false
		}
		_, e := time.Parse(time.RFC3339Nano, s)
		return e == nil
	}
	if t == reflect.TypeOf(ImportTarget{}) {
		object, ok := v.(map[string]any)
		if !ok {
			return false
		}
		var fields []string
		switch object["kind"] {
		case "existing_theme":
			fields = []string{"kind", "direction_id", "theme_id"}
		case "create_theme":
			fields = []string{"kind", "direction_id", "name", "collision_policy"}
		default:
			return false
		}
		if len(object) != len(fields) {
			return false
		}
		for _, field := range fields {
			if _, ok := object[field].(string); !ok {
				return false
			}
		}
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := v.(map[string]any)
		if !ok {
			return false
		}
		known := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tags := strings.Split(f.Tag.Get("json"), ",")
			name := tags[0]
			if name == "" || name == "-" {
				continue
			}
			known[name] = true
			value, exists := object[name]
			optional := false
			for _, tag := range tags[1:] {
				optional = optional || tag == "omitempty"
			}
			if !exists {
				if !optional {
					return false
				}
				continue
			}
			if !operationJSONShape(value, f.Type) {
				return false
			}
		}
		for key := range object {
			if !known[key] {
				return false
			}
		}
		return true
	case reflect.Map:
		object, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for _, value := range object {
			if !operationJSONShape(value, t.Elem()) {
				return false
			}
		}
		return true
	case reflect.Slice:
		array, ok := v.([]any)
		if !ok {
			return false
		}
		for _, value := range array {
			if !operationJSONShape(value, t.Elem()) {
				return false
			}
		}
		return true
	case reflect.String:
		_, ok := v.(string)
		return ok
	case reflect.Bool:
		_, ok := v.(bool)
		return ok
	case reflect.Int, reflect.Int64:
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, e := n.Int64()
		return e == nil
	}
	return false
}
