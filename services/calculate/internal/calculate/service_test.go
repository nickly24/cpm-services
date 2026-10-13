package calculate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func validBody() []byte {
	body, _ := json.Marshal(Envelope{MessageID: "bd095746-6c38-4b77-bc40-c6d0e62d1b2d", MessageType: "tests.grade.requested", SchemaVersion: 1, OccurredAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), Producer: "tests-service", CorrelationID: "operation-123", Payload: json.RawMessage(`{"submission_id":"507f1f77bcf86cd799439011","test_id":"507f1f77bcf86cd799439012","organization_id":1,"division_id":2,"answers":[]}`)})
	return body
}

func TestEnvelopeRejectsAmbiguousOrUnsupportedMessages(t *testing.T) {
	body := validBody()
	got, err := ParseMessage(body, "tests.grade.requested.v1", time.Now())
	if err != nil || got.Status != "received" || got.SubmissionID != "507f1f77bcf86cd799439011" {
		t.Fatalf("valid receipt failed: %+v %v", got, err)
	}
	if bytes.Equal(got.Body, nil) || got.BodyHash == "" || got.PayloadHash == "" {
		t.Fatal("immutable input not captured")
	}
	if got.OrganizationID != 1 || got.DivisionID != 2 {
		t.Fatal("scope metadata was not extracted")
	}
	tests := map[string][]byte{
		"duplicate envelope field":  bytes.Replace(body, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":2`), 1),
		"duplicate nested field":    bytes.Replace(body, []byte(`"division_id":2`), []byte(`"division_id":2,"division_id":3`), 1),
		"unknown schema":            bytes.Replace(body, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"wrong producer":            bytes.Replace(body, []byte(`tests-service`), []byte(`another-service`), 1),
		"non-UTC time":              bytes.Replace(body, []byte(`12:00:00Z`), []byte(`12:00:00+03:00`), 1),
		"trailing JSON":             append(append([]byte(nil), body...), []byte(` {}`)...),
		"missing submission":        bytes.Replace(body, []byte(`"submission_id"`), []byte(`"other_id"`), 1),
		"invalid division identity": bytes.Replace(body, []byte(`"division_id":2`), []byte(`"division_id":-1`), 1),
		"null payload":              []byte(`{"message_id":"bd095746-6c38-4b77-bc40-c6d0e62d1b2d","message_type":"tests.grade.requested","schema_version":1,"occurred_at":"2026-10-07T12:00:00Z","producer":"tests-service","correlation_id":"a","payload":null}`),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMessage(candidate, "tests.grade.requested.v1", time.Now()); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("accepted invalid input: %v", err)
			}
		})
	}
	if _, err := ParseMessage(body, "tests.content.changed.v1", time.Now()); !errors.Is(err, ErrInvalidMessage) {
		t.Fatal("mismatched route accepted")
	}
	if _, err := ParseMessage(bytes.Repeat([]byte("x"), MaxMessageBytes+1), "tests.grade.requested.v1", time.Now()); !errors.Is(err, ErrInvalidMessage) {
		t.Fatal("oversized message accepted")
	}
}

type memoryRepository struct {
	mu        sync.Mutex
	items     map[string]Message
	err       error
	persisted bool
}

func (m *memoryRepository) Receive(ctx context.Context, value Message) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	if m.items == nil {
		m.items = map[string]Message{}
	}
	if existing, exists := m.items[value.ID]; exists {
		if existing.BodyHash != value.BodyHash {
			return false, ErrMessageConflict
		}
		return false, nil
	}
	m.items[value.ID] = value
	m.persisted = true
	return true, nil
}

func (m *memoryRepository) Get(ctx context.Context, id string) (Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.items[id], m.err
}

func (m *memoryRepository) List(ctx context.Context, filter Filter) ([]Message, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	values := []Message{}
	for _, item := range m.items {
		values = append(values, item)
	}
	return values, int64(len(values)), m.err
}

func (m *memoryRepository) Ready(context.Context) error { return m.err }

type recordedAck struct {
	t      *testing.T
	store  *memoryRepository
	acked  bool
	nacked bool
	retry  bool
}

func (a *recordedAck) Ack(multiple bool) error {
	if !a.store.persisted || multiple {
		a.t.Fatal("ACK happened before durable persistence")
	}
	a.acked = true
	return nil
}

func (a *recordedAck) Nack(multiple, requeue bool) error {
	if multiple {
		a.t.Fatal("multiple delivery NACK unsupported")
	}
	a.nacked, a.retry = true, requeue
	return nil
}

func TestPersistencePrecedesAckAndConflictsNeverOverwrite(t *testing.T) {
	store := &memoryRepository{}
	consumer := &Consumer{Store: store}
	for i := 0; i < 2; i++ {
		ack := &recordedAck{t: t, store: store}
		if err := consumer.Handle(context.Background(), validBody(), "tests.grade.requested.v1", ack); err != nil || !ack.acked || ack.nacked {
			t.Fatalf("valid delivery not acknowledged: %v %+v", err, ack)
		}
	}
	if len(store.items) != 1 {
		t.Fatal("duplicate delivery inserted twice")
	}
	ack := &recordedAck{t: t, store: store}
	changed := bytes.Replace(validBody(), []byte(`"division_id":2`), []byte(`"division_id":3`), 1)
	if err := consumer.Handle(context.Background(), changed, "tests.grade.requested.v1", ack); err != nil || !ack.nacked || ack.retry || ack.acked {
		t.Fatal("conflicting identity was not dead-lettered")
	}
	if !bytes.Equal(store.items["bd095746-6c38-4b77-bc40-c6d0e62d1b2d"].Body, validBody()) {
		t.Fatal("immutable receipt overwritten")
	}
}

func TestStorageFailureRequeuesWithoutAck(t *testing.T) {
	store := &memoryRepository{err: errors.New("database unavailable")}
	ack := &recordedAck{t: t, store: store}
	consumer := &Consumer{Store: store}
	if err := consumer.Handle(context.Background(), validBody(), "tests.grade.requested.v1", ack); err == nil || ack.acked || !ack.nacked || !ack.retry {
		t.Fatal("failed persistence must be retried and never ACKed")
	}
	if len(store.items) != 0 {
		t.Fatal("failed receipt wrote data")
	}
}

func TestServiceKeyProtectsMessagesAndPractice(t *testing.T) {
	key := strings.Repeat("k", 48)
	store := &memoryRepository{}
	server := &Server{Store: store, ServiceKey: key, Consumer: &Consumer{}}
	for _, path := range []string{"/internal/calculate/messages", "/internal/calculate/practice/check", "/internal/calculate/practice/finish"} {
		method := "GET"
		if strings.Contains(path, "/practice/") {
			method = "POST"
		}
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-Service-Key", "wrong")
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatal("private route exposed")
		}
	}
	r := httptest.NewRequest("GET", "/internal/calculate/messages?limit=101", nil)
	r.Header.Set("X-Service-Key", key)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatal("pagination bound not enforced")
	}
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("disconnected consumer considered ready")
	}
	server.Consumer.Connected.Store(true)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatal("connected healthy service not ready")
	}
}

func TestPracticePreservesLegacyScoring(t *testing.T) {
	single := Question{ID: 1, Type: "single", Text: "Choose", Points: 5, Answers: []Option{{ID: "a", IsCorrect: true}, {ID: "b"}}}
	multiple := Question{ID: 2, Type: "multiple", Text: "Choose", Points: 7, Answers: []Option{{ID: "a", IsCorrect: true}, {ID: "b"}, {ID: "c", IsCorrect: true}}}
	text := Question{ID: 3, Type: "text", Text: "Enter", Points: 3, CorrectAnswers: []string{"Москва", "İ", "ΟΣ"}}
	tests := []struct {
		name    string
		q       Question
		value   string
		correct bool
		invalid bool
	}{
		{"single correct", single, `"a"`, true, false},
		{"single wrong", single, `"b"`, false, false},
		{"single unknown", single, `"c"`, false, true},
		{"single null", single, `null`, false, true},
		{"multiple shuffled", multiple, `["c","a"]`, true, false},
		{"multiple incomplete", multiple, `["a"]`, false, false},
		{"multiple extra", multiple, `["a","b","c"]`, false, false},
		{"multiple duplicate", multiple, `["a","a"]`, false, true},
		{"multiple null", multiple, `null`, false, true},
		{"text trimmed Cyrillic", text, `"  МОСКВА  "`, true, false},
		{"text Unicode full lower", text, `"i̇"`, true, false},
		{"text contextual sigma", text, `"ος"`, true, false},
		{"text no substring", text, `"Москва!"`, false, false},
		{"text wrong type", text, `["Москва"]`, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := CheckPractice(PracticeInput{Question: test.q, Value: json.RawMessage(test.value)})
			if (err != nil) != test.invalid {
				t.Fatalf("validation mismatch: %v", err)
			}
			if err != nil {
				return
			}
			expectedPoints := int64(0)
			if test.correct {
				expectedPoints = test.q.Points
			}
			if result.IsCorrect != test.correct || result.Points != expectedPoints || result.PossiblePoints != test.q.Points || result.CorrectAnswer == nil {
				t.Fatalf("legacy scoring mismatch: %+v", result)
			}
		})
	}
}

func TestPracticeFinishGradesPinnedSnapshotAndMissingAnswers(t *testing.T) {
	questions := []Question{
		{ID: 1, Type: "single", Text: "Choose", Points: 5, Answers: []Option{{ID: "a", IsCorrect: true}, {ID: "b"}}},
		{ID: 2, Type: "multiple", Text: "Choose", Points: 7, Answers: []Option{{ID: "a", IsCorrect: true}, {ID: "b"}, {ID: "c", IsCorrect: true}}},
		{ID: 3, Type: "text", Text: "Enter", Points: 3, CorrectAnswers: []string{"Москва"}},
	}
	input := PracticeFinishInput{Questions: questions, Answers: []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`"a"`)}, {QuestionID: 3, Value: json.RawMessage(`"Москва!"`)}}}
	result, err := FinishPractice(input)
	if err != nil || result.Score != 33 || result.EarnedPoints != 5 || result.MaxPoints != 15 || len(result.Questions) != 3 {
		t.Fatalf("wrong finished training: %+v %v", result, err)
	}
	if result.Questions[1].Points != 0 || result.Questions[1].IsCorrect {
		t.Fatal("unanswered question awarded points")
	}
	result, err = FinishPractice(PracticeFinishInput{Questions: questions})
	if err != nil || result.Score != 0 || result.EarnedPoints != 0 || result.MaxPoints != 15 {
		t.Fatal("unanswered training must remain zero")
	}
	for name, invalid := range map[string]PracticeFinishInput{
		"duplicate answer":            {Questions: questions, Answers: []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`"a"`)}, {QuestionID: 1, Value: json.RawMessage(`"a"`)}}},
		"unknown question":            {Questions: questions, Answers: []PracticeAnswer{{QuestionID: 4, Value: json.RawMessage(`"a"`)}}},
		"duplicate question":          {Questions: []Question{questions[0], questions[0]}},
		"invalid unanswered question": {Questions: []Question{{ID: 1, Type: "single", Points: 2, Answers: []Option{{ID: "a", IsCorrect: true}}}}},
		"empty snapshot":              {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FinishPractice(invalid); !errors.Is(err, errPractice) {
				t.Fatal("invalid training snapshot accepted")
			}
		})
	}
}

func TestPracticePercentageMatchesLegacyFloatBoundaries(t *testing.T) {
	for _, test := range []struct {
		earned, maximum int64
		expected        int
	}{{399, 20000, 1}, {599, 20000, 3}, {19999, 20000, 100}, {1, 3, 33}, {2, 3, 66}, {0, 5, 0}, {5, 5, 100}} {
		if got := legacyPercentage(test.earned, test.maximum); got != test.expected {
			t.Fatalf("legacy percentage %d/%d: got %d, expected %d", test.earned, test.maximum, got, test.expected)
		}
	}
	questions := []Question{
		{ID: 1, Type: "single", Points: 399, Answers: []Option{{ID: "a", IsCorrect: true}, {ID: "b"}}},
		{ID: 2, Type: "text", Points: 19601, CorrectAnswers: []string{"yes"}},
	}
	result, err := FinishPractice(PracticeFinishInput{Questions: questions, Answers: []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(`"a"`)}}})
	if err != nil || result.Score != 1 || result.EarnedPoints != 399 || result.MaxPoints != 20000 {
		t.Fatalf("training boundary regression: %+v %v", result, err)
	}
}

func TestInboxListExcludesLargePayloadButDetailRetainsIt(t *testing.T) {
	key := strings.Repeat("k", 48)
	largeBody := bytes.Replace(validBody(), []byte(`"answers":[]`), []byte(`"answers":[],"editor_blob":"`+strings.Repeat("x", 2<<20)+`"`), 1)
	message, err := ParseMessage(largeBody, "tests.grade.requested.v1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryRepository{}
	_, _ = store.Receive(context.Background(), message)
	server := &Server{Store: store, ServiceKey: key}
	request := httptest.NewRequest("GET", "/internal/calculate/messages?limit=100", nil)
	request.Header.Set("X-Service-Key", key)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.Len() > 2048 || bytes.Contains(response.Body.Bytes(), []byte(`"payload"`)) || bytes.Contains(response.Body.Bytes(), []byte(`"body"`)) {
		t.Fatalf("metadata list included large payload: status=%d size=%d", response.Code, response.Body.Len())
	}
	request = httptest.NewRequest("GET", "/internal/calculate/messages/"+message.ID, nil)
	request.Header.Set("X-Service-Key", key)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"editor_blob"`)) || response.Body.Len() < 2<<20 {
		t.Fatal("message detail lost immutable full payload")
	}
}
