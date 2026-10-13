package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestBatchReferencesSingleScopedCallAndOwnerOrder(t *testing.T) {
	for _, wrongOrder := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "wrong order"}[wrongOrder], func(t *testing.T) {
			calls := 0
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var input struct {
					OrganizationID int64   `json:"organization_id"`
					DirectionIDs   []int64 `json:"direction_ids"`
				}
				json.NewDecoder(r.Body).Decode(&input)
				if input.OrganizationID != 7 || !reflect.DeepEqual(input.DirectionIDs, []int64{20, 21}) || r.Header.Get("X-Service-Key") != "internal-test-key" {
					t.Error("batch lost owner scope")
				}
				directions := []Reference{{ID: 20}, {ID: 21}}
				if wrongOrder {
					directions[0], directions[1] = directions[1], directions[0]
				}
				json.NewEncoder(w).Encode(map[string]any{"status": true, "data": References{Organization: Reference{ID: 7}, Divisions: []Reference{}, Directions: directions}})
			}))
			defer core.Close()
			d := &Dependencies{CoreURL: core.URL, Key: "internal-test-key", HTTP: core.Client()}
			_, err := d.ReferencesBatch(context.Background(), 7, nil, []int64{20, 21})
			if (err != nil) != wrongOrder || calls != 1 {
				t.Fatalf("batch validation err=%v calls=%d", err, calls)
			}
		})
	}
}
