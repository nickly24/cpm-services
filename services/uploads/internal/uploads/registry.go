package uploads

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) loadFile(r *http.Request, sc Scope, id string) (File, error) {
	var f File
	e := s.DB.QueryRowContext(r.Context(), "SELECT id,organization_id,division_id,actor_account_id,type,owner,owner_reference,target_id,source_hash,size_bytes,extension,expires_at FROM uploads_files_v1 WHERE id=? AND organization_id=? AND division_id=? AND actor_account_id=?", id, sc.OrganizationID, sc.DivisionID, sc.ActorID).Scan(&f.ID, &f.OrganizationID, &f.DivisionID, &f.ActorID, &f.Type, &f.Owner, &f.OwnerID, &f.TargetID, &f.Hash, &f.Size, &f.Extension, &f.Expires)
	return f, e
}
func fileAccess(a Actor, t Type, edit bool) error {
	if a.Role == "student" && (t.ID == "homework.pdf" || t.ID == "attendance.zap-images") {
		return nil
	}
	if t.ID == "homework.pdf" {
		if a.Can("assignments", false) {
			return nil
		}
		return api(403, "owner_access_denied")
	}
	return permission(a, t, edit)
}
func (s *Server) register(r *http.Request, a Actor, sc Scope) (any, int, error) {
	key, e := command(r)
	if e != nil {
		return nil, 0, e
	}
	var in RegisterDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	t, e := findType(in.Type)
	if e != nil {
		return nil, 0, e
	}
	if e = fileAccess(a, t, true); e != nil {
		return nil, 0, e
	}
	if t.ID != "homework.pdf" && t.ID != "attendance.zap-images" {
		return nil, 0, api(400, "use_import_session")
	}
	if t.ID == "homework.pdf" {
		if !keyPattern.MatchString(in.OwnerReference) || in.TargetID != "" {
			return nil, 0, api(400, "invalid_owner_reference")
		}
	} else {
		if _, e = parseID(in.OwnerReference); e != nil {
			return nil, 0, e
		}
		if _, e = parseID(in.TargetID); e != nil {
			return nil, 0, e
		}
	}
	f := File{ID: uuid(), Scope: sc, Type: t.ID, Owner: t.Owner, OwnerID: in.OwnerReference, TargetID: in.TargetID, Expires: time.Now().UTC().Add(365 * 24 * time.Hour)}
	raw, e := s.ownerFile(r.Context(), f, r.Header.Get("Authorization"))
	if e != nil {
		return nil, 0, e
	}
	if t.ID == "attendance.zap-images" {
		var out struct {
			Count int64 `json:"image_count"`
		}
		json.Unmarshal(raw, &out)
		n, _ := parseID(in.OwnerReference)
		if n > out.Count {
			return nil, 0, api(404, "owner_attachment_not_found")
		}
	}
	encoded, _ := json.Marshal(in)
	id, fresh, e := s.reserve(r.Context(), sc, key, "file.register", digest(encoded), f.ID)
	if e != nil {
		return nil, 0, e
	}
	f.ID = id
	if !fresh {
		existing, e := s.loadFile(r, sc, id)
		if e != nil {
			return nil, 0, api(409, "command_outcome_unknown")
		}
		return existing, 200, nil
	}
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(r.Context(), "INSERT INTO uploads_files_v1(id,organization_id,division_id,actor_account_id,type,owner,owner_reference,target_id,expires_at) VALUES(?,?,?,?,?,?,?,?,?)", f.ID, sc.OrganizationID, sc.DivisionID, sc.ActorID, t.ID, t.Owner, f.OwnerID, f.TargetID, f.Expires)
		if e != nil {
			return e
		}
		if e = receipt(r.Context(), tx, sc, key, "completed"); e != nil {
			return e
		}
		return event(r.Context(), tx, sc, t, id, "file.registered", 1)
	})
	return f, 201, e
}
func (s *Server) fileGet(r *http.Request, a Actor, sc Scope) (any, int, error) {
	f, e := s.loadFile(r, sc, r.PathValue("file"))
	if e != nil {
		return nil, 0, e
	}
	t, _ := findType(f.Type)
	if e = fileAccess(a, t, false); e != nil {
		return nil, 0, e
	}
	if !f.Expires.After(time.Now()) {
		return nil, 0, api(410, "file_reference_expired")
	}
	var live json.RawMessage
	if f.OwnerID != "" {
		live, e = s.ownerFile(r.Context(), f, r.Header.Get("Authorization"))
		if e != nil {
			return nil, 0, e
		}
	} else {
		var ssid string
		e = s.DB.QueryRowContext(r.Context(), "SELECT id FROM uploads_sessions_v1 WHERE file_id=? AND organization_id=? AND division_id=? AND actor_account_id=?", f.ID, sc.OrganizationID, sc.DivisionID, sc.ActorID).Scan(&ssid)
		if e != nil {
			return nil, 0, e
		}
		ss, e := loadSession(r.Context(), s.DB, sc, ssid, false)
		if e != nil {
			return nil, 0, e
		}
		if ss.OwnerSessionID != "" {
			_, e = s.ownerGet(r.Context(), ss, r.Header.Get("Authorization"))
			if e != nil {
				return nil, 0, e
			}
		}
	}
	return map[string]any{"file": f, "owner_live": masked(live), "lifecycle_owner": f.Owner}, 200, nil
}
func (s *Server) download(w http.ResponseWriter, r *http.Request, a Actor, sc Scope) error {
	f, e := s.loadFile(r, sc, r.PathValue("file"))
	if e != nil {
		return e
	}
	t, _ := findType(f.Type)
	if e = fileAccess(a, t, false); e != nil {
		return e
	}
	if !f.Expires.After(time.Now()) {
		return api(410, "file_expired")
	}
	if f.Type == "homework.pdf" {
		if _, e = s.ownerFile(r.Context(), f, r.Header.Get("Authorization")); e != nil {
			return e
		}
		return api(409, "use_homework_submission_file_url")
	}
	if f.Type == "attendance.zap-images" {
		if _, e = s.ownerFile(r.Context(), f, r.Header.Get("Authorization")); e != nil {
			return e
		}
		path := "/v1/attendance/divisions/" + strconv.FormatInt(sc.DivisionID, 10) + "/zaps/" + f.TargetID + "/images/" + f.OwnerID
		req, _ := http.NewRequestWithContext(r.Context(), "GET", s.Config.AttendanceURL+path, nil)
		req.Header.Set("Authorization", r.Header.Get("Authorization"))
		res, e := s.HTTP.Do(req)
		if e != nil {
			return api(503, "owner_unavailable")
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return api(res.StatusCode, "owner_download_rejected")
		}
		b, e := io.ReadAll(io.LimitReader(res.Body, maxFile+1))
		if e != nil || len(b) > maxFile {
			return api(503, "invalid_owner_file")
		}
		ct := http.DetectContentType(b)
		if ct != "image/png" && ct != "image/jpeg" {
			return api(503, "invalid_owner_file")
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", `attachment; filename="attachment"`)
		_, e = w.Write(b)
		return e
	}
	if _, _, e = s.fileGet(r, a, sc); e != nil {
		return e
	}
	var b []byte
	e = s.DB.QueryRowContext(r.Context(), "SELECT content FROM uploads_files_v1 WHERE id=? AND organization_id=? AND division_id=? AND actor_account_id=?", f.ID, sc.OrganizationID, sc.DivisionID, sc.ActorID).Scan(&b)
	if e != nil {
		return e
	}
	if len(b) == 0 {
		return api(410, "source_not_retained")
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="source.`+f.Extension+`"`)
	_, e = w.Write(b)
	return e
}
