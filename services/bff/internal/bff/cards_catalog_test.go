package bff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUnifiedCardsServerPageHasStableMixedOrderingAndBoundedStats(t *testing.T) {
	all := []cardsCatalogItem{}
	for i := 0; i < 35; i++ {
		kind := "manual"
		ref := fmt.Sprint(i + 1)
		if i%2 == 1 {
			kind = "test"
			ref = fmt.Sprintf("%024x", i+1)
		}
		all = append(all, cardsCatalogItem{Kind: kind, Ref: ref, Name: fmt.Sprintf("Тема %02d", i), DirectionID: 13})
	}
	statsReads, maxBatch, metadataCalls := 0, 0, 0
	outage := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer student" {
			t.Error("lost session identity")
		}
		data := any(nil)
		switch r.URL.Path {
		case "/v1/auth/me":
			json.NewEncoder(w).Encode(map[string]any{"status": true, "user": map[string]any{"role": "student", "account_id": 23, "id": 999, "organization_id": 7, "division_id": 11}})
			return
		case "/v1/cards/directions/13/catalog":
			metadataCalls++
			data = all
		case "/v1/exams/training/catalog":
			data = []cardsCatalogItem{{Kind: "exam", Ref: "10", Name: "Тема 01", DirectionID: 13, ExamID: 10}}
		case "/v1/exams/10/training/summary":
			statsReads++
			data = cardsCatalogItem{Kind: "exam", Ref: "10", Stats: cardsCatalogStats{Total: 1, Learned: 1, Percent: 100}}
		case "/v1/cards/catalog/stats":
			if outage {
				w.WriteHeader(503)
				json.NewEncoder(w).Encode(map[string]any{"status": false, "error": "dependency_unavailable"})
				return
			}
			var in struct {
				Sections []cardsCatalogItem `json:"sections"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Fatal("bad stats body")
			}
			maxBatch = max(maxBatch, len(in.Sections))
			statsReads += len(in.Sections)
			for i := range in.Sections {
				in.Sections[i].Stats = cardsCatalogStats{Total: 1, Unlearned: 1}
				if in.Sections[i].Kind == "test" {
					in.Sections[i].Stats = cardsCatalogStats{Total: 1, Learned: 1, Percent: 100}
				}
			}
			data = in.Sections
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": true, "data": data})
	}))
	defer upstream.Close()
	s := New(Config{Auth: upstream.URL, Cards: upstream.URL, Exams: upstream.URL, Origin: "http://127.0.0.1:3001"})
	defer s.Close()
	id := strings.Repeat("a", 64)
	identity, _ := json.Marshal(map[string]any{"role": "student", "division_id": 11})
	if err := s.store.Create(context.Background(), id, &session{identity: Identity{Access: "student", Refresh: "refresh", User: identity}, revision: "revision", until: time.Now().Add(time.Hour), accessUntil: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	request := func(query string, status int, revision string) []cardsCatalogItem {
		t.Helper()
		r := httptest.NewRequest("GET", "/bff/cards/directions/13/sections"+query, nil)
		r.AddCookie(&http.Cookie{Name: "cpm_bff", Value: id})
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", revision)
		r.Header.Set("Origin", "http://127.0.0.1:3001")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("HTTP %d want %d: %s", w.Code, status, w.Body.String())
		}
		if status != 200 {
			return nil
		}
		var out struct {
			Data struct {
				Sections   []cardsCatalogItem `json:"sections"`
				Pagination struct {
					Total int `json:"total"`
				} `json:"pagination"`
			} `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("bad json")
		}
		if strings.Contains(w.Body.String(), "questions") || strings.Contains(w.Body.String(), "answer_text") {
			t.Fatal("content leaked")
		}
		return out.Data.Sections
	}
	first := request("?page=1&limit=12", 200, "revision")
	if len(first) != 12 || statsReads != 12 || maxBatch > 12 || first[1].Kind != "exam" || first[2].Kind != "test" {
		t.Fatal("mixed first page", first, statsReads, maxBatch)
	}
	before := statsReads
	second := request("?page=2&limit=12", 200, "revision")
	if len(second) != 12 || statsReads-before != 12 || second[0].Name == first[0].Name {
		t.Fatal("second page", second, statsReads)
	}
	again := request("?page=1&limit=12", 200, "revision")
	for i := range first {
		if first[i].Kind != again[i].Kind || first[i].Ref != again[i].Ref {
			t.Fatal("unstable ordering")
		}
	}
	filtered := request("?search=Тема+01&kind=test", 200, "revision")
	if len(filtered) != 1 || filtered[0].Kind != "test" {
		t.Fatal("search before pagination", filtered)
	}
	learned := request("?progress=learned&limit=50", 200, "revision")
	if len(learned) != 18 {
		t.Fatal("progress before page count", len(learned))
	}
	outage = true
	request("", 503, "revision")
	before = metadataCalls
	for _, q := range []string{"?account_id=999", "?page=-1", "?limit=51", "?kind=unknown", "?page=1&page=2", "?organization_id=8"} {
		request(q, 400, "revision")
	}
	request("", 409, "old-revision")
	if metadataCalls != before {
		t.Fatal("invalid scope reached domains")
	}
}
