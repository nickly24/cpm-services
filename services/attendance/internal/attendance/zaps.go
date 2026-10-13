package attendance

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"
)

type ZapInput struct {
	AccountID int64    `json:"account_id"`
	Text      string   `json:"text"`
	Dates     []string `json:"dates"`
	Images    []Image  `json:"images"`
}
type ZapDecision struct {
	Status string `json:"status"`
	Answer string `json:"answer"`
}

func checkImage(i Image) error {
	// Stored legacy references are assigned only by the migration, never by a
	// client creating a new statement.
	if i.AttachmentID != 0 {
		return bad("invalid_image")
	}
	b, e := base64.StdEncoding.DecodeString(i.Data)
	if e != nil || len(b) == 0 || len(b) > 512<<10 {
		return bad("invalid_image")
	}
	c, f, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || c.Width < 1 || c.Height < 1 || c.Width > 8192 || c.Height > 8192 || c.Width*c.Height > 16000000 || i.MIME != "image/"+f {
		return bad("invalid_image")
	}
	return nil
}
func zapVisible(a Identity, z Zap) bool {
	return a.can(z.DivisionID, "zaps", false) || a.User.Role == "student" && a.User.AccountID == z.AccountID && a.zapAccess(z.DivisionID, false)
}
func (s *Server) zapCreate(ctx context.Context, st *State, a Identity, d int64, in ZapInput, op string) (any, error) {
	if !a.zapAccess(d, true) {
		return nil, denied
	}
	if in.AccountID == 0 && a.User.Role == "student" {
		in.AccountID = a.User.AccountID
	}
	if a.User.Role == "student" && in.AccountID != a.User.AccountID {
		return nil, denied
	}
	if in.AccountID < 1 || !textOK(in.Text, 5000, true) || len(in.Dates) < 1 || len(in.Dates) > 60 || len(in.Images) > 3 {
		return nil, bad("invalid_zap")
	}
	refs, e := s.Peers.Refs(ctx, a.User.OrganizationID, d, []int64{in.AccountID}, nil)
	if e != nil {
		return nil, e
	}
	if len(refs.Members) != 1 || refs.Members[0].Role != "student" {
		return nil, bad("student_required")
	}
	seen := map[string]bool{}
	dates := []ZapDate{}
	for _, raw := range in.Dates {
		v := strings.TrimSpace(raw)
		if !validDate(v) {
			return nil, bad("invalid_zap_date")
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		dates = append(dates, ZapDate{v, "pending", nil})
	}
	for _, i := range in.Images {
		if e := checkImage(i); e != nil {
			return nil, e
		}
	}
	if e := s.Peers.Guard(ctx, a.User.OrganizationID, d, in.AccountID, op); e != nil {
		return nil, e
	}
	id := st.allocate()
	z := Zap{id, d, in.AccountID, in.Text, "pending", "", dates, in.Images}
	st.Zaps[id] = z
	z.Images = nil
	return z, nil
}
func (s *Server) zapLink(ctx context.Context, st *State, a Identity, z *Zap, index int, op string) error {
	zd := &z.Dates[index]
	day := int64(0)
	for _, v := range st.Days {
		if v.DivisionID == z.DivisionID && v.Date == zd.Date {
			day = v.ID
		}
	}
	if day == 0 {
		zd.Status = "no_class_day"
		zd.DayID = nil
		return nil
	}
	k := key(day, z.AccountID)
	old, exists := st.Entries[k]
	if exists && !old.Deleted && old.ZapID != nil && *old.ZapID == z.ID {
		// Updating an approved answer does not create a new mark or reinterpret
		// its recorded type, including after the current default was archived.
		zd.Status = "linked"
		zd.DayID = &day
		return nil
	}
	var typ Type
	for _, t := range st.Types {
		if t.DivisionID == z.DivisionID && t.Active && t.ZapDefault {
			typ = t
		}
	}
	if typ.ID == 0 {
		return conflict("zap_default_type_required")
	}
	refs, e := s.Peers.Refs(ctx, a.User.OrganizationID, z.DivisionID, []int64{z.AccountID}, nil)
	if e != nil {
		return e
	}
	if len(refs.Members) != 1 || refs.Members[0].Role != "student" {
		return bad("student_required")
	}
	if e = s.Peers.Guard(ctx, a.User.OrganizationID, z.DivisionID, z.AccountID, op); e != nil {
		return e
	}
	if exists && !old.Deleted {
		if old.ZapID != nil {
			return conflict("attendance_already_marked")
		}
		// Legacy approval replaces manual/QR attendance with the configured excuse.
		// The previous mark remains in the immutable organization audit snapshot.
	}
	id := old.ID
	if !exists {
		id = st.allocate()
	}
	m := refs.Members[0]
	zapID := z.ID
	st.Entries[k] = Entry{id, z.DivisionID, day, z.AccountID, m.GroupID, m.Name, typ, &zapID, false, time.Now().UTC().Format(time.RFC3339Nano)}
	v := st.Days[day]
	v.HasHistory = true
	st.Days[day] = v
	zd.Status = "linked"
	zd.DayID = &day
	return nil
}
func (s *Server) zapProcess(ctx context.Context, st *State, a Identity, d, id int64, in ZapDecision, op string) (any, error) {
	if !a.can(d, "zaps", true) {
		return nil, denied
	}
	z, ok := st.Zaps[id]
	if !ok || z.DivisionID != d {
		return nil, missing
	}
	if in.Status != "approved" && in.Status != "rejected" || !textOK(in.Answer, 5000, false) {
		return nil, bad("invalid_zap_decision")
	}
	z.Status = in.Status
	z.Answer = in.Answer
	for i := range z.Dates {
		if in.Status == "rejected" {
			// Correcting a decision removes only this request's linked entries.
			// Manual marks made after an unlink remain untouched.
			if day := z.Dates[i].DayID; day != nil {
				k := key(*day, z.AccountID)
				entry, exists := st.Entries[k]
				if exists && !entry.Deleted && entry.ZapID != nil && *entry.ZapID == z.ID {
					entry.Deleted = true
					entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
					st.Entries[k] = entry
				}
			}
			z.Dates[i].DayID = nil
			z.Dates[i].Status = "cancelled"
		} else {
			if e := s.zapLink(ctx, st, a, &z, i, op); e != nil {
				return nil, e
			}
		}
	}
	st.Zaps[id] = z
	z.Images = nil
	return z, nil
}
func (s *Server) zapDateAction(ctx context.Context, st *State, a Identity, d, id int64, date string, unlink bool, op string) (any, error) {
	if !a.can(d, "zaps", true) {
		return nil, denied
	}
	z, ok := st.Zaps[id]
	if !ok || z.DivisionID != d {
		return nil, missing
	}
	if z.Status != "approved" {
		return nil, conflict("zap_not_approved")
	}
	index := -1
	for i, v := range z.Dates {
		if v.Date == date {
			index = i
		}
	}
	if index < 0 {
		return nil, missing
	}
	zd := &z.Dates[index]
	if unlink {
		if zd.DayID != nil {
			e, exists := st.Entries[key(*zd.DayID, z.AccountID)]
			if exists && !e.Deleted && e.ZapID != nil && *e.ZapID == z.ID {
				e.Deleted = true
				e.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
				st.Entries[key(*zd.DayID, z.AccountID)] = e
			}
		}
		zd.Status = "pending"
		zd.DayID = nil
	} else {
		if e := s.zapLink(ctx, st, a, &z, index, op); e != nil {
			return nil, e
		}
	}
	st.Zaps[id] = z
	z.Images = nil
	return z, nil
}
