package tests

// ImportOwnerProof describes the native validation snapshot. It is distinct
// from the source-file digest, Uploads wrapper digest and actual HTTP bytes.
type ImportOwnerProof struct {
	SchemaVersion    int     `json:"schema_version"`
	Scheme           string  `json:"scheme"`
	InputSHA256      string  `json:"input_sha256"`
	ValidationSHA256 string  `json:"validation_sha256"`
	MappingSHA256    *string `json:"mapping_sha256"`
}

func jsonImportOwnerProof(in JSONImportPreviewDTO, validation string) *ImportOwnerProof {
	return &ImportOwnerProof{SchemaVersion: 1, Scheme: "tests.json.v1", InputSHA256: externalImportHash(in), ValidationSHA256: validation}
}

func externalImportOwnerProof(in ExternalImportValidateDTO, preview ExternalImportPreview) *ImportOwnerProof {
	// Match native DTO normalization, preserving exact int64 namespaces and
	// numeric percentage zero. This function receives already validated input.
	in.TestID = preview.TestID
	in.Rows = externalImportNormalizedRows(in.Rows)
	type row struct {
		RowID           string                    `json:"row_id"`
		AccountID       *int64                    `json:"account_id"`
		LegacyStudentID *int64                    `json:"legacy_student_id"`
		StudentFullName string                    `json:"student_full_name"`
		Candidates      []ExternalImportCandidate `json:"candidates"`
	}
	rows := make([]row, len(preview.Rows))
	for i, item := range preview.Rows {
		candidates := append([]ExternalImportCandidate{}, item.Candidates...)
		rows[i] = row{item.RowID, item.AccountID, item.LegacyStudentID, item.StudentFullName, candidates}
	}
	mapping := externalImportHash(struct {
		OrganizationID int64  `json:"organization_id"`
		DivisionID     int64  `json:"division_id"`
		TestID         string `json:"test_id"`
		Revision       int64  `json:"revision"`
		Rows           []row  `json:"rows"`
	}{preview.OrganizationID, preview.DivisionID, preview.TestID, preview.Revision, rows})
	return &ImportOwnerProof{SchemaVersion: 1, Scheme: "tests.external-results.v1", InputSHA256: externalImportHash(in), ValidationSHA256: preview.PreviewHash, MappingSHA256: &mapping}
}
