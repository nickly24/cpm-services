package attendance

import (
	"context"
	"sort"
	"time"
)

func (s *Server) target(ctx context.Context, a Identity, d, u int64, scan bool) (Member, error) {
	if m, ok := cachedCellTarget(ctx, a, d, u); ok {
		return m, nil
	}
	refs, e := s.Peers.Refs(ctx, a.User.OrganizationID, d, []int64{u}, nil)
	if e != nil {
		return Member{}, e
	}
	if len(refs.Members) != 1 || refs.Members[0].Role != "student" {
		return Member{}, bad("student_required")
	}
	m := refs.Members[0]
	if !a.can(d, "attendance", true) && !(scan && a.can(d, "scan", true)) {
		v, ok := a.division(d)
		if !ok || a.User.Role != "proctor" || !same(v.GroupID, m.GroupID) {
			return m, denied
		}
		assigned := false
		for _, g := range refs.Groups {
			if same(&g.ID, m.GroupID) && g.ProctorID != nil && *g.ProctorID == a.User.AccountID {
				assigned = true
			}
		}
		if !assigned {
			return m, denied
		}
	}
	return m, nil
}
func (s *Server) setMark(ctx context.Context, st *State, a Identity, d, day int64, m Mark, op string, scan bool) (any, error) {
	v, ok := st.Days[day]
	if !ok || v.DivisionID != d {
		return nil, missing
	}
	t, ok := st.Types[m.TypeID]
	if !ok || t.DivisionID != d || !t.Active {
		return nil, bad("invalid_attendance_type")
	}
	target, e := s.target(ctx, a, d, m.AccountID, scan)
	if e != nil {
		return nil, e
	}
	k := key(day, m.AccountID)
	old, exists := st.Entries[k]
	if scan && exists && !old.Deleted {
		return nil, conflict("attendance_already_recorded")
	}
	if old.ZapID != nil && !old.Deleted {
		return nil, conflict("attendance_linked_to_zap")
	}
	_, guarded := cachedCellTarget(ctx, a, d, m.AccountID)
	if !guarded {
		e = s.Peers.Guard(ctx, a.User.OrganizationID, d, m.AccountID, op)
	}
	if e != nil {
		return nil, e
	}
	id := old.ID
	if !exists {
		id = st.allocate()
	}
	st.Entries[k] = Entry{id, d, day, m.AccountID, target.GroupID, target.Name, t, nil, false, time.Now().UTC().Format(time.RFC3339Nano)}
	v.HasHistory = true
	st.Days[day] = v
	return st.Entries[k], nil
}
func (s *Server) deleteMark(ctx context.Context, st *State, a Identity, d, day, u int64) (any, error) {
	e, ok := st.Entries[key(day, u)]
	if !ok || e.DivisionID != d || e.Deleted {
		return nil, missing
	}
	if e.ZapID != nil {
		return nil, conflict("attendance_linked_to_zap")
	}
	if !a.can(d, "attendance", true) {
		if _, err := s.target(ctx, a, d, u, false); err != nil {
			return nil, err
		}
		v, _ := a.division(d)
		if !same(v.GroupID, e.GroupID) {
			return nil, denied
		}
	}
	e.Deleted = true
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	st.Entries[key(day, u)] = e
	return map[string]any{"deleted": true}, nil
}
func validType(t Type) bool {
	return codeRE.MatchString(t.Code) && textOK(t.Name, 100, true) && textOK(t.Mark, 8, false) && colorRE.MatchString(t.Color) && t.Sort >= 0 && t.Sort <= 10000 && !(t.Presence && t.Absence) && (!t.ZapDefault || t.Excused) && (!t.ScanDefault || t.Presence)
}
func typeSave(st *State, d, id int64, t Type) (any, error) {
	if !validType(t) {
		return nil, bad("invalid_type")
	}
	t.DivisionID = d
	if id == 0 {
		if t.ID != 0 || t.Version != 0 {
			return nil, bad("create_id_forbidden")
		}
		t.ID = st.allocate()
		t.Version = 1
	} else {
		old, ok := st.Types[id]
		if !ok || old.DivisionID != d {
			return nil, missing
		}
		if old.Code != t.Code {
			return nil, conflict("type_code_immutable")
		}
		t.ID = id
		t.Version = old.Version + 1
	}
	for n, old := range st.Types {
		if n != t.ID && old.DivisionID == d {
			if old.Code == t.Code {
				return nil, conflict("type_code_exists")
			}
			if t.Active && old.Active && (t.ScanDefault && old.ScanDefault || t.ZapDefault && old.ZapDefault) {
				return nil, conflict("default_type_exists")
			}
		}
	}
	st.Types[t.ID] = t
	return t, nil
}
func daySave(st *State, d, id int64, in Day) (any, error) {
	if !validDate(in.Date) || !textOK(in.Comment, 500, false) || len(in.LessonIDs) > 100 {
		return nil, bad("invalid_day")
	}
	in.DivisionID = d
	if in.LessonIDs == nil {
		in.LessonIDs = []int64{}
	}
	if id == 0 {
		if in.ID != 0 || in.HasHistory {
			return nil, bad("create_id_forbidden")
		}
		in.ID = st.allocate()
	} else {
		old, ok := st.Days[id]
		if !ok || old.DivisionID != d {
			return nil, missing
		}
		if old.HasHistory && old.Date != in.Date {
			return nil, conflict("day_has_history")
		}
		in.ID = id
		in.HasHistory = old.HasHistory
	}
	for n, v := range st.Days {
		if n != in.ID && v.DivisionID == d && v.Date == in.Date {
			return nil, conflict("class_day_exists")
		}
	}
	seen := map[int64]bool{}
	for _, n := range in.LessonIDs {
		l, ok := st.Lessons[n]
		if !ok || l.DivisionID != d || l.Date != in.Date || seen[n] {
			return nil, bad("invalid_day_lesson")
		}
		seen[n] = true
	}
	st.Days[in.ID] = in
	return in, nil
}
func (s *Server) report(ctx context.Context, st *State, a Identity, d, day int64, from, to string, account, group *int64) (any, error) {
	if !a.journal(d, false) && !(day > 0 && a.can(d, "scan", false)) {
		return nil, denied
	}
	if !positive(account) || !positive(group) {
		return nil, bad("invalid_filter")
	}
	if day == 0 {
		if e := period(from, to); e != nil {
			return nil, e
		}
	} else {
		v, ok := st.Days[day]
		if !ok || v.DivisionID != d {
			return nil, missing
		}
		from = v.Date
		to = v.Date
	}
	if a.User.Role == "student" {
		if account != nil && *account != a.User.AccountID {
			return nil, denied
		}
		id := a.User.AccountID
		account = &id
		if group != nil {
			return nil, denied
		}
	}
	if a.User.Role == "proctor" && !a.can(d, "attendance", false) {
		v, _ := a.division(d)
		if v.GroupID == nil {
			return nil, denied
		}
		if group != nil && !same(group, v.GroupID) {
			return nil, denied
		}
		group = v.GroupID
		refs, e := s.Peers.Refs(ctx, a.User.OrganizationID, d, []int64{a.User.AccountID}, group)
		if e != nil {
			return nil, e
		}
		assigned := false
		for _, g := range refs.Groups {
			if g.ProctorID != nil && *g.ProctorID == a.User.AccountID {
				assigned = true
			}
		}
		if !assigned {
			return nil, denied
		}
	}
	days := []Day{}
	for _, v := range st.Days {
		if v.DivisionID == d && v.Date >= from && v.Date <= to {
			days = append(days, v)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	entries := []Entry{}
	summary := map[string]int{}
	for _, e := range st.Entries {
		v := st.Days[e.DayID]
		if e.DivisionID != d || e.Deleted || v.Date < from || v.Date > to || day > 0 && e.DayID != day || account != nil && *account != e.AccountID || group != nil && !same(group, e.GroupID) {
			continue
		}
		entries = append(entries, e)
		summary[e.Type.Code]++
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].DayID != entries[j].DayID {
			return entries[i].DayID < entries[j].DayID
		}
		return entries[i].AccountID < entries[j].AccountID
	})
	return map[string]any{"organization_id": a.User.OrganizationID, "division_id": d, "days": days, "entries": entries, "counts_by_type_code": summary, "unmarked_is_absent": false}, nil
}
