package core

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttendanceHistoryServiceBoundary(t *testing.T) {
	s := &Server{InternalKey: strings.Repeat("k", 32)}
	for _, key := range []string{"", "wrong", strings.Repeat("x", 32)} {
		r := httptest.NewRequest("POST", "/internal/core/attendance/history-reservations", strings.NewReader(`{"organization_id":1,"division_id":2,"account_id":3,"operation_key":"fixed-key"}`))
		r.Header.Set("Authorization", "Bearer ordinary-user-token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", key)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("public credentials accepted: %d", w.Code)
		}
	}
}
func TestAttendanceHistoryRejectsAmbiguousScope(t *testing.T) {
	s := &Server{InternalKey: strings.Repeat("k", 32)}
	for _, body := range []string{
		`{}`, `null`, `[]`,
		`{"organization_id":1,"division_id":2,"account_id":3,"operation_key":"fixed-key","role":"admin"}`,
		`{"organization_id":1,"organization_id":2,"division_id":2,"account_id":3,"operation_key":"fixed-key"}`,
		`{"organization_id":0,"division_id":2,"account_id":3,"operation_key":"fixed-key"}`,
		`{"organization_id":1,"division_id":-2,"account_id":3,"operation_key":"fixed-key"}`,
		`{"organization_id":1,"division_id":2,"account_id":null,"operation_key":"fixed-key"}`,
		`{"organization_id":1,"division_id":2,"account_id":3,"operation_key":"short"}`,
		`{"organization_id":1,"division_id":2,"account_id":3,"operation_key":" padded-key"}`,
		`{"organization_id":1,"division_id":2,"account_id":3,"operation_key":"fixed-key"} {}`,
		strings.Repeat(" ", 4097),
	} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("X-Service-Key", s.InternalKey)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.attendanceHistory(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid scope accepted: %d", w.Code)
		}
	}
}
func TestAttendanceHistoryUnavailableNeverConfirmsReservation(t *testing.T) {
	db := sql.OpenDB(&ratingSnapshotConnector{})
	db.Close()
	s := &Server{DB: db, InternalKey: strings.Repeat("k", 32)}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"organization_id":1,"division_id":2,"account_id":3,"operation_key":"fixed-key"}`))
	r.Header.Set("X-Service-Key", s.InternalKey)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.attendanceHistory(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 503 || body["status"] != false || body["data"] != nil {
		t.Fatalf("unconfirmed reservation escaped: %d %s", w.Code, w.Body.String())
	}
}
