package tests

import (
	"crypto/subtle"
	"net/http"
)

type ImportPreparedCommand struct {
	NativeCommandKey   string            `json:"native_command_key"`
	OwnerRequestSHA256 string            `json:"owner_request_sha256"`
	OwnerProof         *ImportOwnerProof `json:"owner_proof"`
}

func (s *Server) importPrepareRoute(fn func(*http.Request, Actor) (any, int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hasImportCarrier(r.Header) || len(r.Header.Values("X-Service-Key")) > 0 {
			fail(w, conflict("operation_request_mismatch"))
			return
		}
		if e := rejectImportSelectors(r); e != nil {
			fail(w, e)
			return
		}
		if _, e := singleOperationHeader(r, "Authorization"); e != nil {
			fail(w, &APIError{401, "invalid_token"})
			return
		}
		if len(s.Config.OperationDeliveryKey) < 32 {
			fail(w, &APIError{503, "authorization_unavailable"})
			return
		}
		key, e := singleOperationHeader(r, "X-CPM-Import-Prepare-Key")
		if e != nil {
			fail(w, e)
			return
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.Config.OperationDeliveryKey)) != 1 {
			fail(w, &APIError{401, "invalid_operation_credential"})
			return
		}
		a, e := s.Dependencies.Identify(r)
		if e != nil {
			fail(w, e)
			return
		}
		if _, e = readImportWire(r); e != nil {
			fail(w, e)
			return
		}
		key, e = singleOperationHeader(r, "Idempotency-Key")
		if e != nil || !operationUUID.MatchString(key) || !externalImportValidUUID(key) {
			fail(w, conflict("operation_request_mismatch"))
			return
		}
		result, code, e := fn(r, a)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, code, Envelope{true, result})
	}
}

func (s *Server) jsonImportPrepare(r *http.Request, a Actor) (any, int, error) {
	var in JSONImportCommitDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.CommandID != r.Header.Get("Idempotency-Key") {
		return nil, 0, conflict("operation_request_mismatch")
	}
	_, hash, e := jsonImportOperation(r, a, in)
	if e != nil {
		return nil, 0, e
	}
	refs, e := s.jsonImportAccess(r, a, in.JSONImportPreviewDTO, 0)
	if e != nil {
		return nil, 0, e
	}
	preview := buildJSONImportPreview(in.JSONImportPreviewDTO, refs.Direction.Name)
	if !preview.Valid {
		return nil, 0, bad("json_import_validation_failed")
	}
	if preview.PreviewHash != in.PreviewHash {
		return nil, 0, conflict("json_import_preview_changed")
	}
	return ImportPreparedCommand{in.CommandID, hash, preview.OwnerProof}, 200, nil
}

func (s *Server) externalImportPrepare(r *http.Request, a Actor) (any, int, error) {
	var raw ExternalImportCommitDTO
	if e := decode(r, &raw); e != nil {
		return nil, 0, e
	}
	if raw.CommandID != r.Header.Get("Idempotency-Key") {
		return nil, 0, conflict("operation_request_mismatch")
	}
	in, id, hash, e := externalImportCommitRequest(raw, a.AccountID)
	if e != nil {
		return nil, 0, e
	}
	if e = s.externalImportAccess(r, a, in.OrganizationID, in.DivisionID, true); e != nil {
		return nil, 0, e
	}
	test, e := s.Store.ExternalGet(r.Context(), in.OrganizationID, id)
	if e != nil {
		return nil, 0, e
	}
	if test.Deleted || test.DivisionID != in.DivisionID {
		return nil, 0, missing
	}
	if test.Revision != in.ExpectedTestRevision {
		return nil, 0, conflict("external_revision_conflict")
	}
	people, e := s.externalPeople(r.Context(), in.OrganizationID, in.DivisionID)
	if e != nil {
		return nil, 0, e
	}
	preview := externalImportBuildPreview(test, in.Rows, people)
	if !preview.CanCommit {
		return nil, 0, bad("external_import_rows_invalid")
	}
	if preview.PreviewHash != in.PreviewHash {
		return nil, 0, conflict("external_import_preview_changed")
	}
	return ImportPreparedCommand{in.CommandID, hash, externalImportOwnerProof(in.ExternalImportValidateDTO, preview)}, 200, nil
}
