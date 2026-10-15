package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"strings"
)

// A digest carries no question/answer text. Only page-selected, freshly
// authorized tests may be requested; Tests remains the content owner.
type CardsDigest struct {
	Ref         string `json:"card_ref"`
	Fingerprint string `json:"content_fingerprint"`
}
type CardsDigestSection struct {
	DirectionID int64         `json:"direction_id"`
	TestID      bson.ObjectID `json:"test_id"`
	Cards       []CardsDigest `json:"cards"`
}

func cardsAnswer(q Question) string {
	parts := []string{}
	if q.Type == "text" {
		for _, v := range q.CorrectAnswers {
			if v = strings.TrimSpace(v); v != "" {
				parts = append(parts, v)
			}
		}
		return strings.Join(parts, " / ")
	}
	for _, v := range q.Answers {
		if v.IsCorrect {
			text := strings.TrimSpace(v.Text)
			if q.Type == "single" {
				return text
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "; ")
}
func cardsFingerprint(q, a string) string {
	raw := "{\"a\": " + string(appendCanonical(nil, strings.TrimSpace(a))) + ", \"q\": " + string(appendCanonical(nil, strings.TrimSpace(q))) + "}"
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
func (s *Server) cardsDigests(r *http.Request, a Actor, _ int64) (any, int, error) {
	var in struct {
		IDs []bson.ObjectID `json:"test_ids"`
	}
	if err := decode(r, &in); err != nil {
		return nil, 0, err
	}
	if len(in.IDs) < 1 || len(in.IDs) > 50 {
		return nil, 0, bad("invalid_page_size")
	}
	seen := map[bson.ObjectID]bool{}
	for _, id := range in.IDs {
		if id.IsZero() || seen[id] {
			return nil, 0, bad("invalid_test_ids")
		}
		seen[id] = true
	}
	filter := bson.M{"organization_id": a.OrganizationID, "is_archived": false, "_id": bson.M{"$in": in.IDs}, "placements.division_id": bson.M{"$in": cardsDivisions(a)}}
	cur, err := s.Store.Collection.Find(r.Context(), filter, options.Find().SetLimit(51))
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(r.Context())
	docs := []Document{}
	if err = cur.All(r.Context(), &docs); err != nil {
		return nil, 0, err
	}
	for _, d := range docs {
		if !seen[d.ID] {
			return nil, 0, unavailable
		}
	}
	projections, err := s.cardsCatalog(r, a, docs, true)
	if err != nil {
		return nil, 0, err
	}
	if len(projections) != len(in.IDs) {
		return nil, 0, missing
	}
	out := []CardsDigestSection{}
	for _, p := range projections {
		item := CardsDigestSection{TestID: p.TestID, DirectionID: p.DirectionID, Cards: []CardsDigest{}}
		for _, q := range p.Questions {
			answer := cardsAnswer(q)
			if strings.TrimSpace(q.Text) != "" && answer != "" {
				item.Cards = append(item.Cards, CardsDigest{fmt.Sprintf("test:%s:%d", p.TestID.Hex(), q.QuestionID), cardsFingerprint(q.Text, answer)})
			}
		}
		out = append(out, item)
	}
	return out, 200, nil
}
