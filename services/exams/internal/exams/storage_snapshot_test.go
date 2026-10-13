package exams

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func codecExam(bankBytes, attempts int) *Exam {
	parts := []Part{{ID: 1, Code: "A", Weight: 5, Quota: 1, Revision: 1}}
	questions := []Question{}
	remaining := bankBytes
	for id := int64(1); remaining > 0; id++ {
		size := remaining
		if size > 32*1024 {
			size = 32 * 1024
		}
		questions = append(questions, Question{ID: id, PartID: 1, Text: strings.Repeat("q", size), Answer: "answer", Revision: 1})
		remaining -= size
	}
	config := Config{FractionalMode: "round_up", Revision: 1, Thresholds: []Threshold{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 5}}}
	e := &Exam{ID: 1, Name: "Exam", Kind: "classic", NextID: 999, Parts: parts, Questions: questions, Divisions: map[int64]*Division{1: {ID: 1, Config: config, Privileges: map[int64]Privilege{}}}}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for i := 0; i < attempts; i++ {
		e.Divisions[1].Attempts = append(e.Divisions[1].Attempts, Attempt{ID: int64(i + 1), Status: "in_progress", Parts: parts, Questions: questions, Config: config, Ready: map[int64]bool{21: true}, ReadyAt: map[int64]time.Time{21: now}})
	}
	return e
}
func TestStorageDefinitionsBoundedFor450Attempts(t *testing.T) {
	e := codecExam(100*1024, 450)
	next := e.NextID
	beforePart := &e.Divisions[1].Attempts[0].Parts[0]
	raw, err := encodeExam(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 2*1024*1024 {
		t.Fatalf("450 attempts repeated bank: %d bytes", len(raw))
	}
	if e.Divisions[1].Attempts[0].DefinitionKey != "" || &e.Divisions[1].Attempts[0].Parts[0] != beforePart {
		t.Fatal("encoder mutated input")
	}
	var doc examDocument
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Definitions) != 1 {
		t.Fatal("duplicate definitions", len(doc.Definitions))
	}
	loaded, err := decodeExam(raw, next)
	if err != nil {
		t.Fatal(err)
	}
	ats := loaded.Divisions[1].Attempts
	if len(ats) != 450 || loaded.NextID != next || ats[0].Questions[0].Text != e.Questions[0].Text {
		t.Fatal("incomplete hydration")
	}
	if &ats[0].Questions[0] != &ats[449].Questions[0] || &ats[0].Parts[0] != &ats[449].Parts[0] {
		t.Fatal("hydrated bank duplicated in memory")
	}
	ats[0].Ready[21] = false
	if !ats[1].Ready[21] {
		t.Fatal("mutable attempt state shared")
	}
	raw2, err := encodeExam(loaded)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := decodeExam(raw2, next)
	if err != nil || reloaded.Divisions[1].Attempts[0].Ready[21] {
		t.Fatal(err)
	}
}
func TestStorageMaximumBankSeveralAttempts(t *testing.T) {
	e := codecExam(16*1024*1024, 3)
	raw, err := encodeExam(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 35*1024*1024 {
		t.Fatalf("maximum bank duplicated per attempt: %d", len(raw))
	}
	loaded, err := decodeExam(raw, e.NextID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Divisions[1].Attempts) != 3 || len(loaded.Divisions[1].Attempts[2].Questions) != 512 {
		t.Fatal("bank truncated")
	}
	if !reflect.DeepEqual(loaded.Divisions[1].Attempts[2].Questions, e.Questions) {
		t.Fatal("snapshot changed")
	}
}
func TestStorageBackwardCompatibilityAndIntegrity(t *testing.T) {
	e := codecExam(1024, 2)
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeExam(raw, e.NextID)
	// Old aggregates gain semantic rating revisions without changing content.
	e.RatingRevision = examRatingRevision(e)
	e.Divisions[1].RatingRevision = divisionRatingRevision(e.Divisions[1])
	if err != nil || !reflect.DeepEqual(e, loaded) {
		t.Fatal("old aggregate decode", err)
	}
	compact, err := encodeExam(e)
	if err != nil {
		t.Fatal(err)
	}
	var document examDocument
	if err = json.Unmarshal(compact, &document); err != nil {
		t.Fatal(err)
	}
	var key string
	for k := range document.Definitions {
		key = k
	}
	delete(document.Definitions, key)
	broken, _ := json.Marshal(document)
	if _, err = decodeExam(broken, 0); err == nil {
		t.Fatal("missing definition accepted")
	}
	json.Unmarshal(compact, &document)
	definition := document.Definitions[key]
	definition.Questions[0].Text = "altered"
	document.Definitions[key] = definition
	broken, _ = json.Marshal(document)
	if _, err = decodeExam(broken, 0); err == nil {
		t.Fatal("altered definition accepted")
	}
	if bytes.Equal(raw, compact) {
		t.Fatal("codec did not compact")
	}
}
func TestStorageCompactPrivateDTOAndImmutableClone(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	f.start(t)
	raw, err := encodeExam(f.e)
	if err != nil {
		t.Fatal(err)
	}
	f.e, err = decodeExam(raw, f.e.NextID)
	if err != nil {
		t.Fatal(err)
	}
	state := queryDomain(t, f, f.x1, "attempt")
	dto, _ := json.Marshal(state)
	for _, private := range []string{"immutable_definitions", "definition_key", "questions_snapshot", "config_snapshot"} {
		if strings.Contains(string(dto), private) {
			t.Fatal("private persistence leak", private)
		}
	}
	before := f.attempt().Questions[0].Text
	q := f.e.Questions[0]
	f.p.QuestionID = q.ID
	f.cmd(t, f.admin, "question-update", map[string]any{"expected_revision": q.Revision, "question_text": "new bank content"})
	if f.attempt().Questions[0].Text != before {
		t.Fatal("command mutated hydrated immutable snapshot")
	}
	f.fail(t, f.x1, "vote", `{"presented_question_id":999,"round_id":999,"value":1}`, "question_not_current")
}
func BenchmarkStorage450AttemptCodec(b *testing.B) {
	e := codecExam(100*1024, 450)
	raw, err := encodeExam(e)
	if err != nil {
		b.Fatal(err)
	}
	e, err = decodeExam(raw, e.NextID)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		raw, err := encodeExam(e)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = decodeExam(raw, e.NextID); err != nil {
			b.Fatal(err)
		}
	}
}
