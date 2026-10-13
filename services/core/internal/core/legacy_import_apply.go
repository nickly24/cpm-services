package core

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
)

func legacySortedKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// A generated bootstrap password exists only in this stack and Auth's hashing
// request. It is never part of an import DTO/receipt/report. Admin resets it via
// the dedicated credentials action before handing access to the new account.
func (s *Server) legacyPrepareHashes(r *http.Request, p LegacyImportPreview) (map[string]string, error) {
	out := map[string]string{}
	keys := []string{}
	for _, key := range legacySortedKeys(p.Proctors) {
		if p.Proctors[key].Action == "create" {
			keys = append(keys, "proctor:"+key)
		}
	}
	for _, row := range p.Students {
		if row.Action == "create" {
			keys = append(keys, fmt.Sprintf("student:%d", row.Row))
		}
	}
	for _, key := range keys {
		secret := make([]byte, 32)
		if _, e := rand.Read(secret); e != nil {
			return nil, unavailable
		}
		hash, e := s.Auth.Hash(r.Context(), base64.RawURLEncoding.EncodeToString(secret))
		clear(secret)
		if e != nil {
			return nil, e
		}
		out[key] = hash
	}
	return out, nil
}
func legacyLogin(r *http.Request, db DBTX, role, name string) (string, error) {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return "", bad("invalid_import_name")
	}
	last, first := []rune(strings.ToLower(parts[0])), []rune(strings.ToLower(parts[1]))
	var prefix string
	if role == "proctor" {
		prefix = "pr" + string(first[:1]) + string(last)
	} else {
		prefix = string(last[:min(4, len(last))]) + string(first[:min(2, len(first))])
	}
	// Native usernames cannot contain whitespace and are capped at 50 runes.
	for attempt := 0; attempt < 10000; attempt++ {
		login := prefix
		if role == "proctor" {
			if attempt > 0 {
				login += fmt.Sprint(attempt)
			}
		} else {
			n, e := rand.Int(rand.Reader, big.NewInt(1000))
			if e != nil {
				return "", unavailable
			}
			login += fmt.Sprintf("%03d", n.Int64())
		}
		if !validText(login, 50, true) || strings.ContainsAny(login, " \t\r\n") {
			return "", bad("generated_login_too_long")
		}
		var count int
		if e := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM auth_users WHERE username=?", login).Scan(&count); e != nil {
			return "", e
		}
		if count == 0 {
			return login, nil
		}
	}
	return "", &APIError{409, "generated_login_exhausted"}
}
func (s *Server) legacyInsertAccount(r *http.Request, tx *sql.Tx, a Actor, d int64, u UserDTO, hash string, row int) (LegacyImportCreated, error) {
	if !strings.HasPrefix(hash, "pbkdf2:sha256:600000$") {
		return LegacyImportCreated{}, unavailable
	}
	login, e := legacyLogin(r, tx, u.Role, u.FullName)
	if e != nil {
		return LegacyImportCreated{}, e
	}
	u.Login, u.Password = login, hash
	account, e := s.insertUser(r, tx, a, d, u, hash)
	if e != nil {
		return LegacyImportCreated{}, e
	}
	var profile int64
	if e = tx.QueryRowContext(r.Context(), "SELECT ref_id FROM auth_users WHERE id=? AND role=?", account, u.Role).Scan(&profile); e != nil {
		return LegacyImportCreated{}, e
	}
	return LegacyImportCreated{account, profile, u.Role, u.FullName, row}, nil
}
func (s *Server) legacyApply(r *http.Request, tx *sql.Tx, a Actor, d int64, p LegacyImportPreview, hashes map[string]string) (LegacyImportReport, error) {
	report := LegacyImportReport{Created: []LegacyImportCreated{}, SkippedStudents: p.Summary.StudentsSkip, CredentialsPolicy: "reset_required"}
	schools, groups := map[string]int64{}, map[string]int64{}
	for _, key := range legacySortedKeys(p.Schools) {
		item := p.Schools[key]
		if item.ExistingID != nil {
			schools[key] = *item.ExistingID
			continue
		}
		if !a.OrganizationAdmin {
			return report, denied
		}
		res, e := tx.ExecContext(r.Context(), "INSERT INTO schools(name,short_name,notes,is_active,organization_id) VALUES(?,NULL,NULL,1,?)", item.Name, a.OrganizationID)
		if e != nil {
			return report, e
		}
		n, e := res.LastInsertId()
		if e != nil {
			return report, e
		}
		if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_school_scope(school_id,organization_id) VALUES(?,?)", n, a.OrganizationID); e != nil {
			return report, e
		}
		if e = audit(r.Context(), tx, a, &d, "school.save", n); e != nil {
			return report, e
		}
		schools[key] = n
		report.CreatedSchools++
	}
	for _, key := range legacySortedKeys(p.Groups) {
		item := p.Groups[key]
		if item.ExistingID != nil {
			groups[key] = *item.ExistingID
			continue
		}
		if !a.Can("groups", true) {
			return report, denied
		}
		res, e := tx.ExecContext(r.Context(), "INSERT INTO `groups`(name) VALUES(?)", item.Name)
		if e != nil {
			return report, e
		}
		n, e := res.LastInsertId()
		if e != nil {
			return report, e
		}
		if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_group_scope(group_id,organization_id,division_id) VALUES(?,?,?)", n, a.OrganizationID, d); e != nil {
			return report, e
		}
		if e = audit(r.Context(), tx, a, &d, "group.save", n); e != nil {
			return report, e
		}
		groups[key] = n
		report.CreatedGroups++
	}
	for _, key := range legacySortedKeys(p.Proctors) {
		item := p.Proctors[key]
		gid := groups[item.GroupKey]
		if gid < 1 {
			return report, bad("invalid_import_group")
		}
		if item.ExistingAccountID != nil {
			if item.ExistingGroupID == nil {
				// Bind an unassigned existing proctor only in this division. Never
				// move an existing proctor away from their current group/history.
				if !a.Can("groups", true) {
					return report, denied
				}
				if _, e := tx.ExecContext(r.Context(), "INSERT INTO core_proctor_groups(user_id,division_id,group_id) VALUES(?,?,?)", *item.ExistingAccountID, d, gid); e != nil {
					return report, e
				}
				if _, e := tx.ExecContext(r.Context(), "UPDATE auth_division_access SET group_id=? WHERE user_id=? AND organization_id=? AND division_id=?", gid, *item.ExistingAccountID, a.OrganizationID, d); e != nil {
					return report, e
				}
				if e := audit(r.Context(), tx, a, &d, "group.assign", *item.ExistingAccountID); e != nil {
					return report, e
				}
			}
			continue
		}
		if !a.OrganizationAdmin {
			return report, denied
		}
		created, e := s.legacyInsertAccount(r, tx, a, d, UserDTO{Role: "proctor", FullName: item.FullName, GroupID: &gid}, hashes["proctor:"+key], 0)
		if e != nil {
			return report, e
		}
		report.Created = append(report.Created, created)
		report.CreatedProctors++
	}
	for _, row := range p.Students {
		if row.Action != "create" {
			continue
		}
		if row.Class == nil {
			return report, bad("invalid_import_class")
		}
		u := UserDTO{Role: "student", FullName: row.FullName, Class: *row.Class, TGName: row.TGName}
		if row.SchoolKey != "" {
			n := schools[row.SchoolKey]
			if n < 1 {
				return report, bad("invalid_import_school")
			}
			u.SchoolID = &n
		}
		if row.GroupKey != "" {
			n := groups[row.GroupKey]
			if n < 1 {
				return report, bad("invalid_import_group")
			}
			u.GroupID = &n
		}
		created, e := s.legacyInsertAccount(r, tx, a, d, u, hashes[fmt.Sprintf("student:%d", row.Row)], row.Row)
		if e != nil {
			return report, e
		}
		report.Created = append(report.Created, created)
		report.CreatedStudents++
	}
	return report, nil
}
