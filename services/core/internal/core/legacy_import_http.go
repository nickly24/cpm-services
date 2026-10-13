package core

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

// Reject duplicate keys, null cells and unknown fields rather than silently
// accepting a different logical command under the same owner receipt key.
func legacyJSONValue(dec *json.Decoder) error {
	t, e := dec.Token()
	if e != nil || t == nil {
		return bad("invalid_request")
	}
	if open, ok := t.(json.Delim); ok {
		switch open {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				t, e := dec.Token()
				key, ok := t.(string)
				if e != nil || !ok || seen[key] {
					return bad("invalid_request")
				}
				seen[key] = true
				if e := legacyJSONValue(dec); e != nil {
					return e
				}
			}
			if close, e := dec.Token(); e != nil || close != json.Delim('}') {
				return bad("invalid_request")
			}
		case '[':
			for dec.More() {
				if e := legacyJSONValue(dec); e != nil {
					return e
				}
			}
			if close, e := dec.Token(); e != nil || close != json.Delim(']') {
				return bad("invalid_request")
			}
		default:
			return bad("invalid_request")
		}
	}
	return nil
}
func decodeLegacyImport(r *http.Request, out any) error {
	if r.Header.Get("Content-Type") != "application/json" && !bytes.HasPrefix([]byte(r.Header.Get("Content-Type")), []byte("application/json;")) {
		return &APIError{415, "json_required"}
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if e != nil || len(body) > 2<<20 || !utf8.Valid(body) || len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
		return bad("invalid_request")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if e := legacyJSONValue(dec); e != nil {
		return e
	}
	if _, e := dec.Token(); e != io.EOF {
		return bad("invalid_request")
	}
	dec = json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return bad("invalid_request")
	}
	return nil
}
func (s *Server) legacyImportPreview(r *http.Request, a Actor, d int64) (any, int, error) {
	if e := legacyImportPermission(a, d, true); e != nil {
		return nil, 0, e
	}
	key, e := legacyCommandKey(r, true)
	if e != nil {
		return nil, 0, e
	}
	sourceKey := ""
	if s.Auth != nil {
		sourceKey = s.Auth.OperationDeliveryKey
	}
	source, e := importSourceClaim(r, sourceKey)
	if e != nil {
		return nil, 0, e
	}
	var in LegacyImportDTO
	if e = decodeLegacyImport(r, &in); e != nil {
		return nil, 0, e
	}
	in.Rows, e = legacyRows(in.Rows)
	if e != nil {
		return nil, 0, e
	}
	digest := legacyCommandHash("preview", 0, in)
	var out LegacyImportSession
	code := 201
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := legacyLockScope(r, tx, a, d); e != nil {
			return e
		}
		stored, c, found, e := legacyReceipt(r, tx, a, d, key, digest)
		if e != nil {
			return e
		}
		if found {
			out, code = stored, c
			return matchImportCreationSource(r, tx, a, d, out.ID, "users.legacy", source)
		}
		idx, e := s.legacyImportIndex(r, tx, a, d)
		if e != nil {
			return e
		}
		out = LegacyImportSession{OrganizationID: a.OrganizationID, DivisionID: d, ActorID: a.AccountID, Revision: 1, MaxRows: 100, Status: "preview", Preview: legacyBuildPreview(in.Rows, idx, a), CreatedAt: importMicrosecond(time.Now())}
		out.ExpiresAt = out.CreatedAt.Add(24 * time.Hour)
		payload, _ := json.Marshal(in)
		preview, _ := json.Marshal(out.Preview)
		res, e := tx.ExecContext(r.Context(), "INSERT INTO core_legacy_imports(organization_id,division_id,actor_id,payload,preview,created_at,expires_at) VALUES(?,?,?,?,?,?,?)", a.OrganizationID, d, a.AccountID, payload, preview, out.CreatedAt, out.ExpiresAt)
		if e != nil {
			return e
		}
		out.ID, e = res.LastInsertId()
		if e != nil {
			return e
		}
		if e := saveImportSource(r, tx, a, d, out.ID, "users.legacy", source); e != nil {
			return e
		}
		if source != nil {
			out.AuthorizationProof = legacySnapshotProof(a, out, in.Rows, source)
			out.SourceSHA256, out.SourceProvenance = &source.SHA256, &source.Provenance
			hash := legacyCommandHash("commit", out.ID, LegacyImportCommitDTO{out.Revision})
			out.OwnerRequestSHA256 = &hash
		}
		if e = legacySaveReceipt(r, tx, a, d, key, digest, out, code); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "legacy_import.preview", out.ID)
	})
	return out, code, e
}
func (s *Server) legacyImportGet(r *http.Request, a Actor, d int64) (any, int, error) {
	if e := legacyImportPermission(a, d, false); e != nil {
		return nil, 0, e
	}
	n, e := id(r, "import")
	if e != nil {
		return nil, 0, e
	}
	out, rows, e := legacyLoadSession(r, s.DB, a, d, n, false)
	if e == nil {
		e = s.decorateLegacyProof(r, s.DB, a, &out, rows)
	}
	if e == nil && requestOperation(r) != nil {
		if e := verifyLegacyGrantSession(r, out); e != nil {
			return nil, 0, e
		}
		source, err := loadImportSource(r, s.DB, a, d, n, "users.legacy")
		if err != nil {
			e = err
		} else {
			e = verifyImportGrantProof(r, "users.legacy", n, source, out.AuthorizationProof, legacyCommandHash("commit", n, LegacyImportCommitDTO{out.Revision}))
		}
	}
	return out, 200, e
}
func (s *Server) legacyImportEdit(r *http.Request, a Actor, d int64) (any, int, error) {
	if e := legacyImportPermission(a, d, true); e != nil {
		return nil, 0, e
	}
	n, e := id(r, "import")
	if e != nil {
		return nil, 0, e
	}
	key, e := legacyCommandKey(r, true)
	if e != nil {
		return nil, 0, e
	}
	var in LegacyImportEditDTO
	if e = decodeLegacyImport(r, &in); e != nil {
		return nil, 0, e
	}
	if in.ExpectedRevision < 1 {
		return nil, 0, bad("expected_revision_required")
	}
	in.Rows, e = legacyRows(in.Rows)
	if e != nil {
		return nil, 0, e
	}
	digest := legacyCommandHash("edit", n, in)
	var out LegacyImportSession
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := legacyLockScope(r, tx, a, d); e != nil {
			return e
		}
		stored, _, found, e := legacyReceipt(r, tx, a, d, key, digest)
		if e != nil {
			return e
		}
		if found {
			out = stored
			return nil
		}
		out, _, e = legacyLoadSession(r, tx, a, d, n, true)
		if e != nil {
			return e
		}
		if e = legacyCAS(out, in.ExpectedRevision); e != nil {
			return e
		}
		idx, e := s.legacyImportIndex(r, tx, a, d)
		if e != nil {
			return e
		}
		out.Preview = legacyBuildPreview(in.Rows, idx, a)
		out.Revision++
		if e := s.decorateLegacyProof(r, tx, a, &out, in.Rows); e != nil {
			return e
		}
		payload, _ := json.Marshal(LegacyImportDTO{in.Rows})
		preview, _ := json.Marshal(out.Preview)
		if _, e = tx.ExecContext(r.Context(), "UPDATE core_legacy_imports SET payload=?,preview=?,revision=? WHERE id=?", payload, preview, out.Revision, n); e != nil {
			return e
		}
		if e = legacySaveReceipt(r, tx, a, d, key, digest, out, 200); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "legacy_import.edit", n)
	})
	return out, 200, e
}
func (s *Server) legacyImportCommit(r *http.Request, a Actor, d int64) (any, int, error) {
	if e := legacyImportPermission(a, d, true); e != nil {
		return nil, 0, e
	}
	n, e := id(r, "import")
	if e != nil {
		return nil, 0, e
	}
	key, e := legacyCommandKey(r, true)
	if e != nil {
		return nil, 0, e
	}
	var in LegacyImportCommitDTO
	if ctx := requestOperation(r); ctx != nil {
		if e := decodeOperationJSON(ctx.Body, &in); e != nil {
			return nil, 0, bad("invalid_request")
		}
	}
	if e = decodeLegacyImport(r, &in); e != nil {
		return nil, 0, e
	}
	if in.ExpectedRevision < 1 {
		return nil, 0, bad("expected_revision_required")
	}
	digest := legacyCommandHash("commit", n, in)
	if ctx := requestOperation(r); ctx != nil {
		if ctx.Envelope == nil || ctx.Envelope.Binding.OwnerRequestSHA256 != digest || ctx.Envelope.Binding.OwnerPreviewRevision == nil || *ctx.Envelope.Binding.OwnerPreviewRevision != strconv.FormatInt(in.ExpectedRevision, 10) {
			return nil, 0, &APIError{409, "operation_request_mismatch"}
		}
	}
	if requestOperation(r) != nil {
		stored, e := importApplicationReceipt(r, s.DB, a, d, key)
		if e != nil {
			return nil, 0, e
		}
		if stored != nil {
			if e := verifyImportGrantReceipt(r, stored); e != nil {
				return nil, 0, e
			}
			result, e := decodeImportReceipt(stored)
			return result, 200, e
		}
	}
	// Fast recovery avoids hashing and recreating any account after a lost reply.
	if out, code, found, e := legacyReceipt(r, s.DB, a, d, key, digest); e != nil || found {
		if found && requestOperation(r) != nil {
			return nil, 0, &APIError{409, "operation_receipt_proof_missing"}
		}
		return out, code, e
	}
	out, _, e := legacyLoadSession(r, s.DB, a, d, n, false)
	if e != nil {
		return nil, 0, e
	}
	if e = legacyCAS(out, in.ExpectedRevision); e != nil {
		return nil, 0, e
	}
	if !out.Preview.CanCommit {
		return nil, 0, &APIError{409, "legacy_import_not_ready"}
	}
	hashes, e := s.legacyPrepareHashes(r, out.Preview)
	if e != nil {
		return nil, 0, e
	}
	// Hashing may take time. The live identity must still authorize the exact
	// pinned actor/context at application time, including trusted Auth grants.
	live, e := s.Auth.Identify(r)
	if e != nil {
		return nil, 0, e
	}
	if live.AccountID != a.AccountID || live.OrganizationID != a.OrganizationID {
		return nil, 0, denied
	}
	if e = legacyImportPermission(live, d, true); e != nil {
		return nil, 0, e
	}
	a = live
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := legacyLockScope(r, tx, a, d); e != nil {
			return e
		}
		stored, _, found, e := legacyReceipt(r, tx, a, d, key, digest)
		if e != nil {
			return e
		}
		if found {
			if requestOperation(r) != nil {
				receipt, e := importApplicationReceipt(r, tx, a, d, key)
				if e != nil {
					return e
				}
				if receipt == nil {
					return &APIError{409, "operation_receipt_proof_missing"}
				}
				if e := verifyImportGrantReceipt(r, receipt); e != nil {
					return e
				}
			}
			out = stored
			return nil
		}
		var rows []LegacyImportRow
		out, rows, e = legacyLoadSession(r, tx, a, d, n, true)
		if e != nil {
			return e
		}
		if e = legacyCAS(out, in.ExpectedRevision); e != nil {
			return e
		}
		idx, e := s.legacyImportIndex(r, tx, a, d)
		if e != nil {
			return e
		}
		fresh := legacyBuildPreview(rows, idx, a)
		if !fresh.CanCommit || legacyCommandHash("plan", n, fresh) != legacyCommandHash("plan", n, out.Preview) {
			return &APIError{409, "legacy_import_preview_changed"}
		}
		source, e := loadImportSource(r, tx, a, d, n, "users.legacy")
		if e != nil {
			return e
		}
		proof := legacySnapshotProof(a, out, rows, source)
		if e := verifyLegacyGrantSession(r, out); e != nil {
			return e
		}
		if e := verifyImportGrantProof(r, "users.legacy", n, source, proof, digest); e != nil {
			return e
		}
		report, e := s.legacyApply(r, tx, a, d, fresh, hashes)
		if e != nil {
			return e
		}
		out.Status, out.Report = "completed", &report
		out.Revision++
		out.Preview.CanCommit = false
		body, _ := json.Marshal(report)
		preview, _ := json.Marshal(out.Preview)
		if _, e = tx.ExecContext(r.Context(), "UPDATE core_legacy_imports SET status='completed',report=?,preview=?,revision=? WHERE id=?", body, preview, out.Revision, n); e != nil {
			return e
		}
		if e = legacySaveReceipt(r, tx, a, d, key, digest, out, 200); e != nil {
			return e
		}
		if e := saveImportApplicationReceipt(r, tx, a, d, n, "users.legacy", key, digest, source, proof, out); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "legacy_import.commit", n)
	})
	return out, 200, e
}
