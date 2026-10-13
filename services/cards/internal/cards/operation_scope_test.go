package cards

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func operationScopeFixture() (Actor, Session, operationRequest, operationBinding) {
	a, ss := proofFixture()
	division := int64(19)
	a.DivisionID, a.Role = &division, "staff_admin"
	a.Permissions = map[string]Permission{"train": {View: true, Edit: true}, "upload": {View: true, Edit: true}}
	op := operationRequest{GrantID: strings.Repeat("e", 32), JobID: strings.Repeat("f", 32), Stage: "apply", NativeKey: "uploads_commit_test", SessionID: ss.ID, Revision: ss.Revision}
	b := operationBinding{GrantID: op.GrantID, JobID: op.JobID, NativeKey: op.NativeKey, SessionID: ss.ID, AccountID: a.AccountID, OrganizationID: a.OrganizationID, DivisionID: division, Revision: ss.Revision, Proof: *importProof(a, ss)}
	return a, ss, op, b
}

func TestOperationScopeRequiresCurrentCanonicalPermissionsAndIdentity(t *testing.T) {
	a, _, op, b := operationScopeFixture()
	if e := b.validateActor(a, op); e != nil {
		t.Fatal(e)
	}
	changes := map[string]func(*Actor, *operationRequest, *operationBinding){
		"account":      func(a *Actor, o *operationRequest, b *operationBinding) { a.AccountID++ },
		"organization": func(a *Actor, o *operationRequest, b *operationBinding) { a.OrganizationID++ },
		"division":     func(a *Actor, o *operationRequest, b *operationBinding) { v := *a.DivisionID + 1; a.DivisionID = &v },
		"no division":  func(a *Actor, o *operationRequest, b *operationBinding) { a.DivisionID = nil },
		"train revoked": func(a *Actor, o *operationRequest, b *operationBinding) {
			a.Permissions["train"] = Permission{View: true}
		},
		"upload revoked": func(a *Actor, o *operationRequest, b *operationBinding) {
			a.Permissions["upload"] = Permission{View: true}
		},
		"alias only": func(a *Actor, o *operationRequest, b *operationBinding) {
			delete(a.Permissions, "train")
			a.Permissions["cards"] = Permission{Edit: true}
		},
		"role escalation": func(a *Actor, o *operationRequest, b *operationBinding) {
			a.Role = "student"
			a.OrganizationAdmin = true
		},
		"unverified admin": func(a *Actor, o *operationRequest, b *operationBinding) {
			a.Role = "admin"
			a.OrganizationAdmin = false
		},
		"different job":         func(a *Actor, o *operationRequest, b *operationBinding) { o.JobID = strings.Repeat("a", 32) },
		"different key":         func(a *Actor, o *operationRequest, b *operationBinding) { o.NativeKey = "other_command" },
		"different session":     func(a *Actor, o *operationRequest, b *operationBinding) { o.SessionID = strings.Repeat("a", 32) },
		"different revision":    func(a *Actor, o *operationRequest, b *operationBinding) { o.Revision++ },
		"recovery cannot apply": func(a *Actor, o *operationRequest, b *operationBinding) { b.ReceiptOnly = true },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			a, _, op, b := operationScopeFixture()
			change(&a, &op, &b)
			if b.validateActor(a, op) == nil {
				t.Fatal("context escalation accepted")
			}
		})
	}
	a, _, op, b = operationScopeFixture()
	a.Role = "admin"
	a.OrganizationAdmin = true
	a.Permissions = nil
	if b.validateActor(a, op) != nil {
		t.Fatal("verified full organization admin denied")
	}
	op.Stage = "receipt"
	b.ReceiptOnly = true
	if b.validateActor(a, op) != nil {
		t.Fatal("fresh receipt-only authorization denied")
	}
}

func TestOperationSessionCannotSubstituteSnapshotOrTransform(t *testing.T) {
	for _, change := range []func(*Session){
		func(s *Session) { s.ID = strings.Repeat("a", 32) }, func(s *Session) { s.OrganizationID++ },
		func(s *Session) { s.Kind = "transform" }, func(s *Session) { s.Revision++ },
		func(s *Session) { s.Preview.Cards[0].Answer = "forged" }, func(s *Session) { s.Preview.ThemeID++ },
		func(s *Session) { s.Preview.SourceSHA256 = strings.Repeat("b", 64) },
	} {
		a, ss, op, b := operationScopeFixture()
		change(&ss)
		r := withOperationAuthorization(httptest.NewRequest("POST", "/", nil), operationAuthorization{op, b})
		if requireOperationSession(r, a, ss) == nil {
			t.Fatal("snapshot substitution accepted")
		}
	}
	a, ss, op, b := operationScopeFixture()
	ss.State = "completed"
	r := withOperationAuthorization(httptest.NewRequest("POST", "/", nil), operationAuthorization{op, b})
	if requireOperationSession(r, a, ss) != nil {
		t.Fatal("completed replay requires unchanged approved snapshot")
	}
}

func TestOperationReceiptOnlyMatchesConfirmedCommit(t *testing.T) {
	a, ss, op, b := operationScopeFixture()
	op.Stage = "receipt"
	b.ReceiptOnly = true
	r := withOperationAuthorization(httptest.NewRequest("GET", "/", nil), operationAuthorization{op, b})
	_, right, _ := commandIdentity(a, b.NativeKey, "session:commit:"+ss.ID, RevisionDTO{ExpectedRevision: ss.Revision})
	if requireOperationReceipt(r, a, right) != nil {
		t.Fatal("matching receipt rejected")
	}
	for _, wrong := range []commandReceipt{{Operation: "import:parse", InputHash: right.InputHash}, {Operation: right.Operation, InputHash: strings.Repeat("0", 64)}, {Operation: "session:commit:" + strings.Repeat("a", 32), InputHash: right.InputHash}} {
		if requireOperationReceipt(r, a, wrong) == nil {
			t.Fatal("foreign operation receipt accepted")
		}
	}
	if requireOperationReceipt(httptest.NewRequest("GET", "/", nil), a, commandReceipt{}) != nil {
		t.Fatal("native flow changed")
	}
}
