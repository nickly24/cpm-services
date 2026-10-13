package cards

import (
	"context"
	"net/http"
)

// Internal, normalized owner binding. Only the authenticated Auth adapter may
// construct this context; no request header/JSON is decoded into it directly.
type operationBinding struct {
	GrantID, JobID, NativeKey, SessionID  string
	AccountID, OrganizationID, DivisionID int64
	Revision                              int64
	Proof                                 ImportProof
	ReceiptOnly                           bool
}

type operationAuthorization struct {
	Request operationRequest
	Binding operationBinding
}

type operationContextKey struct{}
type operationIdentityKey struct{}
type operationIdentity struct {
	Actor Actor
	Scope Scope
}

func (s *Server) identify(r *http.Request) (Actor, Scope, error) {
	if _, ok := requestOperationAuthorization(r); ok {
		identity, verified := r.Context().Value(operationIdentityKey{}).(operationIdentity)
		if !verified {
			return Actor{}, Scope{}, denied
		}
		return identity.Actor, identity.Scope, nil
	}
	if hasOperationCarrier(r) {
		return Actor{}, Scope{}, denied
	}
	return s.Deps.Identify(r)
}

func withOperationAuthorization(r *http.Request, value operationAuthorization) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), operationContextKey{}, value))
}

func requestOperationAuthorization(r *http.Request) (operationAuthorization, bool) {
	value, ok := r.Context().Value(operationContextKey{}).(operationAuthorization)
	return value, ok
}

func (b operationBinding) validateActor(a Actor, op operationRequest) error {
	if b.GrantID != op.GrantID || b.JobID != op.JobID || b.NativeKey != op.NativeKey ||
		!lowerHex(b.SessionID, 32) || b.Revision < 1 || b.Proof.OwnerPreviewRevision != b.Revision ||
		b.Proof.Scheme != "cards.xlsx.v1" || !lowerHex(b.Proof.SHA256, 64) || !lowerHex(b.Proof.SourceSHA256, 64) {
		return &APIError{409, "operation_request_mismatch"}
	}
	if b.AccountID != a.AccountID || b.OrganizationID != a.OrganizationID ||
		b.AccountID < 1 || b.OrganizationID < 1 || b.DivisionID < 1 ||
		a.DivisionID == nil || *a.DivisionID != b.DivisionID {
		return denied
	}
	// Current Auth permissions are used directly, never inferred from role.
	full := a.Role == "admin" && a.OrganizationAdmin
	if !full && !(a.Role == "staff_admin" && a.Permissions["train"].Edit && a.Permissions["upload"].Edit) {
		return denied
	}
	if b.ReceiptOnly && op.Stage != "receipt" {
		return denied
	}
	if op.Stage != "receipt" && op.SessionID != b.SessionID {
		return &APIError{409, "operation_request_mismatch"}
	}
	if op.Stage == "apply" && op.Revision != b.Revision {
		return &APIError{409, "operation_request_mismatch"}
	}
	return nil
}

func (b operationBinding) validateSession(a Actor, ss Session) error {
	if ss.ID != b.SessionID || ss.Kind != "import" || ss.OrganizationID != b.OrganizationID || a.AccountID != b.AccountID {
		return missing
	}
	if ss.Revision != b.Revision {
		return &APIError{409, "operation_snapshot_mismatch"}
	}
	proof := importProof(a, ss)
	if proof == nil || *proof != b.Proof {
		return &APIError{409, "operation_snapshot_mismatch"}
	}
	return nil
}

func requireOperationSession(r *http.Request, a Actor, ss Session) error {
	if authorized, ok := requestOperationAuthorization(r); ok {
		return authorized.Binding.validateSession(a, ss)
	}
	return nil
}

func requireOperationReceipt(r *http.Request, a Actor, saved commandReceipt) error {
	authorized, ok := requestOperationAuthorization(r)
	if !ok {
		return nil
	}
	_, expected, err := commandIdentity(a, authorized.Binding.NativeKey, "session:commit:"+authorized.Binding.SessionID, RevisionDTO{ExpectedRevision: authorized.Binding.Revision})
	if err != nil || saved != expected {
		return &APIError{409, "operation_request_mismatch"}
	}
	return nil
}
