package uploads

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

const creationOutcomeTimeout = 5 * time.Second
const credentialOrphanAge = 180 * time.Second

// Only coordinator SQL persistence outlives browser cancellation. Owner HTTP
// always uses the original live request context and bearer, synchronously.
func (s *Server) persistCreationOutcome(r *http.Request, ss Session, key string, t Type, state, action string, oldOrphanOnly bool) (Session, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), creationOutcomeTimeout)
	defer cancel()
	var out Session
	e := s.tx(ctx, func(tx *sql.Tx) error {
		current, e := loadSession(ctx, tx, ss.Scope, ss.ID, true)
		if e != nil {
			return e
		}
		out = current
		if current.State != "staging" && current.State != "outcome_unknown" {
			// A late response cannot reopen a recovered or queued session.
			if state == "preview" && current.OwnerSessionID != ss.OwnerSessionID {
				return api(409, "owner_reference_conflict")
			}
			return nil
		}
		if oldOrphanOnly && (current.State != "staging" || !current.Created.Before(time.Now().UTC().Add(-credentialOrphanAge))) {
			return nil
		}
		if state == "outcome_unknown" && current.State == state {
			return nil
		}
		var result sql.Result
		if state == "preview" {
			result, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state='preview',owner_session_id=?,owner_revision=? WHERE id=? AND state IN ('staging','outcome_unknown')", ss.OwnerSessionID, ss.OwnerRevision, ss.ID)
		} else {
			result, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state=? WHERE id=? AND state IN ('staging','outcome_unknown')", state, ss.ID)
		}
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return api(409, "creation_outcome_changed")
		}
		receiptState := state
		if state == "preview" {
			receiptState = "completed"
		}
		if e = receipt(ctx, tx, ss.Scope, key, receiptState); e != nil {
			return e
		}
		if e = event(ctx, tx, ss.Scope, t, ss.ID, action, current.Revision); e != nil {
			return e
		}
		out.State = state
		if state == "preview" {
			out.OwnerSessionID, out.OwnerRevision = ss.OwnerSessionID, ss.OwnerRevision
		}
		return nil
	})
	return out, e
}

func creationReference(kind string, raw json.RawMessage) (string, int64, error) {
	id, revision, e := ref(raw)
	if e != nil || kind != "users.credentials" {
		return id, revision, e
	}
	// Core credential sessions have positive numeric IDs, never arbitrary keys.
	n, e := parseID(id)
	if e != nil {
		return "", 0, api(503, "owner_invalid_reference")
	}
	return strconv.FormatInt(n, 10), revision, nil
}

// Recovery uses the original resource's private creation receipt. A new request
// key cannot stand in for it; missing or ambiguous metadata fails closed.
func (s *Server) credentialCreationKey(r *http.Request, ss Session, expected string) (string, error) {
	var key sql.NullString
	var count int64
	e := s.DB.QueryRowContext(r.Context(), "SELECT MIN(command_id),COUNT(*) FROM uploads_receipts_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? AND resource_id=? AND action='session.create'", ss.OrganizationID, ss.DivisionID, ss.ActorID, ss.ID).Scan(&key, &count)
	if e != nil {
		return "", e
	}
	if count != 1 || !key.Valid || !keyPattern.MatchString(key.String) {
		return "", api(409, "creation_receipt_unavailable")
	}
	if expected != "" && expected != key.String {
		return "", api(409, "creation_receipt_mismatch")
	}
	return key.String, nil
}

// A missing Core receipt is not proof that parsing did not run. Recovery reads
// the exact original native creation receipt, never resends credential bytes.
func (s *Server) recoverCredentialPreview(r *http.Request, ss Session, expectedKey string, t Type) (any, int, error) {
	if ss.Type != "users.credentials" || t.ID != ss.Type || ss.Owner != "core" {
		return nil, 0, api(409, "invalid_credential_recovery")
	}
	if ss.State != "completed" && !ss.Expires.After(time.Now().UTC()) {
		return nil, 0, api(410, "session_expired")
	}
	key, e := s.credentialCreationKey(r, ss, expectedKey)
	if e != nil {
		return nil, 0, e
	}
	path := "/v1/core/divisions/" + strconv.FormatInt(ss.DivisionID, 10) + "/imports/receipts/" + ownerCommand("create", ss.ID, "")
	raw, e := s.call(r.Context(), "GET", s.Config.CoreURL, path, r.Header.Get("Authorization"), "", "", nil)
	if ae, ok := e.(*APIError); ok && ae.Code == 404 {
		current, e := s.persistCreationOutcome(r, ss, key, t, "outcome_unknown", "session.outcome_unknown", true)
		if e != nil {
			return nil, 0, e
		}
		return current, 200, nil
	}
	if e != nil {
		return nil, 0, e
	}
	ss.OwnerSessionID, ss.OwnerRevision, e = creationReference(ss.Type, raw)
	if e == nil {
		raw, e = s.ownerGet(r.Context(), ss, r.Header.Get("Authorization"))
		if e == nil {
			var id string
			id, ss.OwnerRevision, e = creationReference(ss.Type, raw)
			if e == nil && id != ss.OwnerSessionID {
				e = api(503, "owner_invalid_reference")
			}
		}
	}
	if e != nil {
		if ae, ok := e.(*APIError); ok && ae.Code >= 500 {
			if _, persistErr := s.persistCreationOutcome(r, ss, key, t, "outcome_unknown", "session.outcome_unknown", false); persistErr != nil {
				return nil, 0, persistErr
			}
		}
		return nil, 0, e
	}
	current, e := s.persistCreationOutcome(r, ss, key, t, "preview", "session.recovered", false)
	if e != nil {
		return nil, 0, e
	}
	// Return the complete protected preview already read above. Calling a recovery
	// entry point through sessionView here would risk recursive recovery or resend.
	current.Preview = masked(raw)
	return current, 200, nil
}
