package uploads

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func executionSession(kind, owner string) Session {
	return Session{ID: strings.Repeat("a", 32), Scope: Scope{1001, 10235, 3}, Type: kind, Owner: owner,
		OwnerSessionID: "17", Revision: 3, OwnerRevision: 2, FileID: strings.Repeat("b", 32), SourceHash: strings.Repeat("c", 64)}
}

func executionStaged(ss Session, input string) *stagedPreview {
	p := &stagedPreview{Revision: ss.Revision, SourceHash: ss.SourceHash, Input: json.RawMessage(input), Preview: json.RawMessage(`{"preview_hash":"proof"}`), OwnerHash: strings.Repeat("d", 64), TargetRevision: 7}
	p.InputHash = digest(p.Input)
	p.Hash = digest(p.Preview)
	return p
}

func TestImmutableOwnerCommandNativeWire(t *testing.T) {
	job := strings.Repeat("e", 32)
	for _, tc := range []struct{ kind, owner, session, target, path, body string }{
		{"users.credentials", "core", "17", "", "/v1/core/divisions/10235/imports/17/commit", "{}"},
		{"users.legacy", "core", "17", "", "/v1/core/divisions/10235/legacy-imports/17/commit", `{"expected_revision":2}`},
		{"cards.xlsx", "cards", strings.Repeat("f", 32), "", "/v1/cards/sessions/" + strings.Repeat("f", 32) + "/commit", `{"expected_revision":2}`},
		{"exams.outside", "exams", "12345678-1234-4123-a123-123456789abc", "42", "/v1/exams/42/imports/outside/12345678-1234-4123-a123-123456789abc/commit", `{"expected_revision":2}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			ss := executionSession(tc.kind, tc.owner)
			ss.OwnerSessionID = tc.session
			ss.TargetID = tc.target
			p, err := immutableOwnerCommand(ss, job, nil)
			if err != nil {
				t.Fatal(err)
			}
			if p.Path != tc.path || string(p.Body) != tc.body || p.BodySHA256 != digest([]byte(tc.body)) || p.NativeKey != ownerCommand("commit", job, "") {
				t.Fatalf("wrong native command: %+v", p)
			}
			if strings.HasPrefix(tc.kind, "exams.") && p.OwnerRequestSHA256 != digest([]byte("POST "+tc.path+"? 10235 "+tc.body)) {
				t.Fatal("wrong Exams native request hash")
			}
			again, err := immutableOwnerCommand(ss, job, nil)
			if err != nil || again.NativeKey != p.NativeKey || !bytes.Equal(again.Body, p.Body) {
				t.Fatal("command changed on rebuild of same snapshot")
			}
		})
	}
}

func TestImmutableOwnerCommandTestsExactNumbers(t *testing.T) {
	ss := executionSession("tests.external-results", "tests")
	ss.TargetID = strings.Repeat("f", 24)
	p := executionStaged(ss, `{"organization_id":1001,"division_id":10235,"test_id":"`+ss.TargetID+`","expected_test_revision":7,"rows":[{"row_id":"x","row":2,"account_id":9007199254740993,"legacy_student_id":9223372036854775807,"percent":0}]}`)
	command, err := immutableOwnerCommand(ss, strings.Repeat("e", 32), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"account_id":9007199254740993`, `"legacy_student_id":9223372036854775807`, `"percent":0`, `"upload_preview_revision":3`, `"upload_id":"` + ss.FileID + `"`, `"command_id":"` + command.NativeKey + `"`} {
		if !bytes.Contains(command.Body, []byte(expected)) {
			t.Fatalf("native DTO lost %s", expected)
		}
	}
	ss = executionSession("tests.json", "tests")
	p = executionStaged(ss, `{"organization_id":1001,"division_id":10235,"direction_id":9,"payload":{"weight":1e2,"factor":1.50,"question_id":9007199254740993}}`)
	command, err = immutableOwnerCommand(ss, strings.Repeat("e", 32), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"weight":1e2`, `"factor":1.50`, `"question_id":9007199254740993`} {
		if !bytes.Contains(command.Body, []byte(expected)) {
			t.Fatal("JSON number lexeme changed")
		}
	}
}

func TestImmutableOwnerCommandRejectsUntrustedSnapshots(t *testing.T) {
	job := strings.Repeat("e", 32)
	for _, mutate := range []func(*Session){
		func(s *Session) { s.Owner = "cards" }, func(s *Session) { s.OwnerSessionID = "017" }, func(s *Session) { s.OwnerSessionID = "17/commit" },
		func(s *Session) { s.DivisionID = 0 }, func(s *Session) { s.SourceHash = strings.Repeat("C", 64) }, func(s *Session) { s.OwnerRevision = 0 },
	} {
		ss := executionSession("users.legacy", "core")
		mutate(&ss)
		if _, err := immutableOwnerCommand(ss, job, nil); err == nil {
			t.Fatalf("unsafe snapshot accepted: %+v", ss)
		}
	}
	ss := executionSession("cards.transform", "cards")
	ss.OwnerSessionID = strings.Repeat("f", 32)
	if _, err := immutableOwnerCommand(ss, job, nil); err == nil {
		t.Fatal("two-owner transform accepted")
	}
	ss = executionSession("tests.json", "tests")
	for _, input := range []string{
		`{"organization_id":1002,"division_id":10235,"direction_id":9,"payload":{}}`,
		`{"organization_id":1001,"division_id":10235,"direction_id":9,"payload":{},"grant":"injected"}`,
		`{"organization_id":1001,"division_id":10235,"direction_id":0,"payload":{}}`,
	} {
		if _, err := immutableOwnerCommand(ss, job, executionStaged(ss, input)); err == nil {
			t.Fatal("invalid scope/DTO accepted")
		}
	}
	p := executionStaged(ss, `{"organization_id":1001,"division_id":10235,"direction_id":9,"payload":{}}`)
	p.Input = append(p.Input, ' ')
	if _, err := immutableOwnerCommand(ss, job, p); err == nil {
		t.Fatal("changed stored bytes accepted")
	}
}
