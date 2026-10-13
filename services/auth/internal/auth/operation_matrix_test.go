package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Exercise each owner's native path and distinct preview/apply bytes against the
// shared binding, rather than trusting an owner-supplied arbitrary URL.
func TestOperationAllOwnerRoutes(t *testing.T) {
	for typ, spec := range operationTypes {
		t.Run(typ, func(t *testing.T) {
			svc, accounts, actor, b := operationFixture(t)
			b.Type, b.Owner, b.OwnerProof.Scheme = typ, spec.owner, spec.scheme
			base := ""
			switch spec.owner {
			case "core":
				family := "legacy-imports"
				if typ == "users.credentials" {
					family = "imports"
					b.OwnerPreviewRevision = nil
					b.OwnerPreviewExpiresAt = nil
					b.ApplyWireBodySHA256 = operationDigest([]byte(`{}`))
				}
				base = "/v1/core/divisions/17/" + family + "/123"
			case "cards":
				b.OwnerSessionID = opString(strings.Repeat("4", 32))
				b.OwnerProof.InputSHA256 = nil
				b.Target = map[string]string{"kind": "existing_theme", "direction_id": "31", "theme_id": "41"}
				base = "/v1/cards/sessions/" + *b.OwnerSessionID
			case "tests":
				b.OwnerSessionID = nil
				b.OwnerPreviewRevision = nil
				b.OwnerPreviewExpiresAt = nil
				b.PreviewWireBodySHA256 = opString(operationDigest([]byte(`{"preview":true}`)))
				b.Target = map[string]string{"kind": "create_tests", "direction_id": "31"}
				base = "/internal/tests/imports/json"
				if typ == "tests.external-results" {
					base = "/internal/tests/external-results"
					b.Target = map[string]string{"kind": "external_test", "test_id": strings.Repeat("a", 24)}
					b.TargetRevision = opString("7")
					b.OwnerProof.MappingSHA256 = opString(strings.Repeat("b", 64))
				}
			case "exams":
				b.OwnerSessionID = opString("44444444-4444-4444-8444-444444444444")
				b.OwnerProof.InputSHA256 = nil
				b.TargetRevision = opString("7")
				b.Target = map[string]string{"kind": "exam", "exam_id": "41"}
				base = "/v1/exams/41/imports/" + strings.TrimPrefix(typ, "exams.") + "/" + *b.OwnerSessionID
			}
			ctx := context.Background()
			g, e := svc.Issue(ctx, accounts.account, actor, "22222222-2222-4222-8222-222222222222", b)
			if e != nil {
				t.Fatal(e)
			}
			for _, stage := range []string{"apply", "preview", "receipt"} {
				r := OperationAuthorizeRequest{ContractVersion: 1, JobID: b.JobID, NativeCommandKey: b.NativeCommandKey, Stage: stage, Method: "GET", Path: base, WireBodySHA256: operationDigest(nil)}
				switch stage {
				case "apply":
					r.Method = "POST"
					r.Path += "/commit"
					r.WireBodySHA256 = b.ApplyWireBodySHA256
				case "preview":
					if spec.owner == "tests" {
						r.Method = "POST"
						r.Path += "/validate"
						r.WireBodySHA256 = *b.PreviewWireBodySHA256
					}
					if spec.owner == "exams" {
						r.RawQuery = "expected_revision=2&limit=200&offset=5000"
					}
				case "receipt":
					r.Path += "/receipts/" + b.NativeCommandKey
					if spec.owner == "cards" {
						r.Path = "/v1/cards/receipts/" + b.NativeCommandKey
					}
					if spec.owner == "tests" {
						r.Method = "POST"
						r.Path = base + "/receipt"
						r.WireBodySHA256 = b.ApplyWireBodySHA256
					}
				}
				if _, e = svc.Authorize(ctx, g.GrantID, spec.owner, r); e != nil {
					t.Fatalf("%s: %v", stage, e)
				}
				wrong := r
				wrong.Path += "/other"
				_, e = svc.Authorize(ctx, g.GrantID, spec.owner, wrong)
				expectOperationError(t, e, 409)
				if stage == "preview" && spec.owner == "exams" {
					for _, query := range []string{"limit=200&expected_revision=2&offset=0", "expected_revision=2&limit=201&offset=0", "expected_revision=2&limit=1&offset=5001", "expected_revision=2&limit=1&offset=00", "expected_revision=2&limit=1&offset=0&offset=0", "expected_revision=3&limit=1&offset=0"} {
						wrong = r
						wrong.RawQuery = query
						_, e = svc.Authorize(ctx, g.GrantID, spec.owner, wrong)
						expectOperationError(t, e, 409)
					}
				}
			}
			if spec.owner == "cards" {
				accounts.account.AuthVersion++
				recovered, e := svc.Recover(ctx, g.GrantID, "55555555-5555-4555-8555-555555555555", accounts.account, actor)
				if e != nil {
					t.Fatal(e)
				}
				dep := OperationAuthorizeRequest{ContractVersion: 1, JobID: b.JobID, NativeCommandKey: b.NativeCommandKey, Stage: "core-direction", Method: "GET", Path: "/internal/core/import-directions/31", WireBodySHA256: operationDigest(nil)}
				if _, e = svc.Authorize(ctx, recovered.GrantID, "core", dep); e != nil {
					t.Fatal(e)
				}
				_, e = svc.Authorize(ctx, recovered.GrantID, "cards", dep)
				expectOperationError(t, e, 404)
			}
		})
	}
}

func TestOperationQuestionScopeAndReceiptContextsStayCurrent(t *testing.T) {
	svc, accounts, actor, b := operationFixture(t)
	accounts.account.Organizations[0].Divisions = append(accounts.account.Organizations[0].Divisions, Division{ID: 18, Permissions: map[string]Permission{"exams": {true, true}}})
	b.Type = "exams.questions"
	b.Owner = "exams"
	b.OwnerProof.Scheme = "exams.import.v1"
	b.OwnerProof.InputSHA256 = nil
	b.OwnerSessionID = opString("44444444-4444-4444-8444-444444444444")
	b.TargetRevision = opString("3")
	b.Target = map[string]string{"kind": "exam", "exam_id": "41"}
	b.RequiredDivisionIDs = []string{"17", "18"}
	g, e := svc.Issue(context.Background(), accounts.account, actor, "22222222-2222-4222-8222-222222222222", b)
	if e != nil {
		t.Fatal(e)
	}
	r := OperationAuthorizeRequest{ContractVersion: 1, JobID: b.JobID, NativeCommandKey: b.NativeCommandKey, Stage: "apply", Method: "POST", Path: "/v1/exams/41/imports/questions/" + *b.OwnerSessionID + "/commit", WireBodySHA256: b.ApplyWireBodySHA256}
	accounts.account.Organizations[0].Divisions[1].Permissions["exams"] = Permission{true, false}
	_, e = svc.Authorize(context.Background(), g.GrantID, "exams", r)
	expectOperationError(t, e, 403)
	// Receipt delegates the native owner's current connected union check using
	// fresh contexts, including the withdrawn permission. No fabricated admin.
	r.Stage = "receipt"
	r.Method = "GET"
	r.Path = strings.TrimSuffix(r.Path, "/commit") + "/receipts/" + b.NativeCommandKey
	r.WireBodySHA256 = operationDigest(nil)
	response, e := svc.Authorize(context.Background(), g.GrantID, "exams", r)
	if e != nil {
		t.Fatal(e)
	}
	if response.Actor.OrganizationAdmin || len(response.DivisionPermissions) != 2 || response.DivisionPermissions[1].Permissions["exams"].Edit {
		t.Fatal("receipt returned stale or elevated contexts")
	}
}

func TestOperationConfigRejectsPartialOrReusedCredentials(t *testing.T) {
	cfg := OperationConfig{Enabled: true, IssuerKey: strings.Repeat("i", 64), TTL: 6 * time.Hour, OwnerKeys: map[string]string{"core": strings.Repeat("c", 64), "cards": strings.Repeat("d", 64), "tests": strings.Repeat("t", 64), "exams": strings.Repeat("e", 64)}}
	if e := cfg.validate("old-internal-key"); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*OperationConfig){func(c *OperationConfig) { c.Enabled = false }, func(c *OperationConfig) { c.TTL = 4 * time.Minute }, func(c *OperationConfig) { c.TTL = 25 * time.Hour }, func(c *OperationConfig) { c.OwnerKeys["tests"] = c.IssuerKey }, func(c *OperationConfig) { delete(c.OwnerKeys, "core") }, func(c *OperationConfig) { c.OwnerKeys["core"] = "old-internal-key" }, func(c *OperationConfig) { c.IssuerKey = " " + c.IssuerKey }, func(c *OperationConfig) { c.OwnerKeys["other"] = strings.Repeat("o", 64) }} {
		copy := cfg
		copy.OwnerKeys = map[string]string{}
		for k, v := range cfg.OwnerKeys {
			copy.OwnerKeys[k] = v
		}
		mutate(&copy)
		if copy.validate("old-internal-key") == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}

func TestOperationDeadlineNeverRoundsBeyondSource(t *testing.T) {
	svc, accounts, actor, b := operationFixture(t)
	deadline := svc.now().Add(time.Minute).Add(123456789 * time.Nanosecond)
	b.UploadExpiresAt = deadline.Format(time.RFC3339Nano)
	g, e := svc.Issue(context.Background(), accounts.account, actor, "22222222-2222-4222-8222-222222222222", b)
	if e != nil {
		t.Fatal(e)
	}
	expiry, e := time.Parse(time.RFC3339Nano, g.ExpiresAt)
	if e != nil || expiry.After(deadline) || expiry.Nanosecond()%1000 != 0 || !expiry.Equal(deadline.Truncate(time.Microsecond)) {
		t.Fatalf("expiry exceeded or changed source deadline: %s", g.ExpiresAt)
	}
	if g.Binding.UploadExpiresAt != b.UploadExpiresAt {
		t.Fatal("immutable source deadline modified")
	}
}
