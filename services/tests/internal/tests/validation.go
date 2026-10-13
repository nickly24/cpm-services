package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

func validText(s string, max int, required bool) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && (!required || strings.TrimSpace(s) != "") && !strings.ContainsRune(s, 0)
}
func validateMetadata(in CreateDTO) error {
	if !validText(in.Title, 200, true) || !validText(in.Description, 2000, false) || in.DirectionID < 1 {
		return bad("invalid_metadata")
	}
	return nil
}
func validateSettings(in Settings) error {
	if in.TimeLimitMinutes < 0 || in.TimeLimitMinutes > 1440 {
		return bad("invalid_time_limit")
	}
	if in.PassingScore != nil && (math.IsNaN(*in.PassingScore) || math.IsInf(*in.PassingScore, 0) || *in.PassingScore < 0 || *in.PassingScore > 100) {
		return bad("invalid_passing_score")
	}
	if (in.StartAt == nil) != (in.EndAt == nil) || in.StartAt != nil && !in.EndAt.After(*in.StartAt) {
		return bad("invalid_date_range")
	}
	for _, t := range []*time.Time{in.StartAt, in.EndAt} {
		if t != nil && (t.Year() < 2000 || t.Year() > 2100) {
			return bad("invalid_date_range")
		}
	}
	if in.Published && (in.StartAt == nil || in.TimeLimitMinutes < 1) {
		return bad("publication_requires_schedule")
	}
	return nil
}

var operationKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func operation(r *http.Request, a Actor, payload any) (bson.ObjectID, string, error) {
	key := r.Header.Get("Idempotency-Key")
	if !operationKey.MatchString(key) {
		return bson.NilObjectID, "", bad("idempotency_key_required")
	}
	seed, _ := json.Marshal([]any{a.OrganizationID, a.AccountID, key})
	sum := sha256.Sum256(seed)
	var id bson.ObjectID
	copy(id[:], sum[:12])
	raw, _ := json.Marshal([]any{r.Method, r.URL.Path, payload})
	hash := sha256.Sum256(raw)
	return id, hex.EncodeToString(hash[:]), nil
}
func checkRevision(got, want int64) error {
	if want < 1 {
		return bad("expected_revision_required")
	}
	if got != want {
		return conflict("stale_revision")
	}
	return nil
}
