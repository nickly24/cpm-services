package exams

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
)

const ImportEditableRetention = 72 * time.Hour
const ImportCommittedRetention = 7 * 24 * time.Hour
const ReceiptRetention = 7 * 24 * time.Hour
const ImportPageLimit = 200

type ImportPage struct {
	Limit            int
	Offset           int
	ExpectedRevision *int64
}

func importPage(p Params) (ImportPage, error) {
	page := ImportPage{Limit: 100}
	for key, target := range map[string]*int{"limit": &page.Limit, "offset": &page.Offset} {
		if raw := p.Query[key]; raw != "" {
			number, err := strconv.Atoi(raw)
			if err != nil {
				return page, Bad("invalid_pagination")
			}
			*target = number
		}
	}
	if page.Limit < 1 || page.Limit > ImportPageLimit || page.Offset < 0 || page.Offset > 100000 {
		return page, Bad("invalid_pagination")
	}
	if raw := p.Query["expected_revision"]; raw != "" {
		version, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || version < 1 {
			return page, Bad("invalid_revision")
		}
		page.ExpectedRevision = &version
	}
	return page, nil
}

type ImportSummary struct {
	Total    int `json:"total_rows"`
	Included int `json:"included_rows"`
	Excluded int `json:"excluded_rows"`
	Errors   int `json:"error_rows"`
	Valid    int `json:"valid_rows"`
}
type ImportErrorRow struct {
	RowID     string   `json:"row_id"`
	SourceRow int      `json:"source_row"`
	Errors    []string `json:"errors"`
}
type ImportPreview struct {
	ID                   string           `json:"id"`
	Revision             int64            `json:"revision"`
	State                string           `json:"state"`
	Kind                 string           `json:"kind"`
	ExamID               int64            `json:"exam_id"`
	ExamRevision         int64            `json:"exam_revision"`
	ConnectedDivisionIDs []int64          `json:"connected_division_ids"`
	OrganizationID       int64            `json:"organization_id"`
	DivisionID           int64            `json:"division_id"`
	AccountID            int64            `json:"account_id"`
	Filename             string           `json:"filename"`
	SourceHash           string           `json:"source_hash"`
	ValidationHash       string           `json:"validation_hash"`
	Rows                 []ImportRow      `json:"rows"`
	Total                int              `json:"total"`
	Limit                int              `json:"limit"`
	Offset               int              `json:"offset"`
	HasMore              bool             `json:"has_more"`
	NextOffset           *int             `json:"next_offset"`
	Summary              ImportSummary    `json:"summary"`
	ErrorReport          []ImportErrorRow `json:"error_report"`
	ExpiresAt            time.Time        `json:"expires_at"`
	Result               any              `json:"result"`
}

func importValidationHash(rows []ImportRow) (string, error) {
	public := importRowsPublic(rows)
	// Baselines are private server CAS data and never participate in the DTO or
	// page-validation hash. Uploads aggregates pages, preserving native errors.
	encoded, err := json.Marshal(public)
	if err != nil {
		return "", err
	}
	// MySQL JSON storage normalizes object key order, including each raw input.
	// Canonicalize the whole public value while preserving integer precision so
	// an unchanged preview hashes identically before and after SQL persistence.
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err = decoder.Decode(&normalized); err != nil {
		return "", err
	}
	encoded, err = json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func importPreview(id string, revision int64, state, kind string, e *Exam, a Actor, p Params, rows []ImportRow, expires time.Time, filename, sourceHash string, result any, page ImportPage) (ImportPreview, error) {
	if page.ExpectedRevision != nil && *page.ExpectedRevision != revision {
		return ImportPreview{}, Conflict("preview_modified")
	}
	validationHash, err := importValidationHash(rows)
	if err != nil {
		return ImportPreview{}, err
	}
	public := importRowsPublic(rows)
	out := ImportPreview{ID: id, Revision: revision, State: state, Kind: kind, ExamID: e.ID, OrganizationID: e.OrganizationID, DivisionID: p.DivisionID, AccountID: a.AccountID, Filename: filename, SourceHash: sourceHash, ValidationHash: validationHash, Total: len(public), Limit: page.Limit, Offset: page.Offset, Summary: ImportSummary{Total: len(public)}, ExpiresAt: expires, Result: result, ErrorReport: []ImportErrorRow{}}
	out.ExamRevision = e.Revision
	out.ConnectedDivisionIDs = allDivisions(e)
	sort.Slice(out.ConnectedDivisionIDs, func(i, j int) bool { return out.ConnectedDivisionIDs[i] < out.ConnectedDivisionIDs[j] })
	for _, row := range public {
		if row.Excluded {
			out.Summary.Excluded++
		} else {
			out.Summary.Included++
			if len(row.Errors) > 0 {
				out.Summary.Errors++
			} else {
				out.Summary.Valid++
			}
		}
	}
	if out.Offset > out.Total {
		out.Offset = out.Total
	}
	end := out.Offset + out.Limit
	if end > out.Total {
		end = out.Total
	}
	out.Rows = public[out.Offset:end]
	if out.Rows == nil {
		out.Rows = []ImportRow{}
	}
	if end < out.Total {
		next := end
		out.NextOffset = &next
		out.HasMore = true
	}
	for _, row := range out.Rows {
		if len(row.Errors) > 0 {
			out.ErrorReport = append(out.ErrorReport, ImportErrorRow{row.RowID, row.SourceRow, append([]string(nil), row.Errors...)})
		}
	}
	return out, nil
}
func importPermission(a Actor, edit bool) bool {
	if !a.Can(edit) {
		return false
	}
	if a.Role == "admin" && a.OrganizationAdmin {
		return true
	}
	permission := a.Permissions["upload"]
	if edit {
		return permission.Edit
	}
	return permission.View || permission.Edit
}
func importMethod(r *http.Request) bool {
	return r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodGet
}
