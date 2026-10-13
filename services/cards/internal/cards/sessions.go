package cards

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/xuri/excelize/v2"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type ImportRow struct {
	Row      int      `json:"row"`
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Action   string   `json:"action"`
	Warnings []string `json:"warnings"`
	Errors   []string `json:"errors"`
}
type PreviewSummary struct {
	Total    int `json:"total_rows"`
	Create   int `json:"cards_create"`
	Warning  int `json:"cards_warning"`
	Skip     int `json:"cards_skip"`
	Errors   int `json:"row_errors"`
	ToImport int `json:"cards_to_import"`
}
type Preview struct {
	Summary        PreviewSummary `json:"summary"`
	SourceSHA256   string         `json:"source_sha256,omitempty"`
	SourceFilename string         `json:"source_filename,omitempty"`
	SourceSheet    string         `json:"source_sheet,omitempty"`

	DirectionID  int64       `json:"direction_id"`
	ThemeID      int64       `json:"theme_id"`
	NewThemeName string      `json:"new_theme_name"`
	CreateNew    bool        `json:"create_new_theme"`
	Cards        []ImportRow `json:"cards"`
	Selected     []Card      `json:"selected_cards,omitempty"`
	Title        string      `json:"draft_title,omitempty"`
	Collision    bool        `json:"theme_name_collision"`
}
type Session struct {
	AuthorizationProof *ImportProof    `json:"authorization_proof,omitempty"`
	OrganizationID     int64           `json:"organization_id"`
	ID                 string          `json:"session_id"`
	Kind               string          `json:"kind"`
	DivisionID         *int64          `json:"division_id"`
	DirectionID        int64           `json:"direction_id"`
	Revision           int64           `json:"revision"`
	State              string          `json:"state"`
	Preview            Preview         `json:"preview"`
	Result             json.RawMessage `json:"result"`
	Expires            time.Time       `json:"expires_at"`
}
type RowEdit struct {
	Row      int    `json:"row"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Skip     bool   `json:"skip"`
}
type SessionEdit struct {
	ExpectedRevision int64     `json:"expected_revision"`
	Cards            []RowEdit `json:"cards"`
}
type TransformDTO struct {
	ThemeID int64   `json:"theme_id"`
	CardIDs []int64 `json:"card_ids"`
}

func normalize(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}
func rowKey(q, a string) string {
	raw, _ := json.Marshal([2]string{strings.Join(strings.Fields(q), " "), strings.Join(strings.Fields(a), " ")})
	return string(raw)
}
func rebuildRows(rows []ImportRow, existing []Card) []ImportRow {
	keys := map[string]bool{}
	seen := map[string]bool{}
	for _, c := range existing {
		keys[rowKey(c.Question, c.Answer)] = true
	}
	out := make([]ImportRow, 0, len(rows))
	for _, r := range rows {
		r.Question = normalize(r.Question)
		r.Answer = normalize(r.Answer)
		r.Errors = []string{}
		r.Warnings = []string{}
		key := rowKey(r.Question, r.Answer)
		if seen[key] {
			r.Warnings = append(r.Warnings, "duplicate_in_file")
		}
		if keys[key] {
			r.Warnings = append(r.Warnings, "duplicate_in_section")
		}
		seen[key] = true
		if r.Action != "skip" {
			r.Action = "create"
			if len(r.Warnings) > 0 {
				r.Action = "warning"
			}
			if !validText(r.Question, 4000) {
				r.Errors = append(r.Errors, "invalid_question")
			}
			if !validText(r.Answer, 16000) {
				r.Errors = append(r.Errors, "invalid_answer")
			}
			if len(r.Errors) > 0 {
				r.Action = "error"
			}
		}
		out = append(out, r)
	}
	return out
}
func (s *Server) prepare(r *http.Request, q querier, a Actor, scope Scope, p *Preview, lock bool) error {
	if !scope.Allows(p.DirectionID) {
		return missing
	}
	if p.CreateNew {
		if !validText(p.NewThemeName, 200) {
			return bad("invalid_theme_name")
		}
		p.ThemeID = 0
		query := "SELECT id FROM cards_themes_v1 WHERE organization_id=? AND direction_id=? AND name=?"
		if lock {
			query += " FOR UPDATE"
		}
		e := q.QueryRowContext(r.Context(), query, a.OrganizationID, p.DirectionID, strings.TrimSpace(p.NewThemeName)).Scan(&p.ThemeID)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		p.Collision = p.ThemeID > 0
	} else if p.ThemeID < 1 {
		return bad("theme_required")
	}
	existing := []Card{}
	if p.ThemeID > 0 {
		t, e := theme(r.Context(), q, a.OrganizationID, p.ThemeID, scope, lock)
		if e != nil {
			return e
		}
		if t.DirectionID != p.DirectionID {
			return bad("theme_direction_mismatch")
		}
		existing, e = manual(r.Context(), q, a.OrganizationID, p.ThemeID)
		if e != nil {
			return e
		}
	}
	p.Cards = rebuildRows(p.Cards, existing)
	p.Summary = PreviewSummary{Total: len(p.Cards)}
	for _, row := range p.Cards {
		switch row.Action {
		case "create":
			p.Summary.Create++
		case "warning":
			p.Summary.Warning++
		case "error":
			p.Summary.Errors++
		case "skip":
			p.Summary.Skip++
		}
	}
	p.Summary.ToImport = p.Summary.Create + p.Summary.Warning
	return nil
}
func (s *Server) saveSession(r *http.Request, a Actor, kind string, p Preview) (Session, error) {
	return saveSession(r, s.DB, a, kind, p)
}
func saveSession(r *http.Request, q querier, a Actor, kind string, p Preview) (Session, error) {
	raw := make([]byte, 16)
	if _, e := rand.Read(raw); e != nil {
		return Session{}, e
	}
	ss := Session{OrganizationID: a.OrganizationID, ID: hex.EncodeToString(raw), Kind: kind, DirectionID: p.DirectionID, Revision: 1, State: "preview", Preview: p, Expires: time.Now().UTC().Add(72 * time.Hour)}
	if kind == "transform" {
		ss.DivisionID = a.DivisionID
	}
	ss.AuthorizationProof = importProof(a, ss)
	payload, _ := json.Marshal(p)
	_, e := q.ExecContext(r.Context(), "INSERT INTO cards_sessions_v1(id,organization_id,account_id,kind,division_id,direction_id,preview,expires_at) VALUES(?,?,?,?,?,?,?,?)", ss.ID, a.OrganizationID, a.AccountID, kind, ss.DivisionID, p.DirectionID, payload, ss.Expires)
	return ss, e
}
func (s *Server) parseImport(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	if e := r.ParseMultipartForm(10 << 20); e != nil {
		return nil, bad("invalid_upload")
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	f, fileHeader, e := r.FormFile("file")
	if e != nil {
		return nil, bad("file_required")
	}
	defer f.Close()
	did, e := number(r.FormValue("direction_id"))
	if e != nil {
		return nil, e
	}
	p := Preview{DirectionID: did, NewThemeName: r.FormValue("new_theme_name"), CreateNew: r.FormValue("create_new_theme") == "true" || r.FormValue("create_new_theme") == "1", Cards: []ImportRow{}}
	if !p.CreateNew {
		p.ThemeID, e = number(r.FormValue("theme_id"))
		if e != nil {
			return nil, e
		}
	}
	if !scope.Allows(did) {
		return nil, missing
	}
	fileBytes, e := io.ReadAll(io.LimitReader(f, (10<<20)+1))
	if e != nil || len(fileBytes) > 10<<20 {
		return nil, bad("invalid_upload")
	}
	hash := sha256.Sum256(fileBytes)
	p.SourceSHA256 = hex.EncodeToString(hash[:])
	p.SourceFilename = filepath.Base(strings.ReplaceAll(fileHeader.Filename, "\\", "/"))
	if len(p.SourceFilename) > 255 {
		p.SourceFilename = "source.xlsx"
	}
	book, e := excelize.OpenReader(bytes.NewReader(fileBytes), excelize.Options{UnzipSizeLimit: 32 << 20, UnzipXMLSizeLimit: 8 << 20})
	if e != nil {
		return nil, bad("invalid_excel")
	}
	defer book.Close()
	sheet := book.GetSheetName(book.GetActiveSheetIndex())
	p.SourceSheet = sheet
	rows, e := book.Rows(sheet)
	if e != nil {
		return nil, bad("invalid_excel")
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, bad("empty_excel")
	}
	header, e := rows.Columns()
	if e != nil {
		return nil, bad("invalid_excel")
	}
	qi, ai := -1, -1
	for i, h := range header {
		h = strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(h), "ё", "е")), " ")
		if h == "вопрос" && qi < 0 {
			qi = i
		}
		if h == "ответ" && ai < 0 {
			ai = i
		}
	}
	if qi < 0 || ai < 0 {
		return nil, bad("required_headers_question_answer")
	}
	line := 1
	for rows.Next() {
		line++
		if line > 1001 {
			return nil, bad("row_limit_exceeded")
		}
		cells, e := rows.Columns()
		if e != nil {
			return nil, bad("invalid_excel")
		}
		nonempty := false
		for _, c := range cells {
			if strings.TrimSpace(c) != "" {
				nonempty = true
			}
		}
		if !nonempty {
			return nil, bad(fmt.Sprintf("empty_row_%d", line))
		}
		get := func(i int) string {
			if i >= len(cells) {
				return ""
			}
			return strings.TrimSpace(normalize(cells[i]))
		}
		p.Cards = append(p.Cards, ImportRow{Row: line, Question: get(qi), Answer: get(ai)})
	}
	if rows.Error() != nil {
		return nil, bad("invalid_excel")
	}
	if len(p.Cards) == 0 {
		return nil, bad("empty_excel")
	}
	input := struct {
		DirectionID  int64
		ThemeID      int64
		CreateNew    bool
		NewThemeName string
		SourceSHA256 string
	}{p.DirectionID, p.ThemeID, p.CreateNew, p.NewThemeName, p.SourceSHA256}
	return s.command(r, a, scope, "import:parse", input, func(tx *sql.Tx) (any, int64, error) {
		if e := s.prepare(r, tx, a, scope, &p, false); e != nil {
			return nil, 0, e
		}
		ss, e := saveSession(r, tx, a, "import", p)
		return ss, p.DirectionID, e
	})
}
func (s *Server) loadSession(r *http.Request, q querier, a Actor, scope Scope, lock bool) (Session, error) {
	var ss Session
	var raw, result []byte
	id := r.PathValue("session")
	if len(id) != 32 {
		return ss, missing
	}
	query := "SELECT id,kind,division_id,direction_id,revision,state,preview,result,expires_at FROM cards_sessions_v1 WHERE id=? AND organization_id=? AND account_id=?"
	if lock {
		query += " FOR UPDATE"
	}
	e := q.QueryRowContext(r.Context(), query, id, a.OrganizationID, a.AccountID).Scan(&ss.ID, &ss.Kind, &ss.DivisionID, &ss.DirectionID, &ss.Revision, &ss.State, &raw, &result, &ss.Expires)
	if e != nil {
		return ss, e
	}
	if ss.Kind != "import" && ss.Kind != "transform" || !scope.Allows(ss.DirectionID) || ss.State == "preview" && time.Now().After(ss.Expires) {
		return ss, missing
	}
	if ss.Kind == "transform" && (!a.CanTestsEdit() || a.DivisionID == nil || ss.DivisionID == nil || *a.DivisionID != *ss.DivisionID) {
		return ss, denied
	}
	if json.Unmarshal(raw, &ss.Preview) != nil {
		return ss, unavailable
	}
	ss.OrganizationID = a.OrganizationID
	ss.Result = result
	ss.AuthorizationProof = importProof(a, ss)
	if e := requireOperationSession(r, a, ss); e != nil {
		return ss, e
	}
	return ss, nil
}
func (s *Server) sessionGet(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	return s.loadSession(r, s.DB, a, scope, false)
}
func (s *Server) sessionUpdate(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	var in SessionEdit
	if e := decode(r, &in); e != nil {
		return nil, e
	}
	return s.command(r, a, scope, "session:update:"+r.PathValue("session"), in, func(tx *sql.Tx) (any, int64, error) {
		ss, e := s.loadSession(r, tx, a, scope, true)
		if e != nil {
			return nil, 0, e
		}
		if ss.Kind != "import" || ss.State != "preview" || ss.Revision != in.ExpectedRevision {
			return nil, 0, conflict
		}
		if len(in.Cards) != len(ss.Preview.Cards) {
			return nil, 0, bad("row_count_mismatch")
		}
		rows := map[int]ImportRow{}
		for _, row := range ss.Preview.Cards {
			rows[row.Row] = row
		}
		seen := map[int]bool{}
		for i, row := range in.Cards {
			if _, ok := rows[row.Row]; !ok || seen[row.Row] || utf8.RuneCountInString(row.Question) > 4000 || utf8.RuneCountInString(row.Answer) > 16000 {
				return nil, 0, bad("invalid_row")
			}
			seen[row.Row] = true
			action := "create"
			if row.Skip {
				action = "skip"
			}
			ss.Preview.Cards[i] = ImportRow{Row: row.Row, Question: row.Question, Answer: row.Answer, Action: action}
		}
		if e = s.prepare(r, tx, a, scope, &ss.Preview, false); e != nil {
			return nil, 0, e
		}
		raw, _ := json.Marshal(ss.Preview)
		_, e = tx.ExecContext(r.Context(), "UPDATE cards_sessions_v1 SET preview=?,revision=revision+1 WHERE id=?", raw, ss.ID)
		if e != nil {
			return nil, 0, e
		}
		ss.Revision++
		ss.AuthorizationProof = importProof(a, ss)
		return ss, ss.DirectionID, nil
	})
}
func (s *Server) transform(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) || !a.CanTestsEdit() || a.DivisionID == nil {
		return nil, denied
	}
	var in TransformDTO
	if e := decode(r, &in); e != nil {
		return nil, e
	}
	if len(in.CardIDs) == 0 || len(in.CardIDs) > 200 {
		return nil, bad("invalid_card_selection")
	}
	return s.command(r, a, scope, fmt.Sprintf("transform:division:%d", *a.DivisionID), in, func(tx *sql.Tx) (any, int64, error) {
		t, e := theme(r.Context(), tx, a.OrganizationID, in.ThemeID, scope, true)
		if e != nil {
			return nil, 0, e
		}
		cards, e := manual(r.Context(), tx, a.OrganizationID, in.ThemeID)
		if e != nil {
			return nil, 0, e
		}
		byID := map[int64]Card{}
		for _, c := range cards {
			byID[c.ID] = c
		}
		p := Preview{ThemeID: t.ID, DirectionID: t.DirectionID, Title: t.Name + " — тест", Selected: []Card{}, Cards: []ImportRow{}}
		seen := map[int64]bool{}
		for _, id := range in.CardIDs {
			c, ok := byID[id]
			if !ok || seen[id] {
				return nil, 0, bad("invalid_card_selection")
			}
			if utf8.RuneCountInString(c.Answer) > 1000 {
				return nil, 0, bad("draft_answer_limit_1000")
			}
			seen[id] = true
			p.Selected = append(p.Selected, c)
		}
		if !validText(p.Title, 200) {
			p.Title = string([]rune(p.Title)[:200])
		}
		ss, e := saveSession(r, tx, a, "transform", p)
		return ss, t.DirectionID, e
	})
}
func (s *Server) sessionCommit(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	var in RevisionDTO
	if e := decode(r, &in); e != nil {
		return nil, e
	}
	return s.command(r, a, scope, "session:commit:"+r.PathValue("session"), in, func(tx *sql.Tx) (any, int64, error) {
		var result json.RawMessage
		ss, e := s.loadSession(r, tx, a, scope, true)
		if e != nil {
			return nil, 0, e
		}
		if ss.Revision != in.ExpectedRevision {
			return nil, 0, conflict
		}
		if ss.State == "completed" {
			result = ss.Result
			return result, ss.DirectionID, nil
		}
		if ss.State != "preview" {
			return nil, 0, conflict
		}
		if ss.Kind == "import" {
			p := ss.Preview
			if e = s.prepare(r, tx, a, scope, &p, true); e != nil {
				return nil, 0, e
			}
			count := 0
			for _, row := range p.Cards {
				if row.Action == "error" {
					return nil, 0, bad("preview_has_errors")
				}
				if row.Action != "skip" {
					count++
				}
			}
			if count == 0 {
				return nil, 0, bad("no_cards_to_import")
			}
			if p.ThemeID == 0 {
				res, e := tx.ExecContext(r.Context(), "INSERT INTO cards_themes_v1(organization_id,direction_id,name) VALUES(?,?,?)", a.OrganizationID, p.DirectionID, strings.TrimSpace(p.NewThemeName))
				if e != nil {
					return nil, 0, e
				}
				p.ThemeID, e = res.LastInsertId()
				if e != nil {
					return nil, 0, e
				}
			}
			var order int64
			if e = tx.QueryRowContext(r.Context(), "SELECT COALESCE(MAX(sort_order),0) FROM cards_items_v1 WHERE organization_id=? AND theme_id=?", a.OrganizationID, p.ThemeID).Scan(&order); e != nil {
				return nil, 0, e
			}
			ids := []int64{}
			for _, row := range p.Cards {
				if row.Action == "skip" {
					continue
				}
				order++
				res, e := tx.ExecContext(r.Context(), "INSERT INTO cards_items_v1(organization_id,theme_id,question,answer,sort_order) VALUES(?,?,?,?,?)", a.OrganizationID, p.ThemeID, strings.TrimSpace(row.Question), strings.TrimSpace(row.Answer), order)
				if e != nil {
					return nil, 0, e
				}
				id, e := res.LastInsertId()
				if e != nil {
					return nil, 0, e
				}
				ids = append(ids, id)
			}
			result, _ = json.Marshal(map[string]any{"theme_id": p.ThemeID, "card_ids": ids, "created": len(ids), "skipped": len(p.Cards) - len(ids)})
		} else {
			p := ss.Preview
			qs := []Question{}
			layout := []map[string]any{}
			for i, c := range p.Selected {
				id := int64(i + 1)
				qs = append(qs, Question{ID: id, Type: "text", Text: c.Question, Points: 1, Answers: []Option{}, Correct: []string{c.Answer}})
				layout = append(layout, map[string]any{"question_id": id, "x": 120 + (i%3)*360, "y": 120 + (i/3)*260})
			}
			body := map[string]any{"title": p.Title, "description": "", "direction_id": p.DirectionID, "questions": qs, "layout": layout}
			var out struct {
				Status bool `json:"status"`
				Data   struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if e = s.Deps.call(r, "POST", s.Deps.Config.TestsURL+"/internal/tests/cards/drafts", body, &out, "cards_"+ss.ID); e != nil {
				return nil, 0, e
			}
			if !out.Status || !testIDPattern.MatchString(out.Data.ID) {
				return nil, 0, unavailable
			}
			result, _ = json.Marshal(map[string]any{"draft_id": out.Data.ID, "cards_selected": len(qs)})
		}
		_, e = tx.ExecContext(r.Context(), "UPDATE cards_sessions_v1 SET state='completed',result=? WHERE id=?", []byte(result), ss.ID)
		return result, ss.DirectionID, e
	})
}
