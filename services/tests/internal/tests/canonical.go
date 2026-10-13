package tests

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
)

// Canonical JSON matches JSON.stringify after recursively sorting object keys:
// no whitespace, raw Unicode/HTML, and no slash escaping. DTO timestamps are
// normalized to UTC milliseconds before hashing to match durable Mongo values.
func canonicalJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var tree any
	if err = decoder.Decode(&tree); err != nil {
		panic(err)
	}
	out := []byte{}
	return appendCanonical(out, tree)
}
func appendCanonical(out []byte, value any) []byte {
	switch v := value.(type) {
	case nil:
		return append(out, "null"...)
	case bool:
		if v {
			return append(out, "true"...)
		}
		return append(out, "false"...)
	case json.Number:
		return append(out, string(v)...)
	case string:
		out = append(out, '"')
		for _, r := range v {
			switch r {
			case '"', '\\':
				out = append(out, '\\', byte(r))
			case '\b':
				out = append(out, '\\', 'b')
			case '\f':
				out = append(out, '\\', 'f')
			case '\n':
				out = append(out, '\\', 'n')
			case '\r':
				out = append(out, '\\', 'r')
			case '\t':
				out = append(out, '\\', 't')
			default:
				if r < 32 {
					out = append(out, '\\', 'u', '0', '0')
					h := strconv.FormatInt(int64(r), 16)
					if len(h) == 1 {
						out = append(out, '0')
					}
					out = append(out, h...)
				} else {
					out = append(out, string(r)...)
				}
			}
		}
		return append(out, '"')
	case []any:
		out = append(out, '[')
		for i, item := range v {
			if i > 0 {
				out = append(out, ',')
			}
			out = appendCanonical(out, item)
		}
		return append(out, ']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, '{')
		for i, key := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			out = appendCanonical(out, key)
			out = append(out, ':')
			out = appendCanonical(out, v[key])
		}
		return append(out, '}')
	}
	panic("non-json canonical value")
}
