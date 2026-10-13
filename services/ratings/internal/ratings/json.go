package ratings

import (
	"bytes"
	"encoding/json"
	"io"
)

func validateJSON(body []byte) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := readJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return bad("invalid_json")
	}
	return nil
}

func readJSONValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return bad("invalid_json")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return bad("invalid_json")
			}
			seen[s] = true
			if err := readJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := readJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return bad("invalid_json")
	}
	_, err = d.Token()
	return err
}
