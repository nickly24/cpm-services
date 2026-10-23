package core

import (
	"bytes"
	"encoding/json"
	"io"
)

func nullableStudentClass(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// Keep legacy DTO literals compatible while distinguishing an omitted/null
// class from the explicitly invalid JSON value zero.
func decodeOptionalClass(raw []byte, target any) (bool, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if e := dec.Decode(target); e != nil {
		return false, e
	}
	if dec.Decode(new(any)) != io.EOF {
		return false, bad("invalid_request")
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		return false, e
	}
	v, ok := fields["class"]
	return ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")), nil
}
func (u *UserDTO) UnmarshalJSON(raw []byte) error {
	type plain UserDTO
	var v plain
	present, e := decodeOptionalClass(raw, &v)
	if e != nil {
		return e
	}
	*u = UserDTO(v)
	u.classSupplied = present
	return nil
}
func (u *ProfileDTO) UnmarshalJSON(raw []byte) error {
	type plain ProfileDTO
	var v plain
	present, e := decodeOptionalClass(raw, &v)
	if e != nil {
		return e
	}
	*u = ProfileDTO(v)
	u.classSupplied = present
	return nil
}
func (u UserDTO) MarshalJSON() ([]byte, error) {
	type plain UserDTO
	return json.Marshal(struct {
		plain
		Class any `json:"class"`
	}{plain(u), studentWireClass(u)})
}
func (u ProfileDTO) MarshalJSON() ([]byte, error) {
	type plain ProfileDTO
	return json.Marshal(struct {
		plain
		Class any `json:"class"`
	}{plain(u), nullableStudentClass(u.Class)})
}

func studentWireClass(u UserDTO) any {
	if u.Role == "student" {
		return nullableStudentClass(u.Class)
	}
	return u.Class
}
