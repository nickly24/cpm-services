package core

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHomeworkReferencesReturnsClassWithoutLosingScopeOrIdentities(t *testing.T) {
	c := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{
		{contains: []string{"d.organization_id=? AND d.id=? AND d.is_active=1 AND o.is_active=1"}, args: []int64{1, 2}, columns: []string{"id"}, rows: [][]driver.Value{{int64(2)}}},
		{contains: []string{"a.id account_id,a.ref_id profile_id,a.role,p.full_name,p.`class`", "school_scope.school_id=p.school_id AND school_scope.organization_id=oa.organization_id", "sc.id school_id,sc.name school_name,sc.short_name school_short_name", "oa.organization_id=?", "m.organization_id=oa.organization_id AND m.division_id=?", "g.organization_id=m.organization_id", "g.division_id=m.division_id", "ORDER BY a.id LIMIT 1001"}, args: []int64{1, 2}, columns: []string{"account_id", "profile_id", "role", "full_name", "class", "school_id", "school_name", "school_short_name", "group_id", "group_name"}, rows: [][]driver.Value{{int64(10), int64(21), "student", "DEV Pupil", int64(7), int64(5), "DEV School", "DS", int64(4), "DEV Group"}, {int64(11), int64(22), "proctor", "DEV Proctor", nil, nil, nil, nil, int64(4), "DEV Group"}}},
		{contains: []string{"m.organization_id=g.organization_id", "m.division_id=g.division_id AND m.group_id=g.group_id", "g.organization_id=? AND g.division_id=? AND g.is_active=1"}, args: []int64{1, 2}, columns: []string{"id", "name", "proctor_account_id"}, rows: [][]driver.Value{{int64(4), "DEV Group", int64(11)}}},
	}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db, InternalKey: strings.Repeat("k", 32)}
	r := httptest.NewRequest("POST", "/internal/core/homework-references", strings.NewReader(`{"organization_id":1,"division_id":2}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Service-Key", s.InternalKey)
	w := httptest.NewRecorder()
	s.homeworkReferences(w, r)
	if w.Code != 200 || !c.readOnly || !c.committed || len(c.queries) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Data struct {
			OrganizationID int64 `json:"organization_id"`
			DivisionID     int64 `json:"division_id"`
			Members        []struct {
				AccountID       int64   `json:"account_id"`
				ProfileID       int64   `json:"profile_id"`
				Class           *int    `json:"class"`
				SchoolID        *int64  `json:"school_id"`
				SchoolName      *string `json:"school_name"`
				SchoolShortName *string `json:"school_short_name"`
			} `json:"members"`
		} `json:"data"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if result.Data.OrganizationID != 1 || result.Data.DivisionID != 2 || len(result.Data.Members) != 2 || result.Data.Members[0].AccountID == result.Data.Members[0].ProfileID || result.Data.Members[0].Class == nil || *result.Data.Members[0].Class != 7 || result.Data.Members[1].Class != nil {
		t.Fatal("class, nullable staff or scope lost", w.Body.String())
	}
	student, staff := result.Data.Members[0], result.Data.Members[1]
	if student.SchoolID == nil || *student.SchoolID != 5 || student.SchoolName == nil || *student.SchoolName != "DEV School" || student.SchoolShortName == nil || *student.SchoolShortName != "DS" || staff.SchoolID != nil || staff.SchoolName != nil || staff.SchoolShortName != nil {
		t.Fatal("scoped school or nullable staff school lost", w.Body.String())
	}
}
func TestHomeworkReferencesForeignAccountCannotReceiveClass(t *testing.T) {
	c := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{
		{contains: []string{"d.organization_id=? AND d.id=?"}, args: []int64{1, 2}, columns: []string{"id"}, rows: [][]driver.Value{{int64(2)}}},
		{contains: []string{"m.organization_id=oa.organization_id AND m.division_id=?", "AND a.id IN (?)"}, args: []int64{1, 2, 99}, columns: []string{"account_id"}},
	}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db, InternalKey: strings.Repeat("k", 32)}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"organization_id":1,"division_id":2,"account_ids":[99]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Service-Key", s.InternalKey)
	w := httptest.NewRecorder()
	s.homeworkReferences(w, r)
	if w.Code != 404 || c.committed || len(c.queries) != 0 {
		t.Fatal("foreign profile disclosed", w.Code, w.Body.String())
	}
}

func TestHomeworkReferencesProfileIDLookupIsScopedAndStudentOnly(t *testing.T) {
	for _, found := range []bool{true, false} {
		rows := [][]driver.Value{}
		if found {
			rows = [][]driver.Value{{int64(10), int64(21), "student"}}
		}
		c := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{
			{contains: []string{"d.organization_id=? AND d.id=?"}, args: []int64{1, 2}, columns: []string{"id"}, rows: [][]driver.Value{{int64(2)}}},
			{contains: []string{"m.organization_id=oa.organization_id AND m.division_id=?", "a.role='student' AND a.ref_id IN (?)", "a.id account_id,a.ref_id profile_id"}, args: []int64{1, 2, 21}, columns: []string{"account_id", "profile_id", "role"}, rows: rows},
		}}
		if found {
			c.queries = append(c.queries, ratingSnapshotQuery{contains: []string{"g.organization_id=? AND g.division_id=?"}, args: []int64{1, 2}, columns: []string{"id", "name", "proctor_account_id"}})
		}
		db := sql.OpenDB(c)
		s := &Server{DB: db, InternalKey: strings.Repeat("k", 32)}
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"organization_id":1,"division_id":2,"profile_ids":[21]}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", s.InternalKey)
		w := httptest.NewRecorder()
		s.homeworkReferences(w, r)
		db.Close()
		want := 404
		if found {
			want = 200
		}
		if w.Code != want || len(c.queries) != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestHomeworkReferencesRejectsAmbiguousProfileFilters(t *testing.T) {
	s := &Server{InternalKey: strings.Repeat("k", 32)}
	for _, body := range []string{`{"organization_id":1,"division_id":2,"profile_ids":[21],"account_ids":[10]}`, `{"organization_id":1,"division_id":2,"profile_ids":[21,21]}`, `{"organization_id":1,"division_id":2,"profile_ids":[0]}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", s.InternalKey)
		w := httptest.NewRecorder()
		s.homeworkReferences(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
