package exams

import (
	"bytes"
	"encoding/json"
	"io"
)

// All API DTOs reject unknown fields, duplicate keys and trailing JSON.
func DecodeStrict(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := uniqueJSON(d); err != nil {
		return Bad("invalid_json")
	}
	if _, err := d.Token(); err != io.EOF {
		return Bad("invalid_json")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(out) != nil {
		return Bad("invalid_request")
	}
	return nil
}
func uniqueJSON(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return Bad("duplicate_json_key")
			}
			seen[s] = true
			if e = uniqueJSON(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := uniqueJSON(d); e != nil {
				return e
			}
		}
	default:
		return Bad("invalid_json")
	}
	_, e = d.Token()
	return e
}

// Stored receipt IDs and scores must survive replay without float64 rounding.
func decodeStoredValue(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var out, extra any
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	if err := d.Decode(&extra); err != io.EOF {
		return nil, Bad("invalid_json")
	}
	return out, nil
}
