package tests

import (
	"bytes"
	"encoding/json"
	"io"
)

// Verify the transport checksum against the browser's original canonical JSON,
// independently of the semantic checksum used for durable replay detection.
// This keeps UTC offsets, millisecond formatting and answer ordering compatible
// with IndexedDB snapshots while allowing equivalent retries after normalization.
func (f *FinalizeDTO) UnmarshalJSON(body []byte) error {
	type dto FinalizeDTO
	var parsed dto
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&parsed); e != nil {
		return e
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		return e
	}
	decoder = json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var tree map[string]any
	if e := decoder.Decode(&tree); e != nil {
		return e
	}
	delete(tree, "snapshot_hash")
	*f = FinalizeDTO(parsed)
	f.ClientHash = hashJSON(tree)
	return nil
}
