package tests

import (
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func externalImportFloat(v float64) *float64 { return &v }
func externalImportInt(v int64) *int64       { return &v }

func externalImportFixture() (ExternalTestDocument, []ExternalPerson, []ExternalImportRowDTO) {
	doc := ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 11, DivisionID: 22,
		Name: "Внешний тест", DirectionID: 33, Date: "2026-10-09", Revision: 7,
		Results: []ExternalResult{}, Commands: []ExternalCommandReceipt{}}
	people := []ExternalPerson{{AccountID: 71, StudentID: 101, FullName: "Иванов Иван Иванович", Active: true},
		{AccountID: 72, StudentID: 102, FullName: "Петров Петр Петрович", Active: false}}
	rows := []ExternalImportRowDTO{{RowID: "row-3", Row: 3, FullName: " Иванов   ИВАН Другой ", Percent: externalImportFloat(0)},
		{RowID: "row-4", Row: 4, FullName: "Петров Петр", Percent: externalImportFloat(0.9)}}
	return doc, people, rows
}

func externalImportCommand(doc ExternalTestDocument, people []ExternalPerson, rows []ExternalImportRowDTO) ExternalImportCommitDTO {
	preview := externalImportBuildPreview(doc, rows, people)
	return ExternalImportCommitDTO{ExternalImportValidateDTO: ExternalImportValidateDTO{OrganizationID: doc.OrganizationID,
		DivisionID: doc.DivisionID, TestID: doc.ID.Hex(), ExpectedTestRevision: doc.Revision, Rows: rows},
		CommandID: "595e3e03-fce4-4eaf-8455-857b7a3a24fc", PreviewHash: preview.PreviewHash,
		UploadID: "upload-1", UploadSessionID: "session-1", UploadPreviewRevision: 3}
}

func externalImportHasError(row ExternalImportPreviewRow, code string) bool {
	for _, err := range row.Errors {
		if err.Code == code {
			return true
		}
	}
	return false
}

func externalImportAssertError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var api *APIError
	if !errors.As(err, &api) || api.Code != status || api.Message != code {
		t.Fatalf("wanted %d/%s, got %v", status, code, err)
	}
}

func TestExternalImportMappingPreservesLegacyNamesAndDistinctProfileIDs(t *testing.T) {
	doc, people, rows := externalImportFixture()
	preview := externalImportBuildPreview(doc, rows, people)
	if !preview.CanCommit || preview.Summary.ImportRows != 2 || len(preview.PreviewHash) != 64 {
		t.Fatalf("expected valid rows including inactive historical pupil: %+v", preview)
	}
	if *preview.Rows[0].AccountID != 71 || *preview.Rows[0].LegacyStudentID != 101 || *preview.Rows[0].Percent != 0 || *preview.Rows[1].Percent != 0.9 {
		t.Fatal("account/profile mapping, zero or raw fractional percent changed")
	}
	if preview.Rows[0].StudentFullName != "Иванов Иван Иванович" || rows[0].FullName != " Иванов   ИВАН Другой " {
		t.Fatal("source rows mutated or core display name lost")
	}
	if externalImportPersonKey("Фёдоров Иван") == externalImportPersonKey("Федоров Иван") {
		t.Fatal("invented legacy ё/е substitution")
	}
	if externalImportPersonKey("Иванов") != "" {
		t.Fatal("one-part name was accepted")
	}
	if externalImportPersonKey("İPEK ΟΣ") != "i\u0307pek|ος" {
		t.Fatal("Unicode lowercase must preserve legacy full case mappings")
	}
}

func TestExternalImportPercentRangeAndMissingAreExplicit(t *testing.T) {
	doc, people, rows := externalImportFixture()
	for _, value := range []*float64{nil, externalImportFloat(-0.01), externalImportFloat(100.01), externalImportFloat(math.NaN()), externalImportFloat(math.Inf(1))} {
		row := rows[0]
		row.Percent = value
		preview := externalImportBuildPreview(doc, []ExternalImportRowDTO{row}, people)
		if preview.CanCommit || !externalImportHasError(preview.Rows[0], "invalid_percent") || preview.Summary.RowErrors != 1 {
			t.Fatalf("invalid or absent percent accepted: %+v", preview)
		}
		if _, err := json.Marshal(preview); err != nil {
			t.Fatal("error preview is not serializable", err)
		}
	}
	for _, value := range []float64{0, 0.9, 99.999, 100} {
		row := rows[0]
		row.Percent = externalImportFloat(value)
		if preview := externalImportBuildPreview(doc, []ExternalImportRowDTO{row}, people); !preview.CanCommit || *preview.Rows[0].Percent != value {
			t.Fatalf("valid percent %v changed or rejected", value)
		}
	}
}

func TestExternalImportAmbiguousMappingRequiresExplicitScopedAccount(t *testing.T) {
	doc, people, rows := externalImportFixture()
	people = append(people, ExternalPerson{AccountID: 73, StudentID: 103, FullName: "Иванов Иван Иной", Active: true})
	preview := externalImportBuildPreview(doc, rows[:1], people)
	if preview.CanCommit || !externalImportHasError(preview.Rows[0], "ambiguous_student") || len(preview.Rows[0].Candidates) != 2 {
		t.Fatal("ambiguous full name selected silently")
	}
	rows[0].AccountID, rows[0].LegacyStudentID = externalImportInt(73), externalImportInt(103)
	if resolved := externalImportBuildPreview(doc, rows[:1], people); !resolved.CanCommit || *resolved.Rows[0].AccountID != 73 {
		t.Fatal("explicit scoped mapping could not resolve ambiguity")
	}
	rows[0].LegacyStudentID = externalImportInt(101)
	if resolved := externalImportBuildPreview(doc, rows[:1], people); resolved.CanCommit || !externalImportHasError(resolved.Rows[0], "student_account_mismatch") {
		t.Fatal("wrong profile silently paired with account")
	}
	rows[0].AccountID = externalImportInt(999)
	if resolved := externalImportBuildPreview(doc, rows[:1], people); resolved.CanCommit || !externalImportHasError(resolved.Rows[0], "student_not_in_scope") {
		t.Fatal("foreign account silently resolved by matching name")
	}
	rows[0].AccountID, rows[0].LegacyStudentID = nil, externalImportInt(103)
	if resolved := externalImportBuildPreview(doc, rows[:1], people); !resolved.CanCommit || *resolved.Rows[0].AccountID != 73 {
		t.Fatal("explicit profile reference was confused with account ID")
	}
}

func TestExternalImportDuplicatesBlockEveryAffectedRow(t *testing.T) {
	doc, people, rows := externalImportFixture()
	for _, test := range []struct {
		name string
		edit func([]ExternalImportRowDTO)
		code string
	}{
		{"name despite explicit accounts", func(r []ExternalImportRowDTO) {
			r[1].FullName = "Иванов Иван Другой"
			r[0].AccountID, r[1].AccountID = externalImportInt(71), externalImportInt(72)
		}, "duplicate_full_name"},
		{"same account with different source names", func(r []ExternalImportRowDTO) {
			r[0].AccountID, r[1].AccountID = externalImportInt(71), externalImportInt(71)
		}, "duplicate_account"},
		{"row identifier", func(r []ExternalImportRowDTO) { r[1].RowID = r[0].RowID }, "duplicate_source_row"},
	} {
		t.Run(test.name, func(t *testing.T) {
			copyRows := append([]ExternalImportRowDTO{}, rows...)
			test.edit(copyRows)
			preview := externalImportBuildPreview(doc, copyRows, people)
			if preview.CanCommit || preview.Summary.DuplicateRows != 2 || preview.Summary.RowErrors != 2 {
				t.Fatalf("duplicates allowed partial import: %+v", preview.Summary)
			}
			for _, row := range preview.Rows {
				if !externalImportHasError(row, test.code) {
					t.Fatalf("affected row lacks %s", test.code)
				}
			}
		})
	}
}

func TestExternalImportExistingZeroOrUnknownResultCannotBeOverwritten(t *testing.T) {
	doc, people, rows := externalImportFixture()
	for _, score := range []*float64{nil, externalImportFloat(0), externalImportFloat(83.5)} {
		doc.Results = []ExternalResult{{AccountID: 71, Score: score}}
		preview := externalImportBuildPreview(doc, rows, people)
		if preview.CanCommit || !externalImportHasError(preview.Rows[0], "existing_result") || preview.Summary.ExistingResults != 1 {
			t.Fatal("existing result was confused with missing or overwritten")
		}
	}
}

func TestExternalImportPlanIsWholeBatchAndKeepsPriorResults(t *testing.T) {
	doc, people, rows := externalImportFixture()
	doc.Results = []ExternalResult{{ID: bson.NewObjectID(), AccountID: 80, StudentID: 180, Revision: 2, Score: externalImportFloat(82)}}
	in, _, hash, err := externalImportCommitRequest(externalImportCommand(doc, people, rows), 9)
	if err != nil {
		t.Fatal(err)
	}
	after, receipt, err := externalImportPlan(doc, in, hash, 9, people)
	if err != nil || len(after.Results) != 3 || len(after.Commands) != 1 || len(doc.Results) != 1 || len(doc.Commands) != 0 {
		t.Fatalf("whole batch plan failed or mutated original: %v", err)
	}
	if !reflect.DeepEqual(after.Results[0], doc.Results[0]) || receipt.SourceRevision != 8 || receipt.ActorID != 9 || len(receipt.Results) != 2 {
		t.Fatal("prior result, revision or command receipt lost")
	}
	if receipt.Results[0].Percent != 0 || receipt.Results[1].Percent != 0.9 || after.Results[1].StudentID != 101 || after.Results[1].SourceRowID != "row-3" {
		t.Fatal("confirmed row identity/score lost")
	}
	rows[1].Percent = nil
	badCommand := externalImportCommand(doc, people, rows)
	_, _, err = externalImportPlan(doc, badCommand, "hash", 9, people)
	externalImportAssertError(t, err, 400, "external_import_rows_invalid")
	if len(doc.Results) != 1 || len(doc.Commands) != 0 {
		t.Fatal("invalid second row changed existing aggregate")
	}
}

func TestExternalImportRevisionAndLiveMappingInvalidatePreview(t *testing.T) {
	doc, people, rows := externalImportFixture()
	in := externalImportCommand(doc, people, rows)
	changed := doc
	changed.Revision++
	_, _, err := externalImportPlan(changed, in, "hash", 9, people)
	externalImportAssertError(t, err, 409, "external_revision_conflict")
	people[0].FullName = "Иванов Иван Измененный"
	_, _, err = externalImportPlan(doc, in, "hash", 9, people)
	externalImportAssertError(t, err, 409, "external_import_preview_changed")
	foreign := doc
	foreign.DivisionID++
	_, _, err = externalImportPlan(foreign, in, "hash", 9, people)
	externalImportAssertError(t, err, 404, "not_found")
}

func TestExternalImportReplayRemainsStableAfterSourceRevisionMoves(t *testing.T) {
	doc, people, rows := externalImportFixture()
	in, _, hash, err := externalImportCommitRequest(externalImportCommand(doc, people, rows), 9)
	if err != nil {
		t.Fatal(err)
	}
	after, receipt, err := externalImportPlan(doc, in, hash, 9, people)
	if err != nil {
		t.Fatal(err)
	}
	after.Revision = 30
	after.Deleted = true
	saved, replay, err := externalImportReplay(after, in.CommandID, hash, 9)
	if err != nil || !replay || !reflect.DeepEqual(saved, receipt) || saved.SourceRevision != 8 {
		t.Fatal("same command retry did not return original immutable receipt after a tombstone")
	}
	_, _, err = externalImportReplay(after, in.CommandID, hash, 10)
	externalImportAssertError(t, err, 409, "external_import_idempotency_conflict")
	in.UploadPreviewRevision++
	_, _, changedHash, err := externalImportCommitRequest(in, 9)
	if err != nil || changedHash == hash {
		t.Fatal("upload audit reference is not bound to immutable command")
	}
	_, _, err = externalImportReplay(after, in.CommandID, changedHash, 9)
	externalImportAssertError(t, err, 409, "external_import_idempotency_conflict")
	encoded, err := bson.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExternalCommandReceipt
	if err = bson.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, receipt) {
		t.Fatal("receipt cannot be persisted without losing retry identity")
	}
}

func TestExternalImportStrictDTORejectsBooleanAndUnknownFields(t *testing.T) {
	doc, _, _ := externalImportFixture()
	for _, extra := range []string{`"percent":true`, `"percent":"50"`, `"percent":50,"action":"import"`} {
		body := `{"organization_id":11,"division_id":22,"test_id":"` + doc.ID.Hex() + `","rows":[{"row_id":"r1","row":1,"full_name":"Иванов Иван",` + extra + `}]}`
		r := httptest.NewRequest("POST", "/validate", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var in ExternalImportValidateDTO
		externalImportAssertError(t, decode(r, &in), 400, "invalid_request")
	}
}

func TestExternalImportCommandCannotUseMissingScopeOrConfirmation(t *testing.T) {
	doc, people, rows := externalImportFixture()
	base := externalImportCommand(doc, people, rows)
	for _, edit := range []func(*ExternalImportCommitDTO){
		func(in *ExternalImportCommitDTO) { in.CommandID = "00000000-0000-0000-0000-000000000000" },
		func(in *ExternalImportCommitDTO) { in.PreviewHash = "wrong" },
		func(in *ExternalImportCommitDTO) { in.UploadID = "" },
		func(in *ExternalImportCommitDTO) { in.UploadSessionID = "" },
		func(in *ExternalImportCommitDTO) { in.UploadPreviewRevision = 0 },
		func(in *ExternalImportCommitDTO) { in.ExpectedTestRevision = 0 },
	} {
		copyCommand := base
		edit(&copyCommand)
		_, _, _, err := externalImportCommitRequest(copyCommand, 9)
		externalImportAssertError(t, err, 400, "invalid_external_import_command")
	}
	request := base.ExternalImportValidateDTO
	request.DivisionID = 0
	_, _, err := externalImportRequest(request)
	externalImportAssertError(t, err, 400, "invalid_external_import_request")
	request.DivisionID, request.Rows = 22, nil
	_, _, err = externalImportRequest(request)
	externalImportAssertError(t, err, 400, "invalid_external_import_request")
	request.Rows = make([]ExternalImportRowDTO, externalImportMaxRows)
	if _, _, err = externalImportRequest(request); err != nil {
		t.Fatal("maximum raw row count rejected before row diagnostics", err)
	}
	request.Rows = append(request.Rows, ExternalImportRowDTO{})
	_, _, err = externalImportRequest(request)
	externalImportAssertError(t, err, 400, "invalid_external_import_request")
}
