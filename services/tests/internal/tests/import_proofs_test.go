package tests

import (
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestJSONOwnerProofKeepsHistoricalValidationHashAndExactInputNumbers(t *testing.T) {
	in := jsonImportFixture()
	in.Payload = json.RawMessage(strings.Replace(string(in.Payload), `"title":`, `"identifier":9007199254740993,"scientific":1e2,"title":`, 1))
	out := buildJSONImportPreview(in, "Математика")
	proof := out.OwnerProof
	if !out.Valid || proof == nil || proof.SchemaVersion != 1 || proof.Scheme != "tests.json.v1" || proof.MappingSHA256 != nil || proof.ValidationSHA256 != out.PreviewHash || proof.InputSHA256 != externalImportHash(in) {
		t.Fatal("invalid owner proof", out)
	}
	out.OwnerProof = nil
	out.PreviewHash = ""
	if externalImportHash(out) != proof.ValidationSHA256 {
		t.Fatal("existing preview hash changed or became self-referential")
	}
	changed := in
	changed.Payload = json.RawMessage(strings.Replace(string(in.Payload), "9007199254740993", "9007199254740992", 1))
	if jsonImportOwnerProof(changed, proof.ValidationSHA256).InputSHA256 == proof.InputSHA256 {
		t.Fatal("large numeric IDs collapsed")
	}
	changed.DivisionID++
	if jsonImportOwnerProof(changed, proof.ValidationSHA256).InputSHA256 == proof.InputSHA256 {
		t.Fatal("input not scoped")
	}
}

func TestExternalOwnerProofSeparatesInputValidationAndCurrentMapping(t *testing.T) {
	zero := 0.0
	account, profile := int64(9007199254740993), int64(71)
	in := ExternalImportValidateDTO{OrganizationID: 11, DivisionID: 17, TestID: bson.NewObjectID().Hex(), ExpectedTestRevision: 4,
		Rows: []ExternalImportRowDTO{{RowID: "r2", Row: 2, FullName: "Примеров Ученик", AccountID: &account, LegacyStudentID: &profile, Percent: &zero}}}
	id, _ := bson.ObjectIDFromHex(in.TestID)
	doc := ExternalTestDocument{ID: id, OrganizationID: 11, DivisionID: 17, Revision: 4}
	people := []ExternalPerson{{AccountID: account, StudentID: profile, FullName: in.Rows[0].FullName, Active: true}}
	preview := externalImportBuildPreview(doc, in.Rows, people)
	proof := externalImportOwnerProof(in, preview)
	if !preview.CanCommit || proof.Scheme != "tests.external-results.v1" || proof.MappingSHA256 == nil || proof.InputSHA256 != externalImportHash(in) || proof.ValidationSHA256 != preview.PreviewHash {
		t.Fatal("owner input invalid", preview, proof)
	}
	people[0].StudentID++
	next := externalImportBuildPreview(doc, in.Rows, people)
	changed := externalImportOwnerProof(in, next)
	if changed.InputSHA256 != proof.InputSHA256 || changed.ValidationSHA256 == proof.ValidationSHA256 || *changed.MappingSHA256 == *proof.MappingSHA256 || next.CanCommit {
		t.Fatal("mapping update did not invalidate proof")
	}
	doc.Revision++
	next = externalImportBuildPreview(doc, in.Rows, people)
	if *externalImportOwnerProof(in, next).MappingSHA256 == *changed.MappingSHA256 {
		t.Fatal("target revision not included")
	}
}
