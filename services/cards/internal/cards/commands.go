package cards

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Commands share the existing private session store. They are not public previews.
// The receipt and domain mutation commit in one transaction, including on retries.
type commandReceipt struct {
	Operation string `json:"operation"`
	InputHash string `json:"input_hash"`
}

func commandIdentity(a Actor, key, operation string, input any) (string, commandReceipt, error) {
	if len(key) < 8 || len(key) > 128 {
		return "", commandReceipt{}, bad("invalid_idempotency_key")
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '-') {
			return "", commandReceipt{}, bad("invalid_idempotency_key")
		}
	}
	// Preserve the identity of UUID commands already issued by the BFF.
	if len(key) == 36 && key[8] == '-' && key[13] == '-' && key[18] == '-' && key[23] == '-' {
		if _, err := hex.DecodeString(strings.ReplaceAll(key, "-", "")); err == nil {
			key = strings.ToLower(key)
		}
	}
	scope, _ := json.Marshal([]any{"cards-command-v1", a.OrganizationID, a.AccountID, key})
	identity := sha256.Sum256(scope)
	payload, err := json.Marshal(input)
	if err != nil {
		return "", commandReceipt{}, bad("invalid_request")
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(identity[:16]), commandReceipt{operation, hex.EncodeToString(digest[:])}, nil
}

func (s *Server) command(r *http.Request, a Actor, scope Scope, operation string, input any,
	fn func(*sql.Tx) (any, int64, error)) (any, error) {
	key := r.Header.Get("Idempotency-Key")
	var id string
	var receipt commandReceipt
	var err error
	if key != "" {
		id, receipt, err = commandIdentity(a, key, operation, input)
		if err != nil {
			return nil, err
		}
	}
	var output any
	err = s.tx(r.Context(), func(tx *sql.Tx) error {
		if key != "" {
			raw, _ := json.Marshal(receipt)
			if _, e := tx.ExecContext(r.Context(), "INSERT INTO cards_sessions_v1(id,organization_id,account_id,kind,direction_id,preview,expires_at) VALUES(?,?,?,'command',0,?,?) ON DUPLICATE KEY UPDATE id=id", id, a.OrganizationID, a.AccountID, raw, time.Now().UTC().Add(72*time.Hour)); e != nil {
				return e
			}
			var kind, state string
			var directionID int64
			var stored, result []byte
			if e := tx.QueryRowContext(r.Context(), "SELECT kind,state,direction_id,preview,result FROM cards_sessions_v1 WHERE id=? AND organization_id=? AND account_id=? FOR UPDATE", id, a.OrganizationID, a.AccountID).Scan(&kind, &state, &directionID, &stored, &result); e != nil {
				return e
			}
			var saved commandReceipt
			if kind != "command" || json.Unmarshal(stored, &saved) != nil || saved != receipt {
				return &APIError{409, "idempotency_conflict"}
			}
			if state == "completed" {
				if !scope.Allows(directionID) {
					return missing
				}
				if _, e := s.receiptAccess(r, tx, a, scope, saved, result); e != nil {
					return e
				}
				output = json.RawMessage(result)
				return nil
			}
		}
		var directionID int64
		var e error
		output, directionID, e = fn(tx)
		if e != nil {
			return e
		}
		if key != "" {
			raw, e := json.Marshal(output)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE cards_sessions_v1 SET state='completed',direction_id=?,result=? WHERE id=? AND organization_id=? AND account_id=?", directionID, raw, id, a.OrganizationID, a.AccountID)
			return e
		}
		return nil
	})
	return output, err
}

// Revalidate the live session without replacing the immutable command result.
func (s *Server) receiptAccess(r *http.Request, q querier, a Actor, scope Scope, saved commandReceipt, result []byte) (string, error) {
	if e := requireOperationReceipt(r, a, saved); e != nil {
		return "", e
	}
	if !json.Valid(result) {
		return "", unavailable
	}
	var sessionID string
	switch {
	case saved.Operation == "import:parse", strings.HasPrefix(saved.Operation, "transform:division:"):
		var ss Session
		if json.Unmarshal(result, &ss) != nil || ss.ID == "" {
			return "", unavailable
		}
		sessionID = ss.ID
	case strings.HasPrefix(saved.Operation, "session:update:"):
		sessionID = strings.TrimPrefix(saved.Operation, "session:update:")
	case strings.HasPrefix(saved.Operation, "session:commit:"):
		sessionID = strings.TrimPrefix(saved.Operation, "session:commit:")
	}
	if sessionID != "" {
		rr := r.Clone(r.Context())
		rr.SetPathValue("session", sessionID)
		if _, e := s.loadSession(rr, q, a, scope, false); e != nil {
			return "", e
		}
	}
	return sessionID, nil
}

type Receipt struct {
	State          string          `json:"state"`
	Operation      string          `json:"operation"`
	RequestHash    string          `json:"request_hash"`
	OrganizationID int64           `json:"organization_id"`
	DirectionID    int64           `json:"direction_id"`
	SessionID      string          `json:"session_id,omitempty"`
	ResourceID     string          `json:"resource_id,omitempty"`
	Result         json.RawMessage `json:"result"`
}

func (s *Server) receiptGet(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	id, _, e := commandIdentity(a, r.PathValue("command"), "", nil)
	if e != nil {
		return nil, e
	}
	var out Receipt
	var raw []byte
	e = s.DB.QueryRowContext(r.Context(), "SELECT state,direction_id,preview,result FROM cards_sessions_v1 WHERE id=? AND organization_id=? AND account_id=? AND kind='command'", id, a.OrganizationID, a.AccountID).Scan(&out.State, &out.DirectionID, &raw, &out.Result)
	if e != nil {
		return nil, e
	}
	if out.State != "completed" || !scope.Allows(out.DirectionID) {
		return nil, missing
	}
	var saved commandReceipt
	if json.Unmarshal(raw, &saved) != nil {
		return nil, unavailable
	}
	out.SessionID, e = s.receiptAccess(r, s.DB, a, scope, saved, out.Result)
	if e != nil {
		return nil, e
	}
	out.ResourceID = out.SessionID
	out.Operation, out.RequestHash, out.OrganizationID = saved.Operation, saved.InputHash, a.OrganizationID
	return out, nil
}
