package tests

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestJSONReceiptOnlyNeverAppliesEvenAfterArchiveOrMissingReceipt(t *testing.T) {
	core := jsonImportCoreServer(t)
	defer core.Close()
	in := JSONImportCommitDTO{JSONImportPreviewDTO: jsonImportFixture(), CommandID: "11111111-1111-4111-8111-111111111111"}
	in.PreviewHash = buildJSONImportPreview(in.JSONImportPreviewDTO, "Математика").PreviewHash
	id, hash, _ := jsonImportOperation(jsonImportRequest(in), jsonImportActor(), in)
	old := Document{ID: id, OrganizationID: 11, OwnerDivisionID: 17, DirectionID: 31, Title: "Существующий", Revision: 9, OperationHash: hash, CreatedBy: 23, Archived: true, Placements: []Placement{{DivisionID: 17}}}
	for _, exists := range []bool{true, false} {
		responses := []bson.D{ratingMongoBatch(CollectionName)}
		if exists {
			responses = []bson.D{ratingMongoBatch(CollectionName, old), ratingMongoBatch(CollectionName, old)}
		}
		commands := []bson.Raw{}
		s := jsonImportMongoServer(t, core.URL, responses, &commands)
		body, code, err := s.jsonImportReceipt(jsonImportRequest(in), jsonImportActor())
		if exists && (err != nil || code != 200 || body.(JSONImportCommitResult).Test.ID != id) {
			t.Fatal("confirmed receipt inaccessible", code, err)
		}
		if !exists {
			assertRatingAPIError(t, err, 404, "import_receipt_not_found")
		}
		for _, command := range commands {
			if command.Lookup("insert").Type != 0 || command.Lookup("update").Type != 0 {
				t.Fatal("receipt attempted apply", command)
			}
		}
	}
}

func TestExternalReceiptOnlyUsesHistoricalInputNotCurrentTargetOrMapping(t *testing.T) {
	core := jsonImportCoreServer(t)
	defer core.Close()
	account, profile, zero := int64(71), int64(101), 0.0
	in := ExternalImportCommitDTO{ExternalImportValidateDTO: ExternalImportValidateDTO{OrganizationID: 11, DivisionID: 17, TestID: bson.NewObjectID().Hex(), ExpectedTestRevision: 2,
		Rows: []ExternalImportRowDTO{{RowID: "r2", Row: 2, FullName: "Примеров Ученик", AccountID: &account, LegacyStudentID: &profile, Percent: &zero}}},
		CommandID: "11111111-1111-4111-8111-111111111111", PreviewHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", UploadID: "source", UploadSessionID: "session", UploadPreviewRevision: 3}
	_, id, hash, err := externalImportCommitRequest(in, 23)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"deleted", "missing", "mismatch"} {
		doc := ExternalTestDocument{ID: id, OrganizationID: 11, DivisionID: 17, Revision: 99, Deleted: true}
		if state != "missing" {
			if state == "mismatch" {
				hash = "different"
			}
			doc.Commands = []ExternalCommandReceipt{{CommandID: in.CommandID, InputHash: hash, ActorID: 23, SourceRevision: 3}}
		}
		commands := []bson.Raw{}
		s := jsonImportMongoServer(t, core.URL, []bson.D{externalRetryCursor(t, doc)}, &commands)
		raw, _ := json.Marshal(in)
		r := httptest.NewRequest("POST", "/internal/tests/external-results/receipt", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		body, code, err := s.externalImportReceipt(r, jsonImportActor())
		switch state {
		case "deleted":
			if err != nil || code != 200 || body.(ExternalImportCommitResult).SourceRevision != 3 {
				t.Fatal("receipt changed after deletion", code, err)
			}
		case "missing":
			assertRatingAPIError(t, err, 404, "import_receipt_not_found")
		case "mismatch":
			assertRatingAPIError(t, err, 409, "external_import_idempotency_conflict")
		}
		for _, command := range commands {
			if command.Lookup("insert").Type != 0 || command.Lookup("update").Type != 0 {
				t.Fatal("receipt attempted apply", command)
			}
		}
	}
}
