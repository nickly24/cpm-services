package attendance

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string { return e.Code }
func bad(c string) error          { return &APIError{400, c} }
func conflict(c string) error     { return &APIError{409, c} }

var denied = &APIError{403, "forbidden"}
var missing = &APIError{404, "not_found"}
var unavailable = &APIError{503, "dependency_unavailable"}
var guardMissing = &APIError{503, "core_history_guard_required"}

type Permission struct {
	View bool `json:"view"`
	Edit bool `json:"edit"`
}
type Division struct {
	ID          int64                 `json:"id"`
	Name        string                `json:"name"`
	GroupID     *int64                `json:"group_id"`
	Permissions map[string]Permission `json:"permissions"`
}
type Organization struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Divisions []Division `json:"divisions"`
}
type User struct {
	AccountID         int64  `json:"account_id"`
	Role              string `json:"role"`
	OrganizationID    int64  `json:"organization_id"`
	DivisionID        *int64 `json:"division_id"`
	OrganizationAdmin bool   `json:"organization_admin"`
}
type Identity struct {
	Status        bool           `json:"status"`
	Active        bool           `json:"active"`
	User          User           `json:"user"`
	Organizations []Organization `json:"organizations"`
}

func (a Identity) division(d int64) (Division, bool) {
	for _, o := range a.Organizations {
		if o.ID == a.User.OrganizationID {
			for _, v := range o.Divisions {
				if v.ID == d {
					return v, true
				}
			}
		}
	}
	return Division{}, false
}
func (a Identity) admin() bool { return a.User.Role == "admin" && a.User.OrganizationAdmin }
func (a Identity) can(d int64, section string, edit bool) bool {
	if a.admin() {
		return true
	}
	v, ok := a.division(d)
	if !ok || a.User.Role != "staff_admin" {
		return false
	}
	p := v.Permissions[section]
	return p.View && (!edit || p.Edit)
}
func (a Identity) journal(d int64, edit bool) bool {
	if a.can(d, "attendance", edit) {
		return true
	}
	_, ok := a.division(d)
	return ok && (a.User.Role == "proctor" || !edit && (a.User.Role == "student" || a.User.Role == "supervisor"))
}
func (a Identity) zapAccess(d int64, edit bool) bool {
	if a.can(d, "zaps", edit) {
		return true
	}
	_, ok := a.division(d)
	return ok && a.User.Role == "student"
}

type Calendar struct {
	DivisionID int64  `json:"division_id"`
	Visibility string `json:"visibility"`
	Timezone   string `json:"timezone"`
}
type Room struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}
type Lesson struct {
	ID         int64   `json:"id"`
	DivisionID int64   `json:"division_id"`
	Date       string  `json:"date"`
	Start      string  `json:"start_time"`
	End        string  `json:"end_time"`
	Name       string  `json:"lesson_name"`
	Teacher    string  `json:"teacher_name"`
	Location   string  `json:"location"`
	Classroom  string  `json:"classroom"`
	Color      string  `json:"color"`
	Changed    bool    `json:"is_changed"`
	InPerson   bool    `json:"is_in_person"`
	ForAll     bool    `json:"is_for_all"`
	Public     bool    `json:"is_public"`
	SchoolID   *int64  `json:"school_id"`
	GroupIDs   []int64 `json:"target_group_ids"`
	TeacherID  *int64  `json:"teacher_account_id"`
	RoomID     *int64  `json:"room_resource_id"`
}
type LessonRef struct {
	ID         int64 `json:"id"`
	DivisionID int64 `json:"division_id"`
}
type Batch struct {
	Creates []Lesson    `json:"creates"`
	Updates []Lesson    `json:"updates"`
	Deletes []LessonRef `json:"deletes"`
}
type Type struct {
	ID          int64  `json:"id"`
	DivisionID  int64  `json:"division_id"`
	Version     int64  `json:"version"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Mark        string `json:"mark"`
	Sort        int    `json:"sort_order"`
	Active      bool   `json:"is_active"`
	Presence    bool   `json:"counts_as_present"`
	Absence     bool   `json:"counts_as_absent"`
	Excused     bool   `json:"is_excused"`
	ScanDefault bool   `json:"scan_default"`
	ZapDefault  bool   `json:"zap_default"`
}
type Day struct {
	ID         int64   `json:"id"`
	DivisionID int64   `json:"division_id"`
	Date       string  `json:"date"`
	Comment    string  `json:"comment"`
	LessonIDs  []int64 `json:"lesson_ids"`
	HasHistory bool    `json:"has_history"`
}
type Entry struct {
	ID         int64  `json:"id"`
	DivisionID int64  `json:"division_id"`
	DayID      int64  `json:"class_day_id"`
	AccountID  int64  `json:"account_id"`
	GroupID    *int64 `json:"group_id"`
	FullName   string `json:"full_name"`
	Type       Type   `json:"type"`
	ZapID      *int64 `json:"zap_id"`
	Deleted    bool   `json:"is_deleted"`
	UpdatedAt  string `json:"updated_at"`
}
type Mark struct {
	AccountID int64 `json:"account_id"`
	TypeID    int64 `json:"attendance_type_id"`
}
type Image struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}
type ZapDate struct {
	Date   string `json:"date"`
	Status string `json:"status"`
	DayID  *int64 `json:"class_day_id"`
}
type Zap struct {
	ID         int64     `json:"id"`
	DivisionID int64     `json:"division_id"`
	AccountID  int64     `json:"account_id"`
	Text       string    `json:"text"`
	Status     string    `json:"status"`
	Answer     string    `json:"answer"`
	Dates      []ZapDate `json:"dates"`
	Images     []Image   `json:"images,omitempty"`
}
type State struct {
	NextID    int64              `json:"next_id"`
	Calendars map[int64]Calendar `json:"calendars"`
	Rooms     map[int64]Room     `json:"rooms"`
	Lessons   map[int64]Lesson   `json:"lessons"`
	Types     map[int64]Type     `json:"types"`
	Days      map[int64]Day      `json:"days"`
	Entries   map[string]Entry   `json:"entries"`
	Zaps      map[int64]Zap      `json:"zaps"`
}

func NewState() *State {
	return &State{NextID: 1, Calendars: map[int64]Calendar{}, Rooms: map[int64]Room{}, Lessons: map[int64]Lesson{}, Types: map[int64]Type{}, Days: map[int64]Day{}, Entries: map[string]Entry{}, Zaps: map[int64]Zap{}}
}
func (s *State) allocate() int64    { id := s.NextID; s.NextID++; return id }
func key(day, account int64) string { return fmt.Sprintf("%d:%d", day, account) }
func clone(s *State) *State {
	b, _ := json.Marshal(s)
	var n State
	_ = json.Unmarshal(b, &n)
	return &n
}
func textOK(s string, max int, required bool) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && (!required || strings.TrimSpace(s) != "") && !strings.ContainsRune(s, 0)
}

var colorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
var codeRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func positive(p *int64) bool { return p == nil || *p > 0 }
func same(a, b *int64) bool  { return a != nil && b != nil && *a == *b }
func validDate(s string) bool {
	t, e := time.Parse("2006-01-02", s)
	return e == nil && t.Format("2006-01-02") == s && t.Year() >= 1900 && t.Year() <= 2200
}
func period(from, to string) error {
	if !validDate(from) || !validDate(to) || from > to {
		return bad("invalid_period")
	}
	a, _ := time.Parse("2006-01-02", from)
	b, _ := time.Parse("2006-01-02", to)
	if b.Sub(a) > 365*24*time.Hour {
		return bad("period_too_long")
	}
	return nil
}

// Find all UTC instants for a local minute; DST gaps/folds are rejected.
func localInstant(date, clock, zone string) (time.Time, error) {
	if !validDate(date) {
		return time.Time{}, bad("invalid_date")
	}
	c, e := time.Parse("15:04", clock)
	if e != nil || c.Format("15:04") != clock {
		return time.Time{}, bad("invalid_time")
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.Time{}, bad("invalid_timezone")
	}
	target := date + " " + clock
	t, e := time.ParseInLocation("2006-01-02 15:04", target, loc)
	if e != nil || t.Format("2006-01-02 15:04") != target {
		return time.Time{}, bad("nonexistent_local_time")
	}
	// Zone offsets can change by non-hour amounts; search neighbouring offset candidates.
	seen := map[int]bool{}
	count := 0
	_, base := t.Zone()
	for h := -48; h <= 48; h++ {
		_, offset := t.Add(time.Duration(h) * time.Hour).Zone()
		if seen[offset] {
			continue
		}
		seen[offset] = true
		candidate := t.Add(time.Duration(base-offset) * time.Second)
		if candidate.In(loc).Format("2006-01-02 15:04") == target {
			count++
		}
	}
	if count != 1 {
		return time.Time{}, bad("ambiguous_local_time")
	}
	return t.UTC(), nil
}
func (s *State) interval(l Lesson) (time.Time, time.Time, error) {
	c, ok := s.Calendars[l.DivisionID]
	if !ok {
		return time.Time{}, time.Time{}, bad("calendar_required")
	}
	a, e := localInstant(l.Date, l.Start, c.Timezone)
	if e != nil {
		return a, a, e
	}
	b, e := localInstant(l.Date, l.End, c.Timezone)
	if e != nil {
		return a, b, e
	}
	if !a.Before(b) || l.Start >= l.End {
		return a, b, bad("invalid_interval")
	}
	return a, b, nil
}
func (s *State) validLesson(l Lesson) error {
	if l.DivisionID < 1 || !textOK(l.Name, 200, true) || !textOK(l.Teacher, 200, true) || !textOK(l.Location, 200, true) || !textOK(l.Classroom, 50, true) || !colorRE.MatchString(l.Color) || !positive(l.SchoolID) || !positive(l.TeacherID) || !positive(l.RoomID) || l.Public != (l.SchoolID == nil) || len(l.GroupIDs) > 100 || !l.ForAll && len(l.GroupIDs) == 0 || l.ForAll && len(l.GroupIDs) > 0 {
		return bad("invalid_lesson")
	}
	seen := map[int64]bool{}
	for _, g := range l.GroupIDs {
		if g < 1 || seen[g] {
			return bad("invalid_target_group")
		}
		seen[g] = true
	}
	if l.RoomID != nil {
		if _, ok := s.Rooms[*l.RoomID]; !ok {
			return bad("invalid_room")
		}
	}
	_, _, e := s.interval(l)
	return e
}
func sortedLessons(s *State) []Lesson {
	out := []Lesson{}
	for _, v := range s.Lessons {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].ID < out[j].ID
	})
	return out
}
