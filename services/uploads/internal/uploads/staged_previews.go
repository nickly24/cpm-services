package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// Tests validates stateless domain input. Uploads stores the input and its exact
// validated preview; this adapter never creates tests or writes subject results.
const stagedInputLimit = 1 << 20
const stagedPreviewLimit = (16 << 20) - 1 // MEDIUMBLOB's exact maximum.

type stagedJSONInput struct {
	DirectionID int64           `json:"direction_id"`
	Payload     json.RawMessage `json:"payload"`
}

type stagedExternalInput struct {
	TestID               string            `json:"test_id,omitempty"`
	ExpectedTestRevision int64             `json:"expected_test_revision,omitempty"`
	Rows                 []json.RawMessage `json:"rows"`
}

type stagedPreview struct {
	Revision, TargetRevision               int64
	SourceHash, InputHash, Hash, OwnerHash string
	Input, Preview                         json.RawMessage
}

var stagedExternalID = regexp.MustCompile(`^(external_)?[0-9a-fA-F]{24}$`)
var stagedOwnerHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func stagedType(kind string) bool {
	return kind == "tests.json" || kind == "tests.external-results"
}

func stagedDecode(raw json.RawMessage, value any) error {
	if len(raw) > stagedInputLimit {
		return api(413, "owner_input_size_limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return api(400, "invalid_owner_payload")
	}
	return nil
}

func stagedCanonical(raw json.RawMessage) (json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF {
		return nil, api(400, "invalid_owner_payload")
	}
	return json.Marshal(v)
}

func stagedTarget(raw string) (string, error) {
	if !stagedExternalID.MatchString(raw) {
		return "", api(400, "external_test_target_required")
	}
	return strings.ToLower(strings.TrimPrefix(raw, "external_")), nil
}

func stagedEnvelope(ss Session, input json.RawMessage, targetRevision int64) (json.RawMessage, error) {
	if !stagedType(ss.Type) || ss.OrganizationID < 1 || ss.DivisionID < 1 || ss.ActorID < 1 || ss.ID == "" {
		return nil, api(400, "invalid_staged_scope")
	}
	var body any
	switch ss.Type {
	case "tests.json":
		var in stagedJSONInput
		if e := stagedDecode(input, &in); e != nil {
			return nil, e
		}
		if in.DirectionID < 1 || len(in.Payload) == 0 || !json.Valid(in.Payload) || bytes.Equal(bytes.TrimSpace(in.Payload), []byte("null")) || ss.TargetID != "" {
			return nil, api(400, "invalid_owner_payload")
		}
		body = struct {
			OrganizationID int64           `json:"organization_id"`
			DivisionID     int64           `json:"division_id"`
			DirectionID    int64           `json:"direction_id"`
			Payload        json.RawMessage `json:"payload"`
		}{ss.OrganizationID, ss.DivisionID, in.DirectionID, in.Payload}
	case "tests.external-results":
		var in stagedExternalInput
		if e := stagedDecode(input, &in); e != nil {
			return nil, e
		}
		target, e := stagedTarget(ss.TargetID)
		if e != nil {
			return nil, e
		}
		if in.TestID != "" {
			requested, e := stagedTarget(in.TestID)
			if e != nil || requested != target {
				return nil, api(400, "pinned_test_target_mismatch")
			}
		}
		if len(in.Rows) == 0 || len(in.Rows) > 5000 {
			return nil, api(400, "invalid_external_import_rows")
		}
		if targetRevision < 1 || in.ExpectedTestRevision < 0 {
			return nil, api(400, "invalid_external_test_revision")
		}
		if in.ExpectedTestRevision > 0 && in.ExpectedTestRevision != targetRevision {
			return nil, api(409, "owner_revision_conflict")
		}
		body = struct {
			OrganizationID       int64             `json:"organization_id"`
			DivisionID           int64             `json:"division_id"`
			TestID               string            `json:"test_id"`
			ExpectedTestRevision int64             `json:"expected_test_revision"`
			Rows                 []json.RawMessage `json:"rows"`
		}{ss.OrganizationID, ss.DivisionID, target, targetRevision, in.Rows}
	}
	raw, e := json.Marshal(body)
	if e != nil || len(raw) > stagedInputLimit {
		return nil, api(413, "owner_input_size_limit")
	}
	return stagedCanonical(raw)
}

func (s *Server) stagedTargetRevision(ctx context.Context, ss Session, token string) (int64, error) {
	target, e := stagedTarget(ss.TargetID)
	if e != nil {
		return 0, e
	}
	raw, e := s.call(ctx, "GET", s.Config.TestsURL, "/v1/tests/divisions/"+strconv.FormatInt(ss.DivisionID, 10)+"/external-tests/"+target, token, "", "", nil)
	if e != nil {
		return 0, e
	}
	var v struct {
		ID             string `json:"id"`
		OrganizationID int64  `json:"organization_id"`
		DivisionID     int64  `json:"division_id"`
		Revision       int64  `json:"revision"`
		Deleted        bool   `json:"is_deleted"`
	}
	if json.Unmarshal(raw, &v) != nil || v.ID != target || v.OrganizationID != ss.OrganizationID || v.DivisionID != ss.DivisionID || v.Revision < 1 || v.Deleted {
		return 0, api(503, "owner_invalid_external_metadata")
	}
	return v.Revision, nil
}

func stagedValidatePreview(ss Session, raw json.RawMessage, targetRevision int64) (json.RawMessage, string, error) {
	if len(raw) > stagedPreviewLimit {
		return nil, "", api(413, "preview_size_limit")
	}
	var v struct {
		OrganizationID int64  `json:"organization_id"`
		DivisionID     int64  `json:"division_id"`
		TestID         string `json:"test_id"`
		Revision       int64  `json:"revision"`
		PreviewHash    string `json:"preview_hash"`
		Valid          *bool  `json:"valid"`
		CanCommit      *bool  `json:"can_commit"`
	}
	if json.Unmarshal(raw, &v) != nil || v.OrganizationID != ss.OrganizationID || v.DivisionID != ss.DivisionID || !stagedOwnerHash.MatchString(v.PreviewHash) {
		return nil, "", api(503, "owner_invalid_staged_preview")
	}
	if ss.Type == "tests.json" && v.Valid == nil {
		return nil, "", api(503, "owner_invalid_staged_preview")
	}
	if ss.Type == "tests.external-results" {
		target, e := stagedTarget(ss.TargetID)
		if e != nil || v.TestID != target || v.Revision != targetRevision || v.CanCommit == nil {
			return nil, "", api(503, "owner_invalid_staged_preview")
		}
	}
	canonical, e := stagedCanonical(raw)
	if e != nil {
		return nil, "", api(503, "owner_invalid_staged_preview")
	}
	return canonical, v.PreviewHash, nil
}

func (s *Server) validateStagedInput(ctx context.Context, ss Session, token string, input json.RawMessage, targetRevision int64) (json.RawMessage, string, error) {
	// Use the owner's public preview with the original live bearer. The Uploads
	// coordinator credential is not the Tests internal service credential.
	path := "/v1/tests/divisions/" + strconv.FormatInt(ss.DivisionID, 10) + "/imports/json/preview"
	if ss.Type == "tests.external-results" {
		path = "/internal/tests/external-results/validate"
	}
	raw, e := s.call(ctx, "POST", s.Config.TestsURL, path, token, "", "application/json", input)
	if e != nil {
		return nil, "", e
	}
	if ss.Type == "tests.json" {
		var sent, received struct {
			DirectionID int64 `json:"direction_id"`
		}
		if json.Unmarshal(input, &sent) != nil || json.Unmarshal(raw, &received) != nil || sent.DirectionID < 1 || sent.DirectionID != received.DirectionID {
			return nil, "", api(503, "owner_invalid_staged_preview")
		}
	}
	return stagedValidatePreview(ss, raw, targetRevision)
}

func loadStagedPreview(ctx context.Context, q query, ss Session, lock bool) (stagedPreview, error) {
	var p stagedPreview
	stmt := "SELECT revision,target_revision,source_hash,input_hash,preview_hash,owner_preview_hash,input,preview FROM uploads_previews_v1 WHERE session_id=? AND organization_id=? AND division_id=? AND actor_account_id=? AND type=?"
	if lock {
		stmt += " FOR UPDATE"
	}
	e := q.QueryRowContext(ctx, stmt, ss.ID, ss.OrganizationID, ss.DivisionID, ss.ActorID, ss.Type).Scan(&p.Revision, &p.TargetRevision, &p.SourceHash, &p.InputHash, &p.Hash, &p.OwnerHash, &p.Input, &p.Preview)
	if e != nil {
		return p, e
	}
	input, ie := stagedCanonical(p.Input)
	preview, pe := stagedCanonical(p.Preview)
	if ie != nil || pe != nil || p.Revision < 1 || p.SourceHash != ss.SourceHash || digest(input) != p.InputHash || digest(preview) != p.Hash {
		return p, api(503, "staged_preview_integrity_failed")
	}
	p.Input, p.Preview = input, preview
	return p, nil
}

func stagedView(ss Session, p stagedPreview) json.RawMessage {
	raw, _ := json.Marshal(struct {
		ID       string          `json:"id"`
		Revision int64           `json:"revision"`
		State    string          `json:"state"`
		Preview  json.RawMessage `json:"preview"`
	}{ss.ID, p.Revision, "preview", p.Preview})
	return raw
}

func (s *Server) freshStagedPreview(ctx context.Context, ss Session, token string, p stagedPreview) error {
	raw, ownerHash, e := s.validateStagedInput(ctx, ss, token, p.Input, p.TargetRevision)
	if e != nil {
		return e
	}
	if ownerHash != p.OwnerHash || digest(raw) != p.Hash {
		return api(409, "owner_preview_changed")
	}
	return nil
}

func (s *Server) createStagedPreview(ctx context.Context, ss Session, token string, input json.RawMessage) (json.RawMessage, error) {
	old, e := loadStagedPreview(ctx, s.DB, ss, false)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	targetRevision := old.TargetRevision
	if errors.Is(e, sql.ErrNoRows) && ss.Type == "tests.external-results" {
		targetRevision, e = s.stagedTargetRevision(ctx, ss, token)
		if e != nil {
			return nil, e
		}
	}
	canonical, e := stagedEnvelope(ss, input, targetRevision)
	if e != nil {
		return nil, e
	}
	if old.Revision > 0 {
		if digest(canonical) != old.InputHash {
			return nil, api(409, "staged_input_conflict")
		}
		if e = s.freshStagedPreview(ctx, ss, token, old); e != nil {
			return nil, e
		}
		return stagedView(ss, old), nil
	}
	preview, ownerHash, e := s.validateStagedInput(ctx, ss, token, canonical, targetRevision)
	if e != nil {
		return nil, e
	}
	p := stagedPreview{Revision: 1, TargetRevision: targetRevision, SourceHash: ss.SourceHash, InputHash: digest(canonical), Hash: digest(preview), OwnerHash: ownerHash, Input: canonical, Preview: preview}
	_, e = s.DB.ExecContext(ctx, "INSERT INTO uploads_previews_v1(session_id,organization_id,division_id,actor_account_id,type,revision,target_revision,source_hash,input_hash,preview_hash,owner_preview_hash,input,preview) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", ss.ID, ss.OrganizationID, ss.DivisionID, ss.ActorID, ss.Type, p.Revision, p.TargetRevision, p.SourceHash, p.InputHash, p.Hash, p.OwnerHash, p.Input, p.Preview)
	if e != nil {
		var my *mysql.MySQLError
		if !errors.As(e, &my) || my.Number != 1062 {
			return nil, e
		}
		old, e = loadStagedPreview(ctx, s.DB, ss, false)
		if e != nil {
			return nil, e
		}
		if old.InputHash != p.InputHash || old.Hash != p.Hash || old.OwnerHash != p.OwnerHash {
			return nil, api(409, "staged_input_conflict")
		}
		p = old
	}
	return stagedView(ss, p), nil
}

func (s *Server) getStagedPreview(ctx context.Context, ss Session, token string) (json.RawMessage, error) {
	p, e := loadStagedPreview(ctx, s.DB, ss, false)
	if e != nil {
		return nil, e
	}
	if e = s.freshStagedPreview(ctx, ss, token, p); e != nil {
		return nil, e
	}
	return stagedView(ss, p), nil
}

func stagedEditInput(ss Session, p stagedPreview, payload json.RawMessage) (json.RawMessage, error) {
	var in struct {
		DirectionID      int64             `json:"direction_id,omitempty"`
		Payload          json.RawMessage   `json:"payload,omitempty"`
		Rows             []json.RawMessage `json:"rows,omitempty"`
		ExpectedRevision *int64            `json:"expected_revision,omitempty"`
	}
	if e := stagedDecode(payload, &in); e != nil {
		return nil, e
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision != ss.OwnerRevision {
		return nil, api(409, "preview_modified")
	}
	if ss.Type == "tests.json" {
		var old stagedJSONInput
		var envelope struct {
			DirectionID int64 `json:"direction_id"`
		}
		if json.Unmarshal(p.Input, &envelope) != nil || in.Rows != nil || len(in.Payload) == 0 || in.DirectionID != 0 && in.DirectionID != envelope.DirectionID {
			return nil, api(400, "pinned_direction_mismatch")
		}
		old.DirectionID, old.Payload = envelope.DirectionID, in.Payload
		raw, _ := json.Marshal(old)
		return stagedEnvelope(ss, raw, 0)
	}
	if in.DirectionID != 0 || len(in.Payload) > 0 {
		return nil, api(400, "invalid_owner_payload")
	}
	raw, _ := json.Marshal(stagedExternalInput{Rows: in.Rows})
	return stagedEnvelope(ss, raw, p.TargetRevision)
}

func (s *Server) editStagedPreview(ctx context.Context, ss Session, token string, payload json.RawMessage) (json.RawMessage, error) {
	p, e := loadStagedPreview(ctx, s.DB, ss, false)
	if e != nil {
		return nil, e
	}
	if p.Revision != ss.OwnerRevision {
		return nil, api(409, "preview_modified")
	}
	input, e := stagedEditInput(ss, p, payload)
	if e != nil {
		return nil, e
	}
	preview, ownerHash, e := s.validateStagedInput(ctx, ss, token, input, p.TargetRevision)
	if e != nil {
		return nil, e
	}
	e = s.tx(ctx, func(tx *sql.Tx) error {
		current, e := loadSession(ctx, tx, ss.Scope, ss.ID, true)
		if e != nil {
			return e
		}
		if current.State != "editing" || current.Revision != ss.Revision || current.OwnerRevision != ss.OwnerRevision {
			return api(409, "preview_modified")
		}
		locked, e := loadStagedPreview(ctx, tx, ss, true)
		if e != nil {
			return e
		}
		if locked.Revision != p.Revision || locked.InputHash != p.InputHash || locked.Hash != p.Hash {
			return api(409, "preview_modified")
		}
		result, e := tx.ExecContext(ctx, "UPDATE uploads_previews_v1 SET revision=revision+1,input=?,input_hash=?,preview=?,preview_hash=?,owner_preview_hash=? WHERE session_id=? AND organization_id=? AND division_id=? AND actor_account_id=? AND type=? AND revision=?", input, digest(input), preview, digest(preview), ownerHash, ss.ID, ss.OrganizationID, ss.DivisionID, ss.ActorID, ss.Type, p.Revision)
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return api(409, "preview_modified")
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	p.Revision++
	p.Input, p.InputHash, p.Preview, p.Hash, p.OwnerHash = input, digest(input), preview, digest(preview), ownerHash
	return stagedView(ss, p), nil
}

func (s *Server) stagedConfirmable(ctx context.Context, ss Session, token string) error {
	p, e := loadStagedPreview(ctx, s.DB, ss, false)
	if e != nil {
		return e
	}
	if p.Revision != ss.OwnerRevision {
		return api(409, "preview_modified")
	}
	if e = s.freshStagedPreview(ctx, ss, token, p); e != nil {
		return e
	}
	var v struct {
		Valid     bool `json:"valid"`
		CanCommit bool `json:"can_commit"`
	}
	if json.Unmarshal(p.Preview, &v) != nil || ss.Type == "tests.json" && !v.Valid || ss.Type == "tests.external-results" && !v.CanCommit {
		return api(409, "owner_preview_not_confirmable")
	}
	return nil
}
