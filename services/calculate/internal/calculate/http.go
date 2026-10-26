package calculate

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type Server struct {
	Store           Repository
	Consumer        *Consumer
	ServiceKey      string
	Worker          *Worker
	RatingsStore    Repository
	RatingsConsumer *Consumer
	RatingsWorker   *RatingWorker
	metricsOnce     sync.Once
	metrics         *serviceMetrics
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	metrics := s.telemetry()
	mux.HandleFunc("GET /metrics", s.metricsHTTP)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, map[string]any{"service": "calculate", "mode": "grading"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if s.Consumer == nil || !s.Consumer.Connected.Load() || s.Store.Ready(ctx) != nil || s.Worker != nil && s.Worker.Tests.Ready(ctx) != nil {
			fail(w, http.StatusServiceUnavailable, "dependencies_unavailable")
			return
		}
		if s.RatingsConsumer != nil && (!s.RatingsConsumer.Connected.Load() || s.RatingsStore == nil || s.RatingsStore.Ready(ctx) != nil || s.RatingsWorker == nil || s.RatingsWorker.Ratings.Ready(ctx) != nil) {
			fail(w, http.StatusServiceUnavailable, "rating_dependencies_unavailable")
			return
		}
		reply(w, http.StatusOK, map[string]any{"service": "calculate", "mode": "grading", "consumer_connected": true})
	})
	mux.HandleFunc("GET /internal/calculate/messages", s.protect(s.listMessages))
	mux.HandleFunc("GET /internal/calculate/messages/{message}", s.protect(s.getMessage))
	mux.HandleFunc("POST /internal/calculate/messages/{message}/retry", s.protect(s.retryMessage))
	if s.RatingsStore != nil {
		mux.HandleFunc("GET /internal/calculate/ratings/messages", s.protect(s.listRatingMessages))
		ratingsServer := &Server{Store: s.RatingsStore, ServiceKey: s.ServiceKey}
		mux.HandleFunc("GET /internal/calculate/ratings/messages/{message}", s.protect(ratingsServer.getMessage))
		mux.HandleFunc("POST /internal/calculate/ratings/messages/{message}/retry", s.protect(ratingsServer.retryMessage))
	}
	mux.HandleFunc("POST /internal/calculate/practice/check", s.protect(s.practice))
	mux.HandleFunc("POST /internal/calculate/practice/finish", s.protect(s.practiceFinish))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/metrics" {
			_, pattern := mux.Handler(r)
			started := time.Now()
			measured := &measuredResponse{ResponseWriter: w}
			w = measured
			defer func() {
				status := measured.status
				if status == 0 {
					status = http.StatusOK
				}
				metrics.observeRequest(r.Method, pattern, status, time.Since(started).Seconds())
			}()
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) retryMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("message")
	if !uuidPattern.MatchString(id) {
		fail(w, http.StatusBadRequest, "invalid_message_id")
		return
	}
	var input struct{}
	if !decodePractice(w, r, &input) {
		return
	}
	store, ok := s.Store.(interface {
		Requeue(context.Context, string) (bool, error)
	})
	if !ok {
		fail(w, http.StatusServiceUnavailable, "retry_unavailable")
		return
	}
	changed, err := store.Requeue(r.Context(), id)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if !changed {
		m, err := s.Store.Get(r.Context(), id)
		if IsNotFound(err) {
			fail(w, http.StatusNotFound, "message_not_found")
			return
		}
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		if m.Status != "received" {
			fail(w, http.StatusConflict, "message_not_retryable")
			return
		}
	}
	reply(w, http.StatusAccepted, map[string]any{"message_id": id, "status": "received"})
}

func (s *Server) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.ServiceKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.ServiceKey)) != 1 {
			fail(w, http.StatusUnauthorized, "invalid_service_key")
			return
		}
		next(w, r)
	}
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	f := Filter{SubmissionID: r.URL.Query().Get("submission_id"), TestID: r.URL.Query().Get("test_id"), Limit: 20}
	for _, id := range []string{f.SubmissionID, f.TestID} {
		if id != "" {
			if _, err := bson.ObjectIDFromHex(id); err != nil {
				fail(w, http.StatusBadRequest, "invalid_filter")
				return
			}
		}
	}
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		f.Limit, err = strconv.ParseInt(v, 10, 64)
		if err != nil || f.Limit < 1 || f.Limit > 100 {
			fail(w, http.StatusBadRequest, "invalid_pagination")
			return
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		f.Offset, err = strconv.ParseInt(v, 10, 64)
		if err != nil || f.Offset < 0 || f.Offset > 100_000 {
			fail(w, http.StatusBadRequest, "invalid_pagination")
			return
		}
	}
	items, total, err := s.Store.List(r.Context(), f)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	reply(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": f.Limit, "offset": f.Offset})
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("message")
	if !uuidPattern.MatchString(id) {
		fail(w, http.StatusBadRequest, "invalid_message_id")
		return
	}
	m, err := s.Store.Get(r.Context(), id)
	if IsNotFound(err) {
		fail(w, http.StatusNotFound, "message_not_found")
		return
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	reply(w, http.StatusOK, m.View())
}

func (s *Server) practice(w http.ResponseWriter, r *http.Request) {
	var input PracticeInput
	if !decodePractice(w, r, &input) {
		return
	}
	result, err := CheckPractice(input)
	if errors.Is(err, errPractice) {
		fail(w, http.StatusBadRequest, "invalid_practice_input")
		return
	}
	reply(w, http.StatusOK, result)
}

func (s *Server) practiceFinish(w http.ResponseWriter, r *http.Request) {
	var input PracticeFinishInput
	if !decodePractice(w, r, &input) {
		return
	}
	result, err := FinishPractice(input)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_practice_input")
		return
	}
	reply(w, http.StatusOK, result)
}

func decodePractice(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "json_required")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil || validateJSON(body) != nil {
		fail(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		fail(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	return true
}

func reply(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": data})
}

func fail(w http.ResponseWriter, code int, name string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": name})
}
