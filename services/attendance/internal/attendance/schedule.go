package attendance

import (
	"context"
	"errors"
	"sort"
	"time"
)

func (s *Server) lessonReferences(ctx context.Context, a Identity, token string, l Lesson) error {
	if _, e := s.Peers.Division(ctx, a.User.OrganizationID, l.DivisionID); e != nil {
		return e
	}
	if l.SchoolID != nil {
		if e := s.Peers.School(ctx, token, *l.SchoolID); e != nil {
			return e
		}
	}
	for _, g := range l.GroupIDs {
		if _, e := s.Peers.Refs(ctx, a.User.OrganizationID, l.DivisionID, []int64{}, &g); e != nil {
			return e
		}
	}
	if l.TeacherID != nil {
		refs, e := s.Peers.Refs(ctx, a.User.OrganizationID, l.DivisionID, []int64{*l.TeacherID}, nil)
		if e != nil {
			return e
		}
		if len(refs.Members) != 1 {
			return unavailable
		}
		role := refs.Members[0].Role
		if role != "staff_admin" && role != "proctor" && role != "examinator" && role != "supervisor" {
			return bad("invalid_teacher_reference")
		}
	}
	return nil
}
func (s *Server) batch(ctx context.Context, st *State, a Identity, token string, b Batch) (any, error) {
	n := len(b.Creates) + len(b.Updates) + len(b.Deletes)
	if n < 1 || n > 100 {
		return nil, bad("invalid_batch_size")
	}
	next := clone(st)
	seen := map[int64]bool{}
	ids := []int64{}
	for _, d := range b.Deletes {
		if d.ID < 1 || d.DivisionID < 1 || seen[d.ID] {
			return nil, bad("duplicate_batch_object")
		}
		seen[d.ID] = true
		if !a.can(d.DivisionID, "schedule", true) {
			return nil, denied
		}
		old, ok := next.Lessons[d.ID]
		if !ok || old.DivisionID != d.DivisionID {
			return nil, missing
		}
		for _, day := range next.Days {
			for _, id := range day.LessonIDs {
				if id == d.ID {
					return nil, conflict("lesson_linked_to_day")
				}
			}
		}
		if _, e := s.Peers.Division(ctx, a.User.OrganizationID, d.DivisionID); e != nil {
			return nil, e
		}
		delete(next.Lessons, d.ID)
	}
	for _, l := range b.Updates {
		if l.GroupIDs == nil {
			l.GroupIDs = []int64{}
		}
		if l.ID < 1 || seen[l.ID] {
			return nil, bad("duplicate_batch_object")
		}
		seen[l.ID] = true
		old, ok := next.Lessons[l.ID]
		if !ok || old.DivisionID != l.DivisionID {
			return nil, missing
		}
		if !a.can(l.DivisionID, "schedule", true) {
			return nil, denied
		}
		for _, day := range next.Days {
			for _, id := range day.LessonIDs {
				if id == l.ID && day.Date != l.Date {
					return nil, conflict("lesson_linked_to_day")
				}
			}
		}
		if e := next.validLesson(l); e != nil {
			return nil, e
		}
		if e := s.lessonReferences(ctx, a, token, l); e != nil {
			return nil, e
		}
		next.Lessons[l.ID] = l
	}
	for _, l := range b.Creates {
		if l.GroupIDs == nil {
			l.GroupIDs = []int64{}
		}
		if l.ID != 0 {
			return nil, bad("create_id_forbidden")
		}
		if !a.can(l.DivisionID, "schedule", true) {
			return nil, denied
		}
		if e := next.validLesson(l); e != nil {
			return nil, e
		}
		if e := s.lessonReferences(ctx, a, token, l); e != nil {
			return nil, e
		}
		l.ID = next.allocate()
		next.Lessons[l.ID] = l
		ids = append(ids, l.ID)
	}
	// All overlaps are allowed by the user's rule; final-state validation still
	// checks each surviving object's time and day relationships atomically.
	for _, l := range next.Lessons {
		if e := next.validLesson(l); e != nil {
			return nil, e
		}
	}
	*st = *next
	return map[string]any{"created_ids": ids, "updated_count": len(b.Updates), "deleted_count": len(b.Deletes)}, nil
}

type Event struct {
	Lesson
	OrganizationID int64  `json:"organization_id"`
	DivisionName   string `json:"division_name"`
	Timezone       string `json:"timezone"`
	StartsAt       string `json:"starts_at"`
	EndsAt         string `json:"ends_at"`
}
type Overlap struct {
	LessonIDs     []int64  `json:"lesson_ids"`
	ResourceKinds []string `json:"resource_kinds"`
}

func readableCalendar(a Identity, c Calendar) bool {
	if a.admin() || a.can(c.DivisionID, "schedule", false) {
		return true
	}
	_, member := a.division(c.DivisionID)
	role := a.User.Role
	ordinary := role == "student" || role == "proctor" || role == "supervisor" || role == "examinator"
	return ordinary && (member || c.Visibility == "organization") || role == "staff_admin" && c.Visibility == "organization"
}
func (s *Server) calendarRead(ctx context.Context, st *State, a Identity, token, from, to string, selected []int64, personal bool) (any, error) {
	if e := period(from, to); e != nil {
		return nil, e
	}
	if personal && a.User.Role != "student" {
		return nil, denied
	}
	allowed := map[int64]bool{}
	calendars := []map[string]any{}
	divisionNames := map[int64]string{}
	for d, c := range st.Calendars {
		if !readableCalendar(a, c) {
			continue
		}
		name, e := s.Peers.Division(ctx, a.User.OrganizationID, d)
		if e != nil {
			// Archived divisions retain their attendance-owned calendar/history.
			// Exclude their calendars without breaking unrelated live calendars.
			if errors.Is(e, missing) {
				continue
			}
			return nil, e
		}
		_, member := a.division(d)
		divisionNames[d] = name
		calendars = append(calendars, map[string]any{"division_id": d, "division_name": name, "visibility": c.Visibility, "timezone": c.Timezone, "is_member": member, "can_edit": a.can(d, "schedule", true)})
		allowed[d] = true
	}
	choose := map[int64]bool{}
	if personal {
		for d := range allowed {
			if _, ok := a.division(d); ok {
				choose[d] = true
			}
		}
	} else {
		for _, d := range selected {
			if !allowed[d] {
				return nil, missing
			}
			choose[d] = true
		}
	}
	var school *int64
	var e error
	if a.User.Role == "student" {
		school, e = s.Peers.OwnSchool(ctx, token)
		if e != nil {
			return nil, e
		}
	}
	events := []Event{}
	for _, l := range sortedLessons(st) {
		if !choose[l.DivisionID] || l.Date < from || l.Date > to {
			continue
		}
		if a.User.Role == "student" {
			if !l.Public && !same(l.SchoolID, school) {
				continue
			}
			if !l.ForAll {
				v, ok := a.division(l.DivisionID)
				matched := false
				if ok && v.GroupID != nil {
					for _, g := range l.GroupIDs {
						if g == *v.GroupID {
							matched = true
						}
					}
				}
				if !matched {
					continue
				}
			}
		}
		start, end, err := st.interval(l)
		if err != nil {
			return nil, err
		}
		events = append(events, Event{l, a.User.OrganizationID, divisionNames[l.DivisionID], st.Calendars[l.DivisionID].Timezone, start.Format(time.RFC3339), end.Format(time.RFC3339)})
	}
	if len(events) > 2000 {
		return nil, bad("calendar_event_limit_exceeded")
	}
	// Different calendar timezones must share one actual chronological order.
	// Local dates still define the requested period for each source calendar.
	sort.Slice(events, func(i, j int) bool {
		if events[i].StartsAt != events[j].StartsAt {
			return events[i].StartsAt < events[j].StartsAt
		}
		if events[i].EndsAt != events[j].EndsAt {
			return events[i].EndsAt < events[j].EndsAt
		}
		return events[i].ID < events[j].ID
	})
	// Emit connected interval clusters for accordion UI plus resource hints.
	// Hidden events never enter this computation.
	clusters := []Overlap{}
	visited := make([]bool, len(events))
	for i := range events {
		if visited[i] {
			continue
		}
		queue := []int{i}
		visited[i] = true
		ids := []int64{}
		kinds := map[string]bool{}
		for len(queue) > 0 {
			j := queue[0]
			queue = queue[1:]
			ids = append(ids, events[j].ID)
			aStart, _ := time.Parse(time.RFC3339, events[j].StartsAt)
			aEnd, _ := time.Parse(time.RFC3339, events[j].EndsAt)
			for k := range events {
				if j == k {
					continue
				}
				bStart, _ := time.Parse(time.RFC3339, events[k].StartsAt)
				bEnd, _ := time.Parse(time.RFC3339, events[k].EndsAt)
				if aStart.Before(bEnd) && bStart.Before(aEnd) {
					if same(events[j].TeacherID, events[k].TeacherID) {
						kinds["teacher"] = true
					}
					if same(events[j].RoomID, events[k].RoomID) {
						kinds["room"] = true
					}
					for _, g := range events[j].GroupIDs {
						for _, h := range events[k].GroupIDs {
							if g == h {
								kinds["group"] = true
							}
						}
					}
					if !visited[k] {
						visited[k] = true
						queue = append(queue, k)
					}
				}
			}
		}
		if len(ids) > 1 {
			ks := []string{}
			for kind := range kinds {
				ks = append(ks, kind)
			}
			sort.Strings(ks)
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			clusters = append(clusters, Overlap{ids, ks})
		}
	}
	sort.Slice(calendars, func(i, j int) bool { return calendars[i]["division_id"].(int64) < calendars[j]["division_id"].(int64) })
	return map[string]any{"organization_id": a.User.OrganizationID, "calendars": calendars, "events": events, "overlap_clusters": clusters}, nil
}
