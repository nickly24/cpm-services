package attendance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCoreReferenceEndpointMissingIsNotAnArchivedCalendar(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unimplemented", 404, "404 page not found", unavailable},
		{"foreign division", 404, `{"status":false,"error":"not_found"}`, missing},
		{"archived", 200, `{"status":true,"data":{"organization":{"id":1},"divisions":[{"id":2,"name":"Old","is_active":false}]}}`, missing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer core.Close()
			peers := NewPeers(core.URL, core.URL, strings.Repeat("k", 32))
			if _, err := peers.Division(context.Background(), 1, 2); err != tc.want {
				t.Fatalf("division boundary got %v want %v", err, tc.want)
			}
			if tc.want == unavailable {
				state := NewState()
				state.Calendars[2] = Calendar{DivisionID: 2, Visibility: "organization", Timezone: "UTC"}
				server := Server{Peers: peers}
				if _, err := server.calendarRead(context.Background(), state, fixtureIdentity(1, "student"), "", "2036-01-01", "2036-01-01", nil, true); err != unavailable {
					t.Fatalf("unimplemented Core became successful empty schedule: %v", err)
				}
			}
		})
	}
}

func TestOverlayOrdersMixedTimezonesByUTC(t *testing.T) {
	s := Server{Peers: &testPeers{Org: 1}}
	state := NewState()
	state.Calendars[1] = Calendar{1, "organization", "Europe/Moscow"}
	state.Calendars[2] = Calendar{2, "organization", "UTC"}
	first := sampleLesson(1, "2036-01-01", "09:00", "10:00")
	first.ID = state.allocate()
	state.Lessons[first.ID] = first
	second := sampleLesson(2, "2036-01-01", "06:30", "07:30")
	second.ID = state.allocate()
	state.Lessons[second.ID] = second
	v, err := s.calendarRead(context.Background(), state, fixtureIdentity(1, "student"), "", "2036-01-01", "2036-01-01", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	events := v.(map[string]any)["events"].([]Event)
	if len(events) != 2 || events[0].ID != first.ID || events[0].StartsAt != "2036-01-01T06:00:00Z" || events[1].ID != second.ID || events[1].StartsAt != "2036-01-01T06:30:00Z" {
		t.Fatalf("local times reversed actual chronological order: %+v", events)
	}
	if len(v.(map[string]any)["overlap_clusters"].([]Overlap)) != 1 {
		t.Fatal("actual UTC overlap lost")
	}
}
