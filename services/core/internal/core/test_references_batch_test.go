package core

import (
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"
)

func TestReferencesBatchUsesScopedSetQueriesAndRestoresInputOrder(t *testing.T) {
	steps := []examReferenceStep{
		{"organizations WHERE id=? AND is_active=1", []string{"id", "name"}, [][]driver.Value{{int64(7), "Org"}}},
		{"organization_id=? AND is_active=1 AND id IN (?,?)", []string{"id", "name", "is_active"}, [][]driver.Value{{int64(10), "D10", int64(1)}, {int64(11), "D11", int64(1)}}},
		{"x.organization_id=? AND p.id IN (?,?)", []string{"id", "name", "is_active"}, [][]driver.Value{{int64(20), "Direction20", int64(1)}, {int64(21), "Direction21", int64(0)}}},
	}
	db, conn := testReferencesDB(t, steps, [][]driver.Value{{int64(7)}, {int64(7), int64(11), int64(10)}, {int64(7), int64(21), int64(20)}})
	key := strings.Repeat("k", 32)
	w := testReferencesRequest(&Server{DB: db, InternalKey: key}, `{"organization_id":7,"division_ids":[11,10],"direction_ids":[21,20]}`, key, "application/json")
	if w.Code != 200 || !conn.committed || conn.next != 3 {
		t.Fatalf("batch did not use one scoped transaction: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data struct {
			Divisions []struct {
				ID int64 `json:"id"`
			} `json:"divisions"`
			Directions []struct {
				ID     int64 `json:"id"`
				Active bool  `json:"is_active"`
			} `json:"directions"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Data.Divisions[0].ID != 11 || out.Data.Directions[0].ID != 21 || out.Data.Directions[0].Active {
		t.Fatal("order or inactive direction lost", w.Body.String())
	}
}

func TestReferencesBatchRejectsMissingForeignDirection(t *testing.T) {
	steps := []examReferenceStep{{"organizations WHERE id=?", []string{"id", "name"}, [][]driver.Value{{int64(7), "Org"}}}, {"x.organization_id=? AND p.id IN (?,?)", []string{"id", "name", "is_active"}, [][]driver.Value{{int64(20), "D20", int64(1)}}}}
	db, conn := testReferencesDB(t, steps, [][]driver.Value{{int64(7)}, {int64(7), int64(20), int64(21)}})
	key := strings.Repeat("k", 32)
	w := testReferencesRequest(&Server{DB: db, InternalKey: key}, `{"organization_id":7,"direction_ids":[20,21]}`, key, "application/json")
	if w.Code != 404 || conn.committed {
		t.Fatal("foreign/missing reference accepted", w.Code, w.Body.String())
	}
}
