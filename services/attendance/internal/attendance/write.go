package attendance

import (
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) write(r *http.Request, st *State, a Identity, d int64, ids map[string]int64, kind string, b json.RawMessage, op string) (any, error) {
	ctx := r.Context()
	switch kind {
	case "calendar":
		var in Calendar
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		if in.DivisionID != 0 && in.DivisionID != d {
			return nil, bad("scope_mismatch")
		}
		in.DivisionID = d
		if in.Visibility != "closed" && in.Visibility != "organization" {
			return nil, bad("invalid_visibility")
		}
		if _, e := time.LoadLocation(in.Timezone); e != nil || in.Timezone == "Local" || in.Timezone == "" {
			return nil, bad("invalid_timezone")
		}
		old, exists := st.Calendars[d]
		if exists && old.Timezone != in.Timezone {
			for _, l := range st.Lessons {
				if l.DivisionID == d {
					return nil, conflict("timezone_has_lessons")
				}
			}
		}
		st.Calendars[d] = in
		return in, nil
	case "room":
		var in Room
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		if in.ID != 0 || !codeRE.MatchString(in.Code) || !textOK(in.Name, 100, true) {
			return nil, bad("invalid_room")
		}
		for _, v := range st.Rooms {
			if v.Code == in.Code {
				return nil, conflict("room_code_exists")
			}
		}
		in.ID = st.allocate()
		st.Rooms[in.ID] = in
		return in, nil
	case "batch":
		var in Batch
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		return s.batch(ctx, st, a, r.Header.Get("Authorization"), in)
	case "lesson":
		in := Lesson{}
		batch := Batch{}
		if r.Method == "DELETE" {
			if e := decodeJSON(b, &struct{}{}); e != nil {
				return nil, e
			}
			batch.Deletes = []LessonRef{{ids["lesson"], d}}
		} else {
			if e := decodeJSON(b, &in); e != nil {
				return nil, e
			}
			if in.DivisionID != 0 && in.DivisionID != d || in.ID != 0 {
				return nil, bad("scope_or_id_forbidden")
			}
			in.DivisionID = d
			if r.Method == "POST" {
				batch.Creates = []Lesson{in}
			} else {
				in.ID = ids["lesson"]
				batch.Updates = []Lesson{in}
			}
		}
		return s.batch(ctx, st, a, r.Header.Get("Authorization"), batch)
	case "type":
		if r.Method == "DELETE" {
			if e := decodeJSON(b, &struct{}{}); e != nil {
				return nil, e
			}
			t, ok := st.Types[ids["type"]]
			if !ok || t.DivisionID != d {
				return nil, missing
			}
			t.Active = false
			t.Version++
			st.Types[t.ID] = t
			return t, nil
		}
		var in Type
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		if in.ID != 0 || in.DivisionID != 0 || in.Version != 0 {
			return nil, bad("generated_fields_forbidden")
		}
		return typeSave(st, d, ids["type"], in)
	case "day":
		if r.Method == "DELETE" {
			if e := decodeJSON(b, &struct{}{}); e != nil {
				return nil, e
			}
			v, ok := st.Days[ids["day"]]
			if !ok || v.DivisionID != d {
				return nil, missing
			}
			if v.HasHistory {
				return nil, conflict("day_has_history")
			}
			delete(st.Days, v.ID)
			return map[string]bool{"deleted": true}, nil
		}
		var in Day
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		if in.ID != 0 || in.DivisionID != 0 || in.HasHistory {
			return nil, bad("generated_fields_forbidden")
		}
		return daySave(st, d, ids["day"], in)
	case "mark":
		var in Mark
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		if in.AccountID < 1 || in.TypeID < 1 {
			return nil, bad("invalid_mark")
		}
		return s.setMark(ctx, st, a, d, ids["day"], in, op, false)
	case "unmark":
		if e := decodeJSON(b, &struct{}{}); e != nil {
			return nil, e
		}
		return s.deleteMark(ctx, st, a, d, ids["day"], ids["account"])
	case "scan":
		var in struct {
			QR string `json:"qr"`
		}
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		if !a.journal(d, true) && !a.can(d, "scan", true) {
			return nil, denied
		}
		account, e := s.scanAccount(ctx, st, a, d, ids["day"], in.QR)
		if e != nil {
			return nil, e
		}
		typ := int64(0)
		for _, t := range st.Types {
			if t.DivisionID == d && t.Active && t.ScanDefault {
				typ = t.ID
			}
		}
		if typ == 0 {
			return nil, conflict("scan_default_type_required")
		}
		return s.setMark(ctx, st, a, d, ids["day"], Mark{account, typ}, op, true)
	case "zap-create":
		var in ZapInput
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		return s.zapCreate(ctx, st, a, d, in, op)
	case "zap-process":
		var in ZapDecision
		if e := decodeJSON(b, &in); e != nil {
			return nil, e
		}
		return s.zapProcess(ctx, st, a, d, ids["zap"], in, op)
	case "zap-retry", "zap-unlink":
		if e := decodeJSON(b, &struct{}{}); e != nil {
			return nil, e
		}
		if !validDate(r.PathValue("date")) {
			return nil, bad("invalid_date")
		}
		return s.zapDateAction(ctx, st, a, d, ids["zap"], r.PathValue("date"), kind == "zap-unlink", op)
	}
	return nil, missing
}
