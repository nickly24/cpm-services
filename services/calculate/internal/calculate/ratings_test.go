package calculate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

func ratingFloat(value float64) *float64 { return &value }

func ratingTestInput() RatingInput {
	return RatingInput{JobID: "ae095746-6c38-4b77-bc40-c6d0e62d1b2d", Revision: 1, OrganizationID: 1, DivisionID: 2, AccountID: 3, InputHash: strings.Repeat("a", 64), Algorithm: RatingAlgorithm, DateFrom: "2026-10-01", DateTo: "2026-10-31", AsOf: time.Date(2026, 10, 8, 21, 0, 0, 0, time.UTC), Homework: []RatingHomeworkInput{}, Tests: []RatingTestInput{}, Exams: []RatingExamInput{}}
}

func TestPersonalRatingPreservesLegacyWeightsMissingScoresAndDirectionMeans(t *testing.T) {
	input := ratingTestInput()
	input.Homework = []RatingHomeworkInput{
		{AssignmentID: 1, TypeName: "ОВ", Deadline: "2026-10-01", Status: 1, Result: ratingFloat(100)},
		{AssignmentID: 2, TypeName: "ОВ", Deadline: "2026-10-02", Status: 0, Result: ratingFloat(100)},
		{AssignmentID: 3, TypeName: "ОВ", Deadline: "2026-10-03", Status: 1, Result: nil},
		{AssignmentID: 4, TypeName: "ОВ", Deadline: "2026-10-04", Status: 1, Result: ratingFloat(0)},
		{AssignmentID: 5, TypeName: "other", Deadline: "2026-10-05", Status: 1, Result: ratingFloat(100)},
		{AssignmentID: 6, TypeName: "ОВ", Deadline: "2026-10-09", Status: 1, Result: ratingFloat(100)},
		{AssignmentID: 7, TypeName: "ОВ", Deadline: "2026-09-30", Status: 1, Result: ratingFloat(100)},
	}
	input.Exams = []RatingExamInput{
		{ExamID: "e1", Date: "2026-10-01", Score: ratingFloat(5)},
		{ExamID: "e2", Date: "2026-10-01", MissingResult: true},
	}
	input.Tests = []RatingTestInput{
		{TestID: "1", Direction: " Математика ", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(100)},
		{TestID: "2", Direction: "МАТЕМАТИКА", Source: "external", CalculationStatus: "ready", Score: ratingFloat(50)},
		{TestID: "3", Direction: "Русский", Source: "internal", CalculationStatus: "missing"},
	}
	result, err := CalculateRating(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Homework.Rating != 25 || result.Exams.Rating != 2.5 || result.Tests.Rating != 37.5 || *result.FinalRating != 38.125 {
		t.Fatalf("formula changed: %+v", result)
	}
	if len(result.Homework.Details) != 4 || result.Homework.Details[3].Status != "Сдано" || result.Homework.Details[3].Score != 0 {
		t.Fatal("completed zero/missing homework semantics changed")
	}
	if len(result.Tests.Directions) != 2 || result.Tests.Directions["Математика"] != 75 || result.Tests.Directions["Русский"] != 0 {
		t.Fatal("direction means or first labels changed")
	}
	if result.Exams.Details[1].Status != "Не сдавал" || !result.Exams.Details[1].MissingResult {
		t.Fatal("missing exam lost")
	}
}

func TestPersonalRatingEmptyComponentsDoNotRedistributeOrClampWeights(t *testing.T) {
	input := ratingTestInput()
	result, err := CalculateRating(input)
	if err != nil || *result.FinalRating != 0 || result.Tests.Details == nil || result.Tests.Directions == nil || result.Homework.Details == nil || result.Exams.Details == nil {
		t.Fatal("empty rating must publish an explicit zero snapshot")
	}
	input.Exams = []RatingExamInput{{ExamID: "e", Date: "2026-10-01", Score: ratingFloat(6)}}
	result, err = CalculateRating(input)
	if err != nil || *result.FinalRating != 36 {
		t.Fatal("owner score was clamped or absent weights redistributed")
	}
	input.Exams = nil
	input.Tests = []RatingTestInput{{TestID: "t", Direction: "", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(100)}}
	result, err = CalculateRating(input)
	if err != nil || *result.FinalRating != 45 || result.Tests.Directions["Неизвестное направление"] != 100 {
		t.Fatal("tests-only formula changed")
	}
}

func TestPersonalRatingKeepsIEEEOperationOrderAndNoRounding(t *testing.T) {
	input := ratingTestInput()
	input.Homework = []RatingHomeworkInput{{AssignmentID: 1, TypeName: "ОВ", Deadline: "2026-10-01", Status: 1, Result: ratingFloat(1.0 / 3.0)}}
	input.Exams = []RatingExamInput{{ExamID: "e", Date: "2026-10-01", Score: ratingFloat(2.0 / 3.0)}}
	input.Tests = []RatingTestInput{{TestID: "t", Direction: "D", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(1.0 / 7.0)}}
	result, err := CalculateRating(input)
	if err != nil {
		t.Fatal(err)
	}
	hw := *input.Homework[0].Result
	ex := *input.Exams[0].Score
	ts := *input.Tests[0].Score
	want := float64((hw*25)/100) + float64(ex*6) + float64((ts*45)/100)
	if math.Float64bits(*result.FinalRating) != math.Float64bits(want) {
		t.Fatalf("aggregate precision changed: %.17g %.17g", *result.FinalRating, want)
	}
}

func TestPersonalRatingMoscowDeadlineAndUnicodeCasefold(t *testing.T) {
	input := ratingTestInput()
	input.AsOf = time.Date(2026, 10, 8, 21, 0, 0, 0, time.UTC) // Moscow: Oct 9.
	input.Homework = []RatingHomeworkInput{{AssignmentID: 1, TypeName: "ОВ", Deadline: "2026-10-08", Status: 1, Result: ratingFloat(100)}, {AssignmentID: 2, TypeName: "ОВ", Deadline: "2026-10-09", Status: 1, Result: ratingFloat(100)}}
	input.Tests = []RatingTestInput{{TestID: "1", Direction: "\x1fStraße\x1c", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(100)}, {TestID: "2", Direction: "STRASSE", Source: "external", CalculationStatus: "ready", Score: ratingFloat(0)}}
	result, err := CalculateRating(input)
	if err != nil || len(result.Homework.Details) != 1 || len(result.Tests.Directions) != 1 || result.Tests.Directions["Straße"] != 50 {
		t.Fatalf("Moscow/casefold mismatch: %+v %v", result, err)
	}
}

func TestPersonalRatingPendingResultsNeverBecomeZero(t *testing.T) {
	for _, state := range []string{"queued", "processing", "retrying", "failed"} {
		input := ratingTestInput()
		input.Tests = []RatingTestInput{{TestID: "t", Source: "internal", CalculationStatus: state}}
		if _, err := CalculateRating(input); !errors.Is(err, ErrRatingPending) {
			t.Fatalf("%s became a usable zero: %v", state, err)
		}
	}
	for _, score := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		input := ratingTestInput()
		input.Tests = []RatingTestInput{{TestID: "t", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(score)}}
		if _, err := CalculateRating(input); !errors.Is(err, ErrInvalidRatingInput) {
			t.Fatal("accepted nonfinite score")
		}
	}
	input := ratingTestInput()
	input.Tests = []RatingTestInput{{TestID: "t", Source: "internal", CalculationStatus: "ready"}}
	if _, err := CalculateRating(input); !errors.Is(err, ErrInvalidRatingInput) {
		t.Fatal("ready result without score was accepted")
	}
}

func hashedRatingInput(t *testing.T, input RatingInput) (RatingInput, []byte) {
	t.Helper()
	raw, _ := json.Marshal(input)
	hash, err := RatingInputHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	input.InputHash = hash
	raw, _ = json.Marshal(input)
	return input, raw
}

func ratingMessage(t *testing.T, input RatingInput) Message {
	t.Helper()
	payload, _ := json.Marshal(ratingInputRequest(input))
	body, _ := json.Marshal(Envelope{MessageID: "bd095746-6c38-4b77-bc40-c6d0e62d1b2d", MessageType: RatingsMessageType, SchemaVersion: 1, OccurredAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), Producer: "ratings-service", CorrelationID: input.JobID, Payload: payload})
	m, err := ParseRatingMessage(body, RatingsRoutingKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m.LeaseToken = "lease"
	return m
}

func TestRatingIngressIsIndependentAndPersistsBeforeACK(t *testing.T) {
	input, _ := hashedRatingInput(t, ratingTestInput())
	m := ratingMessage(t, input)
	if m.JobID != input.JobID || m.InputHash != input.InputHash || m.Revision != 1 {
		t.Fatal("missing job metadata")
	}
	if _, err := ParseMessage(m.Body, RatingsRoutingKey, time.Now()); !errors.Is(err, ErrInvalidMessage) {
		t.Fatal("rating entered grading inbox")
	}
	if _, err := ParseRatingMessage(validBody(), "tests.grade.requested.v1", time.Now()); !errors.Is(err, ErrInvalidMessage) {
		t.Fatal("grading entered rating inbox")
	}
	store := &memoryRepository{}
	consumer := &Consumer{Store: store, Parser: ParseRatingMessage, Queue: RatingsQueueName}
	for i := 0; i < 2; i++ {
		ack := &recordedAck{t: t, store: store}
		if err := consumer.Handle(context.Background(), m.Body, RatingsRoutingKey, ack); err != nil || !ack.acked || ack.nacked {
			t.Fatal("rating was not durably ACKed")
		}
	}
	if len(store.items) != 1 {
		t.Fatal("duplicate rating duplicated receipt")
	}
	for _, body := range [][]byte{bytes.Replace(m.Body, []byte(`"account_id":3`), []byte(`"account_id":3,"account_id":4`), 1), bytes.Replace(m.Body, []byte(`"account_id":3`), []byte(`"account_id":0`), 1), bytes.Replace(m.Body, []byte(`"revision":1`), []byte(`"revision":0`), 1), bytes.Replace(m.Body, []byte("ratings-service"), []byte("tests-service"), 1)} {
		if _, err := ParseRatingMessage(body, RatingsRoutingKey, time.Now()); !errors.Is(err, ErrInvalidMessage) {
			t.Fatal("ambiguous rating accepted")
		}
	}
}

func TestRatingHashCanonicalizesKeysAndRejectsAmbiguity(t *testing.T) {
	a, err := RatingInputHash([]byte(`{"account_id":9007199254740993,"nested":{"b":1.25,"a":2},"input_hash":"ignored"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := RatingInputHash([]byte(`{"nested":{"a":2,"b":1.25},"input_hash":"another","account_id":9007199254740993}`))
	if err != nil || a != b {
		t.Fatal("canonical hash changed keys or 64-bit ID")
	}
	if _, err := RatingInputHash([]byte(`{"account_id":1,"account_id":2}`)); !errors.Is(err, ErrInvalidRatingInput) {
		t.Fatal("ambiguous hash accepted")
	}
}

func TestRatingWorkerCachesBeforeCallbackAndReplaysAfterLostResponse(t *testing.T) {
	input, raw := hashedRatingInput(t, ratingTestInput())
	m := ratingMessage(t, input)
	store := &processingMemory{}
	getCalls, postCalls := 0, 0
	var first []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Key") != "test-key" {
			t.Error("internal key not present")
		}
		if r.Method == http.MethodGet {
			getCalls++
			reply(w, 200, json.RawMessage(raw))
			return
		}
		postCalls++
		var callback RatingResult
		if err := json.NewDecoder(r.Body).Decode(&callback); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(callback)
		if !bytes.Equal(store.checkpoint, encoded) {
			t.Error("callback preceded durable output")
		}
		if postCalls == 1 {
			first = append([]byte(nil), encoded...)
			fail(w, 503, "temporarily_unavailable")
			return
		}
		if !bytes.Equal(first, encoded) {
			t.Error("retry changed computed output")
		}
		reply(w, 200, map[string]any{})
	}))
	defer server.Close()
	worker := &RatingWorker{Store: store, Ratings: NewRatingsClient(server.URL, "test-key")}
	_, _, output, err := worker.Process(context.Background(), m)
	if err == nil || len(output) == 0 || store.checkpointCalls != 1 {
		t.Fatal("lost response did not retain checkpoint")
	}
	m.Output = store.checkpoint
	status, _, output, err := worker.Process(context.Background(), m)
	if err != nil || status != "ready" || getCalls != 1 || postCalls != 2 || !bytes.Equal(first, output) {
		t.Fatalf("replay failed %s %v", status, err)
	}
}

func TestRatingWorkerRejectsTamperedHashAndForeignScope(t *testing.T) {
	base, _ := hashedRatingInput(t, ratingTestInput())
	for _, kind := range []string{"hash", "scope", "job", "revision"} {
		t.Run(kind, func(t *testing.T) {
			input := base
			switch kind {
			case "hash":
				input.DateTo = "2026-10-30"
			case "scope":
				input.DivisionID++
			case "job":
				input.JobID = "be095746-6c38-4b77-bc40-c6d0e62d1b2d"
			case "revision":
				input.Revision++
			}
			raw, _ := json.Marshal(input)
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
				}
				reply(w, 200, json.RawMessage(raw))
			}))
			defer server.Close()
			store := &processingMemory{}
			worker := &RatingWorker{Store: store, Ratings: NewRatingsClient(server.URL, "key")}
			if _, _, _, err := worker.Process(context.Background(), ratingMessage(t, base)); !errors.Is(err, ErrInvalidRatingInput) || posts != 0 || store.checkpointCalls != 0 {
				t.Fatal("foreign/tampered snapshot computed")
			}
		})
	}
}

func TestRatingClientNoRedirectAndSafeErrorClassification(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true; reply(w, 200, nil) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	_, err := NewRatingsClient(redirect.URL, "secret-key").call(context.Background(), "GET", "/input", nil)
	if err == nil || leaked || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), target.URL) {
		t.Fatal("redirect leaked credentials")
	}
	for _, test := range []struct {
		err    error
		status string
	}{{&ratingRemoteError{409, "cancelled"}, "skipped"}, {&ratingRemoteError{409, "result_idempotency_conflict"}, "failed"}, {&ratingRemoteError{404, "removed"}, "skipped"}, {&ratingRemoteError{429, ""}, "retrying"}, {&ratingRemoteError{503, ""}, "retrying"}, {ErrRatingPending, "retrying"}, {ErrInvalidRatingInput, "failed"}} {
		status, _ := classifyRatingError(test.err)
		if status != test.status {
			t.Fatal("wrong error classification")
		}
	}
	badGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>outage</html>"))
	}))
	defer badGateway.Close()
	_, err = NewRatingsClient(badGateway.URL, "key").call(context.Background(), "GET", "/input", nil)
	if status, _ := classifyRatingError(err); status != "retrying" {
		t.Fatal("non-JSON gateway outage became permanent failure")
	}
}

type ratingRunStore struct {
	processingMemory
	message Message
	claimed bool
	done    chan string
	mu      sync.Mutex
}

func (s *ratingRunStore) Claim(context.Context, time.Time, string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return Message{}, mongo.ErrNoDocuments
	}
	s.claimed = true
	return s.message, nil
}
func (s *ratingRunStore) Finish(_ context.Context, _ Message, status, reason string, _ json.RawMessage) error {
	s.done <- status + ":" + reason
	return nil
}
func (s *ratingRunStore) Retry(context.Context, Message, string, time.Time) error {
	s.done <- "retrying"
	return nil
}

func TestRatingWorkerReportsPermanentAndExhaustedFailures(t *testing.T) {
	for _, kind := range []string{"invalid", "exhausted"} {
		t.Run(kind, func(t *testing.T) {
			input, raw := hashedRatingInput(t, ratingTestInput())
			m := ratingMessage(t, input)
			if kind == "exhausted" {
				m.ProcessingAttempts = RatingMaxAttempts
			}
			store := &ratingRunStore{message: m, done: make(chan string, 1)}
			posted := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					if kind == "exhausted" {
						fail(w, 503, "unavailable")
						return
					}
					bad := bytes.Replace(raw, []byte(`"division_id":2`), []byte(`"division_id":9`), 1)
					reply(w, 200, json.RawMessage(bad))
					return
				}
				posted++
				var result RatingResult
				_ = json.NewDecoder(r.Body).Decode(&result)
				if result.Status != "failed" || result.ErrorCode == "" || len(store.checkpoint) == 0 {
					t.Error("failure was not persisted before callback")
				}
				reply(w, 200, map[string]any{})
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			worker := &RatingWorker{Store: store, Ratings: NewRatingsClient(server.URL, "key")}
			go worker.Run(ctx)
			select {
			case status := <-store.done:
				if !strings.HasPrefix(status, "failed:") || posted != 1 {
					t.Fatalf("terminal failure not reported: %s", status)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("worker failed to complete")
			}
			cancel()
		})
	}
}

func TestRatingsConfigurationIsOptionalAndEndpointRestricted(t *testing.T) {
	for key, value := range map[string]string{"MONGO_HOST": "localhost", "MONGO_PORT": "27018", "MONGO_DATABASE": "cpm_dev", "MONGO_USER": "dev", "MONGO_PASSWORD": "synthetic", "RABBITMQ_URL": "amqp://dev:synthetic@localhost:5672/", "AUTH_INTERNAL_KEY": strings.Repeat("k", 48), "CALCULATE_TESTS_URL": "http://tests:8004"} {
		t.Setenv(key, value)
	}
	t.Setenv("CALCULATE_RATINGS_URL", "")
	config, err := LoadConfig()
	if err != nil || config.RatingsURL != "" {
		t.Fatal("optional integration broke existing config")
	}
	for _, endpoint := range []string{"http://ratings:8010/", "http://localhost:8010", "http://127.0.0.1:8010", "https://ratings.example.com"} {
		t.Setenv("CALCULATE_RATINGS_URL", endpoint)
		config, err = LoadConfig()
		if err != nil || strings.HasSuffix(config.RatingsURL, "/") {
			t.Fatal("valid endpoint was rejected")
		}
	}
	for _, endpoint := range []string{"http://external.example.com", "http://ratings/user-input", "https://ratings?key=private", "https://user:password@ratings", "https://ratings#fragment", "ftp://ratings"} {
		t.Setenv("CALCULATE_RATINGS_URL", endpoint)
		if _, err := LoadConfig(); err == nil {
			t.Fatal("unsafe endpoint was accepted")
		}
	}
}

func TestRatingDiagnosticsArePrivateAndListsExcludeCachedPersonalDetails(t *testing.T) {
	input, _ := hashedRatingInput(t, ratingTestInput())
	m := ratingMessage(t, input)
	m.Output = json.RawMessage(`{"private_details":"name and grades"}`)
	store := &memoryRepository{items: map[string]Message{m.ID: m}}
	server := &Server{RatingsStore: store, ServiceKey: strings.Repeat("k", 48)}
	for _, path := range []string{"/internal/calculate/ratings/messages", "/internal/calculate/ratings/messages/" + m.ID} {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal("rating diagnostics were not protected")
		}
	}
	r := httptest.NewRequest("GET", "/internal/calculate/ratings/messages", nil)
	r.Header.Set("X-Service-Key", server.ServiceKey)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("private_details")) || bytes.Contains(w.Body.Bytes(), []byte(`"output"`)) {
		t.Fatal("rating list leaked personal cached details")
	}
	r = httptest.NewRequest("GET", "/internal/calculate/ratings/messages/"+m.ID, nil)
	r.Header.Set("X-Service-Key", server.ServiceKey)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("private_details")) {
		t.Fatal("authorized diagnostic detail lost its cached output")
	}
}
