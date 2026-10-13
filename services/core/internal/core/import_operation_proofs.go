package core

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const credentialSnapshotScheme = "core.credentials.snapshot.v1"
const legacySnapshotScheme = "core.legacy.snapshot.v1"

// This is an owner-computed digest of private stored input, never private input
// itself. In particular, the credential preview receipt's plaintext-input
// digest is not an application proof.
type ImportAuthorizationProof struct {
	Scheme           string  `json:"scheme"`
	ValidationSHA256 string  `json:"validation_sha256"`
	InputSHA256      string  `json:"input_sha256"`
	MappingSHA256    *string `json:"mapping_sha256"`
}
type importOperationSource struct {
	SHA256     string
	Provenance string
}
type importSnapshotV1 struct {
	ContractVersion       int     `json:"contract_version"`
	Scheme                string  `json:"scheme"`
	Type                  string  `json:"type"`
	AccountID             string  `json:"account_id"`
	OrganizationID        string  `json:"organization_id"`
	DivisionID            string  `json:"division_id"`
	OwnerSessionID        string  `json:"owner_session_id"`
	OwnerPreviewRevision  *string `json:"owner_preview_revision"`
	SourceSHA256          string  `json:"source_sha256"`
	SourceProvenance      string  `json:"source_provenance"`
	InputSHA256           string  `json:"input_sha256"`
	PlanSHA256            string  `json:"plan_sha256"`
	OwnerPreviewExpiresAt *string `json:"owner_preview_expires_at"`
}

func importSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func importJSONHash(v any) string {
	b, _ := json.Marshal(v)
	return importSHA256(b)
}
func lowerHex(v string, size int) bool {
	if len(v) != size {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func importMicrosecond(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }
func importProofTime(t time.Time) string {
	return importMicrosecond(t).Format("2006-01-02T15:04:05.000000Z")
}
func credentialCommitHash(session int64) string {
	return legacyCommandHash("credential_commit", session, struct{}{})
}
func importSourceClaim(r *http.Request, key string) (*importOperationSource, error) {
	keys, hashes := r.Header.Values("X-CPM-Import-Source-Key"), r.Header.Values("X-CPM-Import-Source-SHA256")
	if len(keys) == 0 && len(hashes) == 0 {
		return nil, nil
	}
	if len(keys) != 1 || len(hashes) != 1 || len(key) < 32 || subtle.ConstantTimeCompare([]byte(keys[0]), []byte(key)) != 1 {
		return nil, &APIError{401, "invalid_operation_credential"}
	}
	if !lowerHex(hashes[0], 64) {
		return nil, bad("invalid_import_source_sha256")
	}
	return &importOperationSource{hashes[0], "uploads-file-v1"}, nil
}
func saveImportSource(r *http.Request, db DBTX, a Actor, d, n int64, kind string, source *importOperationSource) error {
	if source == nil {
		return nil
	}
	_, e := db.ExecContext(r.Context(), "INSERT INTO core_import_operation_sources(kind,session_id,organization_id,division_id,actor_id,source_sha256,provenance) VALUES(?,?,?,?,?,?,?)", kind, n, a.OrganizationID, d, a.AccountID, source.SHA256, source.Provenance)
	return e
}
func matchImportCreationSource(r *http.Request, db DBTX, a Actor, d, n int64, kind string, source *importOperationSource) error {
	if source == nil {
		return nil
	}
	stored, e := loadImportSource(r, db, a, d, n, kind)
	if e != nil {
		return e
	}
	// Existing credential creation receipts predate source metadata. Preserve
	// bearer recovery without guessing/backfilling source; GET publishes null
	// proof, so these sessions cannot issue a new background apply.
	if stored == nil && source.Provenance == "core-file-v1" {
		return nil
	}
	if stored == nil || *stored != *source {
		return &APIError{409, "idempotency_key_reused"}
	}
	return nil
}
func loadImportSource(r *http.Request, db DBTX, a Actor, d, n int64, kind string) (*importOperationSource, error) {
	var out importOperationSource
	e := db.QueryRowContext(r.Context(), "SELECT source_sha256,provenance FROM core_import_operation_sources WHERE kind=? AND session_id=? AND organization_id=? AND division_id=? AND actor_id=?", kind, n, a.OrganizationID, d, a.AccountID).Scan(&out.SHA256, &out.Provenance)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !lowerHex(out.SHA256, 64) || out.Provenance != "core-file-v1" && out.Provenance != "uploads-file-v1" {
		return nil, unavailable
	}
	return &out, nil
}
func importSnapshotProof(a Actor, d, session int64, kind, scheme string, source *importOperationSource, inputHash, planHash string, revision *string, expiry *string) *ImportAuthorizationProof {
	if source == nil {
		return nil
	}
	snapshot := importSnapshotV1{1, scheme, kind, strconv.FormatInt(a.AccountID, 10), strconv.FormatInt(a.OrganizationID, 10), strconv.FormatInt(d, 10), strconv.FormatInt(session, 10), revision, source.SHA256, source.Provenance, inputHash, planHash, expiry}
	return &ImportAuthorizationProof{scheme, importJSONHash(snapshot), inputHash, nil}
}
func credentialSnapshotProof(a Actor, d, session int64, in ImportDTO, source *importOperationSource) *ImportAuthorizationProof {
	input := importJSONHash(in)
	return importSnapshotProof(a, d, session, "users.credentials", credentialSnapshotScheme, source, input, input, nil, nil)
}
func legacySnapshotProof(a Actor, session LegacyImportSession, rows []LegacyImportRow, source *importOperationSource) *ImportAuthorizationProof {
	revision := strconv.FormatInt(session.Revision, 10)
	expiry := importProofTime(session.ExpiresAt)
	return importSnapshotProof(a, session.DivisionID, session.ID, "users.legacy", legacySnapshotScheme, source, importJSONHash(LegacyImportDTO{Rows: rows}), legacyCommandHash("plan", session.ID, session.Preview), &revision, &expiry)
}
func (s *Server) decorateLegacyProof(r *http.Request, db DBTX, a Actor, out *LegacyImportSession, rows []LegacyImportRow) error {
	source, e := loadImportSource(r, db, a, out.DivisionID, out.ID, "users.legacy")
	if e != nil {
		return e
	}
	if source != nil && out.Status == "preview" {
		out.AuthorizationProof = legacySnapshotProof(a, *out, rows, source)
		out.SourceSHA256, out.SourceProvenance = &source.SHA256, &source.Provenance
		hash := legacyCommandHash("commit", out.ID, LegacyImportCommitDTO{ExpectedRevision: out.Revision})
		out.OwnerRequestSHA256 = &hash
	}
	return nil
}

type importStoredReceipt struct {
	Kind, CommandHash, SourceSHA256 string
	SessionID                       int64
	Proof                           ImportAuthorizationProof
	Response                        json.RawMessage
	HTTPStatus                      int
}

func importApplicationReceipt(r *http.Request, db DBTX, a Actor, d int64, key string) (*importStoredReceipt, error) {
	var out importStoredReceipt
	var proof []byte
	e := db.QueryRowContext(r.Context(), "SELECT kind,session_id,command_hash,source_sha256,proof,response,http_status FROM core_import_operation_receipts WHERE organization_id=? AND division_id=? AND actor_id=? AND command_key=?", a.OrganizationID, d, a.AccountID, key).Scan(&out.Kind, &out.SessionID, &out.CommandHash, &out.SourceSHA256, &proof, &out.Response, &out.HTTPStatus)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if decodeOperationJSON(proof, &out.Proof) != nil || !lowerHex(out.Proof.InputSHA256, 64) || !lowerHex(out.Proof.ValidationSHA256, 64) || out.Proof.MappingSHA256 != nil || !lowerHex(out.CommandHash, 64) || !lowerHex(out.SourceSHA256, 64) || !json.Valid(out.Response) || out.HTTPStatus != 200 {
		return nil, unavailable
	}
	return &out, nil
}
func saveImportApplicationReceipt(r *http.Request, db DBTX, a Actor, d, n int64, kind, key, hash string, source *importOperationSource, proof *ImportAuthorizationProof, response any) error {
	if source == nil || proof == nil || key == "" {
		return nil
	}
	meta, e := json.Marshal(proof)
	if e != nil {
		return e
	}
	body, e := json.Marshal(response)
	if e != nil {
		return e
	}
	_, e = db.ExecContext(r.Context(), "INSERT INTO core_import_operation_receipts(organization_id,division_id,actor_id,command_key,kind,session_id,command_hash,source_sha256,proof,response,http_status) VALUES(?,?,?,?,?,?,?,?,?,?,200)", a.OrganizationID, d, a.AccountID, key, kind, n, hash, source.SHA256, meta, body)
	return e
}
func decodeImportReceipt(out *importStoredReceipt) (any, error) {
	var response any
	dec := json.NewDecoder(bytes.NewReader(out.Response))
	dec.UseNumber()
	e := dec.Decode(&response)
	return response, e
}
func (s *Server) importApplicationReceiptGet(r *http.Request, a Actor, d int64) (any, int, error) {
	if e := legacyImportPermission(a, d, true); e != nil {
		return nil, 0, e
	}
	n, e := id(r, "import")
	if e != nil {
		return nil, 0, e
	}
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Idempotency-Key", r.PathValue("receipt"))
	key, e := legacyCommandKey(copy, true)
	if e != nil {
		return nil, 0, e
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, 0, bad("unexpected_query")
	}
	out, e := importApplicationReceipt(r, s.DB, a, d, key)
	if e != nil {
		return nil, 0, e
	}
	kind := "users.credentials"
	if r.Pattern == "GET /v1/core/divisions/{division}/legacy-imports/{import}/receipts/{receipt}" {
		kind = "users.legacy"
	}
	if out == nil || out.SessionID != n || out.Kind != kind {
		return nil, 0, missing
	}
	if e := verifyImportGrantReceipt(r, out); e != nil {
		return nil, 0, e
	}
	response, e := decodeImportReceipt(out)
	return response, 200, e
}
