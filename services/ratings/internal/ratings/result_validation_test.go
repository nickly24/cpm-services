package ratings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func detailedReadyResult() Result {
	r := readyResult(fixtureJob(), 71)
	r.Homework = json.RawMessage(`{"rating":0,"details":[{"homework_id":1,"name":"","deadline":"2026-10-01","score":0,"status":"Сдано","date_pass":null}]}`)
	r.Tests = json.RawMessage(`{"rating":0,"directions":{"Math":0},"details":[{"test_id":"shared-id","title":"","direction":"Math","score":0,"source":"internal","calculation_status":"ready"},{"test_id":"shared-id","title":"","direction":"Math","score":0,"source":"external","calculation_status":"missing"}]}`)
	r.Exams = json.RawMessage(`{"rating":0,"details":[{"exam_id":"shared-exam","exam_name":"","exam_date":"2026-10-01","score":0,"status":"Сдан","exam_type":"outside_lms","direction_id":null,"attempt_no":null,"has_appeal":false,"missing_result":false}]}`)
	return r
}

func resultComponent(r *Result, kind string) *json.RawMessage {
	switch kind {
	case "homework":
		return &r.Homework
	case "tests":
		return &r.Tests
	default:
		return &r.Exams
	}
}

func changeResultComponent(t *testing.T, r *Result, kind string, change func(map[string]json.RawMessage)) {
	t.Helper()
	raw := resultComponent(r, kind)
	var component map[string]json.RawMessage
	if err := json.Unmarshal(*raw, &component); err != nil {
		t.Fatal(err)
	}
	change(component)
	var err error
	*raw, err = json.Marshal(component)
	if err != nil {
		t.Fatal(err)
	}
}

func changeResultDetail(t *testing.T, r *Result, kind string, change func(map[string]json.RawMessage)) {
	t.Helper()
	changeResultComponent(t, r, kind, func(component map[string]json.RawMessage) {
		var details []json.RawMessage
		if err := json.Unmarshal(component["details"], &details); err != nil {
			t.Fatal(err)
		}
		var detail map[string]json.RawMessage
		if err := json.Unmarshal(details[0], &detail); err != nil {
			t.Fatal(err)
		}
		change(detail)
		details[0], _ = json.Marshal(detail)
		component["details"], _ = json.Marshal(details)
	})
}

func TestResultDetailsRequireObjectsAndFiniteExplicitScores(t *testing.T) {
	for _, kind := range []string{"homework", "tests", "exams"} {
		for _, invalid := range []string{"null", `[]`, `"detail"`, "{}"} {
			t.Run(kind+"/element/"+invalid, func(t *testing.T) {
				r := detailedReadyResult()
				changeResultComponent(t, &r, kind, func(c map[string]json.RawMessage) { c["details"] = json.RawMessage("[" + invalid + "]") })
				assertAPIError(t, validateResult(r), 400, "incomplete_rating_result")
			})
		}
		for _, invalid := range []string{"omitted", "null", "2e309", `"0"`, "false"} {
			t.Run(kind+"/score/"+invalid, func(t *testing.T) {
				r := detailedReadyResult()
				changeResultDetail(t, &r, kind, func(d map[string]json.RawMessage) {
					if invalid == "omitted" {
						delete(d, "score")
					} else {
						d["score"] = json.RawMessage(invalid)
					}
				})
				assertAPIError(t, validateResult(r), 400, "incomplete_rating_result")
			})
		}
	}
}

func TestResultTestDirectionsRequireFiniteObjectIncludingEmptyCatalog(t *testing.T) {
	for _, invalid := range []string{"omitted", "null", "[]", "0", `{"Math":null}`, `{"Math":"0"}`, `{"Math":2e309}`} {
		t.Run(invalid, func(t *testing.T) {
			r := readyResult(fixtureJob(), 71)
			changeResultComponent(t, &r, "tests", func(c map[string]json.RawMessage) {
				if invalid == "omitted" {
					delete(c, "directions")
				} else {
					c["directions"] = json.RawMessage(invalid)
				}
			})
			assertAPIError(t, validateResult(r), 400, "incomplete_rating_result")
		})
	}
	if err := validateResult(readyResult(fixtureJob(), 71)); err != nil {
		t.Fatal("empty directions object and genuine zero result must remain valid", err)
	}
}

func TestResultDetailsValidateIdentityStatusAndNullableMetadata(t *testing.T) {
	cases := []struct {
		kind, field, value string
	}{
		{"homework", "homework_id", "0"},
		{"homework", "homework_id", "1.5"},
		{"homework", "name", "null"},
		{"homework", "deadline", `"2026-02-30"`},
		{"homework", "status", `"ready"`},
		{"homework", "date_pass", "false"},
		{"homework", "date_pass", "omitted"},
		{"tests", "test_id", `""`},
		{"tests", "test_id", "1"},
		{"tests", "title", "null"},
		{"tests", "direction", "omitted"},
		{"tests", "source", `"MongoDB"`},
		{"tests", "calculation_status", "null"},
		{"tests", "calculation_status", `"processing"`},
		{"tests", "calculation_status", `""`},
		{"exams", "exam_id", `""`},
		{"exams", "exam_id", "1"},
		{"exams", "exam_name", "null"},
		{"exams", "exam_date", `"2026-02-30"`},
		{"exams", "status", `"Сдано"`},
		{"exams", "exam_type", "null"},
		{"exams", "direction_id", "0"},
		{"exams", "direction_id", "omitted"},
		{"exams", "attempt_no", "1.5"},
		{"exams", "attempt_no", "0"},
		{"exams", "has_appeal", "null"},
		{"exams", "missing_result", "omitted"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.field+"/"+tc.value, func(t *testing.T) {
			r := detailedReadyResult()
			changeResultDetail(t, &r, tc.kind, func(d map[string]json.RawMessage) {
				if tc.value == "omitted" {
					delete(d, tc.field)
				} else {
					d[tc.field] = json.RawMessage(tc.value)
				}
			})
			assertAPIError(t, validateResult(r), 400, "incomplete_rating_result")
		})
	}
	for _, kind := range []string{"homework", "tests", "exams"} {
		t.Run(kind+"/duplicate_identity", func(t *testing.T) {
			r := detailedReadyResult()
			changeResultComponent(t, &r, kind, func(c map[string]json.RawMessage) {
				var details []json.RawMessage
				_ = json.Unmarshal(c["details"], &details)
				details = append(details, details[0])
				c["details"], _ = json.Marshal(details)
			})
			assertAPIError(t, validateResult(r), 400, "incomplete_rating_result")
		})
	}
}

func TestResultMissingStatusesCannotCarryNonzeroScores(t *testing.T) {
	for _, kind := range []string{"homework", "tests", "exams"} {
		t.Run(kind, func(t *testing.T) {
			r := detailedReadyResult()
			changeResultDetail(t, &r, kind, func(d map[string]json.RawMessage) {
				d["score"] = json.RawMessage("1")
				switch kind {
				case "homework":
					d["status"] = json.RawMessage(`"Не сдано"`)
				case "tests":
					d["calculation_status"] = json.RawMessage(`"missing"`)
				case "exams":
					d["missing_result"], d["status"] = json.RawMessage("true"), json.RawMessage(`"Не сдавал"`)
				}
			})
			assertAPIError(t, validateResult(r), 400, "incomplete_rating_result")
		})
	}
	for _, missing := range []bool{false, true} {
		r := detailedReadyResult()
		changeResultDetail(t, &r, "exams", func(d map[string]json.RawMessage) {
			if missing {
				d["missing_result"] = json.RawMessage("true") // Completed status with missing_result is contradictory even at zero.
			} else {
				d["status"] = json.RawMessage(`"Не сдавал"`)
			}
		})
		assertAPIError(t, validateResult(r), 400, "incomplete_rating_result")
	}
}

func TestResultHistoricalTestStatusOmissionPreservesBytesAndHash(t *testing.T) {
	r := detailedReadyResult()
	changeResultComponent(t, &r, "tests", func(c map[string]json.RawMessage) {
		var details []map[string]json.RawMessage
		_ = json.Unmarshal(c["details"], &details)
		for _, d := range details {
			delete(d, "calculation_status")
		}
		c["details"], _ = json.Marshal(details)
	})
	before := string(r.Tests)
	beforeHash := hash(r)
	if err := validateResult(r); err != nil {
		t.Fatal("actual historical Calculate detail format was rejected", err)
	}
	if string(r.Tests) != before || hash(r) != beforeHash || strings.Contains(string(r.Tests), "calculation_status") {
		t.Fatal("validation rewrote cached output or synthesized an unknown historical status")
	}
	// Shape validation must not reimplement grading, weights or source selection.
	unrelatedFinal := 123.25
	r.Final = &unrelatedFinal
	if err := validateResult(r); err != nil {
		t.Fatal("wire validation tried to recompute the rating", err)
	}
}

func TestResultFiniteLegacyScoresAndNullableMetadataRemainValid(t *testing.T) {
	for _, kind := range []string{"homework", "tests", "exams"} {
		for _, score := range []string{"-12.25", "123.25"} {
			t.Run(kind+"/"+score, func(t *testing.T) {
				r := detailedReadyResult()
				changeResultDetail(t, &r, kind, func(d map[string]json.RawMessage) { d["score"] = json.RawMessage(score) })
				changeResultComponent(t, &r, kind, func(c map[string]json.RawMessage) {
					c["rating"] = json.RawMessage(score)
					if kind == "tests" {
						c["directions"] = json.RawMessage(`{"Math":` + score + `}`)
					}
				})
				if err := validateResult(r); err != nil {
					t.Fatal("wire validation imposed a new score range or recalculated the aggregate", err)
				}
			})
		}
	}
	r := detailedReadyResult()
	changeResultDetail(t, &r, "homework", func(d map[string]json.RawMessage) { d["date_pass"] = json.RawMessage(`"2026-10-01 12:34:56"`) })
	changeResultDetail(t, &r, "exams", func(d map[string]json.RawMessage) {
		d["exam_type"], d["direction_id"], d["attempt_no"], d["has_appeal"] = json.RawMessage(`"classic"`), json.RawMessage("7"), json.RawMessage("2"), json.RawMessage("true")
	})
	if err := validateResult(r); err != nil {
		t.Fatal("completed zero, nullable metadata, legacy date_pass string or appeal were rejected", err)
	}
	changeResultDetail(t, &r, "homework", func(d map[string]json.RawMessage) { d["status"] = json.RawMessage(`"Не сдано"`) })
	changeResultDetail(t, &r, "exams", func(d map[string]json.RawMessage) {
		d["status"], d["missing_result"], d["attempt_no"], d["has_appeal"] = json.RawMessage(`"Не сдавал"`), json.RawMessage("true"), json.RawMessage("null"), json.RawMessage("false")
	})
	if err := validateResult(r); err != nil {
		t.Fatal("explicit missing zero or absent selected exam attempt was rejected", err)
	}
}

func TestMalformedNestedCallbackRejectedBeforeItemAndCoreWrites(t *testing.T) {
	for _, kind := range []string{"homework", "tests", "exams"} {
		t.Run(kind, func(t *testing.T) {
			store, mock := mockStore(t)
			j := fixtureJob()
			j.Status = "running"
			mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\?").WithArgs(j.ID).WillReturnRows(jobRows(j))
			r := detailedReadyResult()
			changeResultDetail(t, &r, kind, func(d map[string]json.RawMessage) { d["score"] = json.RawMessage("null") })
			raw, _ := json.Marshal(r)
			s := &Server{Store: store, Config: Config{ServiceKey: strings.Repeat("k", 32)}}
			request := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
			request.SetPathValue("job", j.ID)
			request.SetPathValue("account", "71")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Service-Key", s.Config.ServiceKey)
			response := httptest.NewRecorder()
			s.result(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "incomplete_rating_result") {
				t.Fatal("malformed callback reached item hashing/core lock/result writes", response.Code, response.Body.String())
			}
		})
	}
}

func TestPublishedResultRejectsCorruptDetailsAndReadsHistoricalStatus(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		store, mock := mockStore(t)
		j := fixtureJob()
		j.Status = "completed"
		r := detailedReadyResult()
		changeResultDetail(t, &r, "tests", func(d map[string]json.RawMessage) {
			delete(d, "calculation_status")
			if corrupt {
				d["score"] = json.RawMessage("2e309")
			}
		})
		profile, _ := json.Marshal(Account{AccountID: 71})
		output, _ := json.Marshal(r)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\? AND organization_id=\\? AND division_id=\\? AND status='completed'").WithArgs(j.ID, j.OrganizationID, j.DivisionID).WillReturnRows(jobRows(j))
		mock.ExpectQuery("SELECT profile,output FROM ratings_items_v1 WHERE job_id=\\? AND status='ready' ORDER BY account_id").WithArgs(j.ID).
			WillReturnRows(sqlmock.NewRows([]string{"profile", "output"}).AddRow(profile, output))
		if corrupt {
			mock.ExpectRollback()
		} else {
			mock.ExpectCommit()
		}
		s := &Server{Store: store}
		_, rows, err := s.readPublished(httptest.NewRequest("GET", "/?version="+j.ID, nil), Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: j.OrganizationID}, j.DivisionID)
		if corrupt {
			assertAPIError(t, err, 503, "dependency_unavailable")
			response := httptest.NewRecorder()
			fail(response, err)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatal("corrupt saved detail was exposed as a valid publication", response.Code)
			}
		} else if err != nil || len(rows) != 1 || strings.Contains(string(rows[0].Result.Tests), `"calculation_status":"ready"`) || string(rows[0].Result.Tests) != string(r.Tests) {
			t.Fatal("historical status omission was rejected or changed during read", err, rows)
		}
	}
}
