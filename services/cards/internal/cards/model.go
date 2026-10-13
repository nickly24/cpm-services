package cards

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

type Permission struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
}
type Actor struct {
	AccountID         int64                 `json:"account_id"`
	OrganizationID    int64                 `json:"organization_id"`
	DivisionID        *int64                `json:"division_id"`
	Role              string                `json:"role"`
	OrganizationAdmin bool                  `json:"organization_admin"`
	Permissions       map[string]Permission `json:"permissions"`
}

func (a Actor) Can(edit bool) bool {
	if a.Role == "admin" && a.OrganizationAdmin {
		return true
	}
	if a.Role != "staff_admin" {
		return false
	}
	for _, key := range []string{"cards", "training", "train"} {
		p := a.Permissions[key]
		if (p.View || p.Edit) && (!edit || p.Edit) {
			return true
		}
	}
	return false
}

func (a Actor) CanTestsEdit() bool {
	return a.Role == "admin" && a.OrganizationAdmin || a.Role == "staff_admin" && a.Permissions["tests"].Edit
}

type Direction struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type Scope struct {
	OrganizationID int64       `json:"organization_id"`
	Directions     []Direction `json:"directions"`
}

func (s Scope) Allows(id int64) bool {
	for _, d := range s.Directions {
		if d.ID == id {
			return true
		}
	}
	return false
}

type Theme struct {
	ID          int64  `json:"id"`
	DirectionID int64  `json:"direction_id"`
	Name        string `json:"name"`
	Revision    int64  `json:"revision"`
}
type Card struct {
	ID          int64  `json:"card_id,omitempty"`
	QuestionID  int64  `json:"question_id,omitempty"`
	Ref         string `json:"card_ref"`
	Question    string `json:"question"`
	Answer      string `json:"answer"`
	Order       int64  `json:"sort_order"`
	Revision    int64  `json:"revision,omitempty"`
	Fingerprint string `json:"content_fingerprint"`
	Status      string `json:"status"`
}
type Stats struct {
	Total     int `json:"total"`
	Learned   int `json:"learned"`
	Changed   int `json:"answer_changed"`
	Unlearned int `json:"unlearned"`
	Percent   int `json:"progress_percent"`
}
type Settings struct {
	BatchSize int    `json:"batch_size"`
	Mode      string `json:"study_mode"`
	LastBatch *int   `json:"last_batch_index"`
}
type Batch struct {
	Index int   `json:"index"`
	From  int   `json:"from"`
	To    int   `json:"to"`
	Size  int   `json:"size"`
	Stats Stats `json:"stats"`
}
type View struct {
	Kind        string   `json:"section_kind"`
	Ref         string   `json:"section_ref_id"`
	Name        string   `json:"section_name"`
	DirectionID int64    `json:"direction_id"`
	Version     string   `json:"content_version_id,omitempty"`
	Cards       []Card   `json:"cards"`
	Stats       Stats    `json:"stats"`
	Settings    Settings `json:"settings"`
	Batches     []Batch  `json:"batches"`
}
type ThemeDTO struct {
	Name             string    `json:"name"`
	DirectionID      int64     `json:"direction_id"`
	ExpectedRevision int64     `json:"expected_revision,omitempty"`
	Questions        []CardDTO `json:"questions,omitempty"`
}
type CardDTO struct {
	Question         string `json:"question"`
	Answer           string `json:"answer"`
	Order            *int64 `json:"sort_order"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}
type RevisionDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type MarkDTO struct {
	CardRef     string `json:"card_ref"`
	Fingerprint string `json:"content_fingerprint"`
}
type Question struct {
	ID      int64    `json:"question_id"`
	Type    string   `json:"type"`
	Text    string   `json:"text"`
	Points  int      `json:"points"`
	Answers []Option `json:"answers"`
	Correct []string `json:"correct_answers"`
}
type Option struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Correct bool   `json:"is_correct"`
}
type Projection struct {
	CardsCount  int        `json:"cards_count"`
	Visible     bool       `json:"visible"`
	TestID      string     `json:"test_id"`
	DirectionID int64      `json:"direction_id"`
	Title       string     `json:"title"`
	Version     string     `json:"content_version_id"`
	Questions   []Question `json:"questions"`
}

// Matches Python json.dumps(sort_keys=True,ensure_ascii=False) including spaces.
func fingerprint(q, a string) string {
	enc := func(s string) string {
		var b strings.Builder
		b.WriteByte('"')
		for _, r := range strings.TrimSpace(s) {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if r < 0x20 {
					fmt.Fprintf(&b, `\u%04x`, r)
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
		return b.String()
	}

	raw := "{\"a\": " + enc(a) + ", \"q\": " + enc(q) + "}"
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
func answer(q Question) string {
	parts := []string{}
	if q.Type == "text" {
		for _, a := range q.Correct {
			if t := strings.TrimSpace(a); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, " / ")
	}
	for _, a := range q.Answers {
		if a.Correct {
			t := strings.TrimSpace(a.Text)
			if q.Type == "single" {
				return t
			}
			if t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "; ")
}
func stats(cards []Card) Stats {
	s := Stats{Total: len(cards)}
	for _, c := range cards {
		switch c.Status {
		case "learned":
			s.Learned++
		case "answer_changed":
			s.Changed++
		default:
			s.Unlearned++
		}
	}
	if s.Total > 0 {
		s.Percent = int(math.RoundToEven(float64(s.Learned) * 100 / float64(s.Total)))
	}
	return s
}
func modeValid(s string) bool {
	return s == "all" || s == "unlearned" || s == "learned" || s == "stale"
}
func filtered(cards []Card, mode string) []Card {
	out := []Card{}
	for _, c := range cards {
		if mode == "all" || mode == "learned" && c.Status == "learned" || mode == "stale" && c.Status == "answer_changed" || mode == "unlearned" && c.Status != "learned" {
			out = append(out, c)
		}
	}
	return out
}
func batches(cards []Card, size int) []Batch {
	out := []Batch{}
	for i := 0; i < len(cards); i += size {
		end := min(i+size, len(cards))
		out = append(out, Batch{len(out), i + 1, end, end - i, stats(cards[i:end])})
	}
	return out
}

func statsForCounts(n, d int) int {
	if d == 0 {
		return 0
	}
	return int(math.RoundToEven(float64(n) * 100 / float64(d)))
}
