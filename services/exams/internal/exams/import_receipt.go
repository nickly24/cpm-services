package exams

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

type importReceiptProof struct {
	SessionID        string `json:"session_id"`
	Revision         int64  `json:"revision"`
	ExamID           int64  `json:"exam_id"`
	ExamRevision     int64  `json:"exam_revision"`
	Kind             string `json:"kind"`
	OrganizationID   int64  `json:"organization_id"`
	DivisionID       int64  `json:"division_id"`
	AccountID        int64  `json:"account_id"`
	SourceHash       string `json:"source_hash"`
	ValidationHash   string `json:"validation_hash"`
	NativeCommandKey string `json:"native_command_key"`
	JobID            string `json:"operation_job_id"`
	State            string `json:"state"`
}

func receiptProof(value any) (importReceiptProof, error) {
	var proof importReceiptProof
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, &proof) != nil {
		return proof, &APIError{503, "invalid_import_result"}
	}
	return proof, nil
}

func verifyOperationReceipt(r *http.Request, value any) error {
	op := requestOperation(r)
	if op == nil {
		return nil
	}
	p, err := receiptProof(value)
	if err != nil {
		return err
	}
	b := op.Binding
	if p.SessionID != b.SessionID || p.Revision != b.OwnerRevision || p.ExamID != b.ExamID || p.ExamRevision != b.TargetRevision || p.Kind != b.Kind || p.OrganizationID != b.OrganizationID || p.DivisionID != b.DivisionID || p.AccountID != b.AccountID || p.SourceHash != b.SourceHash || p.ValidationHash != b.ValidationHash || p.NativeCommandKey != b.NativeCommandKey || p.JobID != op.Request.JobID || p.State != "committed" {
		return Conflict("operation_request_mismatch")
	}
	return nil
}

// Receipt recovery never calls mutate or reads/creates an editable session.
// A 404 says only that this matching receipt is unavailable, not that no commit
// could have happened. Workers must retain an unknown outcome on that response.
func (s *Server) importReceiptRoute(w http.ResponseWriter, r *http.Request) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Idempotency-Key") != "" && r.Header.Get("Idempotency-Key") != r.PathValue("receipt") {
		failure(w, Bad("unexpected_receipt_input"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(raw) > 0 {
		failure(w, Bad("unexpected_receipt_input"))
		return
	}
	p, err := params(r, a)
	if err != nil || p.DivisionID < 1 {
		failure(w, Bad("division_context_required"))
		return
	}
	id, err := examID(r)
	if err != nil {
		failure(w, err)
		return
	}
	kind, session, key := r.PathValue("kind"), r.PathValue("session"), r.PathValue("receipt")
	if !importKind(kind) || !canonicalUUID(session) || !canonicalUUID(key) {
		failure(w, Missing)
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback()
	ex, err := load(r.Context(), tx, a.OrganizationID, id, false)
	if err != nil {
		failure(w, err)
		return
	}
	if err = importRequestScope(ex, a, p, true, kind); err != nil {
		failure(w, err)
		return
	}
	if err = operationImportScope(r, ex, false); err != nil {
		failure(w, err)
		return
	}
	var hash string
	var response []byte
	var code int
	var created time.Time
	err = tx.QueryRowContext(r.Context(), "SELECT request_hash,response,status_code,created_at FROM exams_receipts_v1 WHERE organization_id=? AND division_id=? AND account_id=? AND idempotency_key=?", a.OrganizationID, p.DivisionID, a.AccountID, key).Scan(&hash, &response, &code, &created)
	if err == sql.ErrNoRows {
		failure(w, Missing)
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	if op := requestOperation(r); op != nil && hash != op.Binding.NativeRequestHash {
		failure(w, Conflict("operation_request_mismatch"))
		return
	}
	if code == 410 || !created.Add(ReceiptRetention).After(time.Now().UTC()) {
		failure(w, &APIError{410, "idempotency_expired"})
		return
	}
	if len(response) == 0 {
		failure(w, Missing)
		return
	}
	value, decodeErr := decodeStoredValue(response)
	if decodeErr != nil {
		failure(w, &APIError{503, "invalid_import_result"})
		return
	}
	proof, err := receiptProof(value)
	if err != nil {
		failure(w, err)
		return
	}
	if code != 200 || proof.State != "committed" || proof.SessionID != session || proof.ExamID != id || proof.Kind != kind || proof.OrganizationID != a.OrganizationID || proof.DivisionID != p.DivisionID || proof.AccountID != a.AccountID || proof.NativeCommandKey != key {
		failure(w, Missing)
		return
	}
	if err = verifyOperationReceipt(r, value); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, value)
}
