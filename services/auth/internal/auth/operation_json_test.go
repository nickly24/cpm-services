package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOperationJSONDoesNotDiscardAmbiguousInput(t *testing.T) {
	type child struct {
		Value *string `json:"value"`
	}
	type input struct {
		Job    string          `json:"job_id"`
		Child  child           `json:"child"`
		Force  bool            `json:"force_query"`
		Target json.RawMessage `json:"target"`
	}
	valid := `{"job_id":"job","child":{"value":null},"force_query":false,"target":{"kind":"exam","exam_id":"1"}}`
	for name, raw := range map[string]string{
		"duplicate top":         strings.Replace(valid, `"job_id":"job"`, `"job_id":"a","job_id":"b"`, 1),
		"escaped duplicate":     strings.Replace(valid, `"job_id":"job"`, `"job_id":"a","job_\u0069d":"b"`, 1),
		"nested duplicate":      strings.Replace(valid, `"value":null`, `"value":null,"value":"x"`, 1),
		"raw target duplicate":  strings.Replace(valid, `"exam_id":"1"`, `"exam_id":"1","exam_id":"2"`, 1),
		"wrong case":            strings.Replace(valid, `"job_id"`, `"JOB_ID"`, 1),
		"nested wrong case":     strings.Replace(valid, `"value"`, `"Value"`, 1),
		"unknown":               strings.Replace(valid, `"force_query":false`, `"force_query":false,"unexpected":1`, 1),
		"missing explicit null": strings.Replace(valid, `"value":null`, ``, 1),
		"missing bool":          strings.Replace(valid, `,"force_query":false`, ``, 1),
		"null bool":             strings.Replace(valid, `"force_query":false`, `"force_query":null`, 1),
		"null struct":           strings.Replace(valid, `{"value":null}`, `null`, 1),
		"trailing object":       valid + `{}`,
		"invalid UTF8":          strings.Replace(valid, "job", string([]byte{0xff}), 1),
		"overdeep raw target":   strings.Replace(valid, `{"kind":"exam","exam_id":"1"}`, strings.Repeat("[", 26)+"0"+strings.Repeat("]", 26), 1),
	} {
		t.Run(name, func(t *testing.T) {
			var out input
			if decodeOperationJSON([]byte(raw), &out) == nil {
				t.Fatal("ambiguous operation input accepted")
			}
		})
	}
	var out input
	if err := decodeOperationJSON([]byte(valid), &out); err != nil || out.Child.Value != nil || out.Force || out.Job != "job" {
		t.Fatalf("valid input: %#v %v", out, err)
	}
}
