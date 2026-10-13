package tests

import (
	"errors"
	"net/http"
	"strings"
)

func (s *Server) jsonImportReceiptInternal(r *http.Request) (any, int, error) {
	a, e := s.identifyImport(r)
	if e != nil {
		return nil, 0, e
	}
	if _, e = readImportWire(r); e != nil {
		return nil, 0, e
	}
	return s.jsonImportReceipt(r, a)
}

func (s *Server) externalImportReceiptInternal(r *http.Request) (any, int, error) {
	a, e := s.identifyImport(r)
	if e != nil {
		return nil, 0, e
	}
	if _, e = readImportWire(r); e != nil {
		return nil, 0, e
	}
	return s.externalImportReceipt(r, a)
}

// Receipt lookup is intentionally separate from commit: no branch can insert
// content, recompute a live preview or send a second application command.
func (s *Server) jsonImportReceipt(r *http.Request, a Actor) (any, int, error) {
	var in JSONImportCommitDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !a.Can(true) || !actorUpload(a, true) || in.OrganizationID != a.OrganizationID || in.DivisionID < 1 || in.DirectionID < 1 {
		return nil, 0, denied
	}
	if e := s.scope(r, a, in.DivisionID); e != nil {
		return nil, 0, e
	}
	in.CommandID = strings.ToLower(strings.TrimSpace(in.CommandID))
	id, hash, e := jsonImportOperation(r, a, in)
	if e != nil {
		return nil, 0, e
	}
	if e := checkJSONImportBinding(r, a, in.JSONImportPreviewDTO, in.CommandID, hash); e != nil {
		return nil, 0, e
	}
	old, e := s.Store.Get(r.Context(), a.OrganizationID, id)
	if errors.Is(e, missing) {
		return nil, 0, &APIError{http.StatusNotFound, "import_receipt_not_found"}
	}
	if e != nil {
		return nil, 0, e
	}
	if old.OperationHash != hash || old.OwnerDivisionID != in.DivisionID {
		return nil, 0, conflict("idempotency_conflict")
	}
	if old.CreatedBy != a.AccountID || !old.Editable(a) {
		return nil, 0, denied
	}
	view, e := s.view(r, a, old, in.DivisionID)
	return JSONImportCommitResult{CommandID: in.CommandID, Test: view}, http.StatusOK, e
}

func (s *Server) externalImportReceipt(r *http.Request, a Actor) (any, int, error) {
	var raw ExternalImportCommitDTO
	if e := decode(r, &raw); e != nil {
		return nil, 0, e
	}
	in, id, hash, e := externalImportCommitRequest(raw, a.AccountID)
	if e != nil {
		return nil, 0, e
	}
	if e := checkExternalImportBinding(r, a, in.ExternalImportValidateDTO, in.CommandID, hash); e != nil {
		return nil, 0, e
	}
	if e := checkExternalCommandBinding(r, in); e != nil {
		return nil, 0, e
	}
	if e = s.externalImportAccess(r, a, in.OrganizationID, in.DivisionID, true); e != nil {
		return nil, 0, e
	}
	test, e := s.Store.ExternalGet(r.Context(), in.OrganizationID, id)
	if e != nil {
		return nil, 0, e
	}
	if test.DivisionID != in.DivisionID {
		return nil, 0, missing
	}
	receipt, found, e := externalImportReplay(test, in.CommandID, hash, a.AccountID)
	if e != nil {
		return nil, 0, e
	}
	if !found {
		return nil, 0, &APIError{http.StatusNotFound, "import_receipt_not_found"}
	}
	return externalImportReceiptResult(receipt), http.StatusOK, nil
}
